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
	"maps"
	"slices"

	"github.com/beeper/groupme-lib"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
)

var generalCaps = &bridgev2.NetworkGeneralCapabilities{
	DisappearingMessages: false,
	AggressiveUpdateInfo: false,
}

func (gc *GMConnector) GetCapabilities() *bridgev2.NetworkGeneralCapabilities {
	return generalCaps
}

const MaxFileSize = 25 * 1024 * 1024

var imageMimes = map[string]event.CapabilitySupportLevel{
	"image/png":  event.CapLevelFullySupported,
	"image/jpeg": event.CapLevelFullySupported,
	"image/gif":  event.CapLevelFullySupported,
}

var videoMimes = map[string]event.CapabilitySupportLevel{
	"video/mp4": event.CapLevelFullySupported,
}

var fileMimes = map[string]event.CapabilitySupportLevel{
	"application/*": event.CapLevelFullySupported,
	"text/*":        event.CapLevelFullySupported,
}

var roomCaps = &event.RoomFeatures{
	ID: "fi.mau.groupme.capabilities.2026_10_01.3",
	File: event.FileFeatureMap{
		event.MsgImage: {
			MimeTypes: imageMimes,
			Caption:   event.CapLevelRejected,
			MaxSize:   MaxFileSize,
		},
		event.MsgVideo: {
			MimeTypes: videoMimes,
			Caption:   event.CapLevelRejected,
			MaxSize:   MaxFileSize,
		},
		event.MsgFile: {
			MimeTypes: fileMimes,
			Caption:   event.CapLevelRejected,
			MaxSize:   MaxFileSize,
		},
	},
	Reply:            event.CapLevelFullySupported,
	Reaction:         event.CapLevelFullySupported,
	ReactionCount:    1,
	AllowedReactions: slices.Sorted(maps.Keys(groupme.UnicodeLikeIcons)),
	LocationMessage:  event.CapLevelPartialSupport,
}

var dmRoomCaps = func() *event.RoomFeatures {
	caps := roomCaps.Clone()
	caps.ID += ".dm"
	delete(caps.File, event.MsgFile)
	return caps
}()

func (gc *GMClient) GetCapabilities(ctx context.Context, portal *bridgev2.Portal) *event.RoomFeatures {
	if portal != nil {
		if portalType, _ := ParsePortalID(portal.ID); portalType == PortalTypeDM {
			return dmRoomCaps
		}
	}
	return roomCaps
}
