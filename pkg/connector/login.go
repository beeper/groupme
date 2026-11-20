// mautrix-groupme - A Matrix-GroupMe puppeting bridge.
// Copyright (C) 2026 The mautrix-groupme contributors
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package connector

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"github.com/beeper/groupme-lib"
	"github.com/beeper/groupme/pkg/groupmeext"
)

const (
	LoginFlowIDPassword = "password"
	LoginFlowIDWeb      = "web"
	LoginFlowIDToken    = "access-token"
)

func (gc *GMConnector) GetLoginFlows() []bridgev2.LoginFlow {
	return []bridgev2.LoginFlow{{
		Name:        "GroupMe",
		Description: "Sign in to GroupMe in your browser",
		ID:          LoginFlowIDWeb,
	}, {
		Name:        "GroupMe password",
		Description: "Sign in with your GroupMe email or phone number and password",
		ID:          LoginFlowIDPassword,
	}, {
		Name:        "GroupMe access token",
		Description: "Use a session from your browser, including Google sign-in",
		ID:          LoginFlowIDToken,
	}}
}

func (gc *GMConnector) CreateLogin(ctx context.Context, user *bridgev2.User, flowID string) (bridgev2.LoginProcess, error) {
	switch flowID {
	case LoginFlowIDWeb, LoginFlowIDPassword, LoginFlowIDToken:
	default:
		return nil, bridgev2.ErrInvalidLoginFlowID
	}
	loginCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	return &GMLogin{User: user, Main: gc, FlowID: flowID, ctx: loginCtx, cancel: cancel}, nil
}

type GMLogin struct {
	User     *bridgev2.User
	Main     *GMConnector
	FlowID   string
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	complete bool

	clientHTTP http.RoundTripper

	username         string
	password         string
	deviceID         string
	verificationCode string
}

var _ bridgev2.LoginProcessWithParams = (*GMLogin)(nil)
var _ bridgev2.LoginProcessUserInput = (*GMLogin)(nil)
var _ bridgev2.LoginProcessCookies = (*GMLogin)(nil)

func (gl *GMLogin) Cancel() { gl.cancel() }

func (gl *GMLogin) Start(ctx context.Context) (*bridgev2.LoginStep, error) {
	return gl.StartWithParams(ctx, bridgev2.LoginStartParams{})
}

func (gl *GMLogin) StartWithParams(ctx context.Context, params bridgev2.LoginStartParams) (*bridgev2.LoginStep, error) {
	if err := gl.ctx.Err(); err != nil {
		return nil, err
	}
	if params.HTTP != nil {
		gl.clientHTTP = params.HTTP
	}
	switch gl.FlowID {
	case LoginFlowIDPassword:
		return gl.credentialsStep("Sign in with your GroupMe account. If you sign in to GroupMe with Google, Microsoft or Apple, first set a password at https://web.groupme.com/forgot_password and use that here."), nil
	case LoginFlowIDWeb:
		return &bridgev2.LoginStep{
			Type:         bridgev2.LoginStepTypeCookies,
			StepID:       "fi.mau.groupme.login.web",
			Instructions: "Sign in to your GroupMe account. If Google blocks this browser, close this window and choose GroupMe access token from Login options to use your normal browser instead.",
			CookiesParams: &bridgev2.LoginCookiesParams{
				URL:               "https://web.groupme.com/",
				WaitForURLPattern: `^https://web\.groupme\.com/(?:[^?#]*)(?:[?#].*)?$`,
				Fields: []bridgev2.LoginCookieField{{
					ID:       "token",
					Required: true,
					Sources: []bridgev2.LoginCookieFieldSource{
						{Type: bridgev2.LoginCookieTypeLocalStorage, Name: "access_token"},
						{Type: bridgev2.LoginCookieTypeCookie, Name: "token", CookieDomain: ".groupme.com"},
						{Type: bridgev2.LoginCookieTypeRequestHeader, Name: "X-Access-Token", RequestURLRegex: `^https://api\.groupme\.com/v[34]/`},
					},
				}},
			},
		}, nil
	default:
		return &bridgev2.LoginStep{
			Type:         bridgev2.LoginStepTypeUserInput,
			StepID:       "fi.mau.groupme.login.enter_token",
			Instructions: "Sign in at https://web.groupme.com/ in your normal browser. For Google sign-in, use this browser rather than the embedded login window. In Chrome, open DevTools, then Application, Local Storage, and https://web.groupme.com. Copy the access_token value and paste it below. You can also use a developer token from dev.groupme.com.",
			UserInputParams: &bridgev2.LoginUserInputParams{
				Fields: []bridgev2.LoginInputDataField{{
					Type:        bridgev2.LoginInputFieldTypeToken,
					ID:          "token",
					Name:        "Access token",
					Description: "GroupMe session token (access_token)",
				}},
			},
		}, nil
	}
}

