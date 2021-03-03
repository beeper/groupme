// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2020 Tulir Asokan
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
	"io/ioutil"
	"net/http"
	"regexp"
	"strings"

	"github.com/karmanyaahm/groupme"
	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/id"

	"github.com/karmanyaahm/matrix-groupme-go/database"
	"github.com/karmanyaahm/matrix-groupme-go/types"
	whatsappExt "github.com/karmanyaahm/matrix-groupme-go/whatsapp-ext"
)

var userIDRegex *regexp.Regexp

func (bridge *Bridge) ParsePuppetMXID(mxid id.UserID) (types.GroupMeID, bool) {
	if userIDRegex == nil {
		userIDRegex = regexp.MustCompile(fmt.Sprintf("^@%s:%s$",
			bridge.Config.Bridge.FormatUsername("([0-9]+)"),
			bridge.Config.Homeserver.Domain))
	}
	match := userIDRegex.FindStringSubmatch(string(mxid))
	if match == nil || len(match) != 2 {
		return "", false
	}

	jid := types.GroupMeID(match[1])
	return jid, true
}

func (bridge *Bridge) GetPuppetByMXID(mxid id.UserID) *Puppet {
	jid, ok := bridge.ParsePuppetMXID(mxid)
	if !ok {
		return nil
	}

	return bridge.GetPuppetByJID(jid)
}

func (bridge *Bridge) GetPuppetByJID(jid types.GroupMeID) *Puppet {
	bridge.puppetsLock.Lock()
	defer bridge.puppetsLock.Unlock()
	puppet, ok := bridge.puppets[jid]
	if !ok {
		dbPuppet := bridge.DB.Puppet.Get(jid)
		if dbPuppet == nil {
			dbPuppet = bridge.DB.Puppet.New()
			dbPuppet.JID = jid
			dbPuppet.Insert()
		}
		puppet = bridge.NewPuppet(dbPuppet)
		bridge.puppets[puppet.JID] = puppet
		if len(puppet.CustomMXID) > 0 {
			bridge.puppetsByCustomMXID[puppet.CustomMXID] = puppet
		}
	}
	return puppet
}

func (bridge *Bridge) GetPuppetByCustomMXID(mxid id.UserID) *Puppet {
	bridge.puppetsLock.Lock()
	defer bridge.puppetsLock.Unlock()
	puppet, ok := bridge.puppetsByCustomMXID[mxid]
	if !ok {
		dbPuppet := bridge.DB.Puppet.GetByCustomMXID(mxid)
		if dbPuppet == nil {
			return nil
		}
		puppet = bridge.NewPuppet(dbPuppet)
		bridge.puppets[puppet.JID] = puppet
		bridge.puppetsByCustomMXID[puppet.CustomMXID] = puppet
	}
	return puppet
}

func (bridge *Bridge) GetAllPuppetsWithCustomMXID() []*Puppet {
	return bridge.dbPuppetsToPuppets(bridge.DB.Puppet.GetAllWithCustomMXID())
}

func (bridge *Bridge) GetAllPuppets() []*Puppet {
	return bridge.dbPuppetsToPuppets(bridge.DB.Puppet.GetAll())
}

func (bridge *Bridge) dbPuppetsToPuppets(dbPuppets []*database.Puppet) []*Puppet {
	bridge.puppetsLock.Lock()
	defer bridge.puppetsLock.Unlock()
	output := make([]*Puppet, len(dbPuppets))
	for index, dbPuppet := range dbPuppets {
		if dbPuppet == nil {
			continue
		}
		puppet, ok := bridge.puppets[dbPuppet.JID]
		if !ok {
			puppet = bridge.NewPuppet(dbPuppet)
			bridge.puppets[dbPuppet.JID] = puppet
			if len(dbPuppet.CustomMXID) > 0 {
				bridge.puppetsByCustomMXID[dbPuppet.CustomMXID] = puppet
			}
		}
		output[index] = puppet
	}
	return output
}

func (bridge *Bridge) FormatPuppetMXID(jid types.GroupMeID) id.UserID {
	return id.NewUserID(
		bridge.Config.Bridge.FormatUsername(
			strings.Replace(
				jid,
				whatsappExt.NewUserSuffix, "", 1)),
		bridge.Config.Homeserver.Domain)
}

