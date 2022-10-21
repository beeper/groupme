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
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"io/ioutil"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/bridge"
	"maunium.net/go/mautrix/bridge/bridgeconfig"
	"maunium.net/go/mautrix/crypto/attachment"

	"github.com/Rhymen/go-whatsapp"
	"github.com/gabriel-vasile/mimetype"
	"github.com/karmanyaahm/groupme"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/beeper/groupme/database"
	"github.com/beeper/groupme/groupmeext"
)

func (bridge *GMBridge) GetPortalByMXID(mxid id.RoomID) *Portal {
	bridge.portalsLock.Lock()
	defer bridge.portalsLock.Unlock()
	portal, ok := bridge.portalsByMXID[mxid]
	if !ok {
		return bridge.loadDBPortal(bridge.DB.Portal.GetByMXID(mxid), nil)
	}
	return portal
}

func (br *GMBridge) GetIPortal(mxid id.RoomID) bridge.Portal {
	p := br.GetPortalByMXID(mxid)
	if p == nil {
		return nil
	}
	return p
}

func (portal *Portal) IsEncrypted() bool {
	return portal.Encrypted
}

func (portal *Portal) MarkEncrypted() {
	portal.Encrypted = true
	portal.Update(nil)
}

func (portal *Portal) ReceiveMatrixEvent(user bridge.User, evt *event.Event) {
	if user.GetPermissionLevel() >= bridgeconfig.PermissionLevelUser {
		portal.matrixMessages <- PortalMatrixMessage{user: user.(*User), evt: evt, receivedAt: time.Now()}
	}
}

func (bridge *GMBridge) GetPortalByGMID(key database.PortalKey) *Portal {
	bridge.portalsLock.Lock()
	defer bridge.portalsLock.Unlock()
	portal, ok := bridge.portalsByGMID[key]
	if !ok {
		return bridge.loadDBPortal(bridge.DB.Portal.GetByGMID(key), &key)
	}
	return portal
}

func (br *GMBridge) GetAllPortals() []*Portal {
	return br.dbPortalsToPortals(br.DB.Portal.GetAll())
}

func (br *GMBridge) GetAllIPortals() (iportals []bridge.Portal) {
	portals := br.GetAllPortals()
	iportals = make([]bridge.Portal, len(portals))
	for i, portal := range portals {
		iportals[i] = portal
	}
	return iportals
}

func (br *GMBridge) GetAllPortalsByGMID(gmid groupme.ID) []*Portal {
	return br.dbPortalsToPortals(br.DB.Portal.GetAllByGMID(gmid))
}

func (bridge *GMBridge) dbPortalsToPortals(dbPortals []*database.Portal) []*Portal {
	bridge.portalsLock.Lock()
	defer bridge.portalsLock.Unlock()
	output := make([]*Portal, len(dbPortals))
	for index, dbPortal := range dbPortals {
		if dbPortal == nil {
			continue
		}
		portal, ok := bridge.portalsByGMID[dbPortal.Key]
		if !ok {
			portal = bridge.loadDBPortal(dbPortal, nil)
		}
		output[index] = portal
	}
	return output
}

func (bridge *GMBridge) loadDBPortal(dbPortal *database.Portal, key *database.PortalKey) *Portal {
	if dbPortal == nil {
		if key == nil {
			return nil
		}
		dbPortal = bridge.DB.Portal.New()
		dbPortal.Key = *key
		dbPortal.Insert()
	}
	portal := bridge.NewPortal(dbPortal)
	bridge.portalsByGMID[portal.Key] = portal
	if len(portal.MXID) > 0 {
		bridge.portalsByMXID[portal.MXID] = portal
	}
	return portal
}

func (portal *Portal) GetUsers() []*User {
	return nil
}

func (bridge *GMBridge) NewManualPortal(key database.PortalKey) *Portal {
	portal := &Portal{
		Portal: bridge.DB.Portal.New(),
		bridge: bridge,
		log:    bridge.Log.Sub(fmt.Sprintf("Portal/%s", key)),

		recentlyHandled: make([]string, recentlyHandledLength),

		messages: make(chan PortalMessage, bridge.Config.Bridge.PortalMessageBuffer),
	}
	portal.Key = key
	go portal.handleMessageLoop()
	return portal
}

func (bridge *GMBridge) NewPortal(dbPortal *database.Portal) *Portal {
	portal := &Portal{
		Portal: dbPortal,
		bridge: bridge,
		log:    bridge.Log.Sub(fmt.Sprintf("Portal/%s", dbPortal.Key)),

		recentlyHandled: make([]string, recentlyHandledLength),

		messages: make(chan PortalMessage, bridge.Config.Bridge.PortalMessageBuffer),
	}
	go portal.handleMessageLoop()
	return portal
}

const recentlyHandledLength = 100

type PortalMessage struct {
	chat      database.PortalKey
	source    *User
	data      *groupme.Message
	timestamp uint64
}

type PortalMatrixMessage struct {
	evt        *event.Event
	user       *User
	receivedAt time.Time
}

type Portal struct {
	*database.Portal

	bridge *GMBridge
	log    log.Logger

	roomCreateLock sync.Mutex

	recentlyHandled      []string
	recentlyHandledLock  sync.Mutex
	recentlyHandledIndex uint8

	encryptLock   sync.Mutex
	backfilling   bool
	lastMessageTs uint64

	privateChatBackfillInvitePuppet func()

	messages       chan PortalMessage
	matrixMessages chan PortalMatrixMessage

	hasRelaybot *bool
}

const MaxMessageAgeToCreatePortal = 5 * 60 // 5 minutes

func (portal *Portal) handleMessageLoop() {
	for msg := range portal.messages {
		if len(portal.MXID) == 0 {
			if msg.timestamp+MaxMessageAgeToCreatePortal < uint64(time.Now().Unix()) {
				portal.log.Debugln("Not creating portal room for incoming message: message is too old")
				continue
			}
			portal.log.Debugln("Creating Matrix room from incoming message")
			err := portal.CreateMatrixRoom(msg.source)
			if err != nil {
				portal.log.Errorln("Failed to create portal room:", err)
				continue
			}
		}
		portal.handleMessage(msg)
	}
}

func (portal *Portal) handleMessage(msg PortalMessage) {
	if len(portal.MXID) == 0 {
		portal.log.Warnln("handleMessage called even though portal.MXID is empty")
		return
	}
	portal.HandleTextMessage(msg.source, msg.data)
	// portal.handleReaction(msg.data.ID.String(), msg.data.FavoritedBy)
}

func (portal *Portal) isRecentlyHandled(id groupme.ID) bool {
	idStr := id.String()
	for i := recentlyHandledLength - 1; i >= 0; i-- {
		if portal.recentlyHandled[i] == idStr {
			return true
		}
	}
	return false
}

func (portal *Portal) isDuplicate(id groupme.ID) bool {
	msg := portal.bridge.DB.Message.GetByGMID(portal.Key, id)
	if msg != nil {
		return true
	}

	return false
}

func init() {
}

func (portal *Portal) markHandled(source *User, message *groupme.Message, mxid id.EventID) {
	msg := portal.bridge.DB.Message.New()
	msg.Chat = portal.Key
	msg.GMID = message.ID
	msg.MXID = mxid
	msg.Timestamp = message.CreatedAt.ToTime()
	if message.UserID == source.GMID {
		msg.Sender = source.GMID
	} else if portal.IsPrivateChat() {
		msg.Sender = portal.Key.GMID
	} else {
		msg.Sender = message.SenderID
	}
	// msg.Insert()

	portal.recentlyHandledLock.Lock()
	portal.recentlyHandled[0] = "" //FIFO queue being implemented here //TODO: is this efficent
	portal.recentlyHandled = portal.recentlyHandled[1:]
	portal.recentlyHandled = append(portal.recentlyHandled, message.ID.String())
	portal.recentlyHandledLock.Unlock()
}

func (portal *Portal) getMessageIntent(user *User, info *groupme.Message) *appservice.IntentAPI {
	if portal.IsPrivateChat() {
		if info.UserID == user.GetGMID() { //from me
			return portal.bridge.GetPuppetByGMID(user.GMID).DefaultIntent()
		}
		return portal.MainIntent()
	} else if len(info.UserID.String()) == 0 {
		println("TODO weird uid stuff")
	} else if info.UserID == user.GetGMID() { //from me
		return portal.bridge.GetPuppetByGMID(user.GMID).IntentFor(portal)
	}
	return portal.bridge.GetPuppetByGMID(info.UserID).IntentFor(portal)
}

func (portal *Portal) getReactionIntent(jid groupme.ID) *appservice.IntentAPI {
	return portal.bridge.GetPuppetByGMID(jid).IntentFor(portal)
}

