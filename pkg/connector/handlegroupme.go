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
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/util/ptr"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/event"

	"github.com/beeper/groupme-lib"

	"github.com/beeper/groupme/pkg/groupmeext"
)

// GroupMe push handlers translate native events into bridgev2 remote events.

func (gc *GMClient) HandleError(err error) {
	gc.UserLogin.Log.Err(err).Msg("Error from GroupMe push subscription")
}

func (gc *GMClient) HandleTextMessage(msg groupme.Message) {
	if msg.ID == "" || msg.UserID == "" || gc.portalKeyForMessage(&msg).ID == "" {
		gc.UserLogin.Log.Warn().Msg("Ignoring malformed GroupMe push message")
		return
	}
	gc.Main.br.QueueRemoteEvent(gc.UserLogin, gc.makeRemoteMessage(msg))
	gc.subscribeToChat(gc.portalKeyForMessage(&msg).ID)
}

func (gc *GMClient) makeRemoteMessage(msg groupme.Message) *simplevent.Message[*groupme.Message] {
	portalKey := gc.portalKeyForMessage(&msg)
	sender := gc.messageSender(&msg)

	return &simplevent.Message[*groupme.Message]{
		EventMeta: simplevent.EventMeta{
			Type:         bridgev2.RemoteEventMessage,
			PortalKey:    portalKey,
			Sender:       sender,
			CreatePortal: true,
			Timestamp:    msg.CreatedAt.ToTime(),
		},
		ID:                 MakeMessageID(msg.ID),
		Data:               &msg,
		ConvertMessageFunc: gc.convertMessage,
	}
}

func (gc *GMClient) messageSender(msg *groupme.Message) bridgev2.EventSender {
	if msg.UserID == "system" || msg.System {
		return bridgev2.EventSender{}
	}
	return bridgev2.EventSender{IsFromMe: string(msg.UserID) == gc.Meta.GMID, Sender: MakeUserID(msg.UserID)}
}

func (gc *GMClient) convertMessage(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, msg *groupme.Message) (*bridgev2.ConvertedMessage, error) {
	// Message snapshots may fill a missing profile, never overwrite current
	// account-level data. Conversion runs inside the framework event path.
	if msg.Name != "" && string(msg.UserID) != gc.Meta.GMID && msg.UserID != "system" && !msg.System {
		ghost, err := gc.Main.br.GetGhostByID(ctx, MakeUserID(msg.UserID))
		if err != nil {
			return nil, err
		}
		info := &bridgev2.UserInfo{}
		if !ghostHasRealName(ghost) {
			info.Name = ptr.Ptr(msg.Name)
		}
		if msg.AvatarURL != "" && ghost.AvatarMXC == "" {
			info.Avatar = gc.avatarFor(ctx, msg.AvatarURL)
		}
		if info.Name != nil || info.Avatar != nil {
			ghost.UpdateInfo(ctx, info)
		}
	}
	return gc.convertGroupMeMessage(ctx, portal, intent, msg)
}