func (bridge *Bridge) NewPuppet(dbPuppet *database.Puppet) *Puppet {
	return &Puppet{
		Puppet: dbPuppet,
		bridge: bridge,
		log:    bridge.Log.Sub(fmt.Sprintf("Puppet/%s", dbPuppet.JID)),

		MXID: bridge.FormatPuppetMXID(dbPuppet.JID),
	}
}

type Puppet struct {
	*database.Puppet

	bridge *Bridge
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
	return strings.Replace(puppet.JID, whatsappExt.NewUserSuffix, "", 1)
}

func (puppet *Puppet) IntentFor(portal *Portal) *appservice.IntentAPI {
	if (!portal.IsPrivateChat() && puppet.customIntent == nil) ||
		(portal.backfilling && portal.bridge.Config.Bridge.InviteOwnPuppetForBackfilling) ||
		portal.Key.JID == puppet.JID {
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

func (puppet *Puppet) UpdateAvatar(source *User, avatar string) bool {
	if len(avatar) == 0 {
		err := puppet.DefaultIntent().SetAvatarURL(id.ContentURI{})
		if err != nil {
			puppet.log.Warnln("Failed to remove avatar:", err)
		}
		puppet.AvatarURL = types.ContentURI{}
		puppet.Avatar = avatar
		go puppet.updatePortalAvatar()
		return true
	}

	if puppet.Avatar == avatar {
		return false // up to date
	}

	//TODO check its actually groupme?
	response, err := http.Get(avatar + ".large")
	if err != nil {
		puppet.log.Warnln("Failed to download avatar:", err)
		return false
	}
	defer response.Body.Close()

	image, err := ioutil.ReadAll(response.Body)
	if err != nil {
		puppet.log.Warnln("Failed to read downloaded avatar:", err)
		return false
	}

	mime := response.Header.Get("Content-Type")
	if len(mime) == 0 {
		mime = http.DetectContentType(image)
	}
	resp, err := puppet.DefaultIntent().UploadBytes(image, mime)
	if err != nil {
		puppet.log.Warnln("Failed to upload avatar:", err)
		return false
	}

	puppet.AvatarURL = types.ContentURI{resp.ContentURI}
	err = puppet.DefaultIntent().SetAvatarURL(resp.ContentURI)
	if err != nil {
		puppet.log.Warnln("Failed to set avatar:", err)
	}
	puppet.Avatar = avatar
	go puppet.updatePortalAvatar()
	return true
}

func (puppet *Puppet) UpdateName(source *User, contact groupme.Member) bool {
	newName, quality := puppet.bridge.Config.Bridge.FormatDisplayname(contact)
	if puppet.Displayname != newName && quality >= puppet.NameQuality {
		err := puppet.DefaultIntent().SetDisplayName(newName)
		if err == nil {
			puppet.Displayname = newName
			puppet.NameQuality = quality
			go puppet.updatePortalName()
			puppet.Update()
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
		if len(portal.MXID) > 0 {
			_, err := portal.MainIntent().SetRoomAvatar(portal.MXID, puppet.AvatarURL.ContentURI)
			if err != nil {
				portal.log.Warnln("Failed to set avatar:", err)
			}
		}
		portal.AvatarURL = puppet.AvatarURL
		portal.Avatar = puppet.Avatar
		portal.Update()
	})
}

func (puppet *Puppet) updatePortalName() {
	puppet.updatePortalMeta(func(portal *Portal) {
		if len(portal.MXID) > 0 {
			_, err := portal.MainIntent().SetRoomName(portal.MXID, puppet.Displayname)
			if err != nil {
				portal.log.Warnln("Failed to set name:", err)
			}
		}
		portal.Name = puppet.Displayname
		portal.Update()
	})
}

func (puppet *Puppet) Sync(source *User, contact groupme.Member) {
	if contact.UserID.String() == "system" {
		puppet.log.Warnln("Trying to sync system puppet")

		//		portal.Sync(puppet.bridge.GetUserByJID(portal.Key.Receiver), groupme.Group{})
		//TODO permissoins idk if its fine to use portal owner

		return
	}

	err := puppet.DefaultIntent().EnsureRegistered()
	if err != nil {
		puppet.log.Errorln("Failed to ensure registered:", err)
	}

	//if contact.ID.String() == source.JID {
	//TODO What is this
	//		contact.Notify = source.Conn.Info.Pushname
	//}

	update := false
	update = puppet.UpdateName(source, contact) || update
	update = puppet.UpdateAvatar(source, contact.ImageURL) || update
	if update {
		puppet.Update()
	}
}