func (portal *Portal) startHandling(source *User, info *groupme.Message) *appservice.IntentAPI {
	// TODO these should all be trace logs
	if portal.lastMessageTs > uint64(info.CreatedAt.ToTime().Unix()+1) {
		portal.log.Debugfln("Not handling %s: message is older (%d) than last bridge message (%d)", info.ID, info.CreatedAt, portal.lastMessageTs)
	} else if portal.isRecentlyHandled(info.ID) {
		portal.log.Debugfln("Not handling %s: message was recently handled", info.ID)
	} else if portal.isDuplicate(info.ID) {
		portal.log.Debugfln("Not handling %s: message is duplicate", info.ID)
	} else if info.System {
		portal.log.Debugfln("Not handling %s: message is from system: %s", info.ID, info.Text)
	} else {
		portal.lastMessageTs = uint64(info.CreatedAt.ToTime().Unix())
		intent := portal.getMessageIntent(source, info)
		if intent != nil {
			portal.log.Debugfln("Starting handling of %s (ts: %d)", info.ID, info.CreatedAt)
		} else {
			portal.log.Debugfln("Not handling %s: sender is not known", info.ID.String())
		}
		return intent
	}
	return nil
}

func (portal *Portal) finishHandling(source *User, message *groupme.Message, mxid id.EventID) {
	portal.markHandled(source, message, mxid)
	portal.sendDeliveryReceipt(mxid)
	portal.log.Debugln("Handled message", message.ID.String(), "->", mxid)
}

func (portal *Portal) SyncParticipants(metadata *groupme.Group) {
	changed := false
	levels, err := portal.MainIntent().PowerLevels(portal.MXID)
	if err != nil {
		levels = portal.GetBasePowerLevels()
		changed = true
	}
	participantMap := make(map[groupme.ID]bool)
	for _, participant := range metadata.Members {
		participantMap[participant.UserID] = true
		user := portal.bridge.GetUserByGMID(participant.UserID)
		portal.userMXIDAction(user, portal.ensureMXIDInvited)

		puppet := portal.bridge.GetPuppetByGMID(participant.UserID)
		err := puppet.IntentFor(portal).EnsureJoined(portal.MXID)
		if err != nil {
			portal.log.Warnfln("Failed to make puppet of %s join %s: %v", participant.ID.String(), portal.MXID, err)
		}

		expectedLevel := 0
		//	if participant.IsSuperAdmin {
		//		expectedLevel = 95
		//	} else if participant.IsAdmin {
		//		expectedLevel = 50
		//	}
		changed = levels.EnsureUserLevel(puppet.MXID, expectedLevel) || changed
		if user != nil {
			changed = levels.EnsureUserLevel(user.MXID, expectedLevel) || changed
		}
		puppet.Sync(nil, portal.MXID, *participant) //why nil whynot
	}
	if changed {
		_, err = portal.MainIntent().SetPowerLevels(portal.MXID, levels)
		if err != nil {
			portal.log.Errorln("Failed to change power levels:", err)
		}
	}
	members, err := portal.MainIntent().JoinedMembers(portal.MXID)
	if err != nil {
		portal.log.Warnln("Failed to get member list:", err)
	} else {
		for member := range members.Joined {
			jid, ok := portal.bridge.ParsePuppetMXID(member)
			if ok {
				_, shouldBePresent := participantMap[jid]
				if !shouldBePresent {
					_, err := portal.MainIntent().KickUser(portal.MXID, &mautrix.ReqKickUser{
						UserID: member,
						Reason: "User had left this WhatsApp chat",
					})
					if err != nil {
						portal.log.Warnfln("Failed to kick user %s who had left: %v", member, err)
					}
				}
			}
		}
	}
}

func (portal *Portal) UpdateAvatar(user *User, avatar string, updateInfo bool) bool {
	//	if len(avatar) == 0 {
	//		var err error
	//		avatar, err = user.Conn.GetProfilePicThumb(portal.Key.JID)
	//		if err != nil {
	//			portal.log.Errorln(err)
	//			return false
	//		}
	//	}
	//TODO: duplicated code from puppet.UpdateAvatar
	if len(avatar) == 0 {
		if len(portal.Avatar) == 0 {
			return false
		}
		err := portal.MainIntent().SetAvatarURL(id.ContentURI{})
		if err != nil {
			portal.log.Warnln("Failed to remove avatar:", err)
		}
		portal.AvatarURL = id.ContentURI{}
		portal.Avatar = avatar
		return true
	}

	if portal.Avatar == avatar {
		return false
	}

	//TODO check its actually groupme?
	response, err := http.Get(avatar + ".large")
	if err != nil {
		portal.log.Warnln("Failed to download avatar:", err)
		return false
	}
	defer response.Body.Close()

	image, err := ioutil.ReadAll(response.Body)
	if err != nil {
		portal.log.Warnln("Failed to read downloaded avatar:", err)
		return false
	}

	mime := response.Header.Get("Content-Type")
	if len(mime) == 0 {
		mime = http.DetectContentType(image)
	}
	resp, err := portal.MainIntent().UploadBytes(image, mime)
	if err != nil {
		portal.log.Warnln("Failed to upload avatar:", err)
		return false
	}

	portal.AvatarURL = resp.ContentURI
	if len(portal.MXID) > 0 {
		_, err = portal.MainIntent().SetRoomAvatar(portal.MXID, resp.ContentURI)
		if err != nil {
			portal.log.Warnln("Failed to set room topic:", err)
			return false
		}
	}
	portal.Avatar = avatar
	if updateInfo {
		portal.UpdateBridgeInfo()
	}
	return true
}

func (portal *Portal) UpdateName(name string, setBy groupme.ID, updateInfo bool) bool {
	if portal.Name != name {
		intent := portal.MainIntent()
		if len(setBy) > 0 {
			intent = portal.bridge.GetPuppetByGMID(setBy).IntentFor(portal)
		}
		_, err := intent.SetRoomName(portal.MXID, name)
		if err == nil {
			portal.Name = name
			if updateInfo {
				portal.UpdateBridgeInfo()
			}
			return true
		}
		portal.log.Warnln("Failed to set room name:", err)
	}
	return false
}

func (portal *Portal) UpdateTopic(topic string, setBy groupme.ID, updateInfo bool) bool {
	if portal.Topic != topic {
		intent := portal.MainIntent()
		if len(setBy) > 0 {
			intent = portal.bridge.GetPuppetByGMID(setBy).IntentFor(portal)
		}
		_, err := intent.SetRoomTopic(portal.MXID, topic)
		if err == nil {
			portal.Topic = topic
			if updateInfo {
				portal.UpdateBridgeInfo()
			}
			return true
		}
		portal.log.Warnln("Failed to set room topic:", err)
	}
	return false
}

func (portal *Portal) UpdateMetadata(user *User) bool {
	if portal.IsPrivateChat() {
		return false
	}
	group, err := user.Client.ShowGroup(context.TODO(), groupme.ID(strings.Replace(portal.Key.GMID.String(), groupmeext.NewUserSuffix, "", 1)))
	if err != nil {
		portal.log.Errorln(err)
		return false
	}
	//	if metadata.Status != 0 {
	// 401: access denied
	// 404: group does (no longer) exist
	// 500: ??? happens with status@broadcast

	// TODO: update the room, e.g. change priority level
	//   to send messages to moderator
	//return false
	//	}

	portal.SyncParticipants(group)
	update := false
	update = portal.UpdateName(group.Name, "", false) || update
	update = portal.UpdateTopic(group.Description, "", false) || update

	//	portal.RestrictMessageSending(metadata.Announce)

	return update
}

func (portal *Portal) userMXIDAction(user *User, fn func(mxid id.UserID)) {
	if user == nil {
		return
	}

	fn(user.MXID)
}

func (portal *Portal) ensureMXIDInvited(mxid id.UserID) {
	err := portal.MainIntent().EnsureInvited(portal.MXID, mxid)
	if err != nil {
		portal.log.Warnfln("Failed to ensure %s is invited to %s: %v", mxid, portal.MXID, err)
	}
}

func (portal *Portal) ensureUserInvited(user *User) bool {
	return user.ensureInvited(portal.MainIntent(), portal.MXID, portal.IsPrivateChat())
}

func (portal *Portal) Sync(user *User, group *groupme.Group) {
	portal.log.Infoln("Syncing portal for", user.MXID)

	sub := user.Conn.SubscribeToGroup
	if portal.IsPrivateChat() {
		sub = user.Conn.SubscribeToDM
	}
	err := sub(context.TODO(), groupme.ID(portal.Key.Receiver), user.Token)
	if err != nil {
		portal.log.Errorln("Subscribing failed, live metadata updates won't work", err)
	}

	if len(portal.MXID) == 0 {
		if !portal.IsPrivateChat() {
			portal.Name = group.Name
		}
		err := portal.CreateMatrixRoom(user)
		if err != nil {
			portal.log.Errorln("Failed to create portal room:", err)
			return
		}
	} else {
		portal.ensureUserInvited(user)
	}

	if portal.IsPrivateChat() {
		return
	}

	update := false
	update = portal.UpdateMetadata(user) || update
	update = portal.UpdateAvatar(user, group.ImageURL, false) || update

	if update {
		portal.Update(nil)
		portal.UpdateBridgeInfo()
	}
}

