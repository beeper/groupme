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
	"fmt"
	"regexp"
	"sync"

	log "maunium.net/go/maulogger/v2"

	"github.com/beeper/groupme-lib"

	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/bridge"
	"maunium.net/go/mautrix/id"

	"github.com/beeper/groupme/database"
)

var userIDRegex *regexp.Regexp

func (bridge *GMBridge) ParsePuppetMXID(mxid id.UserID) (groupme.ID, bool) {
	if userIDRegex == nil {
		userIDRegex = regexp.MustCompile(fmt.Sprintf("^@%s:%s$",
			bridge.Config.Bridge.FormatUsername("([0-9]+)"),
			bridge.Config.Homeserver.Domain))
	}
	match := userIDRegex.FindStringSubmatch(string(mxid))
	if match == nil || len(match) != 2 {
		return "", false
	}

	return groupme.ID(match[1]), true
}

func (bridge *GMBridge) GetPuppetByMXID(mxid id.UserID) *Puppet {
	gmid, ok := bridge.ParsePuppetMXID(mxid)
	if !ok {
		return nil
	}

	return bridge.GetPuppetByGMID(gmid)
}

func (bridge *GMBridge) GetPuppetByGMID(gmid groupme.ID) *Puppet {
	bridge.puppetsLock.Lock()
	defer bridge.puppetsLock.Unlock()
	puppet, ok := bridge.puppets[gmid]
	if !ok {
		dbPuppet := bridge.DB.Puppet.Get(gmid)
		if dbPuppet == nil {
			dbPuppet = bridge.DB.Puppet.New()
			dbPuppet.GMID = gmid
			dbPuppet.Insert()
		}
		puppet = bridge.NewPuppet(dbPuppet)
		bridge.puppets[puppet.GMID] = puppet
		if len(puppet.CustomMXID) > 0 {
			bridge.puppetsByCustomMXID[puppet.CustomMXID] = puppet
		}
	}
	return puppet
}

func (bridge *GMBridge) GetPuppetByCustomMXID(mxid id.UserID) *Puppet {
	bridge.puppetsLock.Lock()
	defer bridge.puppetsLock.Unlock()
	puppet, ok := bridge.puppetsByCustomMXID[mxid]
	if !ok {
		dbPuppet := bridge.DB.Puppet.GetByCustomMXID(mxid)
		if dbPuppet == nil {
			return nil
		}
		puppet = bridge.NewPuppet(dbPuppet)
		bridge.puppets[puppet.GMID] = puppet
		bridge.puppetsByCustomMXID[puppet.CustomMXID] = puppet
	}
	return puppet
}

func (user *User) GetIDoublePuppet() bridge.DoublePuppet {
	p := user.bridge.GetPuppetByCustomMXID(user.MXID)
	if p == nil || p.CustomIntent() == nil {
		return nil
	}
	return p
}

func (user *User) GetIGhost() bridge.Ghost {
	if user.GMID.String() == "" {
		return nil
	}
	p := user.bridge.GetPuppetByGMID(user.GMID)
	if p == nil {
		return nil
	}
	return p
}

func (br *GMBridge) IsGhost(id id.UserID) bool {
	_, ok := br.ParsePuppetMXID(id)
	return ok
}

func (br *GMBridge) GetIGhost(id id.UserID) bridge.Ghost {
	p := br.GetPuppetByMXID(id)
	if p == nil {
		return nil
	}
	return p
}

func (puppet *Puppet) GetMXID() id.UserID {
	return puppet.MXID
}

func (bridge *GMBridge) GetAllPuppetsWithCustomMXID() []*Puppet {
	return bridge.dbPuppetsToPuppets(bridge.DB.Puppet.GetAllWithCustomMXID())
}

func (bridge *GMBridge) GetAllPuppets() []*Puppet {
	return bridge.dbPuppetsToPuppets(bridge.DB.Puppet.GetAll())
}

func (bridge *GMBridge) dbPuppetsToPuppets(dbPuppets []*database.Puppet) []*Puppet {
	bridge.puppetsLock.Lock()
	defer bridge.puppetsLock.Unlock()
	output := make([]*Puppet, len(dbPuppets))
	for index, dbPuppet := range dbPuppets {
		if dbPuppet == nil {
			continue
		}
		puppet, ok := bridge.puppets[dbPuppet.GMID]
		if !ok {
			puppet = bridge.NewPuppet(dbPuppet)
			bridge.puppets[dbPuppet.GMID] = puppet
			if len(dbPuppet.CustomMXID) > 0 {
				bridge.puppetsByCustomMXID[dbPuppet.CustomMXID] = puppet
			}
		}
		output[index] = puppet
	}
	return output
}

func (bridge *GMBridge) FormatPuppetMXID(gmid groupme.ID) id.UserID {
	return id.NewUserID(
		bridge.Config.Bridge.FormatUsername(gmid.String()),
		bridge.Config.Homeserver.Domain)
}

