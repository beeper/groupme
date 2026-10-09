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
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"

	"github.com/beeper/groupme-lib"
)

type nativeChat struct {
	portalKey networkid.PortalKey
	latestID  groupme.ID
	dmPartner *groupme.User
}

// A failed listing does not prevent returning the other conversation type.
func (gc *GMClient) listChats(ctx context.Context) []nativeChat {
	log := zerolog.Ctx(ctx)
	var chats []nativeChat

	groups, err := gc.Client.IndexAllGroups(ctx)
	if err != nil {
		log.Err(err).Msg("Failed to list GroupMe groups")
	}
	for _, group := range groups {
		if group != nil && len(group.ID) > 0 {
			chats = append(chats, nativeChat{portalKey: gc.portalKeyForGroup(group.ID), latestID: group.Messages.LastMessageID})
		}
	}

	dms, err := gc.Client.IndexAllChats(ctx)
	if err != nil {
		log.Err(err).Msg("Failed to list GroupMe DM chats")
	}
	for _, dm := range dms {
		if dm == nil || len(dm.OtherUser.ID) == 0 {
			continue
		}
		var latestID groupme.ID
		if dm.LastMessage != nil {
			latestID = dm.LastMessage.ID
		}
		chats = append(chats, nativeChat{portalKey: gc.portalKeyForDM(dm.OtherUser.ID), latestID: latestID, dmPartner: &dm.OtherUser})
	}
	return chats
}

// catchUp runs on user-channel subscription, including reconnects, while that
// channel's live pushes are held. It only queues work so they are not held for
// long. DM info reuses the listing rather than paging it again for every DM.
func (gc *GMClient) catchUp(ctx context.Context) []nativeChat {
	ctx = gc.UserLogin.Log.With().Str("action", "catch up").Logger().WithContext(ctx)
	chats := gc.listChats(ctx)
	zerolog.Ctx(ctx).Info().Int("chat_count", len(chats)).Msg("Syncing GroupMe chats")
	for _, chat := range chats {
		if ctx.Err() != nil {
			return nil
		}
		getChatInfo := gc.GetChatInfo
		if chat.dmPartner != nil {
			getChatInfo = func(ctx context.Context, portal *bridgev2.Portal) (*bridgev2.ChatInfo, error) {
				return gc.dmChatInfo(ctx, chat.dmPartner.ID, chat.dmPartner), nil
			}
		}
		gc.Main.br.QueueRemoteEvent(gc.UserLogin, &simplevent.ChatResync{
			EventMeta: simplevent.EventMeta{
				Type:         bridgev2.RemoteEventChatResync,
				PortalKey:    chat.portalKey,
				CreatePortal: true,
				Timestamp:    time.Now(),
			},
			GetChatInfoFunc: getChatInfo,
			CheckNeedsBackfillFunc: func(ctx context.Context, latest *database.Message) (bool, error) {
				return chat.latestID != "" && (latest == nil || compareMessageIDs(chat.latestID, ParseMessageID(latest.ID)) > 0), nil
			},
		})
		gc.subscribeToChat(chat.portalKey.ID)
	}
	return chats
}

// Appservices never receive chat-view notifications. Keep the native group and
// DM channels subscribed for all discovered chats instead of depending on UI
// focus. WSFayeClient retains these subscriptions across reconnects.
func (gc *GMClient) subscribeToChat(portalID networkid.PortalID) {
	gc.pushMu.Lock()
	defer gc.pushMu.Unlock()
	if gc.push == nil {
		return
	}
	var err error
	kind, chatID := ParsePortalID(portalID)
	switch kind {
	case PortalTypeGroup:
		err = gc.push.SubscribeToGroup(chatID, gc.Meta.Token)
	case PortalTypeDM:
		err = gc.push.SubscribeToDM(DMConversationID(groupme.ID(gc.Meta.GMID), chatID), gc.Meta.Token)
	}
	if err != nil {
		gc.UserLogin.Log.Err(err).Str("portal_id", string(portalID)).Msg("Failed to subscribe to GroupMe chat channel")
	}
}

// The native stream does not replay missed reactions, and an unchanged newest
// message ID says nothing about reaction changes. Refresh one recent page per
// chat as Web does on connection recovery, independently of message backfill.
func (gc *GMClient) refreshRecentReactions(ctx context.Context, chats []nativeChat) {
	ctx = gc.UserLogin.Log.With().Str("action", "refresh reactions").Logger().WithContext(ctx)
	for _, chat := range chats {
		if ctx.Err() != nil {
			return
		}
		portalID := chat.portalKey.ID
		// Groups support 100 messages; DMs return their native page of 20.
		messages, err := gc.fetchMessagePage(ctx, portalID, "", "", 100)
		if err != nil {
			zerolog.Ctx(ctx).Err(err).Str("portal_id", string(portalID)).Msg("Failed to refresh recent GroupMe reactions")
			continue
		}
		for _, msg := range messages {
			if msg.Reactions != nil || msg.FavoritedBy != nil {
				gc.HandleLike(*msg)
			}
		}
		zerolog.Ctx(ctx).Debug().Str("portal_id", string(portalID)).Int("message_count", len(messages)).Msg("Refreshed recent GroupMe reactions")
	}
}
