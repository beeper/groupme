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
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	log "maunium.net/go/maulogger/v2"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/bridge"
	"maunium.net/go/mautrix/bridge/bridgeconfig"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"

	"github.com/beeper/groupme-lib"

	"github.com/beeper/groupme/database"
	"github.com/beeper/groupme/groupmeext"
)

type User struct {
	*database.User
	Conn *groupme.PushSubscription

	bridge *GMBridge
	log    log.Logger

	Admin           bool
	Whitelisted     bool
	PermissionLevel bridgeconfig.PermissionLevel

	BridgeState *bridge.BridgeStateQueue

	Client           *groupmeext.Client
	ConnectionErrors int
	CommunityID      string

	ChatList     map[groupme.ID]groupme.Chat
	GroupList    map[groupme.ID]groupme.Group
	RelationList map[groupme.ID]groupme.User

	cleanDisconnection  bool
	batteryWarningsSent int
	lastReconnection    int64

	chatListReceived chan struct{}
	syncPortalsDone  chan struct{}

	chatListLock    sync.RWMutex
	pollOnce        sync.Once
	pollSeenLock    sync.Mutex
	pollSeen        map[string]struct{}
	pollSeenOrder   []string
	pollInitialized map[string]bool
	pollUpdated     map[string]string
	pollPrimed      bool

	messageInput  chan PortalMessage
	messageOutput chan PortalMessage

	mgmtCreateLock sync.Mutex

	spaceCreateLock        sync.Mutex
	spaceMembershipChecked bool
}

func (br *GMBridge) getUserByMXID(userID id.UserID, onlyIfExists bool) *User {
	_, isPuppet := br.ParsePuppetMXID(userID)
	if isPuppet || userID == br.Bot.UserID {
		return nil
	}
	br.usersLock.Lock()
	defer br.usersLock.Unlock()
	user, ok := br.usersByMXID[userID]
	if !ok {
		userIDPtr := &userID
		if onlyIfExists {
			userIDPtr = nil
		}
		return br.loadDBUser(br.DB.User.GetByMXID(userID), userIDPtr)
	}
	return user
}

func (br *GMBridge) GetUserByMXID(userID id.UserID) *User {
	return br.getUserByMXID(userID, false)
}

func (br *GMBridge) GetIUser(userID id.UserID, create bool) bridge.User {
	u := br.getUserByMXID(userID, !create)
	if u == nil {
		return nil
	}
	return u
}

func (user *User) GetPermissionLevel() bridgeconfig.PermissionLevel {
	return user.PermissionLevel
}

func (user *User) GetManagementRoomID() id.RoomID {
	return user.ManagementRoom
}

func (user *User) GetMXID() id.UserID {
	return user.MXID
}

func (user *User) GetCommandState() map[string]interface{} {
	return nil
}

func (br *GMBridge) GetUserByMXIDIfExists(userID id.UserID) *User {
	return br.getUserByMXID(userID, true)
}

func (bridge *GMBridge) GetUserByGMID(gmid groupme.ID) *User {
	bridge.usersLock.Lock()
	defer bridge.usersLock.Unlock()
	user, ok := bridge.usersByGMID[gmid]
	if !ok {
		return bridge.loadDBUser(bridge.DB.User.GetByGMID(gmid), nil)
	}
	return user
}

func (user *User) addToGMIDMap() {
	user.bridge.usersLock.Lock()
	user.bridge.usersByGMID[user.GMID] = user
	user.bridge.usersLock.Unlock()
}

func (user *User) removeFromGMIDMap() {
	user.bridge.usersLock.Lock()
	jidUser, ok := user.bridge.usersByGMID[user.GMID]
	if ok && user == jidUser {
		delete(user.bridge.usersByGMID, user.GMID)
	}
	user.bridge.usersLock.Unlock()
	user.bridge.Metrics.TrackLoginState(user.GMID, false)
}

func (br *GMBridge) GetAllUsers() []*User {
	br.usersLock.Lock()
	defer br.usersLock.Unlock()
	dbUsers := br.DB.User.GetAll()
	output := make([]*User, len(dbUsers))
	for index, dbUser := range dbUsers {
		user, ok := br.usersByMXID[dbUser.MXID]
		if !ok {
			user = br.loadDBUser(dbUser, nil)
		}
		output[index] = user
	}
	return output
}