func (portal *Portal) GetBasePowerLevels() *event.PowerLevelsEventContent {
	anyone := 0
	nope := 99
	invite := 50
	if portal.bridge.Config.Bridge.AllowUserInvite {
		invite = 0
	}
	return &event.PowerLevelsEventContent{
		UsersDefault:    anyone,
		EventsDefault:   anyone,
		RedactPtr:       &anyone,
		StateDefaultPtr: &nope,
		BanPtr:          &nope,
		InvitePtr:       &invite,
		Users: map[id.UserID]int{
			portal.MainIntent().UserID: 100,
		},
		Events: map[string]int{
			event.StateRoomName.Type:   anyone,
			event.StateRoomAvatar.Type: anyone,
			event.StateTopic.Type:      anyone,
		},
	}
}

func (portal *Portal) RestrictMessageSending(restrict bool) {
	levels, err := portal.MainIntent().PowerLevels(portal.MXID)
	if err != nil {
		levels = portal.GetBasePowerLevels()
	}

	newLevel := 0
	if restrict {
		newLevel = 50
	}

	if levels.EventsDefault == newLevel {
		return
	}

	levels.EventsDefault = newLevel
	_, err = portal.MainIntent().SetPowerLevels(portal.MXID, levels)
	if err != nil {
		portal.log.Errorln("Failed to change power levels:", err)
	}
}

func (portal *Portal) RestrictMetadataChanges(restrict bool) {
	levels, err := portal.MainIntent().PowerLevels(portal.MXID)
	if err != nil {
		levels = portal.GetBasePowerLevels()
	}
	newLevel := 0
	if restrict {
		newLevel = 50
	}
	changed := false
	changed = levels.EnsureEventLevel(event.StateRoomName, newLevel) || changed
	changed = levels.EnsureEventLevel(event.StateRoomAvatar, newLevel) || changed
	changed = levels.EnsureEventLevel(event.StateTopic, newLevel) || changed
	if changed {
		_, err = portal.MainIntent().SetPowerLevels(portal.MXID, levels)
		if err != nil {
			portal.log.Errorln("Failed to change power levels:", err)
		}
	}
}

type BridgeInfoSection struct {
	ID          string              `json:"id"`
	DisplayName string              `json:"displayname,omitempty"`
	AvatarURL   id.ContentURIString `json:"avatar_url,omitempty"`
	ExternalURL string              `json:"external_url,omitempty"`
}

type BridgeInfoContent struct {
	BridgeBot id.UserID          `json:"bridgebot"`
	Creator   id.UserID          `json:"creator,omitempty"`
	Protocol  BridgeInfoSection  `json:"protocol"`
	Network   *BridgeInfoSection `json:"network,omitempty"`
	Channel   BridgeInfoSection  `json:"channel"`
}

var (
	StateBridgeInfo         = event.Type{Type: "m.bridge", Class: event.StateEventType}
	StateHalfShotBridgeInfo = event.Type{Type: "uk.half-shot.bridge", Class: event.StateEventType}
)

func (portal *Portal) getBridgeInfo() (string, BridgeInfoContent) {
	bridgeInfo := BridgeInfoContent{
		BridgeBot: portal.bridge.Bot.UserID,
		Creator:   portal.MainIntent().UserID,
		Protocol: BridgeInfoSection{
			ID:          "whatsapp",
			DisplayName: "WhatsApp",
			AvatarURL:   id.ContentURIString(portal.bridge.Config.AppService.Bot.Avatar),
			ExternalURL: "https://www.whatsapp.com/",
		},
		Channel: BridgeInfoSection{
			ID:          portal.Key.GMID.String(),
			DisplayName: portal.Name,
			AvatarURL:   portal.AvatarURL.CUString(),
		},
	}
	bridgeInfoStateKey := fmt.Sprintf("net.maunium.whatsapp://whatsapp/%s", portal.Key.GMID)
	return bridgeInfoStateKey, bridgeInfo
}

func (portal *Portal) UpdateBridgeInfo() {
	if len(portal.MXID) == 0 {
		portal.log.Debugln("Not updating bridge info: no Matrix room created")
		return
	}
	portal.log.Debugln("Updating bridge info...")
	stateKey, content := portal.getBridgeInfo()
	_, err := portal.MainIntent().SendStateEvent(portal.MXID, StateBridgeInfo, stateKey, content)
	if err != nil {
		portal.log.Warnln("Failed to update m.bridge:", err)
	}
	_, err = portal.MainIntent().SendStateEvent(portal.MXID, StateHalfShotBridgeInfo, stateKey, content)
	if err != nil {
		portal.log.Warnln("Failed to update uk.half-shot.bridge:", err)
	}
}

func (portal *Portal) GetEncryptionEventContent() (evt *event.EncryptionEventContent) {
	evt = &event.EncryptionEventContent{Algorithm: id.AlgorithmMegolmV1}
	if rot := portal.bridge.Config.Bridge.Encryption.Rotation; rot.EnableCustom {
		evt.RotationPeriodMillis = rot.Milliseconds
		evt.RotationPeriodMessages = rot.Messages
	}
	return
}

func (portal *Portal) CreateMatrixRoom(user *User) error {
	portal.roomCreateLock.Lock()
	defer portal.roomCreateLock.Unlock()
	if len(portal.MXID) > 0 {
		return nil
	}

	intent := portal.MainIntent()
	if err := intent.EnsureRegistered(); err != nil {
		return err
	}

	portal.log.Infoln("Creating Matrix room. Info source:", user.MXID)

	var metadata *groupme.Group
	if portal.IsPrivateChat() {
		puppet := portal.bridge.GetPuppetByGMID(portal.Key.GMID)
		if portal.bridge.Config.Bridge.PrivateChatPortalMeta || portal.bridge.Config.Bridge.Encryption.Default {
			portal.Name = puppet.Displayname
			portal.AvatarURL = puppet.AvatarURL
			portal.Avatar = puppet.Avatar
		} else {
			portal.Name = ""
		}
		portal.Topic = "GroupMe private chat"
	} else {
		var err error
		metadata, err = user.Client.ShowGroup(context.TODO(), groupme.ID(portal.Key.GMID))
		if err == nil {
			portal.Name = metadata.Name
			portal.Topic = metadata.Description
		}
		portal.UpdateAvatar(user, metadata.ImageURL, false)
	}

	bridgeInfoStateKey, bridgeInfo := portal.getBridgeInfo()

	initialState := []*event.Event{{
		Type: event.StatePowerLevels,
		Content: event.Content{
			Parsed: portal.GetBasePowerLevels(),
		},
	}, {
		Type:     StateBridgeInfo,
		Content:  event.Content{Parsed: bridgeInfo},
		StateKey: &bridgeInfoStateKey,
	}, {
		// TODO remove this once https://github.com/matrix-org/matrix-doc/pull/2346 is in spec
		Type:     StateHalfShotBridgeInfo,
		Content:  event.Content{Parsed: bridgeInfo},
		StateKey: &bridgeInfoStateKey,
	}}
	if !portal.AvatarURL.IsEmpty() {
		initialState = append(initialState, &event.Event{
			Type: event.StateRoomAvatar,
			Content: event.Content{
				Parsed: event.RoomAvatarEventContent{URL: portal.AvatarURL},
			},
		})
	}

	invite := []id.UserID{user.MXID}

	if portal.bridge.Config.Bridge.Encryption.Default {
		initialState = append(initialState, &event.Event{
			Type: event.StateEncryption,
			Content: event.Content{
				Parsed: event.EncryptionEventContent{Algorithm: id.AlgorithmMegolmV1},
			},
		})
		portal.Encrypted = true
		if portal.IsPrivateChat() {
			invite = append(invite, portal.bridge.Bot.UserID)
		}
	}

	resp, err := intent.CreateRoom(&mautrix.ReqCreateRoom{
		Visibility:   "private",
		Name:         portal.Name,
		Topic:        portal.Topic,
		Invite:       invite,
		Preset:       "private_chat",
		IsDirect:     portal.IsPrivateChat(),
		InitialState: initialState,
	})
	if err != nil {
		return err
	} else if len(resp.RoomID) == 0 {
		return errors.New("Empty room ID")
	}
	portal.MXID = resp.RoomID
	portal.Update(nil)
	portal.bridge.portalsLock.Lock()
	portal.bridge.portalsByMXID[portal.MXID] = portal
	portal.bridge.portalsLock.Unlock()

	// We set the memberships beforehand to make sure the encryption key exchange in initial backfill knows the users are here.
	for _, user := range invite {
		portal.bridge.StateStore.SetMembership(portal.MXID, user, event.MembershipInvite)
	}

	if metadata != nil {
		portal.SyncParticipants(metadata)
		//	if metadata.Announce {
		//		portal.RestrictMessageSending(metadata.Announce)
		//	}
	} else {
		customPuppet := portal.bridge.GetPuppetByCustomMXID(user.MXID)
		if customPuppet != nil && customPuppet.CustomIntent() != nil {
			_ = customPuppet.CustomIntent().EnsureJoined(portal.MXID)
		}
	}
	if portal.IsPrivateChat() {
		puppet := user.bridge.GetPuppetByGMID(portal.Key.GMID)

		if portal.bridge.Config.Bridge.Encryption.Default {
			err = portal.bridge.Bot.EnsureJoined(portal.MXID)
			if err != nil {
				portal.log.Errorln("Failed to join created portal with bridge bot for e2be:", err)
			}
		}

		user.UpdateDirectChats(map[id.UserID][]id.RoomID{puppet.MXID: {portal.MXID}})
	}
	return nil
}

