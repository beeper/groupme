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

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/event"

	"github.com/beeper/groupme-lib"

	"github.com/beeper/groupme/pkg/groupmeext"
)

var _ bridgev2.RedactionHandlingNetworkAPI = (*GMClient)(nil)

func (gc *GMClient) HandleMatrixMessageRemove(ctx context.Context, msg *bridgev2.MatrixMessageRemove) error {
	target := msg.TargetMessage
	if target.Room != msg.Portal.PortalKey || isPollVoteID(target.ID) {
		return permanentFailure(event.MessageStatusUnsupported, "this GroupMe message can't be deleted")
	}
	conversation, err := gc.conversationID(msg.Portal.ID)
	if err != nil {
		return err
	}
	if err = groupmeext.DeleteMessage(ctx, gc.Meta.Token, conversation, ParseMessageID(target.ID)); err != nil {
		return gc.wrapGroupMeError(err, "failed to delete message")
	}
	return nil
}

func (gc *GMClient) messageDeleteEvent(msg groupme.Message) (*simplevent.MessageRemove, bool) {
	if msg.Event == nil || msg.Event.Type != "message.deleted" {
		return nil, false
	}
	var data struct {
		MessageID groupme.ID `json:"message_id"`
	}
	key := gc.portalKeyForMessage(&msg)
	if json.Unmarshal(msg.Event.Data, &data) != nil || data.MessageID == "" || key.ID == "" {
		gc.UserLogin.Log.Warn().Msg("Ignoring malformed GroupMe message deletion")
		return nil, true
	}
	return &simplevent.MessageRemove{
		EventMeta:     simplevent.EventMeta{Type: bridgev2.RemoteEventMessageRemove, PortalKey: key, Timestamp: msg.CreatedAt.ToTime()},
		TargetMessage: MakeMessageID(data.MessageID),
	}, true
}

func isHiddenRecord(msg *groupme.Message) bool {
	return msg.Event != nil && (msg.Event.Type == "message.update" || msg.Event.Type == "message.deleted")
}
