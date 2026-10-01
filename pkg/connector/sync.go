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

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
)

// This file implements an initial sync of a user's existing GroupMe groups
// and direct-message chats, run once per Connect. Without this, portals are
// only ever created reactively in response to live push events arriving
// over the Faye/Bayeux connection (see handlegroupme.go), which means a
// freshly logged-in user who never gets a live push (e.g. because the push
// connection itself is down, or simply because nobody messaged them yet)
// never gets any portal rooms at all, even for chats they've had for years.
//
// This mirrors the "list everything from the REST API, then resync each
// chat" pattern used by other bridgev2 network connectors (e.g.
// mautrix/gmessages) for their equivalent of a startup conversation list
// sync.

// syncChats lists the user's GroupMe groups and DM chats via the REST API
// and queues a ChatResync remote event (with CreatePortal set) for each one,
// so bridgev2 creates/updates a portal for every existing chat rather than
// only ones that happen to receive a live push event after this point.
//
// Errors fetching one list (groups or chats) don't prevent the other from
// being synced; both are best-effort since this is a background convenience
// sync, not something the rest of Connect should fail over.
func (gc *GMClient) syncChats(ctx context.Context) {
	log := gc.UserLogin.Log.With().Str("action", "initial chat sync").Logger()

	groups, err := gc.Client.IndexAllGroups(ctx)
	if err != nil {
		log.Err(err).Msg("Failed to list GroupMe groups for initial sync")
	} else {
		log.Info().Int("group_count", len(groups)).Msg("Syncing GroupMe groups")
		for _, group := range groups {
			if len(group.ID) == 0 {
				continue
			}
			gc.queueChatResync(gc.portalKeyForGroup(group.ID))
		}
	}

	chats, err := gc.Client.IndexAllChats(ctx)
	if err != nil {
		log.Err(err).Msg("Failed to list GroupMe DM chats for initial sync")
	} else {
		log.Info().Int("chat_count", len(chats)).Msg("Syncing GroupMe DM chats")
		for _, chat := range chats {
			if len(chat.OtherUser.ID) == 0 {
				continue
			}
			gc.queueChatResync(gc.portalKeyForDM(chat.OtherUser.ID))
		}
	}
}

// queueChatResync queues a ChatResync remote event for the given portal,
// creating the portal if it doesn't exist yet. This reuses the same
// GetChatInfo-based resync used for live push-triggered resyncs (see
// resyncGroup in handlegroupme.go); the only functional difference is that
// CreatePortal is set here, since a chat found only by this initial sync
// may not have a portal yet at all.
func (gc *GMClient) queueChatResync(portalKey networkid.PortalKey) {
	gc.Main.br.QueueRemoteEvent(gc.UserLogin, &simplevent.ChatResync{
		EventMeta: simplevent.EventMeta{
			Type:         bridgev2.RemoteEventChatResync,
			PortalKey:    portalKey,
			CreatePortal: true,
			Timestamp:    time.Now(),
		},
		GetChatInfoFunc: gc.GetChatInfo,
	})
}
