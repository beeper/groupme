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
	"sync"

	"maunium.net/go/mautrix/bridge"
	"maunium.net/go/mautrix/bridge/commands"
	"maunium.net/go/mautrix/bridge/status"
	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/util/configupgrade"

	"github.com/beeper/groupme/config"
	"github.com/beeper/groupme/database"
	"github.com/beeper/groupme/types"
)

// Information to find out exactly which commit the bridge was built from.
// These are filled at build time with the -X linker flag.
var (
	Tag       = "unknown"
	Commit    = "unknown"
	BuildTime = "unknown"
)

//go:embed example-config.yaml
var ExampleConfig string

type GMBridge struct {
	bridge.Bridge
	Config       *config.Config
	DB           *database.Database
	Provisioning *ProvisioningAPI
	Formatter    *Formatter
	Metrics      *MetricsHandler

	usersByMXID         map[id.UserID]*User
	usersByUsername     map[string]*User
	usersByGMID         map[types.GroupMeID]*User // TODO REMOVE?
	usersLock           sync.Mutex
	spaceRooms          map[id.RoomID]*User
	spaceRoomsLock      sync.Mutex
	managementRooms     map[id.RoomID]*User
	managementRoomsLock sync.Mutex
	portalsByMXID       map[id.RoomID]*Portal
	portalsByGMID       map[database.PortalKey]*Portal
	portalsLock         sync.Mutex
	puppets             map[types.GroupMeID]*Puppet
	puppetsByCustomMXID map[id.UserID]*Puppet
	puppetsLock         sync.Mutex
}

func (br *GMBridge) Init() {
	br.CommandProcessor = commands.NewProcessor(&br.Bridge)
	br.RegisterCommands()

	Segment.log = br.Log.Sub("Segment")
	Segment.key = br.Config.SegmentKey
	if Segment.IsEnabled() {
		Segment.log.Infoln("Segment metrics are enabled")
	}

	br.DB = database.New(br.Bridge.DB, br.Log.Sub("Database"))

	ss := br.Config.Bridge.Provisioning.SharedSecret
	if len(ss) > 0 && ss != "disable" {
		br.Provisioning = &ProvisioningAPI{bridge: br}
	}

	br.Formatter = NewFormatter(br)
	br.Metrics = NewMetricsHandler(br.Config.Metrics.Listen, br.Log.Sub("Metrics"), br.DB)
	br.MatrixHandler.TrackEventDuration = br.Metrics.TrackMatrixEvent
}

func (bridge *GMBridge) Start() {
	if bridge.Provisioning != nil {
		bridge.Log.Debugln("Initializing provisioning API")
		bridge.Provisioning.Init()
	}
	go bridge.StartUsers()
	if bridge.Config.Metrics.Enabled {
		go bridge.Metrics.Start()
	}
}

func (bridge *GMBridge) UpdateBotProfile() {
	bridge.Log.Debugln("Updating bot profile")
	botConfig := bridge.Config.AppService.Bot

	var err error
	var mxc id.ContentURI
	if botConfig.Avatar == "remove" {
		err = bridge.Bot.SetAvatarURL(mxc)
	} else if len(botConfig.Avatar) > 0 {
		mxc, err = id.ParseContentURI(botConfig.Avatar)
		if err == nil {
			err = bridge.Bot.SetAvatarURL(mxc)
		}
	}
	if err != nil {
		bridge.Log.Warnln("Failed to update bot avatar:", err)
	}

	if botConfig.Displayname == "remove" {
		err = bridge.Bot.SetDisplayName("")
	} else if len(botConfig.Avatar) > 0 {
		err = bridge.Bot.SetDisplayName(botConfig.Displayname)
	}
	if err != nil {
		bridge.Log.Warnln("Failed to update bot displayname:", err)
	}
}

func (br *GMBridge) StartUsers() {
	br.Log.Debugln("Starting users")
	foundAnySessions := false
	for _, user := range br.GetAllUsers() {
		if !user.GMID.IsEmpty() {
			foundAnySessions = true
		}
		go user.Connect()
	}
	if !foundAnySessions {
		br.SendGlobalBridgeState(status.BridgeState{StateEvent: status.StateUnconfigured}.Fill(nil))
	}
	br.Log.Debugln("Starting custom puppets")
	for _, loopuppet := range br.GetAllPuppetsWithCustomMXID() {
		go func(puppet *Puppet) {
			puppet.log.Debugln("Starting custom puppet", puppet.CustomMXID)
			err := puppet.StartCustomMXID(true)
			if err != nil {
				puppet.log.Errorln("Failed to start custom puppet:", err)
			}
		}(loopuppet)
	}
}

func (br *GMBridge) Stop() {
	br.Metrics.Stop()
	// TODO anything needed to disconnect the users?
	for _, user := range br.usersByUsername {
		if user.Client == nil {
			continue
		}
		br.Log.Debugln("Disconnecting", user.MXID)
	}
}

func (br *GMBridge) GetExampleConfig() string {
	return ExampleConfig
}

func (br *GMBridge) GetConfigPtr() interface{} {
	br.Config = &config.Config{
		BaseConfig: &br.Bridge.Config,
	}
	br.Config.BaseConfig.Bridge = &br.Config.Bridge
	return br.Config
}

func main() {
	br := &GMBridge{
		usersByMXID:         make(map[id.UserID]*User),
		usersByUsername:     make(map[string]*User),
		spaceRooms:          make(map[id.RoomID]*User),
		managementRooms:     make(map[id.RoomID]*User),
		portalsByMXID:       make(map[id.RoomID]*Portal),
		portalsByGMID:       make(map[database.PortalKey]*Portal),
		puppets:             make(map[types.GroupMeID]*Puppet),
		puppetsByCustomMXID: make(map[id.UserID]*Puppet),
	}
	br.Bridge = bridge.Bridge{
		Name:         "groupme-matrix",
		URL:          "https://github.com/beeper/groupme",
		Description:  "A Matrix-GroupMe puppeting bridge.",
		Version:      "0.1.0",
		ProtocolName: "GroupMe",

		CryptoPickleKey: "github.com/beeper/groupme",

		ConfigUpgrader: &configupgrade.StructUpgrader{
			SimpleUpgrader: configupgrade.SimpleUpgrader(config.DoUpgrade),
			Blocks:         config.SpacedBlocks,
			Base:           ExampleConfig,
		},

		Child: br,
	}
	br.InitVersion(Tag, Commit, BuildTime)

	br.Main()
}
