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

	"github.com/gabriel-vasile/mimetype"

	"github.com/beeper/groupme-lib"

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

		messages:       make(chan PortalMessage, bridge.Config.Bridge.PortalMessageBuffer),
		matrixMessages: make(chan PortalMatrixMessage, bridge.Config.Bridge.PortalMessageBuffer),
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

		messages:       make(chan PortalMessage, bridge.Config.Bridge.PortalMessageBuffer),
		matrixMessages: make(chan PortalMatrixMessage, bridge.Config.Bridge.PortalMessageBuffer),
	}
	go portal.handleMessageLoop()
	return portal
}

const recentlyHandledLength = 100

type PortalMessage struct {
	chat       database.PortalKey
	source     *User
	data       *groupme.Message
	timestamp  uint64
	isReaction bool
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

	currentlyTyping     map[id.UserID]context.CancelFunc
	currentlyTypingLock sync.Mutex

	hasRelaybot *bool
}

const MaxMessageAgeToCreatePortal = 5 * 60 // 5 minutes

func (portal *Portal) handleMessageLoop() {
	for {
		select {
		case msg, ok := <-portal.messages:
			if !ok {
				return
			}
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
		case msg, ok := <-portal.matrixMessages:
			if !ok {
				return
			}
			portal.HandleMatrixMessage(msg.user, msg.evt)
		}
	}
}

func (portal *Portal) handleMessage(msg PortalMessage) {
	if len(portal.MXID) == 0 {
		portal.log.Warnln("handleMessage called even though portal.MXID is empty")
		return
	}
	if msg.isReaction {
		portal.handleReaction(msg.data)
		return
	}
	portal.HandleTextMessage(msg.source, msg.data)
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
	msg.Insert()

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
		portal.log.Warnfln("Message %s has no user ID, using main intent", info.ID)
		return portal.MainIntent()
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
	} else if info.SourceGUID != "" && portal.isRecentlyHandled(groupme.ID(info.SourceGUID)) {
		portal.log.Debugfln("Not handling %s: source GUID %s was recently handled", info.ID, info.SourceGUID)
	} else if portal.isDuplicate(info.ID) {
		portal.log.Debugfln("Not handling %s: message is duplicate", info.ID)
	} else if info.System {
		portal.log.Debugfln("Not handling %s: message is from system: %s", info.ID, info.Text)
		if info.Event != nil && info.Event.Type == "message.deleted" {
			if targetMsgID, ok := info.Event.Data["message_id"].(string); ok && targetMsgID != "" {
				portal.HandleGroupMeDeletion(groupme.ID(targetMsgID))
			}
		}
	} else {
		portal.recentlyHandledLock.Lock()
		portal.recentlyHandled[0] = ""
		portal.recentlyHandled = portal.recentlyHandled[1:]
		portal.recentlyHandled = append(portal.recentlyHandled, info.ID.String())
		portal.recentlyHandledLock.Unlock()

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
		puppet.Sync(nil, participant, false, false)
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
						Reason: "User had left this GroupMe chat",
					})
					if err != nil {
						portal.log.Warnfln("Failed to kick user %s who had left: %v", member, err)
					}
				}
			}
		}
	}
}

func (user *User) updateAvatar(gmdi groupme.ID, avatarID *string, avatarURL *id.ContentURI, avatarSet *bool, log log.Logger, intent *appservice.IntentAPI) bool {
	return false
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

	go func() {
		var err error
		if portal.IsPrivateChat() {
			err = user.Conn.SubscribeToDM(context.TODO(), dmConversationID(portal.Key), user.Token)
		} else {
			err = user.Conn.SubscribeToGroup(context.TODO(), portal.Key.GMID, user.Token)
		}
		if err != nil {
			portal.log.Errorln("Subscribing failed, live metadata updates won't work", err)
		}
	}()

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
		if portal.bridge.Config.Bridge.Encryption.Default {
			_ = portal.MainIntent().EnsureJoined(portal.MXID)
		}
	}

	if portal.IsPrivateChat() {
		return
	}

	portal.SyncParticipants(group)

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