func (br *GMBridge) loadDBUser(dbUser *database.User, mxid *id.UserID) *User {
	if dbUser == nil {
		if mxid == nil {
			return nil
		}
		dbUser = br.DB.User.New()
		dbUser.MXID = *mxid
		dbUser.Insert()
	}
	user := br.NewUser(dbUser)
	br.usersByMXID[user.MXID] = user
	if len(user.GMID) > 0 {
		br.usersByGMID[user.GMID] = user
	}
	if len(user.ManagementRoom) > 0 {
		br.managementRooms[user.ManagementRoom] = user
	}
	return user
}

func (br *GMBridge) NewUser(dbUser *database.User) *User {
	user := &User{
		User:   dbUser,
		bridge: br,
		log:    br.Log.Sub("User").Sub(string(dbUser.MXID)),

		chatListReceived: make(chan struct{}, 1),
		syncPortalsDone:  make(chan struct{}, 1),
		messageInput:     make(chan PortalMessage),
		messageOutput:    make(chan PortalMessage, br.Config.Bridge.PortalMessageBuffer),
		pollSeen:         make(map[string]struct{}),
		pollInitialized:  make(map[string]bool),
		pollUpdated:      make(map[string]string),
	}

	user.PermissionLevel = user.bridge.Config.Bridge.Permissions.Get(user.MXID)
	user.Whitelisted = user.PermissionLevel >= bridgeconfig.PermissionLevelUser
	user.Admin = user.PermissionLevel >= bridgeconfig.PermissionLevelAdmin
	user.BridgeState = br.NewBridgeStateQueue(user)
	go user.handleMessageLoop()
	go user.runMessageRingBuffer()
	return user
}

func (user *User) ensureInvited(intent *appservice.IntentAPI, roomID id.RoomID, isDirect bool) (ok bool) {
	// Avoid re-inviting users who Hungryserv already placed into the room.
	if members, err := intent.JoinedMembers(roomID); err == nil {
		if _, joined := members.Joined[user.MXID]; joined {
			user.bridge.StateStore.SetMembership(roomID, user.MXID, event.MembershipJoin)
			return true
		}
	}

	extraContent := make(map[string]interface{})
	if isDirect {
		extraContent["is_direct"] = true
	}
	customPuppet := user.bridge.GetPuppetByCustomMXID(user.MXID)
	autoAcceptInvite := user.bridge.supportsBeeperAutoJoinInvites() ||
		(customPuppet != nil && customPuppet.CustomIntent() != nil)
	if autoAcceptInvite {
		extraContent["fi.mau.will_auto_accept"] = true
	}
	_, err := intent.InviteUser(roomID, &mautrix.ReqInviteUser{UserID: user.MXID}, extraContent)
	var httpErr mautrix.HTTPError
	if err != nil && errors.As(err, &httpErr) && httpErr.RespError != nil && strings.Contains(httpErr.RespError.Err, "is already in the room") {
		user.bridge.StateStore.SetMembership(roomID, user.MXID, event.MembershipJoin)
		ok = true
		return
	} else if err != nil {
		// Hungryserv may report a forbidden invite even if the user is already
		// present through Beeper initial-members handling. Verify actual room
		// membership before treating the invite as a failure.
		if members, memberErr := intent.JoinedMembers(roomID); memberErr == nil {
			if _, joined := members.Joined[user.MXID]; joined {
				user.bridge.StateStore.SetMembership(roomID, user.MXID, event.MembershipJoin)
				return true
			}
		}
		user.log.Warnfln("Failed to invite user to %s: %v", roomID, err)
	} else {
		ok = true
	}

	if customPuppet != nil && customPuppet.CustomIntent() != nil {
		err = customPuppet.CustomIntent().EnsureJoined(roomID, appservice.EnsureJoinedParams{IgnoreCache: true})
		if err != nil {
			user.log.Warnfln("Failed to auto-join %s: %v", roomID, err)
			ok = false
		} else {
			ok = true
		}
	}
	return
}

func (user *User) GetSpaceRoom() id.RoomID {
	if !user.bridge.Config.Bridge.PersonalFilteringSpaces {
		return ""
	}

	if len(user.SpaceRoom) == 0 {
		user.spaceCreateLock.Lock()
		defer user.spaceCreateLock.Unlock()
		if len(user.SpaceRoom) > 0 {
			return user.SpaceRoom
		}

		resp, err := user.bridge.Bot.CreateRoom(&mautrix.ReqCreateRoom{
			Visibility: "private",
			Name:       "GroupMe",
			Topic:      "Your GroupMe bridged chats",
			InitialState: []*event.Event{{
				Type: event.StateRoomAvatar,
				Content: event.Content{
					Parsed: &event.RoomAvatarEventContent{
						URL: user.bridge.Config.AppService.Bot.ParsedAvatar,
					},
				},
			}},
			CreationContent: map[string]interface{}{
				"type": event.RoomTypeSpace,
			},
			PowerLevelOverride: &event.PowerLevelsEventContent{
				Users: map[id.UserID]int{
					user.bridge.Bot.UserID: 9001,
					user.MXID:              50,
				},
			},
		})

		if err != nil {
			user.log.Errorln("Failed to auto-create space room:", err)
		} else {
			user.SpaceRoom = resp.RoomID
			user.Update()
			user.ensureInvited(user.bridge.Bot, user.SpaceRoom, false)
		}
	} else if !user.spaceMembershipChecked && !user.bridge.StateStore.IsInRoom(user.SpaceRoom, user.MXID) {
		user.ensureInvited(user.bridge.Bot, user.SpaceRoom, false)
	}
	user.spaceMembershipChecked = true

	return user.SpaceRoom
}

