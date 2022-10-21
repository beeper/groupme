// mautrix-groupme - A Matrix-GroupMe puppeting bridge.
// Copyright (C) 2022 Sumner Evans, Karmanyaah Malhotra
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

package whatsappExt

import (
	"encoding/json"

	"github.com/Rhymen/go-whatsapp"
)

type StreamType string

const (
	StreamUpdate = "update"
	StreamSleep  = "asleep"
)

type StreamEvent struct {
	Type StreamType

	IsOutdated bool
	Version    string

	Extra []json.RawMessage
}

type StreamEventHandler interface {
	whatsapp.Handler
	HandleStreamEvent(StreamEvent)
}

func (ext *ExtendedConn) handleMessageStream(message []json.RawMessage) {
	var event StreamEvent
	err := json.Unmarshal(message[0], &event.Type)
	if err != nil {
		ext.jsonParseError(err)
		return
	}

	if event.Type == StreamUpdate && len(message) >= 3 {
		_ = json.Unmarshal(message[1], &event.IsOutdated)
		_ = json.Unmarshal(message[2], &event.Version)
		if len(message) >= 4 {
			event.Extra = message[3:]
		}
	} else if len(message) >= 2 {
		event.Extra = message[1:]
	}

	for _, handler := range ext.handlers {
		streamHandler, ok := handler.(StreamEventHandler)
		if !ok {
			continue
		}

		if ext.shouldCallSynchronously(streamHandler) {
			streamHandler.HandleStreamEvent(event)
		} else {
			go streamHandler.HandleStreamEvent(event)
		}
	}
}
