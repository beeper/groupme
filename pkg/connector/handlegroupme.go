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

// This file implements groupme.HandlerAll, converting GroupMe's real-time
// push events (delivered over the Faye/Bayeux channel via pkg/groupmeext)
// into bridgev2 remote events. It is a port of the equivalent logic that
// used to live in user.go/portal.go before the bridgev2 migration.

func (gc *GMClient) HandleError(err error) {
	gc.UserLogin.Log.Err(err).Msg("Error from GroupMe push subscription")
}

// ghostRefreshCooldown bounds how often HandleTextMessage's opportunistic
// ghost name/avatar refresh (below) can actually run for a given sender,
// regardless of how many messages mention them. See shouldRefreshGhost
// and the call site's doc comment for why this exists -- in short,
// without it, REST polling replaying old messages every tick could flip
// a ghost's name/avatar back and forth forever.
const ghostRefreshCooldown = 10 * time.Minute

// shouldRefreshGhost reports whether HandleTextMessage's opportunistic
// ghost refresh should actually run for gmid right now, and if so records
// that it did. Callers should treat a false return as "skip this time",
// not an error.
func (gc *GMClient) shouldRefreshGhost(gmid groupme.ID) bool {
	gc.ghostRefreshMu.Lock()
	defer gc.ghostRefreshMu.Unlock()
	if gc.ghostRefreshedAt == nil {
		gc.ghostRefreshedAt = make(map[string]time.Time)
	}
	if last, ok := gc.ghostRefreshedAt[gmid.String()]; ok && time.Since(last) < ghostRefreshCooldown {
		return false
	}
	gc.ghostRefreshedAt[gmid.String()] = time.Now()
	return true
}

