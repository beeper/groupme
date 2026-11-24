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
	"go.mau.fi/util/l10n"
)

var L = l10n.NewNamespace("groupme")

var (
	msgFlowWebName             = L.Define("login.flow.web.name", "GroupMe")
	msgFlowWebDescription      = L.Define("login.flow.web.description", "Sign in to GroupMe in your browser")
	msgFlowPasswordName        = L.Define("login.flow.password.name", "GroupMe password")
	msgFlowPasswordDescription = L.Define("login.flow.password.description", "Sign in with your GroupMe email or phone number and password")
	msgFlowTokenName           = L.Define("login.flow.access-token.name", "GroupMe access token")
	msgFlowTokenDescription    = L.Define("login.flow.access-token.description", "Use a session from your browser, including Google sign-in")

	msgFieldUsernameName     = L.Define("login.field.username.name", "Email or phone number")
	msgFieldPasswordName     = L.Define("login.field.password.name", "Password")
	msgFieldTokenName        = L.Define("login.field.access_token.name", "Access token")
	msgFieldTokenDescription = L.Define("login.field.access_token.description", "GroupMe session token (access_token)")
	msgFieldCodeName         = L.Define("login.field.code.name", "Verification code")

	msgStepWebInstructions                 = L.Define("login.step.web.instructions", "Sign in to your GroupMe account. If Google blocks this browser, close this window and choose GroupMe access token from Login options to use your normal browser instead.")
	msgStepTokenInstructions               = L.Define("login.step.enter_token.instructions", "Sign in at https://web.groupme.com/ in your normal browser. For Google sign-in, use this browser rather than the embedded login window. In Chrome, open DevTools, then Application, Local Storage, and https://web.groupme.com. Copy the access_token value and paste it below. You can also use a developer token from dev.groupme.com.")
	msgStepCredentialsInstructions         = L.Define("login.step.credentials.instructions", "Sign in with your GroupMe account. If you sign in to GroupMe with Google, Microsoft or Apple, first set a password at https://web.groupme.com/forgot_password and use that here.")
	msgStepCredentialsRejectedInstructions = L.Define("login.step.credentials_rejected.instructions", "GroupMe didn't accept that email or phone number and password. Check them and try again, with the country code for phone numbers (like +1). If you sign in to GroupMe with Google, Microsoft or Apple, set a password at https://web.groupme.com/forgot_password first.")
	msgStepWebVerifyUnfinishedInstructions = L.Define("login.step.web_verify_unfinished.instructions", "GroupMe's verification check did not finish on this device. Sign in to your GroupMe account in your browser instead.")
	msgStepWebVerifyRejectedInstructions   = L.Define("login.step.web_verify_rejected.instructions", "GroupMe rejected the verification check for password sign-in. Sign in to your GroupMe account in your browser instead.")
	msgStepVerifyingInstructions           = L.Define("login.step.browser_verification.instructions", "Verifying GroupMe sign-in...")
	msgStepCompletingInstructions          = L.Define("login.step.browser_verification_complete.instructions", "Completing GroupMe sign-in...")
	msgStepChallengeInstructions           = L.Define("login.step.browser_verification_challenge.instructions", "Complete the GroupMe verification challenge to continue signing in.")
	msgStepChallengeFinishInstructions     = L.Define("login.step.browser_verification_challenge_finish.instructions", "Complete the GroupMe verification challenge to finish signing in.")
	msgStepVerifyInstructions              = L.Define("login.step.verify.instructions", "Enter the code GroupMe sent you by SMS.")
	msgStepVerifyHintInstructions          = L.Define(
		"login.step.verify_hint.instructions", "Enter the code GroupMe sent by SMS to the number ending in {hint}.",
		l10n.WithComment("hint is the last digits of the phone number, e.g. 42"),
	)
	msgStepVerifyRetryInstructions = L.Define("login.step.verify_retry.instructions", "That code was incorrect. Enter the code GroupMe sent you by SMS.")
	msgStepCompleteInstructions    = L.Define(
		"login.step.complete.instructions", "Successfully logged in as {name}",
		l10n.WithComment("name is the GroupMe display name of the account"),
	)
)