func (portal *Portal) getBridgeInfoStateKey() string {
	return fmt.Sprintf("com.beeper.groupme://groupme/%s", portal.Key.GMID)
}

func (portal *Portal) getBridgeInfo() (string, event.BridgeEventContent) {
	bridgeInfo := event.BridgeEventContent{
		BridgeBot: portal.bridge.Bot.UserID,
		Creator:   portal.MainIntent().UserID,
		Protocol: event.BridgeInfoSection{
			ID:          "groupme",
			DisplayName: "GroupMe",
			AvatarURL:   portal.bridge.Config.AppService.Bot.ParsedAvatar.CUString(),
			ExternalURL: "https://www.groupme.com/",
		},
		Channel: event.BridgeInfoSection{
			ID:          portal.Key.GMID.String(),
			DisplayName: portal.Name,
			AvatarURL:   portal.AvatarURL.CUString(),
		},
	}
	return portal.getBridgeInfoStateKey(), bridgeInfo
}

func (portal *Portal) UpdateBridgeInfo() {
	if len(portal.MXID) == 0 {
		portal.log.Debugln("Not updating bridge info: no Matrix room created")
		return
	}
	portal.log.Debugln("Updating bridge info...")
	stateKey, content := portal.getBridgeInfo()
	_, err := portal.MainIntent().SendStateEvent(portal.MXID, event.StateBridge, stateKey, content)
	if err != nil {
		portal.log.Warnln("Failed to update m.bridge:", err)
	}
	// TODO remove this once https://github.com/matrix-org/matrix-doc/pull/2346 is in spec
	_, err = portal.MainIntent().SendStateEvent(portal.MXID, event.StateHalfShotBridge, stateKey, content)
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
		if err != nil {
			portal.log.Warnfln("Failed to fetch group info to create room: %v", err)
			metadata = nil
		} else {
			portal.Name = metadata.Name
			portal.Topic = metadata.Description
			portal.UpdateAvatar(user, metadata.ImageURL, false)
		}
	}

	bridgeInfoStateKey, bridgeInfo := portal.getBridgeInfo()

	initialState := []*event.Event{{
		Type: event.StatePowerLevels,
		Content: event.Content{
			Parsed: portal.GetBasePowerLevels(),
		},
	}, {
		Type:     event.StateBridge,
		Content:  event.Content{Parsed: bridgeInfo},
		StateKey: &bridgeInfoStateKey,
	}, {
		// TODO remove this once https://github.com/matrix-org/matrix-doc/pull/2346 is in spec
		Type:     event.StateHalfShotBridge,
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

// MediaUploadTimeout bounds the total time spent downloading Matrix media,
// uploading it to GroupMe and waiting for async processing to finish.
const MediaUploadTimeout = 15 * time.Minute

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

func (portal *Portal) sendMainIntentMessage(content *event.MessageEventContent) (*mautrix.RespSendEvent, error) {
	return portal.sendMessage(portal.MainIntent(), event.EventMessage, content, nil, 0)
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

		uploaded, err := intent.UploadBytes(data, uploadMimeType)
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
		vidContents, mime, err := groupmeext.DownloadVideo(attachment.VideoPreviewURL, attachment.URL, source.Token)
		if err != nil {
			return nil, true, fmt.Errorf("failed to download video: %w", err)
		}
		if mime == "" {
			mime = mimetype.Detect(vidContents).String()
		}

		data, uploadMimeType, file := portal.encryptFile(vidContents, mime)
		uploaded, err := intent.UploadBytes(data, uploadMimeType)
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
		fileData, fname, fmime, err := groupmeext.DownloadFile(portal.Key.GMID, attachment.FileID, source.Token)
		if err != nil {
			return nil, true, fmt.Errorf("failed to download file: %w", err)
		}
		if fmime == "" {
			fmime = mimetype.Detect(fileData).String()
		}
		data, uploadMimeType, file := portal.encryptFile(fileData, fmime)

		uploaded, err := intent.UploadBytes(data, uploadMimeType)
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

func (portal *Portal) HandleTextMessage(source *User, message *groupme.Message) {
	intent := portal.startHandling(source, message)
	if intent == nil {
		return
	}

	puppet := portal.bridge.GetPuppetByGMID(message.UserID)
	if puppet != nil {
		puppet.Sync(source, &groupme.Member{
			UserID:   message.UserID,
			Nickname: message.Name,
			ImageURL: message.AvatarURL,
		}, false, false)
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
		resp, err := portal.sendMessage(intent, event.EventMessage, msg, nil, message.CreatedAt.ToTime().Unix()*1000)
		if err != nil {
			portal.log.Errorfln("Failed to handle message %s: %v", "TODOID", err)
			portal.sendMediaBridgeFailure(source, intent, *message, err)
			continue
		}
		sentID = resp.EventID

		sendText = sendText && text
	}

	//	portal.SetReply(content, message.ContextInfo)
	//TODO: mentions
	content := &event.MessageEventContent{
		Body:    message.Text,
		MsgType: event.MsgText,
	}

	_, _ = intent.UserTyping(portal.MXID, false, 0)
	if sendText && strings.TrimSpace(message.Text) != "" {
		resp, err := portal.sendMessage(intent, event.EventMessage, content, nil, message.CreatedAt.ToTime().Unix()*1000)
		if err != nil {
			portal.log.Errorfln("Failed to handle message %s: %v", message.ID, err)
			return
		}
		sentID = resp.EventID

	}
	portal.finishHandling(source, message, sentID)
}

const defaultReactionEmoji = "❤️"

// desiredReactions maps each reacting user to the emoji they reacted with.
// The reactions array from "favorite" push events carries per-emoji user
// lists; older payloads only have favorited_by, which implies the default ❤️.
func desiredReactions(message *groupme.Message) map[groupme.ID]string {
	desired := make(map[groupme.ID]string)
	for _, userID := range message.FavoritedBy {
		desired[groupme.ID(userID)] = defaultReactionEmoji
	}
	for _, reaction := range message.Reactions {
		emoji := reaction.Code
		if reaction.Type != "unicode" || emoji == "" {
			// GroupMe powerup emoji can't be represented on Matrix.
			emoji = defaultReactionEmoji
		}
		for _, userID := range reaction.UserIDs {
			desired[userID] = emoji
		}
	}
	return desired
}

func (portal *Portal) handleReaction(message *groupme.Message) {
	msgID := message.ID
	desired := desiredReactions(message)
	existing := portal.bridge.DB.Reaction.GetAllByTargetGMID(portal.Key, msgID)

	target := portal.bridge.DB.Message.GetByGMID(portal.Key, msgID)
	if target == nil {
		portal.log.Debugfln("Received reaction for unknown message %s", msgID)
		return
	}

	existingBySender := make(map[groupme.ID]*database.Reaction, len(existing))
	for _, reaction := range existing {
		existingBySender[reaction.Sender] = reaction
	}

	// Send new reactions and update ones whose emoji changed.
	for sender, emoji := range desired {
		prev := existingBySender[sender]
		if prev != nil && prev.Emoji == emoji {
			continue
		}
		puppet := portal.bridge.GetPuppetByGMID(sender)
		if puppet == nil {
			continue
		}
		intent := puppet.IntentFor(portal)

		if prev != nil {
			// Emoji changed: redact the old annotation first.
			if _, err := intent.RedactEvent(portal.MXID, prev.MXID); err != nil {
				portal.log.Warnfln("Failed to redact old reaction %s: %v", prev.MXID, err)
			}
		}

		resp, err := portal.sendReaction(intent, target.MXID, emoji)
		if err != nil {
			portal.log.Errorfln("Failed to send reaction to %s from %s: %v", msgID, sender, err)
			continue
		}

		dbReaction := portal.bridge.DB.Reaction.New()
		dbReaction.Chat = portal.Key
		dbReaction.TargetGMID = msgID
		dbReaction.Sender = sender
		dbReaction.MXID = resp.EventID
		dbReaction.GMID = "" // GroupMe reactions don't have IDs of their own
		dbReaction.Emoji = emoji
		dbReaction.Upsert(nil)
	}

	// Redact reactions that were removed on GroupMe.
	for sender, reaction := range existingBySender {
		if _, stillThere := desired[sender]; stillThere {
			continue
		}
		puppet := portal.bridge.GetPuppetByGMID(sender)
		if puppet == nil {
			continue
		}
		if _, err := puppet.IntentFor(portal).RedactEvent(portal.MXID, reaction.MXID); err != nil {
			portal.log.Errorfln("Failed to redact reaction %s: %v", reaction.MXID, err)
		}
		reaction.Delete()
	}
}

// HandleGroupMeDeletion redacts the Matrix event for a message that was
// deleted on GroupMe.
func (portal *Portal) HandleGroupMeDeletion(msgID groupme.ID) {
	msgRecord := portal.bridge.DB.Message.GetByGMID(portal.Key, msgID)
	if msgRecord == nil {
		portal.log.Debugfln("Ignoring deletion of unknown message %s", msgID)
		return
	}
	_, err := portal.MainIntent().RedactEvent(portal.MXID, msgRecord.MXID, mautrix.ReqRedact{
		Reason: "Deleted in GroupMe",
	})
	if err != nil {
		portal.log.Errorfln("Failed to redact Matrix event for GroupMe message %s: %v", msgID, err)
		return
	}
	msgRecord.Delete()
}

// HandleMessageEdit bridges a GroupMe message edit as a Matrix edit event.
func (portal *Portal) HandleMessageEdit(source *User, message *groupme.Message) {
	msgRecord := portal.bridge.DB.Message.GetByGMID(portal.Key, message.ID)
	if msgRecord == nil {
		portal.log.Debugfln("Ignoring edit of unknown message %s", message.ID)
		return
	}

	intent := portal.getMessageIntent(source, message)
	if intent == nil {
		return
	}

	content := &event.MessageEventContent{
		MsgType: event.MsgText,
		Body:    "* " + message.Text,
		NewContent: &event.MessageEventContent{
			MsgType: event.MsgText,
			Body:    message.Text,
		},
		RelatesTo: &event.RelatesTo{
			Type:    event.RelReplace,
			EventID: msgRecord.MXID,
		},
	}

	if _, err := portal.sendMessage(intent, event.EventMessage, content, nil, 0); err != nil {
		portal.log.Errorfln("Failed to bridge edit of message %s: %v", message.ID, err)
	}
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

func (portal *Portal) downloadMatrixMedia(content *event.MessageEventContent) ([]byte, string, error) {
	var data []byte
	var err error
	var mime string

	if content.File != nil {
		var parsedURL id.ContentURI
		parsedURL, err = content.File.URL.Parse()
		if err != nil {
			return nil, "", err
		}
		data, err = portal.MainIntent().DownloadBytes(parsedURL)
		if err != nil {
			return nil, "", err
		}
		data, err = content.File.Decrypt(data)
		if err != nil {
			return nil, "", err
		}
	} else {
		var parsedURL id.ContentURI
		parsedURL, err = content.URL.Parse()
		if err != nil {
			return nil, "", err
		}
		data, err = portal.MainIntent().DownloadBytes(parsedURL)
		if err != nil {
			return nil, "", err
		}
	}

	if content.Info != nil {
		mime = content.Info.MimeType
	}
	return data, mime, nil
}

func (portal *Portal) convertMatrixMessage(sender *User, evt *event.Event) ([]*groupme.Message, *User) {
	content, ok := evt.Content.Parsed.(*event.MessageEventContent)
	if !ok {
		portal.log.Debugfln("Failed to handle event %s: unexpected parsed content type %T", evt.ID, evt.Content.Parsed)
		return nil, sender
	}

	// Message edits are bridged through the v4 edit endpoint instead of
	// being sent as new messages.
	if replaceID := content.RelatesTo.GetReplaceID(); len(replaceID) > 0 {
		portal.handleMatrixEdit(sender, evt, content, replaceID)
		return nil, sender
	}

	info := groupme.Message{
		SourceGUID:     evt.ID.String(),
		GroupID:        groupme.ID(portal.Key.String()),
		ConversationID: groupme.ID(portal.Key.String()),
		ChatID:         groupme.ID(portal.Key.String()),
		RecipientID:    groupme.ID(portal.Key.GMID),
	}
	replyToID := content.GetReplyTo()
	if len(replyToID) > 0 {
		content.RemoveReplyFallback()
		if replyTarget := portal.bridge.DB.Message.GetByMXID(replyToID); replyTarget != nil {
			info.Attachments = append(info.Attachments, &groupme.Attachment{
				Type:        groupme.Reply,
				ReplyID:     replyTarget.GMID,
				BaseReplyID: replyTarget.GMID,
			})
		}
	}
	relaybotFormatted := false

	if evt.Type == event.EventSticker {
		content.MsgType = event.MsgImage
	} else if content.MsgType == event.MsgImage && content.GetInfo().MimeType == "image/gif" {
		content.MsgType = event.MsgVideo
	}

	switch content.MsgType {
	case event.MsgText, event.MsgEmote, event.MsgNotice:
		text := content.Body
		if content.Format == event.FormatHTML {
			text = portal.parseMatrixHTML(content)
		}
		if content.MsgType == event.MsgEmote && !relaybotFormatted {
			text = "/me " + text
		}
		info.Text = text

	case event.MsgImage:
		info.Text = content.Body
		data, mime, err := portal.downloadMatrixMedia(content)
		if err != nil {
			portal.log.Errorfln("Failed to download matrix media for %s: %v", evt.ID, err)
			return nil, sender
		}
		url, err := sender.Client.UploadImage(context.TODO(), data, mime)
		if err != nil {
			portal.log.Errorfln("Failed to upload image to GroupMe for %s: %v", evt.ID, err)
			return nil, sender
		}
		info.Attachments = append(info.Attachments, &groupme.Attachment{
			Type: groupme.Image,
			URL:  url,
		})

	case event.MsgVideo:
		// Video uploads go through GroupMe's async transcode service, which
		// can take a while; handle them in the background.
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), MediaUploadTimeout)
			defer cancel()
			data, _, err := portal.downloadMatrixMedia(content)
			if err != nil {
				portal.log.Errorfln("Failed to download Matrix video for %s: %v", evt.ID, err)
				return
			}
			statusURL, err := sender.Client.UploadVideoAsync(ctx, info.GroupID, data, content.Body)
			if err != nil {
				portal.log.Errorfln("Failed to start async video upload for %s: %v", evt.ID, err)
				return
			}
			videoURL, previewURL, err := sender.Client.PollVideoStatus(ctx, statusURL)
			if err != nil {
				portal.log.Errorfln("Failed to poll video status for %s: %v", evt.ID, err)
				return
			}
			msgCopy := info
			msgCopy.Text = content.Body
			msgCopy.Attachments = append(msgCopy.Attachments, &groupme.Attachment{
				Type:            groupme.Video,
				URL:             videoURL,
				VideoPreviewURL: previewURL,
			})
			if m, err := portal.sendRaw(sender, evt, &msgCopy, -1); err != nil {
				portal.log.Errorfln("Failed to send video message for %s: %v", evt.ID, err)
			} else {
				portal.markHandled(sender, m, evt.ID)
			}
		}()
		return nil, sender

	case event.MsgFile, event.MsgAudio:
		// File uploads also go through an async upload service.
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), MediaUploadTimeout)
			defer cancel()
			data, _, err := portal.downloadMatrixMedia(content)
			if err != nil {
				portal.log.Errorfln("Failed to download Matrix file for %s: %v", evt.ID, err)
				return
			}
			statusURL, err := sender.Client.UploadFileAsync(ctx, info.GroupID, data, content.Body)
			if err != nil {
				portal.log.Errorfln("Failed to start async file upload for %s: %v", evt.ID, err)
				return
			}
			fileID, err := sender.Client.PollFileStatus(ctx, statusURL)
			if err != nil {
				portal.log.Errorfln("Failed to poll file status for %s: %v", evt.ID, err)
				return
			}
			msgCopy := info
			msgCopy.Text = content.Body
			msgCopy.Attachments = append(msgCopy.Attachments, &groupme.Attachment{
				Type:   groupme.File,
				FileID: fileID,
			})
			if m, err := portal.sendRaw(sender, evt, &msgCopy, -1); err != nil {
				portal.log.Errorfln("Failed to send file message for %s: %v", evt.ID, err)
			} else {
				portal.markHandled(sender, m, evt.ID)
			}
		}()
		return nil, sender

	default:
		portal.log.Debugln("Unhandled Matrix event %s: unknown msgtype %s", evt.ID, content.MsgType)
		return nil, sender
	}
	return []*groupme.Message{&info}, sender
}