func (gc *GMClient) HandleTextMessage(msg groupme.Message) {
	portalKey := gc.portalKeyForMessage(&msg)
	sender := bridgev2.EventSender{
		IsFromMe: msg.UserID == groupme.ID(gc.Meta.GMID),
		Sender:   MakeUserID(msg.UserID),
	}

	// Every GroupMe message carries the sender's name as of when it was
	// sent (msg.Name), often also an avatar (msg.AvatarURL). GetChatInfo's
	// group-member sync (chatinfo.go) only knows about *current* group
	// members via their per-group nickname, so anyone who has since left a
	// group -- or whose membership sync hasn't run yet -- would otherwise
	// only ever get a raw-numeric-ID ghost name. Opportunistically refresh
	// the ghost here too, on every message, as a second source that also
	// covers former members. Best-effort and non-blocking: message
	// delivery must not wait on this.
	//
	// Three real production bugs found in this one feature so far, all
	// from the same underlying mistake -- treating "a message mentions
	// this sender" as if it meant "this is fresh, current info about this
	// sender", when a message is really a snapshot from whenever it was
	// *sent*, and this function runs for every message polling re-fetches
	// every tick (poll.go), not just genuinely new ones:
	//
	//  1. msg.AvatarURL is only ever passed through when non-empty --
	//     confirmed live that GroupMe does NOT reliably echo it on every
	//     message even for senders who do have a real profile picture set.
	//     Passing avatarFor("") unconditionally caused a real erase/restore
	//     flicker every time an avatar-url-less message got (re)processed.
	//     UserInfo.Avatar left nil means "don't touch the avatar" (see
	//     bridgev2 Ghost.UpdateInfo), not "remove it".
	//  2. gc.shouldRefreshGhost throttles this to once per sender per
	//     cooldown window -- fixing (1) alone wasn't enough, because
	//     polling replays the same recent-messages page every 60s forever,
	//     and that page can span a real nickname/avatar change; without a
	//     cooldown, every poll tick re-walks the same span of old/new
	//     snapshots and flips the ghost back and forth between them, once
	//     per message per tick, indefinitely.
	//  3. Even with the cooldown, a genuinely bad snapshot could still get
	//     through and *persist* for the whole cooldown window: confirmed
	//     live, two real senders had a years-old message on record with
	//     GroupMe's own literal "GroupMe" as the recorded sender name
	//     (apparently a real GroupMe-side data artifact from whenever
	//     those messages were originally sent), and polling replaying that
	//     one old message was enough to overwrite their ghost's real name
	//     with "GroupMe" every time the cooldown lapsed. The cooldown
	//     controls *frequency*; it does nothing about *correctness* when
	//     the stale snapshot itself is bad. Fixed at the root instead of
	//     patching around it further: skip the refresh entirely unless
	//     this specific message hasn't been bridged before (i.e. this is
	//     the message's first time through this function, live push or
	//     first poll sighting -- never a replay). A message already in the
	//     DB is, by definition, not fresh information about its sender no
	//     matter what it says.
	//     See NOTES.md "Live incident: avatar flicker/reupload storm,
	//     take three" for the full incident -- discovered mid-cleanup of
	//     the historical spam these earlier attempts left behind.
	if msg.Name != "" && msg.UserID != groupme.ID(gc.Meta.GMID) {
		go func(gmid groupme.ID, name, avatarURL string, id networkid.MessageID) {
			ctx := context.Background()
			if existing, err := gc.Main.br.DB.Message.GetAllPartsByID(ctx, gc.UserLogin.ID, id); err != nil {
				gc.UserLogin.Log.Warn().Err(err).Str("gmid", string(gmid)).
					Msg("Failed to check if message is new before opportunistic ghost refresh, skipping to be safe")
				return
			} else if len(existing) > 0 {
				// Already bridged -- this is polling replaying old
				// history, not a genuinely new message. Its sender
				// snapshot is stale by definition; see bug 3 above.
				return
			}
			if !gc.shouldRefreshGhost(gmid) {
				return
			}
			ghost, err := gc.Main.br.GetGhostByID(ctx, MakeUserID(gmid))
			if err != nil {
				gc.UserLogin.Log.Warn().Err(err).Str("gmid", string(gmid)).
					Msg("Failed to get ghost for opportunistic name refresh from message")
				return
			}
			// 4. Fill gaps only. A message's name/avatar are the sender's
			//    *per-group* nickname and picture, so even a genuinely
			//    new message would flip the one shared profile between
			//    groups (see the identity note in chatinfo.go). The group
			//    and DM resyncs own the account-level name/avatar.
			info := &bridgev2.UserInfo{}
			if !ghostHasRealName(ghost) {
				info.Name = ptr.Ptr(name)
			}
			if avatarURL != "" && ghost.AvatarMXC == "" && !avatarAlreadyFailed(ghost, avatarURL) {
				info.Avatar = avatarFor(avatarURL)
			}
			if info.Name == nil && info.Avatar == nil {
				return
			}
			ghost.UpdateInfo(ctx, info)
		}(msg.UserID, msg.Name, msg.AvatarURL, MakeMessageID(msg.ID))
	}

	gc.Main.br.QueueRemoteEvent(gc.UserLogin, &simplevent.Message[*groupme.Message]{
		EventMeta: simplevent.EventMeta{
			Type:         bridgev2.RemoteEventMessage,
			PortalKey:    portalKey,
			Sender:       sender,
			CreatePortal: true,
			Timestamp:    msg.CreatedAt.ToTime(),
		},
		ID:   MakeMessageID(msg.ID),
		Data: &msg,
		ConvertMessageFunc: func(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, data *groupme.Message) (*bridgev2.ConvertedMessage, error) {
			return convertGroupMeMessage(ctx, portal, intent, data, gc.Client, gc.Meta.Token)
		},
	})
}