func (portal *Portal) IsPrivateChat() bool {
	return portal.Key.IsPrivate()
}

func (portal *Portal) IsStatusBroadcastRoom() bool {
	return portal.Key.GMID == "status@broadcast"
}

func (portal *Portal) MainIntent() *appservice.IntentAPI {
	if portal.IsPrivateChat() {
		return portal.bridge.GetPuppetByGMID(portal.Key.GMID).DefaultIntent()
	}
	return portal.bridge.Bot
}

func (portal *Portal) SetReply(content *event.MessageEventContent, msgID groupme.ID) {
	if len(msgID) == 0 {
		return
	}
	message := portal.bridge.DB.Message.GetByGMID(portal.Key, msgID)
	if message != nil {
		evt, err := portal.MainIntent().GetEvent(portal.MXID, message.MXID)
		if err != nil {
			portal.log.Warnln("Failed to get reply target:", err)
			return
		}
		if evt.Type == event.EventEncrypted {
			_ = evt.Content.ParseRaw(evt.Type)
			decryptedEvt, err := portal.bridge.Crypto.Decrypt(evt)
			if err != nil {
				portal.log.Warnln("Failed to decrypt reply target:", err)
			} else {
				evt = decryptedEvt
			}
		}
		_ = evt.Content.ParseRaw(evt.Type)
		content.SetReply(evt)
	}
	return
}

const MessageSendRetries = 5
const MediaUploadRetries = 5
const BadGatewaySleep = 5 * time.Second

func (portal *Portal) sendReaction(intent *appservice.IntentAPI, eventID id.EventID, reaction string) (*mautrix.RespSendEvent, error) {
	var content event.ReactionEventContent
	content.RelatesTo = event.RelatesTo{
		Type:    event.RelAnnotation,
		EventID: eventID,
		Key:     reaction,
	}
	return intent.SendMassagedMessageEvent(portal.MXID, event.EventReaction, &content, time.Now().UnixMilli())
}

func isGatewayError(err error) bool {
	if err == nil {
		return false
	}
	var httpErr mautrix.HTTPError
	return errors.As(err, &httpErr) && (httpErr.IsStatus(http.StatusBadGateway) || httpErr.IsStatus(http.StatusGatewayTimeout))
}

func (portal *Portal) encrypt(intent *appservice.IntentAPI, content *event.Content, eventType event.Type) (event.Type, error) {
	if !portal.Encrypted || portal.bridge.Crypto == nil {
		return eventType, nil
	}
	intent.AddDoublePuppetValue(content)
	// TODO maybe the locking should be inside mautrix-go?
	portal.encryptLock.Lock()
	defer portal.encryptLock.Unlock()
	err := portal.bridge.Crypto.Encrypt(portal.MXID, eventType, content)
	if err != nil {
		return eventType, fmt.Errorf("failed to encrypt event: %w", err)
	}
	return event.EventEncrypted, nil
}

func (portal *Portal) sendMessage(intent *appservice.IntentAPI, eventType event.Type, content *event.MessageEventContent, extraContent map[string]any, timestamp int64) (*mautrix.RespSendEvent, error) {
	wrappedContent := event.Content{Parsed: content, Raw: extraContent}
	var err error
	eventType, err = portal.encrypt(intent, &wrappedContent, eventType)
	if err != nil {
		return nil, err
	}

	_, _ = intent.UserTyping(portal.MXID, false, 0)
	if timestamp == 0 {
		return intent.SendMessageEvent(portal.MXID, eventType, &wrappedContent)
	} else {
		return intent.SendMassagedMessageEvent(portal.MXID, eventType, &wrappedContent, timestamp)
	}
}

