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
	"go.mau.fi/util/l10n"
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

	loginStepIDCredentials                  = "fi.mau.groupme.login.credentials"
	loginStepIDBrowserVerification          = "fi.mau.groupme.login.browser_verification"
	loginStepIDBrowserVerificationChallenge = "fi.mau.groupme.login.browser_verification_challenge"
	loginStepIDVerify                       = "fi.mau.groupme.login.verify"
	loginStepIDWeb                          = "fi.mau.groupme.login.web"
	loginStepIDEnterToken                   = "fi.mau.groupme.login.enter_token"
	loginStepIDComplete                     = "fi.mau.groupme.login.complete"

	loginFieldVerifyToken  = "verify_token"
	loginFieldVerifyStatus = "verify_status"
	verifyStatusChallenge  = "challenge"
)

func (gc *GMConnector) GetLoginFlows() []bridgev2.LoginFlow {
	return []bridgev2.LoginFlow{{
		Name:        msgFlowWebName.Message(),
		Description: msgFlowWebDescription.Message(),
		ID:          LoginFlowIDWeb,
	}, {
		Name:        msgFlowPasswordName.Message(),
		Description: msgFlowPasswordDescription.Message(),
		ID:          LoginFlowIDPassword,
	}, {
		Name:        msgFlowTokenName.Message(),
		Description: msgFlowTokenDescription.Message(),
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

	step                  string
	username              string
	password              string
	deviceID              string
	verificationCode      string
	verificationConfirmed bool
	browserAttempt        uint64
	visibleVerifyTried    bool
}

var _ bridgev2.LoginProcessWithParams = (*GMLogin)(nil)
var _ bridgev2.LoginProcessUserInput = (*GMLogin)(nil)
var _ bridgev2.LoginProcessCookies = (*GMLogin)(nil)

func (gl *GMLogin) Cancel() {
	gl.cancel()
	gl.mu.Lock()
	gl.clearPasswordState()
	gl.mu.Unlock()
}

func (gl *GMLogin) Start(ctx context.Context) (*bridgev2.LoginStep, error) {
	return gl.StartWithParams(ctx, bridgev2.LoginStartParams{})
}

func (gl *GMLogin) StartWithParams(ctx context.Context, params bridgev2.LoginStartParams) (*bridgev2.LoginStep, error) {
	gl.mu.Lock()
	defer gl.mu.Unlock()
	if err := gl.ctx.Err(); err != nil {
		return nil, err
	}
	gl.clearPasswordState()
	gl.step = ""
	gl.browserAttempt = 0
	if params.HTTP != nil {
		gl.clientHTTP = params.HTTP
	}
	switch gl.FlowID {
	case LoginFlowIDPassword:
		return gl.credentialsStep(msgStepCredentialsInstructions.Message()), nil
	case LoginFlowIDWeb:
		return gl.webStep(msgStepWebInstructions.Message()), nil
	default:
		gl.step = loginStepIDEnterToken
		return &bridgev2.LoginStep{
			Type:         bridgev2.LoginStepTypeUserInput,
			StepID:       loginStepIDEnterToken,
			Instructions: msgStepTokenInstructions.Message(),
			UserInputParams: &bridgev2.LoginUserInputParams{
				Fields: []bridgev2.LoginInputDataField{{
					Type:        bridgev2.LoginInputFieldTypeToken,
					ID:          "token",
					Name:        msgFieldTokenName.Message(),
					Description: msgFieldTokenDescription.Message(),
				}},
			},
		}, nil
	}
}

func (gl *GMLogin) SubmitUserInput(ctx context.Context, input map[string]string) (*bridgev2.LoginStep, error) {
	return gl.submit(ctx, func(ctx context.Context) (*bridgev2.LoginStep, error) {
		switch {
		case gl.FlowID == LoginFlowIDToken:
			return gl.finishLogin(ctx, input["token"])
		case gl.FlowID != LoginFlowIDPassword:
			return nil, bridgev2.RespError(mautrix.MBadState.WithMessage("unexpected login input").WithStatus(http.StatusBadRequest))
		case gl.step == loginStepIDVerify && gl.verificationCode != "" && !gl.verificationConfirmed:
			return gl.submitVerificationPIN(ctx, input["code"])
		case gl.step == loginStepIDCredentials || gl.step == "":
			return gl.submitCredentials(ctx, input["username"], input["password"])
		default:
			return nil, bridgev2.RespError(mautrix.MBadState.WithMessage("unexpected login input").WithStatus(http.StatusBadRequest))
		}
	})
}

func (gl *GMLogin) SubmitCookies(ctx context.Context, cookies map[string]string) (*bridgev2.LoginStep, error) {
	return gl.submit(ctx, func(ctx context.Context) (*bridgev2.LoginStep, error) {
		switch {
		case gl.step == loginStepIDBrowserVerification || gl.step == loginStepIDBrowserVerificationChallenge:
			return gl.submitBrowserVerification(ctx, cookies)
		case gl.FlowID == LoginFlowIDWeb || gl.step == loginStepIDWeb:
			return gl.finishLogin(ctx, cookies["token"])
		default:
			return nil, bridgev2.RespError(mautrix.MBadState.WithMessage("unexpected browser cookies").WithStatus(http.StatusBadRequest))
		}
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
		if authErr.IsVerificationError() {
			return bridgev2.RespError(mautrix.MForbidden.WithMessage("GroupMe rejected the verification check for password sign-in; sign in with your browser instead"))
		}
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

func (gl *GMLogin) clearPasswordState() {
	gl.username = ""
	gl.password = ""
	gl.deviceID = ""
	gl.verificationCode = ""
	gl.verificationConfirmed = false
	gl.visibleVerifyTried = false
}

func (gl *GMLogin) submitCredentials(_ context.Context, username, password string) (*bridgev2.LoginStep, error) {
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		return nil, bridgev2.RespError(mautrix.MInvalidParam.WithMessage("email or phone number and password are required"))
	}
	gl.username = username
	gl.password = password
	var mxid string
	if gl.User != nil {
		mxid = string(gl.User.MXID)
	}
	gl.deviceID = groupmeext.AndroidDeviceID(mxid, username)
	gl.verificationCode = ""
	gl.verificationConfirmed = false
	gl.visibleVerifyTried = false
	return gl.browserVerificationStep(true, msgStepVerifyingInstructions.Message()), nil
}

func (gl *GMLogin) submitBrowserVerification(ctx context.Context, cookies map[string]string) (*bridgev2.LoginStep, error) {
	verifyToken := strings.TrimSpace(cookies[loginFieldVerifyToken])
	verifyStatus := strings.TrimSpace(cookies[loginFieldVerifyStatus])
	if verifyToken == "" {
		if verifyStatus == verifyStatusChallenge && !gl.visibleVerifyTried {
			return gl.browserVerificationStep(false, msgStepChallengeInstructions.Message()), nil
		}
		zerolog.Ctx(ctx).Debug().
			Str("verify_status", verifyStatus).
			Bool("visible_tried", gl.visibleVerifyTried).
			Msg("GroupMe browser verification did not return a token, switching to web sign-in")
		gl.clearPasswordState()
		return gl.webStep(msgStepWebVerifyUnfinishedInstructions.Message()), nil
	}
	if gl.username == "" || gl.password == "" {
		gl.clearPasswordState()
		return gl.credentialsStep(msgStepCredentialsInstructions.Message()), nil
	}
	return gl.completePasswordLogin(ctx, verifyToken)
}

func (gl *GMLogin) completePasswordLogin(ctx context.Context, verifyToken string) (*bridgev2.LoginStep, error) {
	if gl.verificationConfirmed {
		result, err := groupmeext.PasswordLogin(ctx, gl.clientHTTP, gl.username, gl.password, gl.deviceID, gl.verificationCode, verifyToken)
		if ctx.Err() != nil {
			gl.clearPasswordState()
			return nil, ctx.Err()
		} else if err != nil {
			zerolog.Ctx(ctx).Err(err).Msg("GroupMe password sign-in after verification failed")
			var authErr *groupmeext.AuthError
			if errors.As(err, &authErr) && authErr.IsVerificationError() {
				if !gl.visibleVerifyTried {
					return gl.browserVerificationStep(false, msgStepChallengeFinishInstructions.Message()), nil
				}
				gl.clearPasswordState()
				return gl.webStep(msgStepWebVerifyRejectedInstructions.Message()), nil
			}
			gl.clearPasswordState()
			return nil, wrapPasswordLoginError(err)
		} else if result.AccessToken == "" {
			gl.clearPasswordState()
			return nil, bridgev2.RespError(mautrix.MUnknown.WithMessage("GroupMe did not return an access token"))
		}
		gl.clearPasswordState()
		return gl.finishLogin(ctx, result.AccessToken)
	}

	result, err := groupmeext.PasswordLogin(ctx, gl.clientHTTP, gl.username, gl.password, gl.deviceID, "", verifyToken)
	if ctx.Err() != nil {
		gl.clearPasswordState()
		return nil, ctx.Err()
	} else if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("GroupMe password sign-in failed")
		var authErr *groupmeext.AuthError
		if errors.As(err, &authErr) && authErr.IsVerificationError() {
			if !gl.visibleVerifyTried {
				return gl.browserVerificationStep(false, msgStepChallengeInstructions.Message()), nil
			}
			gl.clearPasswordState()
			return gl.webStep(msgStepWebVerifyRejectedInstructions.Message()), nil
		}
		if errors.As(err, &authErr) && authErr.MetaCode != 40121 &&
			(authErr.StatusCode == http.StatusUnauthorized || authErr.StatusCode == http.StatusNotFound) {
			gl.clearPasswordState()
			return gl.credentialsStep(msgStepCredentialsRejectedInstructions.Message()), nil
		}
		gl.clearPasswordState()
		return nil, wrapPasswordLoginError(err)
	} else if result.AccessToken != "" {
		gl.clearPasswordState()
		return gl.finishLogin(ctx, result.AccessToken)
	} else if result.Verification == nil || result.Verification.Code == "" {
		gl.clearPasswordState()
		return nil, bridgev2.RespError(mautrix.MUnknown.WithMessage("GroupMe did not return an access token"))
	}
	hint, err := groupmeext.InitiateVerification(ctx, gl.clientHTTP, result.Verification.Code)
	if ctx.Err() != nil {
		gl.clearPasswordState()
		return nil, ctx.Err()
	} else if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to send GroupMe verification code")
		gl.clearPasswordState()
		return nil, wrapVerificationError(err)
	}
	gl.verificationCode = result.Verification.Code
	gl.verificationConfirmed = false
	instructions := msgStepVerifyInstructions.Message()
	if hint != "" {
		instructions = msgStepVerifyHintInstructions.Render(l10n.Params{"hint": hint})
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
		gl.clearPasswordState()
		return nil, ctx.Err()
	} else if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("GroupMe verification failed")
		var authErr *groupmeext.AuthError
		if errors.As(err, &authErr) && authErr.MetaCode == 40002 {
			return gl.verifyStep(msgStepVerifyRetryInstructions.Message()), nil
		}
		gl.clearPasswordState()
		return nil, wrapVerificationError(err)
	}
	gl.verificationConfirmed = true
	gl.visibleVerifyTried = false
	return gl.browserVerificationStep(true, msgStepCompletingInstructions.Message()), nil
}

func (gl *GMLogin) webStep(instructions l10n.Message) *bridgev2.LoginStep {
	gl.step = loginStepIDWeb
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeCookies,
		StepID:       loginStepIDWeb,
		Instructions: instructions,
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
	}
}

