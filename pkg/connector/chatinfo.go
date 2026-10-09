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

	"github.com/rs/zerolog"
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
	avatar := &bridgev2.Avatar{
		ID: networkid.AvatarID(url),
		Get: func(ctx context.Context) ([]byte, error) {
			data, _, err := groupmeext.DownloadImage(ctx, url)
			return data, err
		},
	}
	if gc.Main.useDirectMedia {
		var err error
		avatar.MXC, err = gc.Main.directMediaURI(ctx, "1i", url)
		if err != nil {
			zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to generate GroupMe avatar media URI")
		}
	}
	return avatar
}

// Missing profile data does not imply that the user removed their avatar.
func (gc *GMClient) avatarIfSet(ctx context.Context, url string) *bridgev2.Avatar {
	if url == "" {
		return nil
	}
	return gc.avatarFor(ctx, url)
}

// ghostHasRealName reports whether a ghost already has a proper name, as
// opposed to none or the raw-numeric-ID fallback.
func ghostHasRealName(ghost *bridgev2.Ghost) bool {
	return ghost != nil && ghost.Name != "" && ghost.Name != string(ghost.ID)
}

// Ghost profiles are shared across rooms. Prefer account-level names and
// avatars; group nicknames and pictures only fill missing profile data.
func (gc *GMClient) GetChatInfo(ctx context.Context, portal *bridgev2.Portal) (*bridgev2.ChatInfo, error) {
	portalType, gmid := ParsePortalID(portal.ID)
	switch portalType {
	case PortalTypeGroup:
		group, err := gc.Client.ShowGroup(ctx, gmid)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch GroupMe group: %w", err)
		}
		if group == nil || group.ID != gmid {
			return nil, fmt.Errorf("GroupMe returned invalid group metadata")
		}
		members := &bridgev2.ChatMemberList{
			IsFull:    true,
			MemberMap: make(bridgev2.ChatMemberMap, len(group.Members)),
		}
		for _, m := range group.Members {
			if m == nil || m.UserID == "" {
				continue
			}
			isFromMe := m.UserID == groupme.ID(gc.Meta.GMID)
			// Personal contacts do not include every group member.
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
				if err == nil && (ghost == nil || ghost.AvatarMXC == "") {
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
		// Existing DM partners may be absent from personal contacts.
		var partner *groupme.User
		if chats, err := gc.Client.IndexAllChats(ctx); err == nil {
			for _, c := range chats {
				if c != nil && c.OtherUser.ID == gmid {
					partner = &c.OtherUser
					break
				}
			}
		}
		return gc.dmChatInfo(ctx, gmid, partner), nil
	default:
		return nil, fmt.Errorf("unknown portal type for %s", portal.ID)
	}
}

// partner is the DM listing's entry for gmid, or nil when the listing lacks it;
// personal contacts are then the fallback.
func (gc *GMClient) dmChatInfo(ctx context.Context, gmid groupme.ID, partner *groupme.User) *bridgev2.ChatInfo {
	if partner == nil {
		if relations, err := gc.Client.IndexRelations(ctx); err == nil {
			for _, u := range relations {
				if u != nil && u.ID == gmid {
					partner = u
					break
				}
			}
		}
	}

	name := string(gmid)
	var avatarURL string
	if partner != nil {
		name, avatarURL = partner.Name, partner.AvatarURL
	} else if ghost, err := gc.Main.br.GetExistingGhostByID(ctx, MakeUserID(gmid)); err == nil && ghost != nil && ghost.Name != "" && ghost.Name != string(gmid) {
		// Keep a known name when both native listings lack the user.
		name = ghost.Name
	}
	roomType := database.RoomTypeDM
	// Supply the profile from the DM listing to avoid a contacts-only lookup.
	var otherInfo *bridgev2.UserInfo
	if partner != nil {
		otherInfo = &bridgev2.UserInfo{Name: ptr.Ptr(name), Avatar: gc.avatarIfSet(ctx, avatarURL)}
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
	}
}

func (gc *GMClient) GetUserInfo(ctx context.Context, ghost *bridgev2.Ghost) (*bridgev2.UserInfo, error) {
	gmid := ParseUserID(ghost.ID)
	relations, err := gc.Client.IndexRelations(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch GroupMe relations: %w", err)
	}
	for _, u := range relations {
		if u != nil && u.ID == gmid {
			return &bridgev2.UserInfo{
				Name:   ptr.Ptr(u.Name),
				Avatar: gc.avatarIfSet(ctx, u.AvatarURL),
			}, nil
		}
	}
	if gmid == groupme.ID(gc.Meta.GMID) {
		me, err := gc.Client.MyUser(ctx)
		if err == nil && me != nil && me.ID == gmid {
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
