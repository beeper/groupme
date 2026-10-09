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

	"go.mau.fi/util/ptr"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"github.com/beeper/groupme-lib"

	"github.com/beeper/groupme/pkg/groupmeext"
)

func (gc *GMClient) avatarFor(ctx context.Context, url string) *bridgev2.Avatar {
	if url == "" {
		return &bridgev2.Avatar{ID: "remove", Remove: true}
	}
	return &bridgev2.Avatar{
		ID: networkid.AvatarID(url),
		Get: func(ctx context.Context) ([]byte, error) {
			data, _, err := groupmeext.DownloadImage(ctx, url)
			return data, err
		},
	}
}

// avatarIfSet is avatarFor, except that an empty URL means "no information,
// leave the avatar alone" (nil) rather than "remove it". Used everywhere a
// person's avatar comes from a source that can't be trusted to mean "this
// person has no picture" when it's blank.
func (gc *GMClient) avatarIfSet(ctx context.Context, url string) *bridgev2.Avatar {
	if url == "" {
		return nil
	}
	return gc.avatarFor(ctx, url)
}

// avatarAlreadyFailed reports whether this exact avatar URL was already
// tried for the ghost and couldn't be downloaded: bridgev2 records the
// avatar ID even when the reupload fails, leaving no MXC. Without this, a
// permanently dead GroupMe picture link (i.groupme.com 403s for old,
// removed pictures) would be re-downloaded and fail on every resync.
func avatarAlreadyFailed(ghost *bridgev2.Ghost, url string) bool {
	return ghost != nil && ghost.AvatarMXC == "" && string(ghost.AvatarID) == url
}

// ghostHasRealName reports whether a ghost already has a proper name, as
// opposed to none or the raw-numeric-ID fallback.
func ghostHasRealName(ghost *bridgev2.Ghost) bool {
	return ghost != nil && ghost.Name != "" && ghost.Name != string(ghost.ID)
}

// A person has exactly one Matrix profile (their ghost), shared by every
// room, but GroupMe gives them a separate nickname and avatar in every
// group. Letting each group's resync write its own nickname/avatar into the
// one shared profile made it flip between groups on every restart --
// confirmed live on 2026-09-25: 138 profile writes in one restart, e.g.
// "AJ" <-> "AJ Ball", "Baker Long 2" <-> "Baker Long", avatars swapping
// between two different per-group pictures, and each flip posting a
// "changed their name/profile picture" event into every shared room. (bridgev2
// v0.31's ChatMember.Nickname, which would allow real per-room names, is
// documented "Not yet used".) So every source now agrees on one identity:
//
//   - Name: the account-wide name (group member "name", DM other_user.name),
//     never a per-group nickname, except as a fallback when nothing else
//     is known.
//   - Avatar: set from account-level data (the DM chats list, contacts);
//     a group's per-group picture only fills in a missing avatar, never
//     replaces or removes one.