func (portal *Portal) handleAttachment(intent *appservice.IntentAPI, attachment *groupme.Attachment, source *User, message *groupme.Message) (msg *event.MessageEventContent, sendText bool, err error) {
	sendText = true
	switch attachment.Type {
	case "image":
		imgData, mime, err := groupmeext.DownloadImage(attachment.URL)
		if err != nil {
			return nil, true, fmt.Errorf("failed to load media info: %w", err)
		}

		var width, height int
		if strings.HasPrefix(mime, "image/") {
			cfg, _, _ := image.DecodeConfig(bytes.NewReader(*imgData))
			width, height = cfg.Width, cfg.Height
		}
		data, uploadMimeType, file := portal.encryptFile(*imgData, mime)

		uploaded, err := portal.uploadWithRetry(intent, data, uploadMimeType, MediaUploadRetries)
		if err != nil {
			if errors.Is(err, mautrix.MTooLarge) {
				err = errors.New("homeserver rejected too large file")
			} else if httpErr := err.(mautrix.HTTPError); httpErr.IsStatus(413) {
				err = errors.New("proxy rejected too large file")
			} else {
				err = fmt.Errorf("failed to upload media: %w", err)
			}
			return nil, true, err
		}
		attachmentUrl, _ := url.Parse(attachment.URL)
		urlParts := strings.Split(attachmentUrl.Path, ".")
		var fname1, fname2 string
		if len(urlParts) == 2 {
			fname1, fname2 = urlParts[1], urlParts[0]
		} else if len(urlParts) > 2 {
			fname1, fname2 = urlParts[2], urlParts[1]
		} //TODO abstract groupme url parsing in groupmeext
		fname := fmt.Sprintf("%s.%s", fname1, fname2)

		content := &event.MessageEventContent{
			Body: fname,
			File: file,
			Info: &event.FileInfo{
				Size:     len(data),
				MimeType: mime,
				Width:    width,
				Height:   height,
				//Duration: int(msg.length),
			},
		}
		if content.File != nil {
			content.File.URL = uploaded.ContentURI.CUString()
		} else {
			content.URL = uploaded.ContentURI.CUString()
		}
		//TODO thumbnail since groupme supports it anyway
		content.MsgType = event.MsgImage

		return content, true, nil
	case "video":
		vidContents, mime := groupmeext.DownloadVideo(attachment.VideoPreviewURL, attachment.URL, source.Token)
		if mime == "" {
			mime = mimetype.Detect(vidContents).String()
		}

		data, uploadMimeType, file := portal.encryptFile(vidContents, mime)
		uploaded, err := portal.uploadWithRetry(intent, data, uploadMimeType, MediaUploadRetries)
		if err != nil {
			if errors.Is(err, mautrix.MTooLarge) {
				err = errors.New("homeserver rejected too large file")
			} else if httpErr := err.(mautrix.HTTPError); httpErr.IsStatus(413) {
				err = errors.New("proxy rejected too large file")
			} else {
				err = fmt.Errorf("failed to upload media: %w", err)
			}
			return nil, true, err
		}

		text := strings.Split(attachment.URL, "/")
		content := &event.MessageEventContent{
			Body: text[len(text)-1],
			File: file,
			Info: &event.FileInfo{
				Size:     len(data),
				MimeType: mime,
				//Width:    width,
				//Height:   height,
				//Duration: int(msg.length),
			},
		}
		if content.File != nil {
			content.File.URL = uploaded.ContentURI.CUString()
		} else {
			content.URL = uploaded.ContentURI.CUString()
		}
		content.MsgType = event.MsgVideo

		message.Text = strings.Replace(message.Text, attachment.URL, "", 1)
		return content, true, nil
	case "file":
		fileData, fname, fmime := groupmeext.DownloadFile(portal.Key.GMID, attachment.FileID, source.Token)
		if fmime == "" {
			fmime = mimetype.Detect(fileData).String()
		}
		data, uploadMimeType, file := portal.encryptFile(fileData, fmime)

		uploaded, err := portal.uploadWithRetry(intent, data, uploadMimeType, MediaUploadRetries)
		if err != nil {
			if errors.Is(err, mautrix.MTooLarge) {
				err = errors.New("homeserver rejected too large file")
			} else if httpErr := err.(mautrix.HTTPError); httpErr.IsStatus(413) {
				err = errors.New("proxy rejected too large file")
			} else {
				err = fmt.Errorf("failed to upload media: %w", err)
			}
			return nil, true, err
		}

		content := &event.MessageEventContent{
			Body: fname,
			File: file,
			Info: &event.FileInfo{
				Size:     len(data),
				MimeType: fmime,
				//Width:    width,
				//Height:   height,
				//Duration: int(msg.length),
			},
		}
		if content.File != nil {
			content.File.URL = uploaded.ContentURI.CUString()
		} else {
			content.URL = uploaded.ContentURI.CUString()
		}
		//TODO thumbnail since groupme supports it anyway
		if strings.HasPrefix(fmime, "image") {
			content.MsgType = event.MsgImage
		} else if strings.HasPrefix(fmime, "video") {
			content.MsgType = event.MsgVideo
		} else {
			content.MsgType = event.MsgFile
		}

		return content, false, nil
	case "location":
		name := attachment.Name
		lat, _ := strconv.ParseFloat(attachment.Latitude, 64)
		lng, _ := strconv.ParseFloat(attachment.Longitude, 64)
		latChar := 'N'
		if lat < 0 {
			latChar = 'S'
		}
		longChar := 'E'
		if lng < 0 {
			longChar = 'W'
		}
		formattedLoc := fmt.Sprintf("%.4f° %c %.4f° %c", math.Abs(lat), latChar, math.Abs(lng), longChar)

		content := &event.MessageEventContent{
			MsgType: event.MsgLocation,
			Body:    fmt.Sprintf("Location: %s\n%s", name, formattedLoc), //TODO link and stuff
			GeoURI:  fmt.Sprintf("geo:%.5f,%.5f", lat, lng),
		}

		return content, false, nil
	case "reply":
		fmt.Printf("%+v\n", attachment)
		content := &event.MessageEventContent{
			Body:    message.Text,
			MsgType: event.MsgText,
		}
		portal.SetReply(content, attachment.ReplyID)
		return content, false, nil

	default:
		portal.log.Warnln("Unable to handle groupme attachment type", attachment.Type)
		return nil, true, fmt.Errorf("Unable to handle groupme attachment type %s", attachment.Type)
	}
	// return nil, true, errors.New("Unknown type")
}
func (portal *Portal) HandleMediaMessage(source *User, msg mediaMessage) {
	//	intent := portal.startHandling(source, msg.info)
	//	if intent == nil {
	//		return
	//	}
	//
	//	data, err := msg.download()
	//	if err == whatsapp.ErrMediaDownloadFailedWith404 || err == whatsapp.ErrMediaDownloadFailedWith410 {
	//		portal.log.Warnfln("Failed to download media for %s: %v. Calling LoadMediaInfo and retrying download...", msg.info.Id, err)
	//		_, err = source.Conn.LoadMediaInfo(msg.info.RemoteJid, msg.info.Id, msg.info.FromMe)
	//		if err != nil {
	//			portal.sendMediaBridgeFailure(source, intent, msg.info, fmt.Errorf("failed to load media info: %w", err))
	//			return
	//		}
	//		data, err = msg.download()
	//	}
	//	if err == whatsapp.ErrNoURLPresent {
	//		portal.log.Debugfln("No URL present error for media message %s, ignoring...", msg.info.Id)
	//		return
	//	} else if err != nil {
	//		portal.sendMediaBridgeFailure(source, intent, msg.info, err)
	//		return
	//	}
	//
	//	var width, height int
	//	if strings.HasPrefix(msg.mimeType, "image/") {
	//		cfg, _, _ := image.DecodeConfig(bytes.NewReader(data))
	//		width, height = cfg.Width, cfg.Height
	//	}
	//
	//	data, uploadMimeType, file := portal.encryptFile(data, msg.mimeType)
	//
	//	uploaded, err := portal.uploadWithRetry(intent, data, uploadMimeType, MediaUploadRetries)
	//	if err != nil {
	//		if errors.Is(err, mautrix.MTooLarge) {
	//			portal.sendMediaBridgeFailure(source, intent, msg.info, errors.New("homeserver rejected too large file"))
	//		} else if httpErr := err.(mautrix.HTTPError); httpErr.IsStatus(413) {
	//			portal.sendMediaBridgeFailure(source, intent, msg.info, errors.New("proxy rejected too large file"))
	//		} else {
	//			portal.sendMediaBridgeFailure(source, intent, msg.info, fmt.Errorf("failed to upload media: %w", err))
	//		}
	//		return
	//	}
	//
	//	if msg.fileName == "" {
	//		mimeClass := strings.Split(msg.mimeType, "/")[0]
	//		switch mimeClass {
	//		case "application":
	//			msg.fileName = "file"
	//		default:
	//			msg.fileName = mimeClass
	//		}
	//
	//		exts, _ := mime.ExtensionsByType(msg.mimeType)
	//		if exts != nil && len(exts) > 0 {
	//			msg.fileName += exts[0]
	//		}
	//	}
	//
	//	content := &event.MessageEventContent{
	//		Body: msg.fileName,
	//		File: file,
	//		Info: &event.FileInfo{
	//			Size:     len(data),
	//			MimeType: msg.mimeType,
	//			Width:    width,
	//			Height:   height,
	//			Duration: int(msg.length),
	//		},
	//	}
	//	if content.File != nil {
	//		content.File.URL = uploaded.ContentURI.CUString()
	//	} else {
	//		content.URL = uploaded.ContentURI.CUString()
	//	}
	//	portal.SetReply(content, msg.context)
	//
	//	if msg.thumbnail != nil && portal.bridge.Config.Bridge.WhatsappThumbnail {
	//		thumbnailMime := http.DetectContentType(msg.thumbnail)
	//		thumbnailCfg, _, _ := image.DecodeConfig(bytes.NewReader(msg.thumbnail))
	//		thumbnailSize := len(msg.thumbnail)
	//		thumbnail, thumbnailUploadMime, thumbnailFile := portal.encryptFile(msg.thumbnail, thumbnailMime)
	//		uploadedThumbnail, err := intent.UploadBytes(thumbnail, thumbnailUploadMime)
	//		if err != nil {
	//			portal.log.Warnfln("Failed to upload thumbnail for %s: %v", msg.info.Id, err)
	//		} else if uploadedThumbnail != nil {
	//			if thumbnailFile != nil {
	//				thumbnailFile.URL = uploadedThumbnail.ContentURI.CUString()
	//				content.Info.ThumbnailFile = thumbnailFile
	//			} else {
	//				content.Info.ThumbnailURL = uploadedThumbnail.ContentURI.CUString()
	//			}
	//			content.Info.ThumbnailInfo = &event.FileInfo{
	//				Size:     thumbnailSize,
	//				Width:    thumbnailCfg.Width,
	//				Height:   thumbnailCfg.Height,
	//				MimeType: thumbnailMime,
	//			}
	//		}
	//	}
	//
	//	switch strings.ToLower(strings.Split(msg.mimeType, "/")[0]) {
	//	case "image":
	//		if !msg.sendAsSticker {
	//			content.MsgType = event.MsgImage
	//		}
	//	case "video":
	//		content.MsgType = event.MsgVideo
	//	case "audio":
	//		content.MsgType = event.MsgAudio
	//	default:
	//		content.MsgType = event.MsgFile
	//	}
	//
	//	_, _ = intent.UserTyping(portal.MXID, false, 0)
	//	ts := int64(msg.info.Timestamp * 1000)
	//	eventType := event.EventMessage
	//	if msg.sendAsSticker {
	//		eventType = event.EventSticker
	//	}
	//	resp, err := portal.sendMessage(intent, eventType, content, ts)
	//	if err != nil {
	//		portal.log.Errorfln("Failed to handle message %s: %v", msg.info.Id, err)
	//		return
	//	}
	//
	//	if len(msg.caption) > 0 {
	//		captionContent := &event.MessageEventContent{
	//			Body:    msg.caption,
	//			MsgType: event.MsgNotice,
	//		}
	//
	//		portal.bridge.Formatter.ParseWhatsApp(captionContent, msg.context.MentionedJID)
	//
	//		_, err := portal.sendMessage(intent, event.EventMessage, captionContent, ts)
	//		if err != nil {
	//			portal.log.Warnfln("Failed to handle caption of message %s: %v", msg.info.Id, err)
	//		}
	//		// TODO store caption mxid?
	//	}
	//
	//	portal.finishHandling(source, msg.info.Source, resp.EventID)
}

func (portal *Portal) HandleTextMessage(source *User, message *groupme.Message) {
	intent := portal.startHandling(source, message)
	if intent == nil {
		return
	}

	sendText := true
	var sentID id.EventID
	for _, a := range message.Attachments {
		msg, text, err := portal.handleAttachment(intent, a, source, message)

		if err != nil {
			portal.log.Errorfln("Failed to handle message %s: %v", "TODOID", err)
			portal.sendMediaBridgeFailure(source, intent, *message, err)
			continue
		}
		if msg == nil {
			continue
		}
		resp, err := portal.sendMessage(intent, event.EventMessage, msg, nil, message.CreatedAt.ToTime().Unix())
		if err != nil {
			portal.log.Errorfln("Failed to handle message %s: %v", "TODOID", err)
			portal.sendMediaBridgeFailure(source, intent, *message, err)
			continue
		}
		sentID = resp.EventID

		sendText = sendText && text
	}

	//	portal.bridge.Formatter.ParseWhatsApp(content, message.ContextInfo.MentionedJID)
	//	portal.SetReply(content, message.ContextInfo)
	//TODO: mentions
	content := &event.MessageEventContent{
		Body:    message.Text,
		MsgType: event.MsgText,
	}

	_, _ = intent.UserTyping(portal.MXID, false, 0)
	if sendText {
		resp, err := portal.sendMessage(intent, event.EventMessage, content, nil, message.CreatedAt.ToTime().Unix())
		if err != nil {
			portal.log.Errorfln("Failed to handle message %s: %v", message.ID, err)
			return
		}
		sentID = resp.EventID

	}
	portal.finishHandling(source, message, sentID)
}

// func (portal *Portal) handleReaction(msgID groupme.ID, ppl []groupme.ID) {
// 	reactions := portal.bridge.DB.Reaction.GetByGMID(msgID)
// 	newLikes := newReactions(reactions, ppl)
// 	removeLikes := oldReactions(reactions, ppl)