func (gl *GMLogin) SubmitUserInput(ctx context.Context, input map[string]string) (*bridgev2.LoginStep, error) {
	return gl.submit(ctx, func(ctx context.Context) (*bridgev2.LoginStep, error) {
		switch {
		case gl.FlowID != LoginFlowIDPassword:
			return gl.finishLogin(ctx, input["token"])
		case gl.verificationCode != "":
			return gl.submitVerificationPIN(ctx, input["code"])
		default:
			return gl.submitPassword(ctx, input["username"], input["password"])
		}
	})
}

func (gl *GMLogin) SubmitCookies(ctx context.Context, cookies map[string]string) (*bridgev2.LoginStep, error) {
	return gl.submit(ctx, func(ctx context.Context) (*bridgev2.LoginStep, error) {
		return gl.finishLogin(ctx, cookies["token"])
	})
}

func (gl *GMLogin) submit(ctx context.Context, fn func(context.Context) (*bridgev2.LoginStep, error)) (*bridgev2.LoginStep, error) {
	gl.mu.Lock()
	defer gl.mu.Unlock()
	if gl.complete {
		return nil, bridgev2.RespError(mautrix.MBadState.WithMessage("login already completed").WithStatus(http.StatusBadRequest))
	}
	if err := gl.ctx.Err(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(gl.ctx, cancel)
	defer stop()
	return fn(ctx)
}

func wrapTokenValidationError(err error) error {
	var meta *groupme.Meta
	if errors.As(err, &meta) {
		switch meta.Code {
		case 401, 403:
			return bridgev2.RespError(mautrix.MForbidden.WithMessage("GroupMe rejected that access token"))
		case 429:
			return bridgev2.RespError(mautrix.MLimitExceeded.WithMessage("GroupMe is rate limiting sign-ins, try again later").WithStatus(http.StatusBadRequest))
		}
	}
	return bridgev2.RespError(mautrix.MUnknown.WithMessage("could not validate your GroupMe session; try signing in again"))
}

// Statuses and meta codes as the GroupMe Android app maps them in
// SignInViewModel.signInErrorReason (com.groupme.android 16.36.2).
func wrapPasswordLoginError(err error) error {
	var authErr *groupmeext.AuthError
	if !errors.As(err, &authErr) {
		return bridgev2.RespError(mautrix.MUnknown.WithMessage("GroupMe sign-in failed; try again later"))
	}
	switch authErr.StatusCode {
	case http.StatusUnauthorized:
		if authErr.MetaCode == 40121 {
			return bridgev2.RespError(mautrix.MForbidden.WithMessage("GroupMe has banned this account"))
		}
		return bridgev2.RespError(mautrix.MForbidden.WithMessage("incorrect GroupMe email, phone number or password. If you sign in to GroupMe with Google, Microsoft or Apple, set a password at https://web.groupme.com/forgot_password and use that instead"))
	case http.StatusForbidden:
		return bridgev2.RespError(mautrix.MForbidden.WithMessage("GroupMe has locked this account; sign in on web.groupme.com to unlock it"))
	case http.StatusNotFound:
		return bridgev2.RespError(mautrix.MForbidden.WithMessage("no GroupMe account matches that email or phone number"))
	case http.StatusNotAcceptable:
		return bridgev2.RespError(mautrix.MForbidden.WithMessage("GroupMe needs to verify your age; sign in on web.groupme.com first"))
	case http.StatusConflict:
		return bridgev2.RespError(mautrix.MForbidden.WithMessage("this GroupMe account is scheduled for deletion; sign in on web.groupme.com to keep it"))
	case http.StatusGone:
		return bridgev2.RespError(mautrix.MForbidden.WithMessage("GroupMe needs you to verify your phone number; sign in on web.groupme.com first"))
	case http.StatusTooManyRequests:
		return bridgev2.RespError(mautrix.MLimitExceeded.WithMessage("GroupMe is rate limiting sign-ins, try again later").WithStatus(http.StatusBadRequest))
	}
	return bridgev2.RespError(mautrix.MUnknown.WithMessage("GroupMe sign-in failed; try again later"))
}

// Meta codes from the Android app's multi_factor_auth.ConfirmPinRequest;
// 40002 (incorrect PIN) is handled in submitVerificationPIN.
func wrapVerificationError(err error) error {
	var authErr *groupmeext.AuthError
	if !errors.As(err, &authErr) {
		return bridgev2.RespError(mautrix.MUnknown.WithMessage("GroupMe sign-in failed; try again later"))
	}
	switch {
	case authErr.MetaCode == 40003:
		return bridgev2.RespError(mautrix.MLimitExceeded.WithMessage("too many incorrect codes; start the GroupMe login again later").WithStatus(http.StatusBadRequest))
	case authErr.MetaCode == 40602:
		return bridgev2.RespError(mautrix.MForbidden.WithMessage("the GroupMe verification code expired; start the login again"))
	case authErr.StatusCode == http.StatusTooManyRequests:
		return bridgev2.RespError(mautrix.MLimitExceeded.WithMessage("GroupMe is rate limiting sign-ins, try again later").WithStatus(http.StatusBadRequest))
	}
	return bridgev2.RespError(mautrix.MUnknown.WithMessage("GroupMe could not send or check the verification code; start the login again"))
}

func (gl *GMLogin) submitPassword(ctx context.Context, username, password string) (*bridgev2.LoginStep, error) {
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		return nil, bridgev2.RespError(mautrix.MInvalidParam.WithMessage("email or phone number and password are required"))
	}
	gl.deviceID = groupmeext.AndroidDeviceID(string(gl.User.MXID), username)
	result, err := groupmeext.PasswordLogin(ctx, gl.clientHTTP, username, password, gl.deviceID, "")
	if ctx.Err() != nil {
		return nil, ctx.Err()
	} else if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("GroupMe password sign-in failed")
		var authErr *groupmeext.AuthError
		if errors.As(err, &authErr) && authErr.MetaCode != 40121 &&
			(authErr.StatusCode == http.StatusUnauthorized || authErr.StatusCode == http.StatusNotFound) {
			return gl.credentialsStep("GroupMe didn't accept that email or phone number and password. Check them and try again, with the country code for phone numbers (like +1). If you sign in to GroupMe with Google, Microsoft or Apple, set a password at https://web.groupme.com/forgot_password first."), nil
		}
		return nil, wrapPasswordLoginError(err)
	} else if result.AccessToken != "" {
		return gl.finishLogin(ctx, result.AccessToken)
	} else if result.Verification == nil || result.Verification.Code == "" {
		return nil, bridgev2.RespError(mautrix.MUnknown.WithMessage("GroupMe did not return an access token"))
	}
	hint, err := groupmeext.InitiateVerification(ctx, gl.clientHTTP, result.Verification.Code)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	} else if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to send GroupMe verification code")
		return nil, wrapVerificationError(err)
	}
	gl.username, gl.password, gl.verificationCode = username, password, result.Verification.Code
	instructions := "Enter the code GroupMe sent you by SMS."
	if hint != "" {
		instructions = fmt.Sprintf("Enter the code GroupMe sent by SMS to the number ending in %s.", hint)
	}
	return gl.verifyStep(instructions), nil
}