// convertGroupMeMessage builds the Matrix message parts for an incoming
// GroupMe message, including any attachments or poll event. token is the
// account's own GroupMe access token, needed by the video/file download
// paths (see groupmeext.DownloadVideo/DownloadFile) -- image attachments
// don't need it, GroupMe's image CDN is a plain public GET. client is
// needed separately to fetch a poll's full definition (see
// convertGroupMePollEvent/groupme.Client.GetPoll) -- unlike the raw HTTP
// download helpers, that's a normal authenticated groupme-lib API call.
func convertGroupMeMessage(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, msg *groupme.Message, client *groupmeext.Client, token string) (*bridgev2.ConvertedMessage, error) {
	cm := &bridgev2.ConvertedMessage{}
	log := zerolog.Ctx(ctx)

	// Poll lifecycle messages (poll.created/poll.reminder/poll.finished)
	// carry their real content in Event.Data, not as a normal attachment
	// -- the "poll" attachment some of them also carry (see the Poll
	// attachmentType's doc comment) is just a pointer, not enough on its
	// own to render anything useful. Handle these separately and return
	// early; falling through to the attachment loop below would either
	// skip the poll attachment as unhandled (poll.created) or find no
	// attachments at all (poll.reminder/poll.finished have none), in both
	// cases falling back to GroupMe's own bare-text notice
	// ("Created new poll 'X'") instead of the actual question/options/
	// results.
	if msg.Event != nil && strings.HasPrefix(msg.Event.Type, "poll.") {
		if part := convertGroupMePollEvent(ctx, client, msg); part != nil {
			cm.Parts = append(cm.Parts, part)
			return cm, nil
		}
		log.Warn().Str("event_type", msg.Event.Type).Msg("Failed to convert GroupMe poll event, falling back to bare message text")
	}

	for i, att := range msg.Attachments {
		partID := networkid.PartID(fmt.Sprintf("attachment-%d", i))
		var content *event.MessageEventContent

		switch att.Type {
		case groupme.Image:
			imgData, mime, err := groupmeext.DownloadImage(att.URL)
			if err != nil {
				log.Warn().Err(err).Msg("Failed to download GroupMe image attachment")
				continue
			}
			mxc, file, err := intent.UploadMedia(ctx, portal.MXID, *imgData, "image", mime)
			if err != nil {
				log.Warn().Err(err).Msg("Failed to upload GroupMe image attachment to Matrix media repo")
				continue
			}
			content = &event.MessageEventContent{
				MsgType: event.MsgImage,
				Body:    "image",
				Info: &event.FileInfo{
					MimeType: mime,
					Size:     len(*imgData),
				},
			}
			if file != nil {
				content.File = file
			} else {
				content.URL = mxc
			}

		case groupme.Video:
			vidData, mime, err := groupmeext.DownloadVideo(att.URL, token)
			if err != nil {
				log.Warn().Err(err).Msg("Failed to download GroupMe video attachment")
				continue
			}
			mxc, file, err := intent.UploadMedia(ctx, portal.MXID, vidData, "video", mime)
			if err != nil {
				log.Warn().Err(err).Msg("Failed to upload GroupMe video attachment to Matrix media repo")
				continue
			}
			content = &event.MessageEventContent{
				MsgType: event.MsgVideo,
				Body:    "video",
				Info: &event.FileInfo{
					MimeType: mime,
					Size:     len(vidData),
				},
			}
			if file != nil {
				content.File = file
			} else {
				content.URL = mxc
			}

		case groupme.File:
			// GroupMe's file-sharing feature is group-only (no known DM
			// equivalent), and its download API is keyed by group ID, not
			// conversation ID -- msg.GroupID is empty for DMs, so this
			// deliberately no-ops rather than guessing a wrong ID for
			// something that shouldn't be reachable from a DM anyway.
			if len(msg.GroupID) == 0 {
				log.Warn().Str("file_id", att.FileID).Msg("Got a GroupMe file attachment outside a group chat, don't know how to fetch it")
				continue
			}
			fileData, filename, mime, err := groupmeext.DownloadFile(msg.GroupID, att.FileID, token)
			if err != nil {
				log.Warn().Err(err).Msg("Failed to download GroupMe file attachment")
				continue
			}
			if mime == "" {
				mime = http.DetectContentType(fileData)
			}
			mxc, file, err := intent.UploadMedia(ctx, portal.MXID, fileData, filename, mime)
			if err != nil {
				log.Warn().Err(err).Msg("Failed to upload GroupMe file attachment to Matrix media repo")
				continue
			}
			content = &event.MessageEventContent{
				MsgType: event.MsgFile,
				Body:    filename,
				Info: &event.FileInfo{
					MimeType: mime,
					Size:     len(fileData),
				},
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
			// Handled above via msg.Event, before this loop even starts --
			// reaching this case means that handling didn't produce a
			// part (e.g. Event was nil/malformed for some reason), so
			// there's nothing useful to do with just the poll ID here.
			continue

		default:
			// Mentions/Emoji/Reply attachments ride alongside msg.Text
			// rather than needing their own message part (mentions are
			// just formatting metadata over the text; a reply's quoted
			// content isn't bridged as a separate part here -- see
			// NOTES.md "Known gaps"), and anything genuinely unknown is
			// safe to just skip rather than fail the whole message over.
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

	return cm, nil
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

// HandleLike is called when GroupMe reports that a message's reactions
// changed. GroupMe doesn't tell us who added/removed which reaction, just
// the resulting state, so this is bridged as a full reaction resync for
// the message.
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
	portalKey := gc.portalKeyForMessage(&msg)
	users := make(map[networkid.UserID]*bridgev2.ReactionSyncUser)

	if len(msg.Reactions) > 0 {
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

	gc.Main.br.QueueRemoteEvent(gc.UserLogin, &simplevent.ReactionSync{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventReactionSync,
			PortalKey: portalKey,
			Timestamp: time.Now(),
		},
		TargetMessage: MakeMessageID(msg.ID),
		Reactions: &bridgev2.ReactionSyncData{
			Users:       users,
			HasAllUsers: true,
		},
	})
}

func (gc *GMClient) HandleJoin(id groupme.ID) {
	gc.resyncGroup(id)
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