// 	var eventID id.EventID
// 	if len(newLikes) > 0 {
// 		message := portal.bridge.DB.Message.GetByGMID(portal.Key, msgID)
// 		if message == nil {
// 			portal.log.Errorln("Received reaction for unknown message", msgID)
// 			return
// 		}
// 		eventID = message.MXID
// 	}

// 	for _, jid := range newLikes {
// 		intent := portal.getReactionIntent(jid)
// 		resp, err := portal.sendReaction(intent, eventID, "❤")
// 		if err != nil {
// 			portal.log.Errorln("Something wrong with sending reaction", msgID, jid, err)
// 			continue
// 		}

// 		newReaction := portal.bridge.DB.Reaction.New()
// 		newReaction.MXID = resp.EventID
// 		newReaction.MessageJID = msgID
// 		newReaction.MessageMXID = eventID
// 		newReaction.PuppetJID = jid

// 		newReaction.Insert()

// 	}

// 	for _, reaction := range removeLikes {
// 		if len(reaction.Puppet.JID) == 0 {
// 			portal.log.Warnln("Reaction user state wrong", reaction.MXID, msgID)
// 			continue
// 		}
// 		intent := portal.getReactionIntent(reaction.PuppetJID)
// 		_, err := intent.RedactEvent(portal.MXID, reaction.MXID)
// 		if err != nil {
// 			portal.log.Errorln("Something wrong with reaction redaction", reaction.MXID)
// 			continue
// 		}
// 		reaction.Delete()

// 	}
// }

// func oldReactions(a []*database.Reaction, b []string) (ans []*database.Reaction) {
// 	for _, i := range a {
// 		flag := false
// 		for _, j := range b {
// 			if i.PuppetJID == j {
// 				flag = true
// 				break
// 			}
// 		}
// 		if !flag {
// 			ans = append(ans, i)
// 		}
// 	}

// 	return
// }

// func newReactions(a []*database.Reaction, b []string) (ans []string) {
// 	for _, j := range b {
// 		flag := false
// 		for _, i := range a {
// 			if i.GMID == j {
// 				flag = true
// 				break
// 			}
// 		}
// 		if !flag {
// 			ans = append(ans, j)
// 		}
// 	}

// 	return
// }

func (portal *Portal) HandleLocationMessage(source *User, message whatsapp.LocationMessage) {
	//	intent := portal.startHandling(source, message.Info)
	//	if intent == nil {
	//		return
	//	}
	//
	//	url := message.Url
	//	if len(url) == 0 {
	//		url = fmt.Sprintf("https://maps.google.com/?q=%.5f,%.5f", message.DegreesLatitude, message.DegreesLongitude)
	//	}
	//	name := message.Name
	//	if len(name) == 0 {
	//		latChar := 'N'
	//		if message.DegreesLatitude < 0 {
	//			latChar = 'S'
	//		}
	//		longChar := 'E'
	//		if message.DegreesLongitude < 0 {
	//			longChar = 'W'
	//		}
	//		name = fmt.Sprintf("%.4f° %c %.4f° %c", math.Abs(message.DegreesLatitude), latChar, math.Abs(message.DegreesLongitude), longChar)
	//	}
	//
	//	content := &event.MessageEventContent{
	//		MsgType:       event.MsgLocation,
	//		Body:          fmt.Sprintf("Location: %s\n%s\n%s", name, message.Address, url),
	//		Format:        event.FormatHTML,
	//		FormattedBody: fmt.Sprintf("Location: <a href='%s'>%s</a><br>%s", url, name, message.Address),
	//		GeoURI:        fmt.Sprintf("geo:%.5f,%.5f", message.DegreesLatitude, message.DegreesLongitude),
	//	}
	//
	//	if len(message.JpegThumbnail) > 0 {
	//		thumbnailMime := http.DetectContentType(message.JpegThumbnail)
	//		uploadedThumbnail, _ := intent.UploadBytes(message.JpegThumbnail, thumbnailMime)
	//		if uploadedThumbnail != nil {
	//			cfg, _, _ := image.DecodeConfig(bytes.NewReader(message.JpegThumbnail))
	//			content.Info = &event.FileInfo{
	//				ThumbnailInfo: &event.FileInfo{
	//					Size:     len(message.JpegThumbnail),
	//					Width:    cfg.Width,
	//					Height:   cfg.Height,
	//					MimeType: thumbnailMime,
	//				},
	//				ThumbnailURL: uploadedThumbnail.ContentURI.CUString(),
	//			}
	//		}
	//	}
	//
	//	portal.SetReply(content, message.ContextInfo)
	//
	//	_, _ = intent.UserTyping(portal.MXID, false, 0)
	//	resp, err := portal.sendMessage(intent, event.EventMessage, content, int64(message.Info.Timestamp*1000))
	//	if err != nil {
	//		portal.log.Errorfln("Failed to handle message %s: %v", message.Info.Id, err)
	//		return
	//	}
	//	portal.finishHandling(source, message.Info.Source, resp.EventID)
	//}

	//func (portal *Portal) HandleContactMessage(source *User, message whatsapp.ContactMessage) {
	//	intent := portal.startHandling(source, message.Info)
	//	if intent == nil {
	//		return
	//	}
	//
	//	fileName := fmt.Sprintf("%s.vcf", message.DisplayName)
	//	data := []byte(message.Vcard)
	//	mimeType := "text/vcard"
	//	data, uploadMimeType, file := portal.encryptFile(data, mimeType)
	//
	//	uploadResp, err := intent.UploadBytesWithName(data, uploadMimeType, fileName)
	//	if err != nil {
	//		portal.log.Errorfln("Failed to upload vcard of %s: %v", message.DisplayName, err)
	//		return
	//	}
	//
	//	content := &event.MessageEventContent{
	//		Body:    fileName,
	//		MsgType: event.MsgFile,
	//		File:    file,
	//		Info: &event.FileInfo{
	//			MimeType: mimeType,
	//			Size:     len(message.Vcard),
	//		},
	//	}
	//	if content.File != nil {
	//		content.File.URL = uploadResp.ContentURI.CUString()
	//	} else {
	//		content.URL = uploadResp.ContentURI.CUString()
	//	}
	//
	//	portal.SetReply(content, message.ContextInfo)
	//
	//	_, _ = intent.UserTyping(portal.MXID, false, 0)
	//	resp, err := portal.sendMessage(intent, event.EventMessage, content, int64(message.Info.Timestamp*1000))
	//	if err != nil {
	//		portal.log.Errorfln("Failed to handle message %s: %v", message.Info.Id, err)
	//		return
	//	}
	//	portal.finishHandling(source, message.Info.Source, resp.EventID)
}

func (portal *Portal) sendMediaBridgeFailure(source *User, intent *appservice.IntentAPI, message groupme.Message, bridgeErr error) {
	portal.log.Errorfln("Failed to bridge media for %s: %v", message.UserID.String(), bridgeErr)
	resp, err := portal.sendMessage(intent, event.EventMessage, &event.MessageEventContent{
		MsgType: event.MsgNotice,
		Body:    "Failed to bridge media",
	}, nil, int64(message.CreatedAt.ToTime().Unix()*1000))
	if err != nil {
		portal.log.Errorfln("Failed to send media download error message for %s: %v", message.UserID.String(), err)
	} else {
		portal.finishHandling(source, &message, resp.EventID)
	}
}

func (portal *Portal) encryptFile(data []byte, mimeType string) ([]byte, string, *event.EncryptedFileInfo) {
	if !portal.Encrypted {
		return data, mimeType, nil
	}

	file := &event.EncryptedFileInfo{
		EncryptedFile: *attachment.NewEncryptedFile(),
		URL:           "",
	}
	return file.Encrypt(data), "application/octet-stream", file
}

func (portal *Portal) tryKickUser(userID id.UserID, intent *appservice.IntentAPI) error {
	_, err := intent.KickUser(portal.MXID, &mautrix.ReqKickUser{UserID: userID})
	if err != nil {
		httpErr, ok := err.(mautrix.HTTPError)
		if ok && httpErr.RespError != nil && httpErr.RespError.ErrCode == "M_FORBIDDEN" {
			_, err = portal.MainIntent().KickUser(portal.MXID, &mautrix.ReqKickUser{UserID: userID})
		}
	}
	return err
}

func (portal *Portal) removeUser(isSameUser bool, kicker *appservice.IntentAPI, target id.UserID, targetIntent *appservice.IntentAPI) {
	if !isSameUser || targetIntent == nil {
		err := portal.tryKickUser(target, kicker)
		if err != nil {
			portal.log.Warnfln("Failed to kick %s from %s: %v", target, portal.MXID, err)
			if targetIntent != nil {
				_, _ = targetIntent.LeaveRoom(portal.MXID)
			}
		}
	} else {
		_, err := targetIntent.LeaveRoom(portal.MXID)
		if err != nil {
			portal.log.Warnfln("Failed to leave portal as %s: %v", target, err)
			_, _ = portal.MainIntent().KickUser(portal.MXID, &mautrix.ReqKickUser{UserID: target})
		}
	}
}