func (gl *GMLogin) submitVerificationPIN(ctx context.Context, pin string) (*bridgev2.LoginStep, error) {
	pin = strings.TrimSpace(pin)
	if pin == "" {
		return nil, bridgev2.RespError(mautrix.MInvalidParam.WithMessage("verification code is required"))
	}
	err := groupmeext.ConfirmVerification(ctx, gl.clientHTTP, gl.verificationCode, pin)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	} else if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("GroupMe verification failed")
		var authErr *groupmeext.AuthError
		if errors.As(err, &authErr) && authErr.MetaCode == 40002 {
			return gl.verifyStep("That code was incorrect. Enter the code GroupMe sent you by SMS."), nil
		}
		return nil, wrapVerificationError(err)
	}
	result, err := groupmeext.PasswordLogin(ctx, gl.clientHTTP, gl.username, gl.password, gl.deviceID, gl.verificationCode)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	} else if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("GroupMe password sign-in after verification failed")
		return nil, wrapPasswordLoginError(err)
	} else if result.AccessToken == "" {
		return nil, bridgev2.RespError(mautrix.MUnknown.WithMessage("GroupMe did not return an access token"))
	}
	return gl.finishLogin(ctx, result.AccessToken)
}

func (gl *GMLogin) credentialsStep(instructions string) *bridgev2.LoginStep {
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeUserInput,
		StepID:       "fi.mau.groupme.login.credentials",
		Instructions: instructions,
		UserInputParams: &bridgev2.LoginUserInputParams{
			Fields: []bridgev2.LoginInputDataField{{
				Type:           bridgev2.LoginInputFieldTypeUsername,
				ID:             "username",
				Name:           "Email or phone number",
				AutofillDomain: "groupme.com",
			}, {
				Type:           bridgev2.LoginInputFieldTypePassword,
				ID:             "password",
				Name:           "Password",
				AutofillDomain: "groupme.com",
			}},
		},
	}
}

