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

	"github.com/beeper/groupme-lib"
)

func (gc *GMClient) setSubgroupParent(subgroupID, parentID groupme.ID) {
	gc.subgroupMu.Lock()
	defer gc.subgroupMu.Unlock()
	if gc.subgroupParents == nil {
		gc.subgroupParents = make(map[groupme.ID]groupme.ID)
	}
	gc.subgroupParents[subgroupID] = parentID
}

func (gc *GMClient) subgroupsOf(parentID groupme.ID) []groupme.ID {
	gc.subgroupMu.Lock()
	defer gc.subgroupMu.Unlock()
	var ids []groupme.ID
	for subgroupID, parent := range gc.subgroupParents {
		if parent == parentID {
			ids = append(ids, subgroupID)
		}
	}
	return ids
}

func (gc *GMClient) subgroupParent(portal *bridgev2.Portal, subgroupID groupme.ID) groupme.ID {
	gc.subgroupMu.Lock()
	parentID := gc.subgroupParents[subgroupID]
	gc.subgroupMu.Unlock()
	if parentID != "" {
		return parentID
	}
	if meta, ok := portal.Metadata.(*PortalMetadata); ok && meta != nil {
		return meta.ParentID
	}
	return ""
}

func (gc *GMClient) listSubgroups(ctx context.Context, parentID groupme.ID) []nativeChat {
	subgroups, err := gc.Client.IndexSubgroups(ctx, parentID)
	if err != nil {
		gc.UserLogin.Log.Err(err).Str("parent_id", string(parentID)).Msg("Failed to list GroupMe subgroups")
		return nil
	}
	chats := make([]nativeChat, 0, len(subgroups))
	for _, sub := range subgroups {
		if sub == nil || len(sub.ID) == 0 {
			continue
		}
		gc.setSubgroupParent(sub.ID, parentID)
		chats = append(chats, nativeChat{portalKey: gc.portalKeyForGroup(sub.ID), latestID: sub.Messages.LastMessageID})
	}
	return chats
}

func (gc *GMClient) subgroupChatInfo(ctx context.Context, parentID, subgroupID groupme.ID) (*bridgev2.ChatInfo, error) {
	sub, err := gc.Client.ShowSubgroup(ctx, parentID, subgroupID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch GroupMe subgroup: %w", err)
	}
	if sub == nil || sub.ID != subgroupID {
		return nil, fmt.Errorf("GroupMe returned invalid subgroup metadata")
	}
	parent, err := gc.Client.ShowGroup(ctx, parentID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch GroupMe subgroup parent: %w", err)
	}
	if parent == nil || parent.ID != parentID {
		return nil, fmt.Errorf("GroupMe returned invalid group metadata")
	}
	info := gc.groupChatInfo(ctx, parent)
	if sub.Topic != "" {
		info.Name = ptr.Ptr(fmt.Sprintf("%s / %s", parent.Name, sub.Topic))
	}
	info.Topic = nil
	if sub.AvatarURL != "" {
		info.Avatar = gc.avatarFor(ctx, sub.AvatarURL)
	}
	info.ExtraUpdates = func(ctx context.Context, portal *bridgev2.Portal) bool {
		meta, ok := portal.Metadata.(*PortalMetadata)
		if !ok || meta == nil || meta.ParentID == parentID {
			return false
		}
		meta.ParentID = parentID
		return true
	}
	return info, nil
}