func (gl *GMLogin) browserVerificationStep(hidden bool, instructions l10n.Message) *bridgev2.LoginStep {
	gl.browserAttempt++
	attempt := gl.browserAttempt
	stepID := loginStepIDBrowserVerification
	if !hidden {
		stepID = loginStepIDBrowserVerificationChallenge
		gl.visibleVerifyTried = true
	}
	gl.step = stepID
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeCookies,
		StepID:       stepID,
		Instructions: instructions,
		CookiesParams: &bridgev2.LoginCookiesParams{
			URL:       fmt.Sprintf("https://web.groupme.com/signin#beeper-verify-%d", attempt),
			Hidden:    hidden,
			ExtractJS: groupmeArkoseExtractionJS(attempt, hidden),
			Fields: []bridgev2.LoginCookieField{{
				ID:       loginFieldVerifyToken,
				Required: true,
				Sources: []bridgev2.LoginCookieFieldSource{{
					Type: bridgev2.LoginCookieTypeSpecial,
					Name: loginFieldVerifyToken,
				}},
			}, {
				ID:       loginFieldVerifyStatus,
				Required: false,
				Sources: []bridgev2.LoginCookieFieldSource{{
					Type: bridgev2.LoginCookieTypeSpecial,
					Name: loginFieldVerifyStatus,
				}},
			}},
		},
	}
}

