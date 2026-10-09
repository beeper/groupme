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
	"strings"
	"sync"

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

	pushMu sync.Mutex
	push   *groupme.PushSubscription
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
	// Live pushes wait until catch-up is queued, so a gap is filled before
	// newer messages move the bridged history past it. Live messages only
	// arrive over push, so the login is connected only while the user channel is.
	userChannel := groupme.UserChannel(me.ID)
	var reactionRefreshes sync.WaitGroup
	sub := groupme.NewPushSubscription(ctx)
	sub.AddFullHandler(gc)
	sub.StartListening(ctx, groupmeext.NewWSFayeClient(gc.UserLogin.Log, func(ctx context.Context, channel string) {
		if channel != userChannel {
			return
		}
		chats := gc.catchUp(ctx)
		if ctx.Err() != nil {
			return
		}
		gc.UserLogin.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnected})
		reactionRefreshes.Add(1)
		go func() {
			defer reactionRefreshes.Done()
			gc.refreshRecentReactions(ctx, chats)
		}()
	}, func(err error) {
		gc.UserLogin.BridgeState.Send(status.BridgeState{
			StateEvent: status.StateTransientDisconnect,
			Error:      "groupme-push-disconnected",
			Message:    err.Error(),
		})
	}))
	gc.pushMu.Lock()
	gc.push = sub
	gc.pushMu.Unlock()
	if err := sub.SubscribeToUser(me.ID, gc.Meta.Token); err != nil {
		gc.UserLogin.Log.Err(err).Msg("Failed to subscribe to GroupMe push channel")
	}
	<-ctx.Done()
	gc.pushMu.Lock()
	gc.push = nil
	gc.pushMu.Unlock()
	sub.Wait()
	reactionRefreshes.Wait()
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
	self := groupme.ID(gc.Meta.GMID)
	conversationID := msg.ConversationID
	if conversationID == "" {
		conversationID = msg.ChatID
	}
	if conversationID != "" {
		participants := strings.Split(string(conversationID), "+")
		if len(participants) == 2 && participants[0] != "" && participants[1] != "" {
			if participants[0] == string(self) {
				return gc.portalKeyForDM(groupme.ID(participants[1]))
			}
			if participants[1] == string(self) {
				return gc.portalKeyForDM(groupme.ID(participants[0]))
			}
		}
		return networkid.PortalKey{}
	}
	if msg.UserID == self && msg.RecipientID != "" {
		return gc.portalKeyForDM(msg.RecipientID)
	}
	if msg.RecipientID == self && msg.UserID != "" {
		return gc.portalKeyForDM(msg.UserID)
	}
	return networkid.PortalKey{}
}