func (user *User) GetManagementRoom() id.RoomID {
	if len(user.ManagementRoom) == 0 {
		user.mgmtCreateLock.Lock()
		defer user.mgmtCreateLock.Unlock()
		if len(user.ManagementRoom) > 0 {
			return user.ManagementRoom
		}
		creationContent := make(map[string]interface{})
		if !user.bridge.Config.Bridge.FederateRooms {
			creationContent["m.federate"] = false
		}
		resp, err := user.bridge.Bot.CreateRoom(&mautrix.ReqCreateRoom{
			Topic:           "GroupMe bridge notices",
			IsDirect:        true,
			CreationContent: creationContent,
		})
		if err != nil {
			user.log.Errorln("Failed to auto-create management room:", err)
		} else {
			user.SetManagementRoom(resp.RoomID)
		}
	}
	return user.ManagementRoom
}

func (user *User) SetManagementRoom(roomID id.RoomID) {
	existingUser, ok := user.bridge.managementRooms[roomID]
	if ok {
		existingUser.ManagementRoom = ""
		existingUser.Update()
	}

	user.ManagementRoom = roomID
	user.bridge.managementRooms[user.ManagementRoom] = user
	user.Update()
}

func (user *User) ensureRESTClient() error {
	if len(user.Token) == 0 {
		return errors.New("missing GroupMe access token")
	}
	if user.Client == nil {
		user.Client = groupmeext.NewClient(user.Token)
	}
	if len(user.GMID) == 0 {
		me, err := user.Client.MyUser(context.TODO())
		if err != nil {
			return fmt.Errorf("failed to resolve GroupMe user: %w", err)
		}
		user.GMID = me.ID
		user.Update()
	}
	user.addToGMIDMap()
	return nil
}

func (user *User) Connect() bool {
	if len(user.Token) == 0 {
		return false
	}
	if err := user.ensureRESTClient(); err != nil {
		user.log.Errorln("Failed to initialize GroupMe REST client:", err)
		return false
	}

	// GroupMe's legacy Faye endpoint currently returns gateway timeouts during
	// the Bayeux handshake. Keep the bridge usable by relying on the REST API
	// for both initial chat discovery and a lightweight message poller.
	user.log.Infoln("Using GroupMe REST polling fallback; realtime Faye push is disabled")
	user.ConnectionErrors = 0
	user.PostLogin()
	user.startRESTPolling()
	return true
}

func (user *User) RestoreSession() bool {
	return user.Connect()
}

func (user *User) HasSession() bool {
	return len(user.Token) > 0
}

func (user *User) IsConnected() bool {
	return user.Client != nil && len(user.Token) > 0
}

func (user *User) IsLoggedIn() bool {
	return user.IsConnected()
}

func (user *User) IsLoginInProgress() bool {
	return false
}

func (user *User) GetGMID() groupme.ID {
	if len(user.GMID) == 0 {
		if err := user.ensureRESTClient(); err != nil {
			user.log.Errorln("Failed to get own GroupMe ID:", err)
			return ""
		}
	}
	return user.GMID
}

func (user *User) Login(token string) error {
	oldToken := user.Token
	oldClient := user.Client
	oldGMID := user.GMID

	user.Token = token
	user.Client = groupmeext.NewClient(token)
	user.GMID = ""
	if err := user.ensureRESTClient(); err != nil {
		user.Token = oldToken
		user.Client = oldClient
		user.GMID = oldGMID
		return err
	}
	if !user.Connect() {
		user.Token = oldToken
		user.Client = oldClient
		user.GMID = oldGMID
		return errors.New("failed to connect")
	}
	return nil
}

type Chat struct {
	Portal          *Portal
	LastMessageTime uint64
	Group           *groupme.Group
	DM              *groupme.Chat
}

type ChatList []Chat