// convertGroupMeMessage builds the Matrix message parts for an incoming
// GroupMe message, including any attachments or poll event. token is the
// account's own GroupMe access token, needed by the video/file download
// paths (see groupmeext.DownloadVideo/DownloadFile) -- image attachments
// don't need it, GroupMe's image CDN is a plain public GET. client is
// needed separately to fetch a poll's full definition (see
// convertGroupMePollEvent/groupme.Client.GetPoll) -- unlike the raw HTTP
// download helpers, that's a normal authenticated groupme-lib API call.
func (gc *GMClient) convertGroupMeMessage(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, msg *groupme.Message) (*bridgev2.ConvertedMessage, error) {
	cm := &bridgev2.ConvertedMessage{}
	log := zerolog.Ctx(ctx)
	for _, att := range msg.Attachments {
		if att != nil && att.Type == groupme.Reply && att.ReplyID != "" {
			cm.ReplyTo = &networkid.MessageOptionalPartID{MessageID: MakeMessageID(att.ReplyID)}
			break
		}
	}

	// Poll events carry content in Event.Data; attachments only identify the
	// poll. Fall back to the native message text if conversion fails.
	if msg.Event != nil && strings.HasPrefix(msg.Event.Type, "poll.") {
		if part := convertGroupMePollEvent(ctx, gc.Client, msg); part != nil {
			cm.Parts = append(cm.Parts, part)
			return cm, nil
		}
		log.Warn().Str("event_type", msg.Event.Type).Msg("Failed to convert GroupMe poll event, falling back to bare message text")
	}

	for i, att := range msg.Attachments {
		if att == nil {
			continue
		}
		partID := networkid.PartID(fmt.Sprintf("attachment-%d", i))
		var content *event.MessageEventContent
		switch att.Type {
		case groupme.Image, groupme.Video, groupme.File:
			if att.Type == groupme.File && msg.GroupID == "" {
				log.Warn().Msg("Ignoring GroupMe file attachment outside a group")
				continue
			}

			var data []byte
			var mime string
			var err error
			filename := string(att.Type)
			switch att.Type {
			case groupme.Image:
				data, mime, err = groupmeext.DownloadImage(ctx, att.URL)
			case groupme.Video:
				data, mime, err = groupmeext.DownloadVideo(ctx, att.URL, gc.Meta.Token)
			case groupme.File:
				data, filename, mime, err = groupmeext.DownloadFile(ctx, msg.GroupID, att.FileID, gc.Meta.Token)
			}
			if err != nil {
				log.Warn().Err(err).Msg("Failed to download GroupMe attachment")
				cm.Parts = append(cm.Parts, failedAttachment(partID))
				continue
			}
			if mime == "" {
				mime = http.DetectContentType(data)
			}
			mxc, file, err := intent.UploadMedia(ctx, portal.MXID, data, filename, mime)
			if err != nil {
				return nil, fmt.Errorf("upload GroupMe attachment: %w", err)
			}
			content = &event.MessageEventContent{
				MsgType: event.MessageType("m." + att.Type),
				Body:    filename,
				Info:    &event.FileInfo{MimeType: mime, Size: len(data)},
			}
			if file != nil {
				content.File = file
			} else {
				content.URL = mxc
			}

		case groupme.Location:
			lat, latErr := strconv.ParseFloat(att.Latitude, 64)
			lng, lngErr := strconv.ParseFloat(att.Longitude, 64)
			if latErr != nil || lngErr != nil {
				log.Warn().Str("lat", att.Latitude).Str("lng", att.Longitude).
					Msg("Failed to parse GroupMe location attachment coordinates")
				continue
			}
			name := att.Name
			if name == "" {
				name = "Location"
			}
			content = &event.MessageEventContent{
				MsgType: event.MsgLocation,
				Body:    fmt.Sprintf("%s: %.5f,%.5f", name, lat, lng),
				GeoURI:  fmt.Sprintf("geo:%.5f,%.5f", lat, lng),
			}

		case groupme.Poll:
			// Handled through Event.Data above.
			continue
		case groupme.Reply:
			continue

		default:
			// Mentions and custom emoji retain their plaintext fallback.
			continue
		}

		cm.Parts = append(cm.Parts, &bridgev2.ConvertedMessagePart{
			ID:      partID,
			Type:    event.EventMessage,
			Content: content,
		})
	}

	if len(msg.Text) > 0 || len(cm.Parts) == 0 {
		cm.Parts = append(cm.Parts, &bridgev2.ConvertedMessagePart{
			ID:   "",
			Type: event.EventMessage,
			Content: &event.MessageEventContent{
				MsgType: event.MsgText,
				Body:    msg.Text,
			},
		})
	}

	cm.MergeCaption()
	return cm, nil
}

func failedAttachment(partID networkid.PartID) *bridgev2.ConvertedMessagePart {
	return &bridgev2.ConvertedMessagePart{ID: partID, Type: event.EventMessage, Content: &event.MessageEventContent{
		MsgType: event.MsgNotice, Body: "Could not download this GroupMe attachment. View it in GroupMe.",
	}}
}