func (gl *GMLogin) verifyStep(instructions string) *bridgev2.LoginStep {
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeUserInput,
		StepID:       "fi.mau.groupme.login.verify",
		Instructions: instructions,
		UserInputParams: &bridgev2.LoginUserInputParams{
			Fields: []bridgev2.LoginInputDataField{{
				Type: bridgev2.LoginInputFieldType2FACode,
				ID:   "code",
				Name: "Verification code",
			}},
		},
	}
}

func (gl *GMLogin) finishLogin(ctx context.Context, token string) (*bridgev2.LoginStep, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, bridgev2.RespError(mautrix.MInvalidParam.WithMessage("access token is required"))
	}
	client := groupmeext.NewClient(token)
	defer client.Close()
	me, err := client.MyUser(ctx)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to validate GroupMe token")
		return nil, wrapTokenValidationError(err)
	}
	if me == nil || me.ID == "" {
		return nil, bridgev2.RespError(mautrix.MUnknown.WithMessage("GroupMe did not return an account ID"))
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}

	loginID := networkid.UserLoginID(me.ID)
	existing, err := gl.User.Bridge.GetExistingUserLoginByID(ctx, loginID)
	if err != nil {
		return nil, fmt.Errorf("failed to check existing login: %w", err)
	}
	if existing != nil && existing.UserMXID == gl.User.MXID && existing.Client != nil {
		// NewLogin holds the bridge cache lock, which sync workers may need to exit.
		existing.Client.Disconnect()
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	ul, err := gl.User.NewLogin(ctx, &database.UserLogin{
		ID:         loginID,
		RemoteName: me.Name,
		Metadata: &UserLoginMetadata{
			Token: token,
			GMID:  string(me.ID),
		},
	}, &bridgev2.NewLoginParams{
		DeleteOnConflict: false,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to save login: %w", err)
	}
	ul.Client.Connect(ul.Log.WithContext(gl.User.Bridge.BackgroundCtx))
	gl.complete = true

	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeComplete,
		StepID:       "fi.mau.groupme.login.complete",
		Instructions: fmt.Sprintf("Successfully logged in as %s", me.Name),
		CompleteParams: &bridgev2.LoginCompleteParams{
			UserLoginID: loginID,
			UserLogin:   ul,
		},
	}, nil
}