func (cl ChatList) Len() int {
	return len(cl)
}

func (cl ChatList) Less(i, j int) bool {
	return cl[i].LastMessageTime > cl[j].LastMessageTime
}

func (cl ChatList) Swap(i, j int) {
	cl[i], cl[j] = cl[j], cl[i]
}

func (user *User) PostLogin() {
	user.bridge.Metrics.TrackConnectionState(user.GMID, true)
	user.bridge.Metrics.TrackLoginState(user.GMID, true)
	user.bridge.Metrics.TrackBufferLength(user.MXID, 0)
	go user.intPostLogin()
}

func (user *User) tryAutomaticDoublePuppeting() {
	if !user.bridge.Config.CanAutoDoublePuppet(user.MXID) {
		return
	}
	user.log.Debugln("Checking if double puppeting needs to be enabled")
	puppet := user.bridge.GetPuppetByGMID(user.GMID)
	if len(puppet.CustomMXID) > 0 {
		user.log.Debugln("User already has double-puppeting enabled")
		// Custom puppet already enabled
		return
	}
	accessToken, err := puppet.loginWithSharedSecret(user.MXID)
	if err != nil {
		user.log.Warnln("Failed to login with shared secret:", err)
		return
	}
	err = puppet.SwitchCustomMXID(accessToken, user.MXID)
	if err != nil {
		puppet.log.Warnln("Failed to switch to auto-logined custom puppet:", err)
		return
	}
	user.log.Infoln("Successfully automatically enabled custom puppet")
}

func (user *User) sendBridgeNotice(formatString string, args ...interface{}) {
	notice := fmt.Sprintf(formatString, args...)
	_, err := user.bridge.Bot.SendNotice(user.GetManagementRoom(), notice)
	if err != nil {
		user.log.Warnf("Failed to send bridge notice \"%s\": %v", notice, err)
	}
}

func (user *User) sendMarkdownBridgeAlert(formatString string, args ...interface{}) {
	notice := fmt.Sprintf(formatString, args...)
	content := format.RenderMarkdown(notice, true, false)
	_, err := user.bridge.Bot.SendMessageEvent(user.GetManagementRoom(), event.EventMessage, content)
	if err != nil {
		user.log.Warnf("Failed to send bridge alert \"%s\": %v", notice, err)
	}
}

func (user *User) postConnPing() bool {
	// user.log.Debugln("Making post-connection ping")
	// err := user.Conn.AdminTest()
	// if err != nil {
	// 	user.log.Errorfln("Post-connection ping failed: %v. Disconnecting and then reconnecting after a second", err)
	// 	sess, disconnectErr := user.Conn.Disconnect()
	// 	if disconnectErr != nil {
	// 		user.log.Warnln("Error while disconnecting after failed post-connection ping:", disconnectErr)
	// 	} else {
	// 		user.Session = &sess
	// 	}
	// 	user.bridge.Metrics.TrackDisconnection(user.MXID)
	// 	go func() {
	// 		time.Sleep(1 * time.Second)
	// 		user.tryReconnect(fmt.Sprintf("Post-connection ping failed: %v", err))
	// 	}()
	// 	return false
	// } else {
	// 	user.log.Debugln("Post-connection ping OK")
	// 	return true
	// }
	return true
}

func (user *User) intPostLogin() {
	user.lastReconnection = time.Now().Unix()
	if err := user.ensureRESTClient(); err != nil {
		user.log.Errorln("Post-login initialization failed:", err)
		return
	}
	user.tryAutomaticDoublePuppeting()
	user.HandleChatList()
}

// func (user *User) intPostLogin() {
// 	defer user.syncWait.Done()
// 	user.lastReconnection = time.Now().Unix()
// 	user.Client = groupmeext.NewClient(user.Token)
// 	if len(user.JID) == 0 {
// 		myuser, err := user.Client.MyUser(context.TODO())
// 		if err != nil {
// 			log.Fatal(err) //TODO
// 		}
// 		user.JID = myuser.ID.String()
// 	}
// 	user.Update()

// 	user.tryAutomaticDoublePuppeting()

// 	user.log.Debugln("Waiting for chat list receive confirmation")
// 	user.HandleChatList()
// 	select {
// 	case <-user.chatListReceived:
// 		user.log.Debugln("Chat list receive confirmation received in PostLogin")
// 	case <-time.After(time.Duration(user.bridge.Config.Bridge.ChatListWait) * time.Second):
// 		user.log.Warnln("Timed out waiting for chat list to arrive!")
// 		user.postConnPing()
// 		return
// 	}

