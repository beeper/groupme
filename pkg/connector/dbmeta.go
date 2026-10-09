// mautrix-groupme - A Matrix-GroupMe puppeting bridge.
// Copyright (C) 2022 Sumner Evans, Karmanyaah Malhotra
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
	"maunium.net/go/mautrix/bridgev2/database"
)

// UserLoginMetadata is stored on the bridgev2 UserLogin row and holds the
// GroupMe access token that was obtained during login.
type UserLoginMetadata struct {
	Token string `json:"token"`
	GMID  string `json:"gmid"`
}

// PortalType distinguishes GroupMe groups from GroupMe direct messages. Both
// are bridged as portals, but they use different GroupMe API endpoints.
type PortalType string

const (
	PortalTypeGroup PortalType = "group"
	PortalTypeDM    PortalType = "dm"
)

// PortalMetadata is stored on the bridgev2 Portal row.
type PortalMetadata struct {
	Type PortalType `json:"type"`
}

// MessageMetadata contains native protocol identifiers used by later actions.
type MessageMetadata struct {
	PollID string `json:"poll_id,omitempty"`
}

func (gc *GMConnector) GetDBMetaTypes() database.MetaTypes {
	return database.MetaTypes{
		Portal: func() any {
			return &PortalMetadata{}
		},
		Ghost:    nil,
		Message:  func() any { return &MessageMetadata{} },
		Reaction: nil,
		UserLogin: func() any {
			return &UserLoginMetadata{}
		},
	}
}
