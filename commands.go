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

package main

import (
	"maunium.net/go/mautrix/bridge/commands"
)

type WrappedCommandEvent struct {
	*commands.Event
	Bridge *GMBridge
	User   *User
	Portal *Portal
}

func (br *GMBridge) RegisterCommands() {
	proc := br.CommandProcessor.(*commands.Processor)
	proc.AddHandlers(
	// cmdSetRelay,
	// cmdUnsetRelay,
	// cmdInviteLink,
	// cmdResolveLink,
	// cmdJoin,
	// cmdAccept,
	// cmdCreate,
	// cmdLogin,
	// cmdLogout,
	// cmdTogglePresence,
	// cmdDeleteSession,
	// cmdReconnect,
	// cmdDisconnect,
	// cmdPing,
	// cmdDeletePortal,
	// cmdDeleteAllPortals,
	// cmdBackfill,
	// cmdList,
	// cmdSearch,
	// cmdOpen,
	// cmdPM,
	// cmdSync,
	// cmdDisappearingTimer,
	)
}
