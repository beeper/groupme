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

// Package connector implements the GroupMe network connector for the
// bridgev2 framework in maunium.net/go/mautrix.
//
// It is a port of the pre-bridgev2 mautrix-groupme bridge logic (see the
// pkg/groupmeext package and NOTES.md at the repository root for details
// about what has and has not been ported yet).
package connector

import (
	"context"

	"go.mau.fi/util/configupgrade"
	"maunium.net/go/mautrix/bridgev2"
)

type GMConnector struct {
	br             *bridgev2.Bridge
	useDirectMedia bool
}

var _ bridgev2.NetworkConnector = (*GMConnector)(nil)

func (gc *GMConnector) Init(bridge *bridgev2.Bridge) {
	gc.br = bridge
}

func (gc *GMConnector) Start(ctx context.Context) error {
	if !gc.br.Config.Backfill.Enabled {
		gc.br.Log.Warn().Msg("Backfill is disabled, so messages sent while the bridge was disconnected from GroupMe will not be bridged")
	}
	return nil
}

func (gc *GMConnector) GetConfig() (example string, data any, upgrader configupgrade.Upgrader) {
	return "", nil, nil
}

func (gc *GMConnector) GetName() bridgev2.BridgeName {
	return bridgev2.BridgeName{
		DisplayName:          "GroupMe",
		NetworkURL:           "https://groupme.com",
		NetworkIcon:          "",
		NetworkID:            "groupme",
		BeeperBridgeType:     "groupme",
		DefaultPort:          29331,
		DefaultCommandPrefix: "!gm",
	}
}

func (gc *GMConnector) GetBridgeInfoVersion() (info, caps int) {
	return 1, 2
}