func (gc *GMClient) GetChatInfo(ctx context.Context, portal *bridgev2.Portal) (*bridgev2.ChatInfo, error) {
	portalType, gmid := ParsePortalID(portal.ID)
	switch portalType {
	case PortalTypeGroup:
		group, err := gc.Client.ShowGroup(ctx, gmid)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch GroupMe group: %w", err)
		}
		members := &bridgev2.ChatMemberList{
			IsFull:    true,
			MemberMap: make(bridgev2.ChatMemberMap, len(group.Members)),
		}
		for _, m := range group.Members {
			isFromMe := m.UserID == groupme.ID(gc.Meta.GMID)
			// Use the names GroupMe already gave us in the member list,
			// rather than relying on GetUserInfo's IndexRelations
			// (personal contacts) lookup, which only knows about people
			// the logged-in user has DMed 1:1 -- other group members fell
			// through to a raw-numeric-ID fallback there. Account name
			// first, per-group nickname only as a fallback (see above).
			name := m.Name
			if name == "" {
				name = m.Nickname
			}
			if name == "" {
				name = string(m.UserID)
			}
			info := &bridgev2.UserInfo{Name: ptr.Ptr(name)}
			if m.ImageURL != "" {
				ghost, err := gc.Main.br.GetExistingGhostByID(ctx, MakeUserID(m.UserID))
				if err == nil && (ghost == nil || ghost.AvatarMXC == "") && !avatarAlreadyFailed(ghost, m.ImageURL) {
					info.Avatar = gc.avatarFor(ctx, m.ImageURL)
				}
			}
			members.MemberMap.Set(bridgev2.ChatMember{
				EventSender: bridgev2.EventSender{
					IsFromMe: isFromMe,
					Sender:   MakeUserID(m.UserID),
				},
				Membership: "join",
				UserInfo:   info,
			})
		}
		roomType := database.RoomTypeDefault
		return &bridgev2.ChatInfo{
			Name:    ptr.Ptr(group.Name),
			Topic:   ptr.Ptr(group.Description),
			Avatar:  gc.avatarFor(ctx, group.ImageURL),
			Members: members,
			Type:    &roomType,
		}, nil
	case PortalTypeDM:
		name := string(gmid)
		var avatarURL string
		var found bool

		// Primary source: the "your chats" listing, which is guaranteed to
		// include every DM thread the account actually has (this is what
		// sync.go uses to discover DM portals in the first place). Preferred
		// over IndexRelations (personal contacts), which -- confirmed live,
		// see NOTES.md -- does NOT necessarily include everyone there's an
		// active DM thread with, causing DM room names to fall back to a raw
		// numeric ID for such people.
		if chats, err := gc.Client.IndexAllChats(ctx); err == nil {
			for _, c := range chats {
				if c.OtherUser.ID == gmid {
					name = c.OtherUser.Name
					avatarURL = c.OtherUser.AvatarURL
					found = true
					break
				}
			}
		}

		// Fallback: personal contacts list.
		if !found {
			if relations, err := gc.Client.IndexRelations(ctx); err == nil {
				for _, u := range relations {
					if u.ID == gmid {
						name = u.Name
						avatarURL = u.AvatarURL
						found = true
						break
					}
				}
			}
		}

		// Last resort before the raw ID: whatever name we already know for
		// this ghost, e.g. from HandleTextMessage's message-sender-based
		// refresh (handlegroupme.go), so this doesn't regress a name we
		// already have to worse info.
		if !found {
			if ghost, err := gc.Main.br.GetExistingGhostByID(ctx, MakeUserID(gmid)); err == nil && ghost != nil && ghost.Name != "" && ghost.Name != string(gmid) {
				name = ghost.Name
			}
		}
		roomType := database.RoomTypeDM
		// Hand the account-level profile we just looked up to the ghost
		// directly. Without it, bridgev2 fell back to GetUserInfo, which
		// only checks personal contacts and set the ghost's name to the
		// raw numeric ID for anyone not in them (seen live: "Hilton
		// Sampson" -> "94228122" -> back, on one restart).
		var otherInfo *bridgev2.UserInfo
		if found {
			otherInfo = &bridgev2.UserInfo{Name: ptr.Ptr(name), Avatar: gc.avatarIfSet(ctx, avatarURL)}
			if ghost, err := gc.Main.br.GetExistingGhostByID(ctx, MakeUserID(gmid)); err == nil && avatarAlreadyFailed(ghost, avatarURL) {
				otherInfo.Avatar = nil
			}
		}
		members := &bridgev2.ChatMemberList{
			IsFull: true,
			MemberMap: bridgev2.ChatMemberMap{
				MakeUserID(gmid): bridgev2.ChatMember{
					EventSender: bridgev2.EventSender{Sender: MakeUserID(gmid)},
					Membership:  "join",
					UserInfo:    otherInfo,
				},
				MakeUserID(groupme.ID(gc.Meta.GMID)): bridgev2.ChatMember{
					EventSender: bridgev2.EventSender{IsFromMe: true, Sender: MakeUserID(groupme.ID(gc.Meta.GMID))},
					Membership:  "join",
				},
			},
			OtherUserID: MakeUserID(gmid),
		}
		return &bridgev2.ChatInfo{
			Name:    ptr.Ptr(name),
			Avatar:  gc.avatarIfSet(ctx, avatarURL),
			Members: members,
			Type:    &roomType,
		}, nil
	default:
		return nil, fmt.Errorf("unknown portal type for %s", portal.ID)
	}
}

func (gc *GMClient) GetUserInfo(ctx context.Context, ghost *bridgev2.Ghost) (*bridgev2.UserInfo, error) {
	gmid := ParseUserID(ghost.ID)
	relations, err := gc.Client.IndexRelations(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch GroupMe relations: %w", err)
	}
	for _, u := range relations {
		if u.ID == gmid {
			return &bridgev2.UserInfo{
				Name:   ptr.Ptr(u.Name),
				Avatar: gc.avatarIfSet(ctx, u.AvatarURL),
			}, nil
		}
	}
	if gmid == groupme.ID(gc.Meta.GMID) {
		me, err := gc.Client.MyUser(ctx)
		if err == nil {
			return &bridgev2.UserInfo{
				Name:   ptr.Ptr(me.Name),
				Avatar: gc.avatarIfSet(ctx, me.AvatarURL),
			}, nil
		}
	}
	// Not in contacts. Don't overwrite a name we already have (e.g. from a
	// group member list) with the raw ID -- that's strictly worse info.
	if ghostHasRealName(ghost) {
		return &bridgev2.UserInfo{}, nil
	}
	return &bridgev2.UserInfo{Name: ptr.Ptr(string(gmid))}, nil
}
