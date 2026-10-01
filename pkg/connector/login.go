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
	"fmt"
	"strings"
	"sync"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"github.com/beeper/groupme/pkg/groupmeext"
)

const (
	LoginFlowIDWeb   = "web"
	LoginFlowIDToken = "access-token"
)

func (gc *GMConnector) GetLoginFlows() []bridgev2.LoginFlow {
	return []bridgev2.LoginFlow{{
		Name:        "GroupMe",
		Description: "Sign in to GroupMe in your browser",
		ID:          LoginFlowIDWeb,
	}, {
		Name:        "GroupMe access token",
		Description: "Log in with a GroupMe developer access token",
		ID:          LoginFlowIDToken,
	}}
}

func (gc *GMConnector) CreateLogin(ctx context.Context, user *bridgev2.User, flowID string) (bridgev2.LoginProcess, error) {
	if flowID != LoginFlowIDToken && flowID != LoginFlowIDWeb {
		return nil, fmt.Errorf("unknown login flow ID %q", flowID)
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
}

var _ bridgev2.LoginProcessUserInput = (*GMLogin)(nil)
var _ bridgev2.LoginProcessCookies = (*GMLogin)(nil)

func (gl *GMLogin) Cancel() { gl.cancel() }

func (gl *GMLogin) Start(ctx context.Context) (*bridgev2.LoginStep, error) {
	if err := gl.ctx.Err(); err != nil {
		return nil, err
	}
	if gl.FlowID == LoginFlowIDWeb {
		return &bridgev2.LoginStep{
			Type:         bridgev2.LoginStepTypeCookies,
			StepID:       "fi.mau.groupme.login.web",
			Instructions: "Sign in to your GroupMe account.",
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
	}
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeUserInput,
		StepID:       "fi.mau.groupme.login.enter_token",
		Instructions: "Enter your GroupMe access token from the Access Token page at dev.groupme.com.",
		UserInputParams: &bridgev2.LoginUserInputParams{
			Fields: []bridgev2.LoginInputDataField{{
				Type:        bridgev2.LoginInputFieldTypeToken,
				ID:          "token",
				Name:        "Access token",
				Description: "GroupMe API access token",
			}},
		},
	}, nil
}

func (gl *GMLogin) SubmitUserInput(ctx context.Context, input map[string]string) (*bridgev2.LoginStep, error) {
	return gl.submitToken(ctx, input["token"])
}

func (gl *GMLogin) SubmitCookies(ctx context.Context, cookies map[string]string) (*bridgev2.LoginStep, error) {
	return gl.submitToken(ctx, cookies["token"])
}

func (gl *GMLogin) submitToken(ctx context.Context, token string) (*bridgev2.LoginStep, error) {
	gl.mu.Lock()
	defer gl.mu.Unlock()
	if gl.complete {
		return nil, fmt.Errorf("login already completed")
	}
	if err := gl.ctx.Err(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(gl.ctx, cancel)
	defer stop()
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, fmt.Errorf("access token is required")
	}
	client := groupmeext.NewClient(token)
	defer client.Close()
	me, err := client.MyUser(ctx)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, fmt.Errorf("could not validate your GroupMe session; try signing in again")
	}
	if me == nil || me.ID == "" {
		return nil, fmt.Errorf("GroupMe did not return an account ID")
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