// 	if !user.postConnPing() {
// 		user.log.Debugln("Post-connection ping failed, unlocking processing of incoming messages.")
// 		return
// 	}

// 	user.log.Debugln("Waiting for portal sync complete confirmation")
// 	select {
// 	case <-user.syncPortalsDone:
// 		user.log.Debugln("Post-connection portal sync complete, unlocking processing of incoming messages.")
// 	// TODO this is too short, maybe a per-portal duration?
// 	case <-time.After(time.Duration(user.bridge.Config.Bridge.PortalSyncWait) * time.Second):
// 		user.log.Warnln("Timed out waiting for portal sync to complete! Unlocking processing of incoming messages.")
// 	}
// }

func (user *User) HandleChatList() {
	chatMap := map[groupme.ID]groupme.Group{}
	chats, err := user.Client.IndexAllGroups()
	if err != nil {
		user.log.Errorln("chat sync error", err) //TODO: handle
		return
	}
	for _, chat := range chats {
		chatMap[chat.ID] = *chat
	}
	user.chatListLock.Lock()
	user.GroupList = chatMap
	user.chatListLock.Unlock()

	dmMap := map[groupme.ID]groupme.Chat{}
	dms, err := user.Client.IndexAllChats()
	if err != nil {
		user.log.Errorln("chat sync error", err) //TODO: handle
		return
	}
	for _, dm := range dms {
		dmMap[dm.OtherUser.ID] = *dm
	}
	user.chatListLock.Lock()
	user.ChatList = dmMap
	user.chatListLock.Unlock()

	userMap := map[groupme.ID]groupme.User{}
	users, err := user.Client.IndexAllRelations()
	if err != nil {
		user.log.Errorln("Error syncing user list, continuing sync", err)
	}
	for _, u := range users {
		puppet := user.bridge.GetPuppetByGMID(u.ID)
		//               "" for overall user not related to one group
		puppet.Sync(nil, &groupme.Member{
			UserID:   u.ID,
			Nickname: u.Name,
			ImageURL: u.AvatarURL,
		}, false, false)
		userMap[u.ID] = *u
	}
	user.chatListLock.Lock()
	user.RelationList = userMap
	user.chatListLock.Unlock()

	user.log.Infoln("Chat list received")
	select {
	case user.chatListReceived <- struct{}{}:
	default:
	}
	go user.syncPortals(false)
}

func (user *User) snapshotChats() (map[groupme.ID]groupme.Group, map[groupme.ID]groupme.Chat) {
	user.chatListLock.RLock()
	defer user.chatListLock.RUnlock()
	groups := make(map[groupme.ID]groupme.Group, len(user.GroupList))
	for id, group := range user.GroupList {
		groups[id] = group
	}
	dms := make(map[groupme.ID]groupme.Chat, len(user.ChatList))
	for id, chat := range user.ChatList {
		dms[id] = chat
	}
	return groups, dms
}

func (user *User) syncPortals(createAll bool) {
	groups, dms := user.snapshotChats()
	chats := make(ChatList, 0, len(groups)+len(dms))

	for _, group := range groups {
		portal := user.bridge.GetPortalByGMID(database.GroupPortalKey(group.ID))
		chats = append(chats, Chat{
			Portal:          portal,
			LastMessageTime: uint64(group.UpdatedAt.ToTime().Unix()),
			Group:           &group,
		})
	}
	for _, dm := range dms {
		portal := user.bridge.GetPortalByGMID(database.NewPortalKey(dm.OtherUser.ID, user.GMID))
		chats = append(chats, Chat{
			Portal:          portal,
			LastMessageTime: uint64(dm.UpdatedAt.ToTime().Unix()),
			DM:              &dm,
		})
	}

	sort.Sort(chats)
	limit := user.bridge.Config.Bridge.HistorySync.MaxInitialConversations
	if limit <= 0 {
		limit = 5
	}
	if limit > len(chats) {
		limit = len(chats)
	}
	for i, chat := range chats {
		if chat.Portal == nil {
			continue
		}
		if createAll || len(chat.Portal.MXID) > 0 || i < limit {
			chat.Portal.Sync(user, chat.Group)
		}
	}
	user.UpdateDirectChats(nil)
	user.log.Infoln("Finished syncing portals")
	select {
	case user.syncPortalsDone <- struct{}{}:
	default:
	}
}

const restPollInterval = 10 * time.Second
const restChatRefreshInterval = 1 * time.Minute
const restSeenMessageLimit = 5000