func groupmeArkoseExtractionJS(attempt uint64, hidden bool) string {
	return fmt.Sprintf(`new Promise(resolve => {
	const attempt = %d
	if (window.__mautrixGroupMeVerifyAttempt === attempt && window.__mautrixGroupMeVerifyPromise) {
		window.__mautrixGroupMeVerifyPromise.then(resolve)
		return
	}
	window.__mautrixGroupMeVerifyAttempt = attempt
	window.__mautrixGroupMeVerifyPromise = new Promise(done => {
		const hidden = %t
		let settled = false
		let started = false
		let shown = false
		let timer = null
		const cleanup = () => {
			window.removeEventListener('arkoseCaptchaCompleted', onCompleted)
			window.removeEventListener('arkoseCaptchaShown', onShown)
			window.removeEventListener('arkoseCaptchaFailed', onFailed)
			window.removeEventListener('arkoseCaptchaError', onError)
			window.removeEventListener('arkoseCaptchaScriptError', onScriptError)
			window.removeEventListener('arkoseCaptchaHide', onHide)
			window.removeEventListener('arkoseCaptchaReady', onReady)
			clearTimeout(timer)
		}
		const finish = (token, status, reset) => {
			if (settled) return
			settled = true
			cleanup()
			if (reset) {
				try {
					window.ArkoseEnforcement && typeof window.ArkoseEnforcement.reset === 'function' && window.ArkoseEnforcement.reset()
				} catch {}
			}
			done({ verify_token: token, verify_status: status })
		}
		const onCompleted = e => {
			const token = typeof e?.detail?.token === 'string' ? e.detail.token.trim() : ''
			if (token) {
				window.__mautrixGroupMeVerifyConsumed = true
				finish(token, 'ok', true)
			} else {
				finish('', 'failed', true)
			}
		}
		const onShown = () => {
			shown = true
			if (hidden) {
				finish('', 'challenge', true)
				return
			}
			clearTimeout(timer)
			timer = setTimeout(() => finish('', 'timeout', true), 120000)
		}
		const onFailed = () => finish('', 'failed', true)
		const onError = () => finish('', 'failed', true)
		const onScriptError = () => finish('', 'failed', false)
		const onHide = () => {
			if (shown) finish('', 'dismissed', true)
		}
		const start = () => {
			if (started || settled) return
			started = true
			try {
				if (window.__mautrixGroupMeVerifyConsumed && typeof window.ArkoseEnforcement.reset === 'function') {
					window.ArkoseEnforcement.reset()
				}
				window.ArkoseEnforcement.run()
			} catch {
				finish('', 'failed', false)
			}
		}
		const onReady = () => {
			if (window.__arkoseReady && window.ArkoseEnforcement) start()
		}
		if (window.__arkoseScriptError) {
			finish('', 'failed', false)
			return
		}
		window.addEventListener('arkoseCaptchaCompleted', onCompleted)
		window.addEventListener('arkoseCaptchaShown', onShown)
		window.addEventListener('arkoseCaptchaFailed', onFailed)
		window.addEventListener('arkoseCaptchaError', onError)
		window.addEventListener('arkoseCaptchaScriptError', onScriptError)
		window.addEventListener('arkoseCaptchaHide', onHide)
		window.addEventListener('arkoseCaptchaReady', onReady)
		timer = setTimeout(() => finish('', 'timeout', true), 20000)
		if (window.__arkoseReady && window.ArkoseEnforcement) start()
	})
	window.__mautrixGroupMeVerifyPromise.then(resolve)
})`, attempt, hidden)
}