func (portal *Portal) convertMatrixMessage(sender *User, evt *event.Event) ([]*groupme.Message, *User) {
	content, ok := evt.Content.Parsed.(*event.MessageEventContent)
	if !ok {
		portal.log.Debugfln("Failed to handle event %s: unexpected parsed content type %T", evt.ID, evt.Content.Parsed)
		return nil, sender
	}

	//ts := uint64(evt.Timestamp / 1000)
	//status := waProto.WebMessageInfo_ERROR
	//fromMe := true
	//	info := &waProto.WebMessageInfo{
	//		Key: &waProto.MessageKey{
	//			FromMe:    &fromMe,
	//			Id:        makeMessageID(),
	//			RemoteJid: &portal.Key.JID,
	//		},
	//		MessageTimestamp: &ts,
	//		Message:          &waProto.Message{},
	//		Status:           &status,
	//	}
	//
	info := groupme.Message{
		GroupID:        groupme.ID(portal.Key.String()),
		ConversationID: groupme.ID(portal.Key.String()),
		ChatID:         groupme.ID(portal.Key.String()),
		RecipientID:    groupme.ID(portal.Key.GMID),
	}
	replyToID := content.GetReplyTo()
	if len(replyToID) > 0 {
		//		content.RemoveReplyFallback()
		//		msg := portal.bridge.DB.Message.GetByMXID(replyToID)
		//		if msg != nil && msg.Content != nil {
		//			ctxInfo.StanzaId = &msg.JID
		//			ctxInfo.Participant = &msg.Sender
		//			ctxInfo.QuotedMessage = msg.Content
		//		}
	}
	relaybotFormatted := false
	if sender.NeedsRelaybot(portal) {
		if !portal.HasRelaybot() {
			if sender.HasSession() {
				portal.log.Debugln("Database says", sender.MXID, "not in chat and no relaybot, but trying to send anyway")
			} else {
				portal.log.Debugln("Ignoring message from", sender.MXID, "in chat with no relaybot")
				return nil, sender
			}
		} else {
			relaybotFormatted = portal.addRelaybotFormat(sender, content)
			sender = portal.bridge.Relaybot
		}
	}
	if evt.Type == event.EventSticker {
		content.MsgType = event.MsgImage
	} else if content.MsgType == event.MsgImage && content.GetInfo().MimeType == "image/gif" {
		content.MsgType = event.MsgVideo
	}

	switch content.MsgType {
	case event.MsgText, event.MsgEmote, event.MsgNotice:
		text := content.Body
		if content.Format == event.FormatHTML {
			text, _ = portal.bridge.Formatter.ParseMatrix(content.FormattedBody)
			//TODO mentions
		}
		if content.MsgType == event.MsgEmote && !relaybotFormatted {
			text = "/me " + text
		}
		info.Text = text

	//	if ctxInfo.StanzaId != nil || ctxInfo.MentionedJid != nil {
	//		info.Message.ExtendedTextMessage = &waProto.ExtendedTextMessage{
	//			Text:        &text,
	//			ContextInfo: ctxInfo,
	//		}
	// }
	//else {
	//			info.Message.Conversation = &text
	//		}
	//	 case event.MsgImage:
	//	 	media := portal.preprocessMatrixMedia(sender, relaybotFormatted, content, evt.ID, whatsapp.MediaImage)
	//	 	if media == nil {
	//	 		return nil, sender
	//	 	}
	//	 	ctxInfo.MentionedJid = media.MentionedJIDs
	//	 	info.Message.ImageMessage = &waProto.ImageMessage{
	//	 		ContextInfo:   ctxInfo,
	//	 		Caption:       &media.Caption,
	//	 		JpegThumbnail: media.Thumbnail,
	//	 		Url:           &media.URL,
	//	 		MediaKey:      media.MediaKey,
	//	 		Mimetype:      &content.GetInfo().MimeType,
	//	 		FileEncSha256: media.FileEncSHA256,
	//	 		FileSha256:    media.FileSHA256,
	//	 		FileLength:    &media.FileLength,
	//	 	}
	//	 case event.MsgVideo:
	//	 	gifPlayback := content.GetInfo().MimeType == "image/gif"
	//	 	media := portal.preprocessMatrixMedia(sender, relaybotFormatted, content, evt.ID, whatsapp.MediaVideo)
	//	 	if media == nil {
	//	 		return nil, sender
	//	 	}
	//	 	duration := uint32(content.GetInfo().Duration)
	//	 	ctxInfo.MentionedJid = media.MentionedJIDs
	//	 	info.Message.VideoMessage = &waProto.VideoMessage{
	//	 		ContextInfo:   ctxInfo,
	//	 		Caption:       &media.Caption,
	//	 		JpegThumbnail: media.Thumbnail,
	//	 		Url:           &media.URL,
	//	 		MediaKey:      media.MediaKey,
	//	 		Mimetype:      &content.GetInfo().MimeType,
	//	 		GifPlayback:   &gifPlayback,
	//	 		Seconds:       &duration,
	//	 		FileEncSha256: media.FileEncSHA256,
	//	 		FileSha256:    media.FileSHA256,
	//	 		FileLength:    &media.FileLength,
	//	 	}
	//	 case event.MsgAudio:
	//	 	media := portal.preprocessMatrixMedia(sender, relaybotFormatted, content, evt.ID, whatsapp.MediaAudio)
	//	 	if media == nil {
	//	 		return nil, sender
	//	 	}
	//	 	duration := uint32(content.GetInfo().Duration)
	//	 	info.Message.AudioMessage = &waProto.AudioMessage{
	//	 		ContextInfo:   ctxInfo,
	//	 		Url:           &media.URL,
	//	 		MediaKey:      media.MediaKey,
	//	 		Mimetype:      &content.GetInfo().MimeType,
	//	 		Seconds:       &duration,
	//	 		FileEncSha256: media.FileEncSHA256,
	//	 		FileSha256:    media.FileSHA256,
	//	 		FileLength:    &media.FileLength,
	//	 	}
	//	 case event.MsgFile:
	//	 	media := portal.preprocessMatrixMedia(sender, relaybotFormatted, content, evt.ID, whatsapp.MediaDocument)
	//	 	if media == nil {
	//	 		return nil, sender
	//	 	}
	//	 	info.Message.DocumentMessage = &waProto.DocumentMessage{
	//	 		ContextInfo:   ctxInfo,
	//	 		Url:           &media.URL,
	//	 		Title:         &content.Body,
	//	 		FileName:      &content.Body,
	//	 		MediaKey:      media.MediaKey,
	//	 		Mimetype:      &content.GetInfo().MimeType,
	//	 		FileEncSha256: media.FileEncSHA256,
	//	 		FileSha256:    media.FileSHA256,
	//	 		FileLength:    &media.FileLength,
	//	 	}
	default:
		portal.log.Debugln("Unhandled Matrix event %s: unknown msgtype %s", evt.ID, content.MsgType)
		return nil, sender
	}
	return []*groupme.Message{&info}, sender
}

func (portal *Portal) wasMessageSent(sender *User, id string) bool {
	// _, err := sender.Conn.LoadMessagesAfter(portal.Key.JID, id, true, 0)
	// if err != nil {
	// 	if err != whatsapp.ErrServerRespondedWith404 {
	// 		portal.log.Warnfln("Failed to check if message was bridged without response: %v", err)
	// 	}
	// 	return false
	// }
	return true
}

func (portal *Portal) sendErrorMessage(message string) id.EventID {
	resp, err := portal.sendMainIntentMessage(event.MessageEventContent{
		MsgType: event.MsgNotice,
		Body:    fmt.Sprintf("\u26a0 Your message may not have been bridged: %v", message),
	})
	if err != nil {
		portal.log.Warnfln("Failed to send bridging error message:", err)
		return ""
	}
	return resp.EventID
}

func (portal *Portal) sendDeliveryReceipt(eventID id.EventID) {
	if portal.bridge.Config.Bridge.DeliveryReceipts {
		err := portal.bridge.Bot.MarkRead(portal.MXID, eventID)
		if err != nil {
			portal.log.Debugfln("Failed to send delivery receipt for %s: %v", eventID, err)
		}
	}
}

var timeout = errors.New("message sending timed out")

func (portal *Portal) HandleMatrixMessage(sender *User, evt *event.Event) {
	if !portal.HasRelaybot() && ((portal.IsPrivateChat() && sender.JID != portal.Key.Receiver) ||
		portal.sendMatrixConnectionError(sender, evt.ID)) {
		return
	}
	portal.log.Debugfln("Received event %s", evt.ID)
	info, sender := portal.convertMatrixMessage(sender, evt)
	if info == nil {
		return
	}
	for _, i := range info {
		portal.log.Debugln("Sending event", evt.ID, "to GroupMe", info[0].ID)

		var err error
		i, err = portal.sendRaw(sender, evt, info[0], -1) //TODO deal with multiple messages for longer messages
		if err != nil {
			portal.log.Warnln("Unable to handle message from Matrix", evt.ID)
			//TODO handle deleted room and such
		} else {

			portal.markHandled(sender, i, evt.ID)
		}
	}

}