func (user *User) rememberPolledMessage(conversation string, messageID groupme.ID) (seenBefore bool, initialized bool) {
	key := conversation + ":" + messageID.String()
	user.pollSeenLock.Lock()
	defer user.pollSeenLock.Unlock()
	initialized = user.pollInitialized[conversation]
	_, seenBefore = user.pollSeen[key]
	if !seenBefore {
		user.pollSeen[key] = struct{}{}
		user.pollSeenOrder = append(user.pollSeenOrder, key)
		if len(user.pollSeenOrder) > restSeenMessageLimit {
			old := user.pollSeenOrder[0]
			user.pollSeenOrder = user.pollSeenOrder[1:]
			delete(user.pollSeen, old)
		}
	}
	return
}

func (user *User) markPollConversationInitialized(conversation string) {
	user.pollSeenLock.Lock()
	user.pollInitialized[conversation] = true
	user.pollSeenLock.Unlock()
}

func (user *User) pollGroupMessages(group groupme.Group) {
	resp, err := user.Client.IndexMessages(context.TODO(), group.ID, &groupme.IndexMessagesQuery{Limit: 20})
	if err != nil {
		user.log.Warnfln("REST poll failed for group %s: %v", group.ID, err)
		return
	}
	conversation := "group:" + group.ID.String()
	for i := len(resp.Messages) - 1; i >= 0; i-- {
		msg := resp.Messages[i]
		if msg == nil || len(msg.ID) == 0 {
			continue
		}
		seen, initialized := user.rememberPolledMessage(conversation, msg.ID)
		if initialized && !seen {
			key := database.GroupPortalKey(group.ID)
			portal := user.bridge.GetPortalByGMID(key)
			if portal != nil && len(portal.MXID) == 0 {
				portal.Sync(user, &group)
			}
			user.messageInput <- PortalMessage{key, user, msg, uint64(msg.CreatedAt.ToTime().Unix())}
		}
	}
	user.markPollConversationInitialized(conversation)
}

func (user *User) pollDMMessages(chat groupme.Chat) {
	conversation := "dm:" + chat.OtherUser.ID.String()
	key := database.NewPortalKey(chat.OtherUser.ID, user.GMID)

	// The /chats response already contains the complete latest DM message.
	// Prefer that over a second /direct_messages request: it is cheaper and,
	// more importantly, avoids relying on another legacy endpoint just to get
	// the message we already have in hand.
	msg := chat.LastMessage
	if msg == nil || len(msg.ID) == 0 {
		user.log.Debugfln("REST poll DM %s has no last_message payload", chat.OtherUser.ID)
		user.markPollConversationInitialized(conversation)
		return
	}

	seen, initialized := user.rememberPolledMessage(conversation, msg.ID)
	user.log.Debugfln("REST poll DM %s latest=%s initialized=%t seen=%t", chat.OtherUser.ID, msg.ID, initialized, seen)
	if initialized && !seen {
		portal := user.bridge.GetPortalByGMID(key)
		if portal != nil && len(portal.MXID) == 0 {
			portal.Sync(user, nil)
		}
		user.log.Infofln("REST poll detected new DM message %s from %s; queueing for Matrix", msg.ID, msg.UserID)
		user.messageInput <- PortalMessage{key, user, msg, uint64(msg.CreatedAt.ToTime().Unix())}
	}
	user.markPollConversationInitialized(conversation)
}

func (user *User) conversationNeedsPoll(conversation, cursor string) (needsPoll, knownConversation bool) {
	user.pollSeenLock.Lock()
	defer user.pollSeenLock.Unlock()
	previous, ok := user.pollUpdated[conversation]
	user.pollUpdated[conversation] = cursor
	return !ok || cursor != previous, ok
}

func (user *User) pollingPrimed() bool {
	user.pollSeenLock.Lock()
	defer user.pollSeenLock.Unlock()
	return user.pollPrimed
}

func (user *User) markPollingPrimed() {
	user.pollSeenLock.Lock()
	user.pollPrimed = true
	user.pollSeenLock.Unlock()
}

func groupPollCursor(group *groupme.Group) string {
	if group == nil {
		return ""
	}
	if len(group.Messages.LastMessageID) > 0 {
		return "message:" + group.Messages.LastMessageID.String()
	}
	return fmt.Sprintf("updated:%d", group.UpdatedAt.ToTime().Unix())
}

func dmPollCursor(chat *groupme.Chat) string {
	if chat == nil {
		return ""
	}
	if chat.LastMessage != nil && len(chat.LastMessage.ID) > 0 {
		return "message:" + chat.LastMessage.ID.String()
	}
	return fmt.Sprintf("updated:%d", chat.UpdatedAt.ToTime().Unix())
}

