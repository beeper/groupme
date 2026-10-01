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
	"sync"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/status"

	"github.com/beeper/groupme-lib"

	"github.com/beeper/groupme/pkg/groupmeext"
)

// GMClient implements [bridgev2.NetworkAPI] for a single logged-in GroupMe
// account. It also implements groupme.HandlerAll so it can be registered
// directly on the push subscription (see handlegroupme.go).
type GMClient struct {
	Main      *GMConnector
	UserLogin *bridgev2.UserLogin
	Meta      *UserLoginMetadata
	Client    *groupmeext.Client

	sessionMu     sync.Mutex
	sessionCancel context.CancelFunc
	sessionDone   chan struct{}

	// pollBackoff tracks, per chat ID, when polling may resume after that
	// chat got a 429 (rate limited) from GroupMe. See poll.go. Guarded by
	// pollBackoffMu since multiple chats' polls can be in flight
	// concurrently... actually they aren't (pollOnce is sequential/
	// staggered, see poll.go), but the mutex costs nothing and removes any
	// doubt if that ever changes.
	pollBackoffMu sync.Mutex
	pollBackoff   map[string]time.Time

	// ghostRefreshedAt tracks, per sender GroupMe ID, the last time
	// HandleTextMessage's opportunistic ghost name/avatar refresh actually
	// ran for them (see handlegroupme.go). Guarded by ghostRefreshMu.
	//
	// Needed because HandleTextMessage is called for every message polling
	// re-fetches every tick (poll.go), not just genuinely new ones -- see
	// its "safe to call unconditionally" reasoning, which is true for
	// message bridging (bridgev2 core dedupes by message ID) but was NOT
	// true for this refresh, which ran as a direct side effect before
	// bridgev2 ever got a chance to dedupe anything. Confirmed live: a
	// chat's most-recent ~20 messages can span a real nickname/avatar
	// change, so replaying that same page every poll tick made the
	// refresh flip back and forth between the old and new name/avatar
	// forever, once per message per tick, generating a real, ever-growing
	// stream of Matrix profile-change events (and, for avatars
	// specifically, real re-uploads to the media repo) for any active
	// sender -- see NOTES.md "Live incident: avatar flicker/reupload
	// storm" for the first (avatar-only, still incomplete) fix attempt.
	ghostRefreshMu   sync.Mutex
	ghostRefreshedAt map[string]time.Time
}

var _ bridgev2.NetworkAPI = (*GMClient)(nil)
var _ groupme.HandlerAll = (*GMClient)(nil)

func (gc *GMConnector) LoadUserLogin(ctx context.Context, login *bridgev2.UserLogin) error {
	meta, ok := login.Metadata.(*UserLoginMetadata)
	if !ok || meta == nil {
		meta = &UserLoginMetadata{}
		login.Metadata = meta
	}
	gcli := &GMClient{
		Main:      gc,
		UserLogin: login,
		Meta:      meta,
	}
	if meta.Token != "" {
		gcli.Client = groupmeext.NewClient(meta.Token)
	}
	login.Client = gcli
	return nil
}

func (gc *GMClient) Connect(ctx context.Context) {
	gc.sessionMu.Lock()
	defer gc.sessionMu.Unlock()
	gc.disconnectLocked()
	if ctx.Err() != nil {
		return
	}
	if gc.Client == nil || gc.Meta.Token == "" {
		gc.UserLogin.BridgeState.Send(status.BridgeState{
			StateEvent: status.StateBadCredentials,
			Error:      "groupme-not-logged-in",
		})
		return
	}
	runCtx, cancel := context.WithCancel(gc.UserLogin.Log.WithContext(gc.Main.br.BackgroundCtx))
	gc.sessionCancel = cancel
	done := make(chan struct{})
	gc.sessionDone = done
	gc.UserLogin.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnecting})
	go func() {
		defer close(done)
		defer cancel()
		gc.runSession(runCtx)
	}()
}