func (portal *Portal) sendRaw(sender *User, evt *event.Event, info *groupme.Message, retries int) (*groupme.Message, error) {
	if retries == -1 {
		retries = 2
	}

	var m *groupme.Message
	var err error

	if portal.IsPrivateChat() {
		m, err = sender.Client.CreateDirectMessage(context.TODO(), info)
	} else {
		m, err = sender.Client.CreateMessage(context.TODO(), info.GroupID, info)
	}

	id := ""
	if m != nil {
		id = m.ID.String()
	}
	if err != nil {
		portal.log.Warnln(err, id, info.GroupID.String())

		if retries > 0 {
			return portal.sendRaw(sender, evt, info, retries-1)
		}
	}
	return m, nil
	// errChan := make(chan error, 1)
	// go sender.Conn.SendRaw(info, errChan)

	// var err error
	// var errorEventID id.EventID
	// select {
	// case err = <-errChan:
	// 	var statusResp whatsapp.StatusResponse
	// 	if !isRetry && errors.As(err, &statusResp) && statusResp.Status == 599 {
	// 		portal.log.Debugfln("599 status response sending %s to WhatsApp (%+v), retrying...", evt.ID, statusResp)
	// 		errorEventID = portal.sendErrorMessage(fmt.Sprintf("%v. The bridge will retry in 5 seconds.", err))
	// 		time.Sleep(5 * time.Second)
	// 		portal.sendRaw(sender, evt, info, true)
	// 	}
	// case <-time.After(time.Duration(portal.bridge.Config.Bridge.ConnectionTimeout) * time.Second):
	// 	if portal.bridge.Config.Bridge.FetchMessageOnTimeout && portal.wasMessageSent(sender, info.Key.GetId()) {
	// 		portal.log.Debugln("Matrix event %s was bridged, but response didn't arrive within timeout")
	// 		portal.sendDeliveryReceipt(evt.ID)
	// 	} else {
	// 		portal.log.Warnfln("Response when bridging Matrix event %s is taking long to arrive", evt.ID)
	// 		errorEventID = portal.sendErrorMessage(timeout.Error())
	// 	}
	// 	err = <-errChan
	// }
	// if err != nil {
	// 	portal.log.Errorfln("Error handling Matrix event %s: %v", evt.ID, err)
	// 	var statusResp whatsapp.StatusResponse
	// 	if errors.As(err, &statusResp) && statusResp.Status == 599 {
	// 		portal.log.Debugfln("599 status response data: %+v", statusResp)
	// 	}
	// 	portal.sendErrorMessage(err.Error())
	// } else {
	// 	portal.log.Debugfln("Handled Matrix event %s", evt.ID)
	// 	portal.sendDeliveryReceipt(evt.ID)
	// }
	// if errorEventID != "" {
	// 	_, err = portal.MainIntent().RedactEvent(portal.MXID, errorEventID)
	// 	if err != nil {
	// 		portal.log.Warnfln("Failed to redact timeout warning message %s: %v", errorEventID, err)
	// 	}
	// }
}

func (portal *Portal) HandleMatrixRedaction(sender *User, evt *event.Event) {
	// if portal.IsPrivateChat() && sender.JID != portal.Key.Receiver {
	// 	return
	// }

	// msg := portal.bridge.DB.Message.GetByMXID(evt.Redacts)
	// if msg == nil || msg.Sender != sender.JID {
	// 	return
	// }

	// ts := uint64(evt.Timestamp / 1000)
	// status := waProto.WebMessageInfo_PENDING
	// protoMsgType := waProto.ProtocolMessage_REVOKE
	// fromMe := true
	// info := &waProto.WebMessageInfo{
	// 	Key: &waProto.MessageKey{
	// 		FromMe:    &fromMe,
	// 		Id:        makeMessageID(),
	// 		RemoteJid: &portal.Key.JID,
	// 	},
	// 	MessageTimestamp: &ts,
	// 	Message: &waProto.Message{
	// 		ProtocolMessage: &waProto.ProtocolMessage{
	// 			Type: &protoMsgType,
	// 			Key: &waProto.MessageKey{
	// 				FromMe:    &fromMe,
	// 				Id:        &msg.JID,
	// 				RemoteJid: &portal.Key.JID,
	// 			},
	// 		},
	// 	},
	// 	Status: &status,
	// }
	// errChan := make(chan error, 1)
	// go sender.Conn.SendRaw(info, errChan)

	// var err error
	// select {
	// case err = <-errChan:
	// case <-time.After(time.Duration(portal.bridge.Config.Bridge.ConnectionTimeout) * time.Second):
	// 	portal.log.Warnfln("Response when bridging Matrix redaction %s is taking long to arrive", evt.ID)
	// 	err = <-errChan
	// }
	// if err != nil {
	// 	portal.log.Errorfln("Error handling Matrix redaction %s: %v", evt.ID, err)
	// } else {
	// 	portal.log.Debugln("Handled Matrix redaction %s of %s", evt.ID, evt.Redacts)
	// 	portal.sendDeliveryReceipt(evt.ID)
	// }
}

func (portal *Portal) Delete() {
	portal.Portal.Delete()
	portal.bridge.portalsLock.Lock()
	delete(portal.bridge.portalsByJID, portal.Key)
	if len(portal.MXID) > 0 {
		delete(portal.bridge.portalsByMXID, portal.MXID)
	}
	portal.bridge.portalsLock.Unlock()
}

func (portal *Portal) GetMatrixUsers() ([]id.UserID, error) {
	members, err := portal.MainIntent().JoinedMembers(portal.MXID)
	if err != nil {
		return nil, fmt.Errorf("failed to get member list: %w", err)
	}
	var users []id.UserID
	for userID := range members.Joined {
		_, isPuppet := portal.bridge.ParsePuppetMXID(userID)
		if !isPuppet && userID != portal.bridge.Bot.UserID {
			users = append(users, userID)
		}
	}
	return users, nil
}

func (portal *Portal) CleanupIfEmpty() {
	users, err := portal.GetMatrixUsers()
	if err != nil {
		portal.log.Errorfln("Failed to get Matrix user list to determine if portal needs to be cleaned up: %v", err)
		return
	}

	if len(users) == 0 {
		portal.log.Infoln("Room seems to be empty, cleaning up...")
		portal.Delete()
		portal.Cleanup(false)
	}
}

func (portal *Portal) Cleanup(puppetsOnly bool) {
	if len(portal.MXID) == 0 {
		return
	}
	if portal.IsPrivateChat() {
		_, err := portal.MainIntent().LeaveRoom(portal.MXID)
		if err != nil {
			portal.log.Warnln("Failed to leave private chat portal with main intent:", err)
		}
		return
	}
	intent := portal.MainIntent()
	members, err := intent.JoinedMembers(portal.MXID)
	if err != nil {
		portal.log.Errorln("Failed to get portal members for cleanup:", err)
		return
	}
	for member := range members.Joined {
		if member == intent.UserID {
			continue
		}
		puppet := portal.bridge.GetPuppetByMXID(member)
		if puppet != nil {
			_, err = puppet.DefaultIntent().LeaveRoom(portal.MXID)
			if err != nil {
				portal.log.Errorln("Error leaving as puppet while cleaning up portal:", err)
			}
		} else if !puppetsOnly {
			_, err = intent.KickUser(portal.MXID, &mautrix.ReqKickUser{UserID: member, Reason: "Deleting portal"})
			if err != nil {
				portal.log.Errorln("Error kicking user while cleaning up portal:", err)
			}
		}
	}
	_, err = intent.LeaveRoom(portal.MXID)
	if err != nil {
		portal.log.Errorln("Error leaving with main intent while cleaning up portal:", err)
	}
}

func (portal *Portal) HandleMatrixLeave(sender *User) {
	if portal.IsPrivateChat() {
		portal.log.Debugln("User left private chat portal, cleaning up and deleting...")
		portal.Delete()
		portal.Cleanup(false)
		return
	} else {
		// TODO should we somehow deduplicate this call if this leave was sent by the bridge?
		err := sender.Client.RemoveFromGroup(sender.JID, portal.Key.GMID)
		if err != nil {
			portal.log.Errorfln("Failed to leave group as %s: %v", sender.MXID, err)
			return
		}
		portal.CleanupIfEmpty()
	}
}

func (portal *Portal) HandleMatrixKick(sender *User, evt *event.Event) {
	// puppet := portal.bridge.GetPuppetByMXID(id.UserID(evt.GetStateKey()))
	// if puppet != nil {
	// 	resp, err := sender.Conn.RemoveMember(portal.Key.JID, []string{puppet.JID})
	// 	if err != nil {
	// 		portal.log.Errorfln("Failed to kick %s from group as %s: %v", puppet.JID, sender.MXID, err)
	// 		return
	// 	}
	// 	portal.log.Infoln("Kick %s response: %s", puppet.JID, <-resp)
	// }
}

func (portal *Portal) HandleMatrixInvite(sender *User, evt *event.Event) {
	// puppet := portal.bridge.GetPuppetByMXID(id.UserID(evt.GetStateKey()))
	// if puppet != nil {
	// 	resp, err := sender.Conn.AddMember(portal.Key.JID, []string{puppet.JID})
	// 	if err != nil {
	// 		portal.log.Errorfln("Failed to add %s to group as %s: %v", puppet.JID, sender.MXID, err)
	// 		return
	// 	}
	// 	portal.log.Infoln("Add %s response: %s", puppet.JID, <-resp)
	// }
}
