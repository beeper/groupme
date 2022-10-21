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
	"os"
	"regexp"
	"strings"

	"github.com/karmanyaahm/groupme"
	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/bridge"
	"maunium.net/go/mautrix/id"

	"github.com/beeper/groupme/database"
	"github.com/beeper/groupme/groupmeext"
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
		bridge.Config.Bridge.FormatUsername(
			gmid),
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
}

func (puppet *Puppet) PhoneNumber() string {
	println("phone num")
	return strings.Replace(puppet.GMID, whatsappExt.NewUserSuffix, "", 1)
}

func (puppet *Puppet) IntentFor(portal *Portal) *appservice.IntentAPI {
	if (!portal.IsPrivateChat() && puppet.customIntent == nil) ||
		(portal.backfilling && portal.bridge.Config.Bridge.InviteOwnPuppetForBackfilling) ||
		portal.Key.GMID == puppet.GMID {
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

func (puppet *Puppet) UpdateAvatar(source *User, portalMXID id.RoomID, avatar string) bool {
	memberRaw, _ := puppet.bridge.StateStore.TryGetMemberRaw(portalMXID, puppet.MXID) //TODO Handle

	if memberRaw.Avatar == avatar {
		return false // up to date
	}

	if len(avatar) == 0 {
		var err error
		// err = puppet.DefaultIntent().SetRoomAvatarURL(portalMXID, id.ContentURI{})

		if err != nil {
			puppet.log.Warnln("Failed to remove avatar:", err, puppet.MXID)
			os.Exit(1)
		}
		memberRaw.Avatar = avatar
		memberRaw.AvatarURL = ""

		go puppet.updatePortalAvatar()

		puppet.bridge.StateStore.SetMemberRaw(&memberRaw) //TODO handle
		return true
	}

	//TODO check its actually groupme?
	image, mime, err := groupmeext.DownloadImage(avatar + ".large")
	if err != nil {
		puppet.log.Warnln(err)
		return false
	}

	resp, err := puppet.DefaultIntent().UploadBytes(*image, mime)
	if err != nil {
		puppet.log.Warnln("Failed to upload avatar:", err)
		return false
	}
	// err = puppet.DefaultIntent().SetRoomAvatarURL(portalMXID, resp.ContentURI)
	if err != nil {
		puppet.log.Warnln("Failed to set avatar:", err)
	}

	memberRaw.AvatarURL = resp.ContentURI.String()
	memberRaw.Avatar = avatar

	puppet.bridge.StateStore.SetMemberRaw(&memberRaw) //TODO handle

	go puppet.updatePortalAvatar()
	return true
}

func (puppet *Puppet) UpdateName(source *User, portalMXID id.RoomID, contact groupme.Member) bool {
	newName, _ := puppet.bridge.Config.Bridge.FormatDisplayname(contact)

	memberRaw, _ := puppet.bridge.StateStore.TryGetMemberRaw(portalMXID, puppet.MXID) //TODO Handle

	if memberRaw.DisplayName != newName { //&& quality >= puppet.NameQuality[portalMXID] {
		var err error
		// err = puppet.DefaultIntent().SetRoomDisplayName(portalMXID, newName)

		if err == nil {
			memberRaw.DisplayName = newName
			//	puppet.NameQuality[portalMXID] = quality

			puppet.bridge.StateStore.SetMemberRaw(&memberRaw) //TODO handle; maybe .Update() ?
			go puppet.updatePortalName()
		} else {
			puppet.log.Warnln("Failed to set display name:", err)
		}
		return true
	}
	return false
}

func (puppet *Puppet) updatePortalMeta(meta func(portal *Portal)) {
	if puppet.bridge.Config.Bridge.PrivateChatPortalMeta {
		for _, portal := range puppet.bridge.GetAllPortalsByJID(puppet.JID) {
			meta(portal)
		}
	}
}

func (puppet *Puppet) updatePortalAvatar() {
	puppet.updatePortalMeta(func(portal *Portal) {

		m, _ := puppet.bridge.StateStore.TryGetMemberRaw(portal.MXID, puppet.MXID)
		if len(portal.MXID) > 0 {
			_, err := portal.MainIntent().SetRoomAvatar(portal.MXID, id.MustParseContentURI(m.AvatarURL))
			if err != nil {
				portal.log.Warnln("Failed to set avatar:", err)
			}
		}
		portal.AvatarURL = id.MustParseContentURI(m.AvatarURL)
		portal.Avatar = m.Avatar
		portal.Update()
	})
}

func (puppet *Puppet) updatePortalName() {
	puppet.updatePortalMeta(func(portal *Portal) {
		m, _ := puppet.bridge.StateStore.TryGetMemberRaw(portal.MXID, puppet.MXID)
		if len(portal.MXID) > 0 {
			_, err := portal.MainIntent().SetRoomName(portal.MXID, m.DisplayName)
			if err != nil {
				portal.log.Warnln("Failed to set name:", err)
			}
		}
		portal.Name = m.DisplayName
		portal.Update()
	})
}

func (puppet *Puppet) Sync(source *User, portalMXID id.RoomID, contact groupme.Member) {
	if contact.UserID.String() == "system" {
		puppet.log.Warnln("Trying to sync system puppet")
		return
	}

	err := puppet.DefaultIntent().EnsureRegistered()
	if err != nil {
		puppet.log.Errorln("Failed to ensure registered:", err)
	}

	update := false
	update = puppet.UpdateName(source, portalMXID, contact) || update
	update = puppet.UpdateAvatar(source, portalMXID, contact.ImageURL) || update
	if update {
		puppet.Update()
	}
}