// convertGroupMePollEvent renders a poll.created/poll.reminder/
// poll.finished event (see json.go's Event/PollEventData) as a single
// readable text message part. Returns nil if the event's Data couldn't be
// parsed or its Type isn't one of the three handled here, so the caller
// can fall back to GroupMe's own plain-text notice instead of dropping
// the message.
//
// Matrix has a native poll event type (MSC3381) that Element and some
// other clients render as an interactive, votable widget; this
// deliberately doesn't use it. Two-way vote sync (a Matrix poll vote ->
// GroupMe's vote API, and GroupMe vote changes -> updating the Matrix
// poll's state) would be a substantially bigger feature -- tracking poll
// state across both sides, handling a poll closing on either end, etc --
// and hasn't been attempted here; this only makes the poll's
// question/options/results legible as a normal message, matching the
// scope of every other attachment type this bridge bridges one-way.
func convertGroupMePollEvent(ctx context.Context, client *groupmeext.Client, msg *groupme.Message) *bridgev2.ConvertedMessagePart {
	log := zerolog.Ctx(ctx)

	var data groupme.PollEventData
	if err := json.Unmarshal(msg.Event.Data, &data); err != nil {
		log.Warn().Err(err).Str("event_type", msg.Event.Type).Msg("Failed to parse GroupMe poll event data")
		return nil
	}

	var body string
	switch msg.Event.Type {
	case "poll.created":
		poll, err := client.GetPoll(ctx, data.Conversation.ID, data.Poll.ID)
		if err != nil {
			// Best-effort: GroupMe's own notice text already mentions the
			// poll by name, so a failed detail fetch degrades to
			// "here's a poll, go look at it in the app" instead of losing
			// the message entirely.
			log.Warn().Err(err).Str("poll_id", data.Poll.ID).Msg("Failed to fetch GroupMe poll details, falling back to bare subject")
			body = fmt.Sprintf("📊 New poll: \"%s\"\nVote in the GroupMe app.", data.Poll.Subject)
			break
		}
		var b strings.Builder
		fmt.Fprintf(&b, "📊 New poll: \"%s\"\n", poll.Subject)
		for _, opt := range poll.Options {
			fmt.Fprintf(&b, "• %s\n", opt.Title)
		}
		visibility := poll.Visibility
		if visibility == "" {
			visibility = "anonymous"
		}
		fmt.Fprintf(&b, "\nVote in the GroupMe app (%s voting)", visibility)
		if poll.Expiration > 0 {
			fmt.Fprintf(&b, ". Closes %s.", poll.Expiration.ToTime().Local().Format("Jan 2, 3:04 PM"))
		} else {
			b.WriteString(".")
		}
		body = b.String()

	case "poll.finished":
		var b strings.Builder
		fmt.Fprintf(&b, "📊 Poll ended: \"%s\"\n", data.Poll.Subject)
		for _, opt := range data.Options {
			plural := "s"
			if opt.Votes == 1 {
				plural = ""
			}
			fmt.Fprintf(&b, "• %s — %d vote%s\n", opt.Title, opt.Votes, plural)
		}
		body = strings.TrimRight(b.String(), "\n")

	case "poll.reminder":
		body = fmt.Sprintf("📊 Poll \"%s\" is about to close.", data.Poll.Subject)

	default:
		// Some other poll.* event type not seen live (GroupMe's poll
		// lifecycle has only ever been observed to emit these three) --
		// rather than guess at a format, let the caller fall back to
		// GroupMe's own message text.
		return nil
	}

	return &bridgev2.ConvertedMessagePart{
		ID:   "",
		Type: event.EventMessage,
		Content: &event.MessageEventContent{
			MsgType: event.MsgText,
			Body:    body,
		},
	}
}

// HandleLike receives a favorite event's complete reaction snapshot, bridged
// through the SDK's reaction resync. Individual like.create/like.delete changes
// use HandleReaction instead.
//
// GroupMe now supports full per-emoji reactions (confirmed live against
// the real API: msg.Reactions is a list of {emoji code, user_ids} pairs,
// e.g. a "\U0001F44D" (👍) entry with its own reactor list, separate from
// any "❤️" (❤️) entry on the same message). msg.Reactions is a
// local addition to the pinned groupme-lib dependency, which predates this
// GroupMe feature entirely -- see thirdparty/groupme-lib/json.go.
// FavoritedBy (the older, single-undifferentiated-like field) is kept only
// as a fallback for payloads that might not carry the newer field (e.g.
// possibly some live-push payload shapes, unverified) so a like is never
// silently dropped -- but it can only ever be represented as a generic ❤,
// since it doesn't say which emoji was actually used.
func (gc *GMClient) HandleLike(msg groupme.Message) {
	if msg.ID == "" || gc.portalKeyForMessage(&msg).ID == "" {
		return
	}
	gc.Main.br.QueueRemoteEvent(gc.UserLogin, &simplevent.ReactionSync{
		EventMeta:     simplevent.EventMeta{Type: bridgev2.RemoteEventReactionSync, PortalKey: gc.portalKeyForMessage(&msg), Timestamp: time.Now()},
		TargetMessage: MakeMessageID(msg.ID), Reactions: gc.messageReactions(&msg),
	})
}

