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

func wrapCommand(handler func(*WrappedCommandEvent)) func(*commands.Event) {
	return func(ce *commands.Event) {
		user := ce.User.(*User)
		var portal *Portal
		if ce.Portal != nil {
			portal = ce.Portal.(*Portal)
		}
		br := ce.Bridge.Child.(*GMBridge)
		handler(&WrappedCommandEvent{ce, br, user, portal})
	}
}

var (
	HelpSectionConnectionManagement = commands.HelpSection{Name: "Connection management", Order: 11}
	HelpSectionCreatingPortals      = commands.HelpSection{Name: "Creating portals", Order: 15}
	HelpSectionPortalManagement     = commands.HelpSection{Name: "Portal management", Order: 20}
	HelpSectionInvites              = commands.HelpSection{Name: "Group invites", Order: 25}
	HelpSectionMiscellaneous        = commands.HelpSection{Name: "Miscellaneous", Order: 30}
)

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
		cmdLogin,
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

var cmdLogin = &commands.FullHandler{
	Func: wrapCommand(fnLogin),
	Name: "login",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionAuth,
		Description: "Link the bridge to your GroupMe account.",
	},
}

func fnLogin(ce *WrappedCommandEvent) {
	if ce.User.Client != nil {
		if ce.User.IsConnected() {
			ce.Reply("You're already logged in")
		} else {
			ce.Reply("You're already logged in. Perhaps you wanted to `reconnect`?")
		}
		return
	}

	if len(ce.Args) < 1 {
		ce.Reply(`Get your access token from https://dev.groupme.com/ which should be the first argument to login`)
		return
	}

	defer ce.Bot.RedactEvent(ce.RoomID, ce.EventID)

	err := ce.User.Login(ce.Args[0])
	if err != nil {
		ce.Reply("Failed to log in: %v", err)
	}

	ce.Reply("Logged in successfully!")
}
