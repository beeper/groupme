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
	_ "embed"
	"sync"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridge"
	"maunium.net/go/mautrix/bridge/commands"
	"maunium.net/go/mautrix/bridge/status"
	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/util/configupgrade"

	"github.com/beeper/groupme-lib"

	"github.com/beeper/groupme/config"
	"github.com/beeper/groupme/database"
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
	Metrics      *MetricsHandler

	usersByMXID         map[id.UserID]*User
	usersByGMID         map[groupme.ID]*User
	usersLock           sync.Mutex
	spaceRooms          map[id.RoomID]*User
	spaceRoomsLock      sync.Mutex
	managementRooms     map[id.RoomID]*User
	managementRoomsLock sync.Mutex
	portalsByMXID       map[id.RoomID]*Portal
	portalsByGMID       map[database.PortalKey]*Portal
	portalsLock         sync.Mutex
	puppets             map[groupme.ID]*Puppet
	puppetsByCustomMXID map[id.UserID]*Puppet
	puppetsLock         sync.Mutex
}

func (br *GMBridge) Init() {
	br.CommandProcessor = commands.NewProcessor(&br.Bridge)
	br.RegisterCommands()

	matrixHTMLParser.PillConverter = br.pillConverter

	Segment.log = br.Log.Sub("Segment")
	Segment.key = br.Config.SegmentKey
	Segment.userID = br.Config.SegmentUserID
	if Segment.IsEnabled() {
		Segment.log.Infoln("Segment metrics are enabled")
		if Segment.userID != "" {
			Segment.log.Infoln("Overriding Segment user_id with %v", Segment.userID)
		}
	}

	br.DB = database.New(br.Bridge.DB, br.Log.Sub("Database"))

	ss := br.Config.Bridge.Provisioning.SharedSecret
	if len(ss) > 0 && ss != "disable" {
		br.Provisioning = &ProvisioningAPI{bridge: br}
	}

	br.Metrics = NewMetricsHandler(br.Config.Metrics.Listen, br.Log.Sub("Metrics"), br.DB)
	br.MatrixHandler.TrackEventDuration = br.Metrics.TrackMatrixEvent
}

func (br *GMBridge) Start() {
	if br.Provisioning != nil {
		br.Log.Debugln("Initializing provisioning API")
		br.Provisioning.Init()
	}
	go br.StartUsers()
	if br.Config.Metrics.Enabled {
		go br.Metrics.Start()
	}
}

func (br *GMBridge) StartUsers() {
	br.Log.Debugln("Starting users")
	foundAnySessions := false
	for _, user := range br.GetAllUsers() {
		if user.GMID.Valid() {
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
	for _, user := range br.usersByGMID {
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

const unstableFeatureBatchSending = "org.matrix.msc2716"

func (br *GMBridge) CheckFeatures(versions *mautrix.RespVersions) (string, bool) {
	if br.Config.Bridge.HistorySync.Backfill {
		supported, known := versions.UnstableFeatures[unstableFeatureBatchSending]
		if !known {
			return "Backfilling is enabled in bridge config, but homeserver does not support MSC2716 batch sending", false
		} else if !supported {
			return "Backfilling is enabled in bridge config, but MSC2716 batch sending is not enabled on homeserver", false
		}
	}
	return "", true
}

func main() {
	br := &GMBridge{
		usersByMXID:         make(map[id.UserID]*User),
		usersByGMID:         make(map[groupme.ID]*User),
		spaceRooms:          make(map[id.RoomID]*User),
		managementRooms:     make(map[id.RoomID]*User),
		portalsByMXID:       make(map[id.RoomID]*Portal),
		portalsByGMID:       make(map[database.PortalKey]*Portal),
		puppets:             make(map[groupme.ID]*Puppet),
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