func (user *User) pollChangedMessages() {
	primed := user.pollingPrimed()
	groups, groupErr := user.Client.IndexAllGroups()
	if groupErr != nil {
		user.log.Warnln("REST polling failed to refresh groups:", groupErr)
	} else {
		groupMap := make(map[groupme.ID]groupme.Group, len(groups))
		for _, group := range groups {
			if group == nil {
				continue
			}
			groupMap[group.ID] = *group
			conversation := "group:" + group.ID.String()
			cursor := groupPollCursor(group)
			needsPoll, knownConversation := user.conversationNeedsPoll(conversation, cursor)
			if needsPoll {
				if primed && !knownConversation {
					// This group appeared after the startup baseline. Treat its current
					// latest messages as live instead of silently seeding them.
					user.markPollConversationInitialized(conversation)
					user.log.Infofln("REST poll discovered new group %s after startup; current messages are eligible for delivery", group.ID)
				}
				user.log.Debugfln("REST poll group %s cursor changed to %s", group.ID, cursor)
				user.pollGroupMessages(*group)
			}
		}
		user.chatListLock.Lock()
		user.GroupList = groupMap
		user.chatListLock.Unlock()
	}

	dms, dmErr := user.Client.IndexAllChats()
	if dmErr != nil {
		user.log.Warnln("REST polling failed to refresh direct chats:", dmErr)
	} else {
		dmMap := make(map[groupme.ID]groupme.Chat, len(dms))
		for _, dm := range dms {
			if dm == nil {
				continue
			}
			dmMap[dm.OtherUser.ID] = *dm
			conversation := "dm:" + dm.OtherUser.ID.String()
			cursor := dmPollCursor(dm)
			needsPoll, knownConversation := user.conversationNeedsPoll(conversation, cursor)
			if needsPoll {
				if primed && !knownConversation {
					user.markPollConversationInitialized(conversation)
					user.log.Infofln("REST poll discovered new DM %s after startup; current message is eligible for delivery", dm.OtherUser.ID)
				}
				user.log.Debugfln("REST poll DM %s cursor changed to %s", dm.OtherUser.ID, cursor)
				user.pollDMMessages(*dm)
			}
		}
		user.chatListLock.Lock()
		user.ChatList = dmMap
		user.chatListLock.Unlock()
	}

	if !primed {
		user.markPollingPrimed()
		user.log.Debugln("REST polling startup baseline complete")
	}
}

func (user *User) startRESTPolling() {
	user.pollOnce.Do(func() {
		go func() {
			// Initial pass seeds each conversation's latest messages without replaying them.
			// Subsequent passes fetch message bodies only when the chat's updated_at changes.
			user.pollChangedMessages()
			pollTicker := time.NewTicker(restPollInterval)
			refreshTicker := time.NewTicker(restChatRefreshInterval)
			defer pollTicker.Stop()
			defer refreshTicker.Stop()
			for {
				select {
				case <-pollTicker.C:
					user.pollChangedMessages()
				case <-refreshTicker.C:
					// Refresh relations/puppet metadata and ensure newly discovered chats
					// get portal rooms according to the configured initial sync limit.
					user.HandleChatList()
				}
			}
		}()
	})
}

func (user *User) getDirectChats() map[id.UserID][]id.RoomID {
	res := make(map[id.UserID][]id.RoomID)
	privateChats := user.bridge.DB.Portal.FindPrivateChats(user.GMID)
	for _, portal := range privateChats {
		if len(portal.MXID) > 0 {
			res[user.bridge.FormatPuppetMXID(portal.Key.GMID)] = []id.RoomID{portal.MXID}
		}
	}
	return res
}

func (user *User) UpdateDirectChats(chats map[id.UserID][]id.RoomID) {
	if !user.bridge.Config.Bridge.SyncDirectChatList {
		return
	}
	puppet := user.bridge.GetPuppetByCustomMXID(user.MXID)
	if puppet == nil || puppet.CustomIntent() == nil {
		return
	}
	intent := puppet.CustomIntent()
	method := http.MethodPatch
	if chats == nil {
		chats = user.getDirectChats()
		method = http.MethodPut
	}
	user.log.Debugln("Updating m.direct list on homeserver")
	var err error
	if user.bridge.Config.Homeserver.Software == bridgeconfig.SoftwareAsmux {
		urlPath := intent.BuildClientURL("unstable", "com.beeper.asmux", "dms")
		_, err = intent.MakeFullRequest(mautrix.FullRequest{
			Method:      method,
			URL:         urlPath,
			Headers:     http.Header{"X-Asmux-Auth": {user.bridge.AS.Registration.AppToken}},
			RequestJSON: chats,
		})
	} else {
		existingChats := make(map[id.UserID][]id.RoomID)
		err = intent.GetAccountData(event.AccountDataDirectChats.Type, &existingChats)
		if err != nil {
			user.log.Warnln("Failed to get m.direct list to update it:", err)
			return
		}
		for userID, rooms := range existingChats {
			if _, ok := user.bridge.ParsePuppetMXID(userID); !ok {
				// This is not a ghost user, include it in the new list
				chats[userID] = rooms
			} else if _, ok := chats[userID]; !ok && method == http.MethodPatch {
				// This is a ghost user, but we're not replacing the whole list, so include it too
				chats[userID] = rooms
			}
		}
		err = intent.SetAccountData(event.AccountDataDirectChats.Type, &chats)
	}
	if err != nil {
		user.log.Warnln("Failed to update m.direct list:", err)
	}
}