// Modern like.create/like.delete pushes carry an individual user's change.
// The empty EmojiID identifies their single reaction, allowing bridgev2 to
// replace its emoji or remove it without affecting anyone else's reaction.
func (gc *GMClient) HandleReaction(msg groupme.Message, userID groupme.ID, reaction *groupme.Reaction) {
	portalKey := gc.portalKeyForMessage(&msg)
	if msg.ID == "" || userID == "" || portalKey.ID == "" {
		return
	}
	evt := &simplevent.Reaction{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventReactionRemove,
			PortalKey: portalKey,
			Sender:    bridgev2.EventSender{IsFromMe: string(userID) == gc.Meta.GMID, Sender: MakeUserID(userID)},
			Timestamp: time.Now(),
		},
		TargetMessage: MakeMessageID(msg.ID),
	}
	if reaction != nil {
		evt.Type = bridgev2.RemoteEventReaction
		evt.Emoji = reaction.Code
	}
	gc.Main.br.QueueRemoteEvent(gc.UserLogin, evt)
}

func (gc *GMClient) messageReactions(msg *groupme.Message) *bridgev2.ReactionSyncData {
	users := make(map[networkid.UserID]*bridgev2.ReactionSyncUser)

	if msg.Reactions != nil {
		for _, r := range msg.Reactions {
			if r.Code == "" {
				continue
			}
			for _, userIDStr := range r.UserIDs {
				uid := MakeUserID(groupme.ID(userIDStr))
				u, ok := users[uid]
				if !ok {
					u = &bridgev2.ReactionSyncUser{HasAllReactions: true}
					users[uid] = u
				}
				u.Reactions = append(u.Reactions, &bridgev2.BackfillReaction{
					Sender: bridgev2.EventSender{
						IsFromMe: groupme.ID(userIDStr) == groupme.ID(gc.Meta.GMID),
						Sender:   uid,
					},
					Emoji: r.Code,
				})
			}
		}
	} else {
		for _, userIDStr := range msg.FavoritedBy {
			uid := MakeUserID(groupme.ID(userIDStr))
			users[uid] = &bridgev2.ReactionSyncUser{
				HasAllReactions: true,
				Reactions: []*bridgev2.BackfillReaction{{
					Sender: bridgev2.EventSender{
						IsFromMe: groupme.ID(userIDStr) == groupme.ID(gc.Meta.GMID),
						Sender:   uid,
					},
					Emoji: "❤",
				}},
			}
		}
	}

	return &bridgev2.ReactionSyncData{Users: users, HasAllUsers: true}
}

func (gc *GMClient) HandleJoin(id groupme.ID) {
	gc.resyncGroup(id)
	gc.subscribeToChat(MakeGroupPortalID(id))
}

func (gc *GMClient) HandleGroupName(group groupme.ID, newName string) {
	gc.resyncGroup(group)
}

func (gc *GMClient) HandleGroupTopic(group groupme.ID, newTopic string) {
	gc.resyncGroup(group)
}

func (gc *GMClient) HandleGroupAvatar(group groupme.ID, newAvatar string) {
	gc.resyncGroup(group)
}

func (gc *GMClient) HandleLikeIcon(group groupme.ID, packID, packIndex int, typ string) {
	// Custom "like" icons aren't represented on the Matrix side yet.
}

func (gc *GMClient) HandleNewNickname(group, user groupme.ID, name string) {
	gc.resyncGroup(group)
}

func (gc *GMClient) HandleNewAvatarInGroup(group, user groupme.ID, url string) {
	gc.resyncGroup(group)
}

func (gc *GMClient) HandleMembers(group groupme.ID, members []groupme.Member, added bool) {
	gc.resyncGroup(group)
}

// resyncGroup queues a full chat-info resync for a GroupMe group. Several
// push events (name/topic/avatar/membership changes) don't carry enough
// information to apply an incremental update, so the legacy bridge always
// refetched the whole group; this preserves that behavior.
func (gc *GMClient) resyncGroup(id groupme.ID) {
	gc.Main.br.QueueRemoteEvent(gc.UserLogin, &simplevent.ChatResync{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventChatResync,
			PortalKey: gc.portalKeyForGroup(id),
			Timestamp: time.Now(),
		},
		GetChatInfoFunc: gc.GetChatInfo,
	})
}