func (portal *Portal) wasMessageSent(sender *User, id string) bool {
	return true
}

var timeout = errors.New("message sending timed out")

func (portal *Portal) HandleMatrixMessage(sender *User, evt *event.Event) {
	portal.log.Debugfln("Received event %s of type %s", evt.ID, evt.Type.Type)

	if evt.Type == event.EventReaction || evt.Type.Type == "m.reaction" {
		portal.handleMatrixReaction(sender, evt)
		return
	}
	if evt.Type == event.EventRedaction || evt.Type.Type == "m.room.redaction" {
		portal.HandleMatrixRedaction(sender, evt)
		return
	}

	info, sender := portal.convertMatrixMessage(sender, evt)
	if info == nil {
		return
	}
	for _, i := range info {
		portal.log.Debugln("Sending event", evt.ID, "to GroupMe")

		portal.recentlyHandledLock.Lock()
		portal.recentlyHandled[0] = ""
		portal.recentlyHandled = portal.recentlyHandled[1:]
		portal.recentlyHandled = append(portal.recentlyHandled, evt.ID.String())
		portal.recentlyHandledLock.Unlock()

		var err error
		i, err = portal.sendRaw(sender, evt, i, -1) //TODO deal with multiple messages for longer messages
		if err != nil {
			portal.log.Warnfln("Unable to handle message %s from Matrix: %v", evt.ID, err)
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

	if err != nil {
		portal.log.Warnfln("Failed to send message to %s: %v", info.GroupID, err)
		if retries > 0 {
			return portal.sendRaw(sender, evt, info, retries-1)
		}
		return nil, err
	}
	return m, nil
}

// handleMatrixEdit bridges a Matrix edit (m.replace) to GroupMe's message
// edit endpoint. GroupMe only supports editing group messages, and only for
// a limited period after they were sent.
func (portal *Portal) handleMatrixEdit(sender *User, evt *event.Event, content *event.MessageEventContent, replaceID id.EventID) {
	msg := portal.bridge.DB.Message.GetByMXID(replaceID)
	if msg == nil {
		portal.log.Warnfln("Failed to find message %s to bridge edit %s", replaceID, evt.ID)
		return
	}
	if portal.IsPrivateChat() {
		portal.log.Warnfln("Ignoring edit %s: GroupMe does not support editing direct messages", evt.ID)
		return
	}

	newContent := content.NewContent
	if newContent == nil {
		newContent = content
	}
	text := newContent.Body
	if newContent.Format == event.FormatHTML {
		text = portal.parseMatrixHTML(newContent)
	}

	err := sender.Client.EditMessage(context.TODO(), portal.Key.GMID, msg.GMID, text, nil)
	if err != nil {
		portal.log.Errorfln("Failed to edit GroupMe message %s: %v", msg.GMID, err)
	}
}

func (portal *Portal) HandleMatrixRedaction(sender *User, evt *event.Event) {
	if sender.Client == nil {
		portal.log.Warnfln("Sender %s has no GroupMe client, dropping redaction %s", sender.MXID, evt.ID)
		return
	}

	// The redacted event ID is normally in the top-level `redacts` field, but
	// room version 11 (MSC2174) and some clients (including Beeper) put it in
	// the content instead. mautrix v0.15.0 only parses the top-level field, so
	// fall back to the content when it's empty.
	redacts := evt.Redacts
	if redacts == "" {
		if raw, ok := evt.Content.Raw["redacts"].(string); ok {
			redacts = id.EventID(raw)
		}
	}
	if redacts == "" {
		portal.log.Warnfln("Dropping redaction %s with no redacts target", evt.ID)
		return
	}
	portal.log.Debugfln("Handling redaction %s targeting %s", evt.ID, redacts)

	if reaction := portal.bridge.DB.Reaction.GetByMXID(redacts); reaction != nil {
		portal.log.Debugfln("Redaction %s matched reaction on GroupMe message %s, removing", evt.ID, reaction.TargetGMID)
		err := sender.Client.DestroyLike(context.Background(), portal.Key.GMID, reaction.TargetGMID)
		if err != nil {
			portal.log.Errorfln("Failed to remove reaction on GroupMe message %s: %v", reaction.TargetGMID, err)
			return
		}
		reaction.Delete()
		return
	}

	if msg := portal.bridge.DB.Message.GetByMXID(redacts); msg != nil {
		err := sender.Client.DeleteMessage(context.Background(), portal.Key.GMID, msg.GMID)
		if err != nil {
			portal.log.Errorfln("Failed to delete GroupMe message %s: %v", msg.GMID, err)
			return
		}
		msg.Delete()
		return
	}

	portal.log.Debugfln("Redaction %s (target %s) matched no known reaction or message", evt.ID, redacts)
}

// getValidGroupMeReaction returns the emoji to send to GroupMe for a Matrix
// reaction. GroupMe accepts arbitrary unicode emoji as reactions (verified
// against the live API), so the reaction key is passed through as-is; only a
// blank key falls back to the default heart.
func getValidGroupMeReaction(reaction string) string {
	reaction = strings.TrimSpace(reaction)
	if reaction == "" {
		return defaultReactionEmoji
	}
	return reaction
}

func (portal *Portal) handleMatrixReaction(sender *User, evt *event.Event) {
	reaction, ok := evt.Content.Parsed.(*event.ReactionEventContent)
	if !ok {
		portal.log.Warnfln("Failed to parse content of reaction %s", evt.ID)
		return
	}

	msg := portal.bridge.DB.Message.GetByMXID(reaction.RelatesTo.EventID)
	if msg == nil {
		portal.log.Warnfln("Failed to find message %s for reaction %s", reaction.RelatesTo.EventID, evt.ID)
		return
	}

	if sender.Client == nil {
		portal.log.Warnfln("Sender %s has no GroupMe client, cannot send reaction", sender.MXID)
		return
	}

	// GroupMe only allows one reaction per user per message. If this user
	// already reacted to this message, Matrix lets them stack a second
	// reaction, but GroupMe will just replace the old one. Capture the
	// previous reaction so we can redact it below and keep the two in sync.
	prev := portal.bridge.DB.Reaction.GetByTargetGMID(portal.Key, msg.GMID, sender.GMID)

	emoji := getValidGroupMeReaction(reaction.RelatesTo.Key)
	portal.log.Debugfln("Sending reaction %q (from key %q) to GroupMe message %s as event %s", emoji, reaction.RelatesTo.Key, msg.GMID, evt.ID)
	err := sender.Client.CreateLike(context.Background(), portal.Key.GMID, msg.GMID, &groupme.ReactionRequest{
		LikeIcon: groupme.ReactionIcon{
			Type: "unicode",
			Code: emoji,
		},
	})
	if err != nil {
		portal.log.Errorfln("Failed to react to GroupMe message %s: %v", msg.GMID, err)
		return
	}

	dbReaction := portal.bridge.DB.Reaction.New()
	dbReaction.Chat = portal.Key
	dbReaction.TargetGMID = msg.GMID
	dbReaction.Sender = sender.GMID
	dbReaction.MXID = evt.ID
	dbReaction.GMID = "" // GroupMe reactions don't have IDs of their own
	dbReaction.Emoji = emoji
	dbReaction.Upsert(nil)

	// GroupMe replaced any previous reaction by this user, so redact the
	// superseded Matrix reaction. This mirrors GroupMe's one-reaction-per-user
	// model and keeps the stored event ID (used for removal) correct. The
	// redaction is done with the bridge bot so it isn't echoed back to us.
	if prev != nil && prev.MXID != evt.ID {
		portal.log.Debugfln("Redacting superseded reaction %s (replaced by %s)", prev.MXID, evt.ID)
		if _, err := portal.MainIntent().RedactEvent(portal.MXID, prev.MXID); err != nil {
			portal.log.Warnfln("Failed to redact superseded reaction %s: %v", prev.MXID, err)
		}
	}
}

func (portal *Portal) Delete() {
	portal.Portal.Delete()
	portal.bridge.portalsLock.Lock()
	delete(portal.bridge.portalsByGMID, portal.Key)
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
		err := sender.Client.RemoveFromGroup(sender.GMID, portal.Key.GMID)
		if err != nil {
			portal.log.Errorfln("Failed to leave group as %s: %v", sender.MXID, err)
			return
		}
		portal.CleanupIfEmpty()
	}
}

func (portal *Portal) HandleMatrixKick(sender *User, evt *event.Event) {
}

func (portal *Portal) HandleMatrixInvite(sender *User, evt *event.Event) {
}

// dmConversationID builds the GroupMe DM conversation ID ("smallerID+largerID")
// for a private chat portal.
func dmConversationID(key database.PortalKey) groupme.ID {
	a, b := key.GMID.String(), key.Receiver.String()
	// GroupMe puts the numerically smaller user ID first.
	if len(b) < len(a) || (len(a) == len(b) && b < a) {
		a, b = b, a
	}
	return groupme.ID(a + "+" + b)
}

// HandleMatrixReadReceipt bridges Matrix read receipts to GroupMe. GroupMe
// only supports read receipts in direct messages.
func (portal *Portal) HandleMatrixReadReceipt(sender bridge.User, eventID id.EventID, receipt event.ReadReceipt) {
	user, ok := sender.(*User)
	if !ok || user.Client == nil || !portal.IsPrivateChat() {
		return
	}
	msg := portal.bridge.DB.Message.GetByMXID(eventID)
	if msg == nil {
		return
	}
	err := user.Client.MarkDirectMessageRead(context.TODO(), dmConversationID(portal.Key), msg.GMID)
	if err != nil {
		portal.log.Warnfln("Failed to mark message %s as read on GroupMe: %v", msg.GMID, err)
	}
}

// HandleMatrixTyping bridges Matrix typing notifications to GroupMe. GroupMe
// expects a fresh typing event every 5 seconds while the user keeps typing,
// so a background loop re-publishes until the user stops.
func (portal *Portal) HandleMatrixTyping(userIDs []id.UserID) {
	portal.currentlyTypingLock.Lock()
	defer portal.currentlyTypingLock.Unlock()
	if portal.currentlyTyping == nil {
		portal.currentlyTyping = make(map[id.UserID]context.CancelFunc)
	}

	typing := make(map[id.UserID]bool, len(userIDs))
	for _, mxid := range userIDs {
		typing[mxid] = true
		if _, alreadyTyping := portal.currentlyTyping[mxid]; alreadyTyping {
			continue
		}
		user := portal.bridge.GetUserByMXIDIfExists(mxid)
		if user == nil || user.Conn == nil || len(user.GMID) == 0 {
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		portal.currentlyTyping[mxid] = cancel
		go portal.sendTypingLoop(ctx, user)
	}

	for mxid, cancel := range portal.currentlyTyping {
		if !typing[mxid] {
			cancel()
			delete(portal.currentlyTyping, mxid)
		}
	}
}

func (portal *Portal) sendTypingLoop(ctx context.Context, user *User) {
	chatID := portal.Key.GMID
	isDM := portal.IsPrivateChat()
	if isDM {
		chatID = dmConversationID(portal.Key)
	}
	ticker := time.NewTicker(4 * time.Second)
	defer ticker.Stop()
	// Matrix servers resend the typing state regularly, but cap the loop in
	// case a stop event is missed.
	deadline := time.After(90 * time.Second)
	for {
		if err := user.Conn.SendTyping(ctx, chatID, isDM, user.GMID); err != nil {
			portal.log.Debugfln("Failed to send typing indicator to GroupMe: %v", err)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-deadline:
			return
		case <-ticker.C:
		}
	}
}