func (user *User) HandleError(err error) {
}

func (user *User) ShouldCallSynchronously() bool {
	return true
}

func (user *User) HandleJSONParseError(err error) {
	user.log.Errorln("GroupMe JSON parse error:", err)
}

func (user *User) PortalKey(gmid groupme.ID) database.PortalKey {
	return database.NewPortalKey(gmid, user.GMID)
}

func (user *User) GetPortalByGMID(gmid groupme.ID) *Portal {
	return user.bridge.GetPortalByGMID(user.PortalKey(gmid))
}

func (user *User) runMessageRingBuffer() {
	for msg := range user.messageInput {
		select {
		case user.messageOutput <- msg:
			user.bridge.Metrics.TrackBufferLength(user.MXID, len(user.messageOutput))
		default:
			dropped := <-user.messageOutput
			user.log.Warnln("Buffer is full, dropping message in", dropped.chat)
			user.messageOutput <- msg
		}
	}
}

func (user *User) handleMessageLoop() {
	for {
		select {
		case msg := <-user.messageOutput:
			user.bridge.Metrics.TrackBufferLength(user.MXID, len(user.messageOutput))
			puppet := user.bridge.GetPuppetByGMID(msg.data.UserID)
			portal := user.bridge.GetPortalByGMID(msg.chat)
			if puppet != nil {
				puppet.Sync(user, &groupme.Member{
					UserID:   msg.data.UserID,
					Nickname: msg.data.Name,
					ImageURL: msg.data.AvatarURL,
				}, false, false)
			}
			portal.messages <- msg
		}
	}
}

func (user *User) HandleTextMessage(message groupme.Message) {
	id := database.ParsePortalKey(message.GroupID.String())

	if id == nil {
		id = database.ParsePortalKey(message.ConversationID.String())
	}
	if id == nil {
		user.log.Errorln("Error parsing conversationid/portalkey", message.ConversationID.String(), "ignoring message")
		return
	}

	user.messageInput <- PortalMessage{*id, user, &message, uint64(message.CreatedAt.ToTime().Unix())}
}

func (user *User) HandleLike(msg groupme.Message) {
	user.HandleTextMessage(msg)
}

func (user *User) HandleJoin(id groupme.ID) {
	user.HandleChatList()
	//TODO: efficient
}

func (user *User) HandleGroupName(group groupme.ID, newName string) {
	//p := user.GetPortalByJID(group.String())
	//if p != nil {
	//	p.UpdateName(newName, "", false)
	// 		       get more info abt actual user TODO
	//}
	//bugs atm with above?
	user.HandleChatList()

}

func (user *User) HandleGroupTopic(_ groupme.ID, _ string) {
	user.HandleChatList()
}
func (user *User) HandleGroupMembership(_ groupme.ID, _ string) {
	user.HandleChatList()
	//TODO
}

func (user *User) HandleGroupAvatar(_ groupme.ID, _ string) {
	user.HandleChatList()
}

func (user *User) HandleLikeIcon(_ groupme.ID, _, _ int, _ string) {
	//TODO
}

func (user *User) HandleNewNickname(groupID, userID groupme.ID, name string) {
	puppet := user.bridge.GetPuppetByGMID(userID)
	if puppet != nil {
		puppet.UpdateName(groupme.Member{
			Nickname: name,
			UserID:   userID,
		}, false)
	}
}

func (user *User) HandleNewAvatarInGroup(groupID, userID groupme.ID, url string) {
	puppet := user.bridge.GetPuppetByGMID(userID)
	puppet.UpdateAvatar(user, false)
}

func (user *User) HandleMembers(_ groupme.ID, _ []groupme.Member, _ bool) {
	user.HandleChatList()
}

type FakeMessage struct {
	Text  string
	ID    string
	Alert bool
}
