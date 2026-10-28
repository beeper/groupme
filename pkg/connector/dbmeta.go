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
	"encoding/json"
	"fmt"
	"sync"

	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"github.com/beeper/groupme-lib"
	"github.com/beeper/groupme/pkg/groupmeext"
)

// UserLoginMetadata is stored on the bridgev2 UserLogin row and holds the
// GroupMe access token that was obtained during login.
type UserLoginMetadata struct {
	Token       string      `json:"token"`
	GMID        string      `json:"gmid"`
	ActivePolls ActivePolls `json:"active_polls"`
}

func (m *UserLoginMetadata) CopyFrom(other any) {
	otherMeta, ok := other.(*UserLoginMetadata)
	if !ok || otherMeta == nil {
		panic(fmt.Errorf("invalid type %T provided to UserLoginMetadata.CopyFrom", other))
	}
	m.Token, m.GMID = otherMeta.Token, otherMeta.GMID
}

// ActivePolls marshals under its own lock because the framework can save the
// login while the poller is changing it.
type ActivePolls struct {
	sync.Mutex
	polls map[string]*ActivePoll
}

func (ap *ActivePolls) MarshalJSON() ([]byte, error) {
	ap.Lock()
	defer ap.Unlock()
	return json.Marshal(ap.polls)
}

func (ap *ActivePolls) UnmarshalJSON(data []byte) error {
	return json.Unmarshal(data, &ap.polls)
}

type ActivePoll struct {
	GroupID    groupme.ID          `json:"group_id"`
	MessageID  networkid.MessageID `json:"message_id"`
	Expiration groupme.Timestamp   `json:"expiration"`
	Selections map[string][]string `json:"selections"`
	voting     bool
	revision   uint64
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
	EditRevisionID groupme.ID                 `json:"edit_revision_id,omitempty"`
	PollID         string                     `json:"poll_id,omitempty"`
	PollAnswerIDs  map[string]string          `json:"poll_answer_ids,omitempty"`
	NativeContent  *groupmeext.MessageContent `json:"native_content,omitempty"`
	EditedAt       groupme.Timestamp          `json:"edited_at,omitempty"`
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
