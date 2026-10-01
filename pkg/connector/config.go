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
	_ "embed"

	up "go.mau.fi/util/configupgrade"
)

//go:embed example-config.yaml
var ExampleConfig string

type PushConfig struct {
	ConnectionTimeout int `yaml:"connection_timeout"`
}

// PollConfig controls the REST-API polling fallback (see pkg/connector/poll.go
// and NOTES.md "REST polling fallback"), which periodically fetches new
// messages via GroupMe's REST API independent of whether the Faye push
// connection above is working.
type PollConfig struct {
	// Enabled turns REST polling on/off. Defaults to true: the Faye push
	// connection has been observed to fail for extended periods in
	// production, and polling is this bridge's only other way to learn
	// about new messages. It's safe to leave enabled even when Faye is
	// working, since bridgev2 dedupes incoming messages by ID.
	Enabled bool `yaml:"enabled"`
	// IntervalSeconds is how often, in seconds, each known chat is polled
	// for new messages. Values below 30 are clamped up to 30 to avoid
	// hammering GroupMe's API; unset or <= 0 defaults to 60.
	IntervalSeconds int `yaml:"interval_seconds"`
}

type Config struct {
	Push PushConfig `yaml:"push"`
	Poll PollConfig `yaml:"poll"`
}

func (gc *GMConnector) GetConfig() (example string, data any, upgrader up.Upgrader) {
	return ExampleConfig, &gc.Config, up.SimpleUpgrader(upgradeConfig)
}

func upgradeConfig(helper up.Helper) {
	helper.Copy(up.Int, "push", "connection_timeout")
	helper.Copy(up.Bool, "poll", "enabled")
	helper.Copy(up.Int, "poll", "interval_seconds")
}