func (gl *GMLogin) credentialsStep(instructions l10n.Message) *bridgev2.LoginStep {
	gl.step = loginStepIDCredentials
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeUserInput,
		StepID:       loginStepIDCredentials,
		Instructions: instructions,
		UserInputParams: &bridgev2.LoginUserInputParams{
			Fields: []bridgev2.LoginInputDataField{{
				Type:           bridgev2.LoginInputFieldTypeUsername,
				ID:             "username",
				Name:           msgFieldUsernameName.Message(),
				AutofillDomain: "groupme.com",
			}, {
				Type:           bridgev2.LoginInputFieldTypePassword,
				ID:             "password",
				Name:           msgFieldPasswordName.Message(),
				AutofillDomain: "groupme.com",
			}},
		},
	}
}

func (gl *GMLogin) verifyStep(instructions l10n.Message) *bridgev2.LoginStep {
	gl.step = loginStepIDVerify
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeUserInput,
		StepID:       loginStepIDVerify,
		Instructions: instructions,
		UserInputParams: &bridgev2.LoginUserInputParams{
			Fields: []bridgev2.LoginInputDataField{{
				Type: bridgev2.LoginInputFieldType2FACode,
				ID:   "code",
				Name: msgFieldCodeName.Message(),
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
		StepID:       loginStepIDComplete,
		Instructions: msgStepCompleteInstructions.Render(l10n.Params{"name": me.Name}),
		CompleteParams: &bridgev2.LoginCompleteParams{
			UserLoginID: loginID,
			UserLogin:   ul,
		},
	}, nil
}