func (gc *GMClient) runSession(ctx context.Context) {
	me, err := gc.Client.MyUser(ctx)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		state := status.BridgeState{StateEvent: status.StateUnknownError, Error: "groupme-connect-failed"}
		var meta *groupme.Meta
		if errors.As(err, &meta) && (meta.Code == 401 || meta.Code == 403) {
			state.StateEvent = status.StateBadCredentials
			state.Error = "groupme-session-expired"
		}
		gc.UserLogin.BridgeState.Send(state)
		return
	}
	if me == nil || string(me.ID) != gc.Meta.GMID || string(me.ID) != string(gc.UserLogin.ID) {
		gc.UserLogin.BridgeState.Send(status.BridgeState{StateEvent: status.StateBadCredentials, Error: "groupme-account-mismatch"})
		return
	}
	sub := groupme.NewPushSubscription(ctx)
	sub.AddFullHandler(gc)
	sub.StartListening(ctx, groupmeext.NewWSFayeClient(gc.UserLogin.Log))
	var workers sync.WaitGroup
	workers.Add(3)
	go func() {
		defer workers.Done()
		if err := sub.SubscribeToUser(ctx, groupme.ID(gc.Meta.GMID), gc.Meta.Token); err != nil && ctx.Err() == nil {
			gc.UserLogin.Log.Err(err).Msg("Failed to subscribe to GroupMe push channel")
		}
	}()
	go func() { defer workers.Done(); gc.syncChats(ctx) }()
	go func() { defer workers.Done(); gc.pollMessages(ctx) }()
	gc.UserLogin.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnected})
	<-ctx.Done()
	sub.Wait()
	workers.Wait()
}

func (gc *GMClient) disconnectLocked() {
	if gc.sessionCancel != nil {
		gc.sessionCancel()
		<-gc.sessionDone
		gc.sessionCancel = nil
		gc.sessionDone = nil
	}
}

func (gc *GMClient) Disconnect() {
	gc.sessionMu.Lock()
	defer gc.sessionMu.Unlock()
	gc.disconnectLocked()
}

func (gc *GMClient) IsLoggedIn() bool {
	return gc.Client != nil && gc.Meta.Token != ""
}

func (gc *GMClient) LogoutRemote(ctx context.Context) {
	gc.Disconnect()
	gc.Meta.Token = ""
	gc.Client = nil
}

func (gc *GMClient) IsThisUser(ctx context.Context, userID networkid.UserID) bool {
	return string(userID) == gc.Meta.GMID
}

// portalKeyForGroup returns the portal key for a GroupMe group chat.
func (gc *GMClient) portalKeyForGroup(groupID groupme.ID) networkid.PortalKey {
	return networkid.PortalKey{
		ID:       MakeGroupPortalID(groupID),
		Receiver: gc.UserLogin.ID,
	}
}

// portalKeyForDM returns the portal key for a GroupMe direct message with
// the given other user.
func (gc *GMClient) portalKeyForDM(otherUserID groupme.ID) networkid.PortalKey {
	return networkid.PortalKey{
		ID:       MakeDMPortalID(otherUserID),
		Receiver: gc.UserLogin.ID,
	}
}

// portalKeyForMessage determines the portal key that an incoming push
// message belongs to. GroupMe group messages carry GroupID; direct messages
// only carry ConversationID/ChatID plus the sender/recipient user IDs.
func (gc *GMClient) portalKeyForMessage(msg *groupme.Message) networkid.PortalKey {
	if len(msg.GroupID) > 0 {
		return gc.portalKeyForGroup(msg.GroupID)
	}
	other := msg.RecipientID
	if msg.UserID != groupme.ID(gc.Meta.GMID) {
		other = msg.UserID
	}
	return gc.portalKeyForDM(other)
}

func (gc *GMClient) fatalError(err error, code status.BridgeStateErrorCode) {
	gc.UserLogin.BridgeState.Send(status.BridgeState{
		StateEvent: status.StateUnknownError,
		Error:      code,
		Message:    err.Error(),
	})
}