func (bridge *GMBridge) NewPuppet(dbPuppet *database.Puppet) *Puppet {
	return &Puppet{
		Puppet: dbPuppet,
		bridge: bridge,
		log:    bridge.Log.Sub(fmt.Sprintf("Puppet/%s", dbPuppet.GMID)),

		MXID: bridge.FormatPuppetMXID(dbPuppet.GMID),
	}
}

type Puppet struct {
	*database.Puppet

	bridge *GMBridge
	log    log.Logger

	typingIn id.RoomID
	typingAt int64

	MXID id.UserID

	customIntent   *appservice.IntentAPI
	customTypingIn map[id.RoomID]bool
	customUser     *User

	syncLock sync.Mutex
}

func (puppet *Puppet) PhoneNumber() string {
	return puppet.GMID.String()
}

func (puppet *Puppet) IntentFor(portal *Portal) *appservice.IntentAPI {
	if puppet.customIntent == nil || portal.Key.GMID == puppet.GMID {
		return puppet.DefaultIntent()
	}
	return puppet.customIntent
}

func (puppet *Puppet) CustomIntent() *appservice.IntentAPI {
	return puppet.customIntent
}

func (puppet *Puppet) DefaultIntent() *appservice.IntentAPI {
	return puppet.bridge.AS.Intent(puppet.MXID)
}

//func (puppet *Puppet) SetRoomMetadata(name, avatarURL string) bool {
//
//}

func (puppet *Puppet) UpdateAvatar(source *User, forcePortalSync bool) bool {
	changed := source.updateAvatar(puppet.GMID, &puppet.Avatar, &puppet.AvatarURL, &puppet.AvatarSet, puppet.log, puppet.DefaultIntent())
	if !changed || puppet.Avatar == "unauthorized" {
		if forcePortalSync {
			go puppet.updatePortalAvatar()
		}
		return changed
	}
	err := puppet.DefaultIntent().SetAvatarURL(puppet.AvatarURL)
	if err != nil {
		puppet.log.Warnln("Failed to set avatar:", err)
	} else {
		puppet.AvatarSet = true
	}
	go puppet.updatePortalAvatar()
	return true
}

func (puppet *Puppet) UpdateName(member groupme.Member, forcePortalSync bool) bool {
	newName := puppet.bridge.Config.Bridge.FormatDisplayname(puppet.GMID, member)
	if puppet.Displayname != newName || !puppet.NameSet {
		oldName := puppet.Displayname
		puppet.Displayname = newName
		puppet.NameSet = false
		err := puppet.DefaultIntent().SetDisplayName(newName)
		if err == nil {
			puppet.log.Debugln("Updated name", oldName, "->", newName)
			puppet.NameSet = true
			go puppet.updatePortalName()
		} else {
			puppet.log.Warnln("Failed to set display name:", err)
		}
		return true
	} else if forcePortalSync {
		go puppet.updatePortalName()
	}
	return false
}

func (puppet *Puppet) updatePortalMeta(meta func(portal *Portal)) {
	if puppet.bridge.Config.Bridge.PrivateChatPortalMeta || puppet.bridge.Config.Bridge.Encryption.Allow {
		for _, portal := range puppet.bridge.GetAllPortalsByGMID(puppet.GMID) {
			if !puppet.bridge.Config.Bridge.PrivateChatPortalMeta && !portal.Encrypted {
				continue
			}
			// Get room create lock to prevent races between receiving contact info and room creation.
			portal.roomCreateLock.Lock()
			meta(portal)
			portal.roomCreateLock.Unlock()
		}
	}
}

func (puppet *Puppet) updatePortalAvatar() {
	puppet.updatePortalMeta(func(portal *Portal) {
		if portal.Avatar == puppet.Avatar && portal.AvatarURL == puppet.AvatarURL && portal.AvatarSet {
			return
		}
		portal.AvatarURL = puppet.AvatarURL
		portal.Avatar = puppet.Avatar
		portal.AvatarSet = false
		defer portal.Update(nil)
		if len(portal.MXID) > 0 {
			_, err := portal.MainIntent().SetRoomAvatar(portal.MXID, puppet.AvatarURL)
			if err != nil {
				portal.log.Warnln("Failed to set avatar:", err)
			} else {
				portal.AvatarSet = true
				portal.UpdateBridgeInfo()
			}
		}
	})
}

func (puppet *Puppet) updatePortalName() {
	puppet.updatePortalMeta(func(portal *Portal) {
		portal.UpdateName(puppet.Displayname, groupme.ID(""), true)
	})
}

func (puppet *Puppet) Sync(source *User, member *groupme.Member, forceAvatarSync, forcePortalSync bool) {
	puppet.syncLock.Lock()
	defer puppet.syncLock.Unlock()
	err := puppet.DefaultIntent().EnsureRegistered()
	if err != nil {
		puppet.log.Errorln("Failed to ensure registered:", err)
	}

	puppet.log.Debugfln("Syncing info through %s", source.GMID)

	// TODO
}
