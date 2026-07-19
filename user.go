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
	extraContent := make(map[string]interface{})
	if isDirect {
		extraContent["is_direct"] = true
	}
	customPuppet := user.bridge.GetPuppetByCustomMXID(user.MXID)
	if customPuppet != nil && customPuppet.CustomIntent() != nil {
		extraContent["fi.mau.will_auto_accept"] = true
	}
	_, err := intent.InviteUser(roomID, &mautrix.ReqInviteUser{UserID: user.MXID}, extraContent)
	var httpErr mautrix.HTTPError
	if err != nil && errors.As(err, &httpErr) && httpErr.RespError != nil && strings.Contains(httpErr.RespError.Err, "is already in the room") {
		user.bridge.StateStore.SetMembership(roomID, user.MXID, event.MembershipJoin)
		ok = true
		return
	} else if err != nil {
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

func (user *User) SyncChats() {
	if user.Client == nil {
		return
	}
	user.log.Infoln("Syncing chats and creating portals")

	// Fetch groups
	groups, err := user.Client.IndexAllGroups()
	if err != nil {
		user.log.Errorln("Failed to fetch groups:", err)
	} else {
		for _, group := range groups {
			portal := user.bridge.GetPortalByGMID(database.GroupPortalKey(group.ID))
			portal.Sync(user, group)
		}
	}

	// Fetch DMs
	dms, err := user.Client.IndexAllChats()
	if err != nil {
		user.log.Errorln("Failed to fetch DMs:", err)
	} else {
		for _, dm := range dms {
			portal := user.bridge.GetPortalByGMID(database.NewPortalKey(dm.OtherUser.ID, user.GMID))
			portal.Sync(user, nil)
			puppet := user.bridge.GetPuppetByGMID(dm.OtherUser.ID)
			puppet.Sync(user, &groupme.Member{
				Nickname: dm.OtherUser.Name,
				UserID:   dm.OtherUser.ID,
			}, false, false)
		}
	}
	user.log.Infoln("Finished syncing chats")
}

func (user *User) Connect() bool {
	if user.Conn != nil {
		return true
	} else if len(user.Token) == 0 {
		return false
	}

	user.log.Debugfln("Connecting to GroupMe")
	if user.Client == nil {
		user.Client = groupmeext.NewClient(user.Token)
	}
	if len(user.GMID) == 0 {
		myuser, err := user.Client.MyUser(context.TODO())
		if err != nil {
			user.log.Errorln("Failed to get own GroupMe ID:", err)
			return false
		}
		user.GMID = myuser.ID
		user.Update()
		user.addToGMIDMap()
	}

	user.tryAutomaticDoublePuppeting()

	// Launch REST-based chat sync immediately in the background so it's not blocked by Faye
	go user.SyncChats()

	// Launch the REST polling fallback, used while the push connection is down
	go user.PollLoop()

	timeout := time.Duration(user.bridge.Config.GroupMe.ConnectionTimeout)
	if timeout == 0 {
		timeout = 20
	}
	conn := groupme.NewPushSubscription(context.Background())
	user.Conn = &conn
	user.Conn.StartListening(context.Background(), groupmeext.NewFayeClient(user.log), user.Token)
	user.Conn.AddFullHandler(user)

	//TODO: typing notification?
	return user.RestoreSession()
}

// PollLoop polls the REST API for new messages as a fallback for the Faye
// push connection. While the push connection is healthy it only runs an
// occasional safety-net pass; when the connection drops it polls every tick
// until the connection recovers. Messages already bridged via push are
// deduplicated by Portal.startHandling.
func (user *User) PollLoop() {
	const pollInterval = 10 * time.Second
	// While connected, only poll every Nth tick as a safety net.
	const connectedPollDivisor = 6

	user.log.Infoln("Starting GroupMe message polling fallback loop")
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	// Map to track the last processed message ID for each portal (key: portal.Key.String())
	lastMessageIDs := make(map[string]groupme.ID)

	tick := 0
	for range ticker.C {
		if user.Client == nil {
			return
		}
		tick++
		if user.Conn != nil && user.Conn.Connected() && tick%connectedPollDivisor != 0 {
			continue
		}

		user.bridge.portalsLock.Lock()
		var activePortals []*Portal
		for _, portal := range user.bridge.portalsByGMID {
			if len(portal.MXID) > 0 {
				activePortals = append(activePortals, portal)
			}
		}
		user.bridge.portalsLock.Unlock()

		for _, portal := range activePortals {
			if portal.IsPrivateChat() && portal.Key.Receiver != user.GMID {
				continue
			}

			var messages []*groupme.Message
			var err error

			if portal.IsPrivateChat() {
				var resp groupme.IndexDirectMessagesResponse
				resp, err = user.Client.IndexDirectMessages(context.TODO(), portal.Key.GMID.String(), &groupme.IndexDirectMessagesQuery{})
				messages = resp.Messages
			} else {
				var resp groupme.IndexMessagesResponse
				resp, err = user.Client.IndexMessages(context.TODO(), portal.Key.GMID, &groupme.IndexMessagesQuery{Limit: 10})
				messages = resp.Messages
			}

			if err != nil {
				continue
			}

			portalKeyStr := portal.Key.String()

			lastID, ok := lastMessageIDs[portalKeyStr]
			if !ok {
				// Initialize last processed message ID with the latest message in this fetch
				if len(messages) > 0 {
					lastMessageIDs[portalKeyStr] = messages[0].ID
				} else {
					lastMessageIDs[portalKeyStr] = ""
				}
				continue
			}

			// Find if the last processed message is in the current fetch
			foundLast := false
			var newMessages []*groupme.Message
			for _, msg := range messages {
				if msg.ID == lastID {
					foundLast = true
					break
				}
				newMessages = append(newMessages, msg)
			}

			// Process new messages in chronological order (oldest first)
			if foundLast {
				for i := len(newMessages) - 1; i >= 0; i-- {
					msg := newMessages[i]
					if portal.isRecentlyHandled(msg.ID) {
						continue
					}
					portal.log.Debugfln("Polled new message %s from GroupMe, forwarding to Matrix", msg.ID)
					portal.HandleTextMessage(user, msg)
				}
			} else {
				// Fallback if the last message isn't found (e.g. fresh portal, or gap of >10 messages)
				for i := len(messages) - 1; i >= 0; i-- {
					msg := messages[i]
					if portal.isRecentlyHandled(msg.ID) {
						continue
					}
					portal.log.Debugfln("Polled new message %s (gap fallback) from GroupMe, forwarding to Matrix", msg.ID)
					portal.HandleTextMessage(user, msg)
				}
			}

			// Update the last message ID to the most recent one
			if len(messages) > 0 {
				lastMessageIDs[portalKeyStr] = messages[0].ID
			}
		}
	}
}

func (user *User) RestoreSession() bool {
	if len(user.Token) > 0 {
		err := user.Conn.SubscribeToUser(context.TODO(), groupme.ID(user.GMID), user.Token)
		if err != nil {
			user.log.Errorln("Failed to subscribe to user channel:", err)
		}
		user.ConnectionErrors = 0
		user.log.Debugln("Session restored successfully")
		user.PostLogin()
		return true
	} else {
		user.log.Debugln("tried login but no token")
		return false
	}
}

func (user *User) HasSession() bool {
	return len(user.Token) > 0
}

func (user *User) IsConnected() bool {
	// TODO: better connection check
	return user.Conn != nil
}

func (user *User) IsLoggedIn() bool {
	return true
}

func (user *User) IsLoginInProgress() bool {
	// return user.Conn != nil && user.Conn.IsLoginInProgress()
	return false
}

func (user *User) GetGMID() groupme.ID {
	if len(user.GMID) == 0 {
		u, err := user.Client.MyUser(context.TODO())
		if err != nil {
			user.log.Errorln("Failed to get own GroupMe ID:", err)
			return ""
		}
		user.GMID = u.ID
	}
	return user.GMID
}

func (user *User) Login(token string) error {
	user.Token = token

	if user.Client == nil {
		user.Client = groupmeext.NewClient(user.Token)
	}
	if len(user.GMID) == 0 {
		myuser, err := user.Client.MyUser(context.TODO())
		if err != nil {
			user.log.Errorln("Failed to get own GroupMe ID:", err)
			return err
		}
		user.GMID = myuser.ID
		user.Update()
		user.addToGMIDMap()
	}

	user.PostLogin()
	if user.Connect() {
		return nil
	}
	return errors.New("failed to connect")
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
	// go user.intPostLogin()
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
	user.GroupList = chatMap

	dmMap := map[groupme.ID]groupme.Chat{}
	dms, err := user.Client.IndexAllChats()
	if err != nil {
		user.log.Errorln("chat sync error", err) //TODO: handle
		return
	}
	for _, dm := range dms {
		dmMap[dm.OtherUser.ID] = *dm
	}
	user.ChatList = dmMap

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
	user.RelationList = userMap

	user.log.Infoln("Chat list received")
	user.chatListReceived <- struct{}{}
	go user.syncPortals(false)
}

func (user *User) syncPortals(createAll bool) {
	//	user.log.Infoln("Reading chat list")

	//	chats := make(ChatList, 0, len(user.GroupList)+len(user.ChatList))
	//	portalKeys := make([]database.PortalKeyWithMeta, 0, cap(chats))

	//	for _, group := range user.GroupList {

	//		portal := user.bridge.GetPortalByJID(database.GroupPortalKey(group.ID.String()))

	//		chats = append(chats, Chat{
	//			Portal:          portal,
	//			LastMessageTime: uint64(group.UpdatedAt.ToTime().Unix()),
	//			Group:           &group,
	//		})
	//	}
	//	for _, dm := range user.ChatList {
	//		portal := user.bridge.GetPortalByJID(database.NewPortalKey(dm.OtherUser.ID.String(), user.JID))
	//		chats = append(chats, Chat{
	//			Portal:          portal,
	//			LastMessageTime: uint64(dm.UpdatedAt.ToTime().Unix()),
	//			DM:              &dm,
	//		})
	//	}

	//	for _, chat := range chats {
	//		var inCommunity, ok bool
	//		if inCommunity, ok = existingKeys[chat.Portal.Key]; !ok || !inCommunity {
	//			inCommunity = user.addPortalToCommunity(chat.Portal)
	//			if chat.Portal.IsPrivateChat() {
	//				puppet := user.bridge.GetPuppetByJID(chat.Portal.Key.GMID)
	//				user.addPuppetToCommunity(puppet)
	//			}
	//		}
	//		portalKeys = append(portalKeys, database.PortalKeyWithMeta{PortalKey: chat.Portal.Key, InCommunity: inCommunity})
	//	}
	//	user.log.Infoln("Read chat list, updating user-portal mapping")

	//	err := user.SetPortalKeys(portalKeys)
	//	if err != nil {
	//		user.log.Warnln("Failed to update user-portal mapping:", err)
	//	}
	//	sort.Sort(chats)
	//	limit := user.bridge.Config.Bridge.InitialChatSync
	//	if limit < 0 {
	//		limit = len(chats)
	//	}
	//	now := uint64(time.Now().Unix())
	//	user.log.Infoln("Syncing portals")

	//	wg := sync.WaitGroup{}
	//	for i, chat := range chats {
	//		if chat.LastMessageTime+user.bridge.Config.Bridge.SyncChatMaxAge < now {
	//			break
	//		}
	//		wg.Add(1)
	//		go func(chat Chat, i int) {
	//			create := (chat.LastMessageTime >= user.LastConnection && user.LastConnection > 0) || i < limit
	//			if len(chat.Portal.MXID) > 0 || create || createAll {
	//				chat.Portal.Sync(user, chat.Group)
	//				err := chat.Portal.BackfillHistory(user, chat.LastMessageTime)
	//				if err != nil {
	//					chat.Portal.log.Errorln("Error backfilling history:", err)
	//				}
	//			}

	//			wg.Done()
	//		}(chat, i)

	// }
	// wg.Wait()
	// //TODO: handle leave from groupme side
	// user.UpdateDirectChats(nil)
	// user.log.Infoln("Finished syncing portals")
	// select {
	// case user.syncPortalsDone <- struct{}{}:
	// default:
	// }
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
	user.log.Errorln("GroupMe connection error:", err)
}

// portalKeyFromMessage resolves the portal key for a pushed message, using
// the group ID or falling back to the DM conversation ID. For DMs the key is
// normalized so that Receiver is this user and GMID is the other user.
func (user *User) portalKeyFromMessage(message *groupme.Message) *database.PortalKey {
	key := database.ParsePortalKey(message.GroupID.String())
	if key == nil {
		key = database.ParsePortalKey(message.ConversationID.String())
	}
	return user.normalizePortalKey(key)
}

func (user *User) normalizePortalKey(key *database.PortalKey) *database.PortalKey {
	if key != nil && key.IsPrivate() && key.GMID == user.GMID {
		key.GMID, key.Receiver = key.Receiver, key.GMID
	}
	return key
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
	id := user.portalKeyFromMessage(&message)
	if id == nil {
		user.log.Errorln("Error parsing conversationid/portalkey", message.ConversationID.String(), "ignoring message")
		return
	}

	user.messageInput <- PortalMessage{*id, user, &message, uint64(message.CreatedAt.ToTime().Unix()), false}
}

func (user *User) HandleLike(msg groupme.Message) {
	id := user.portalKeyFromMessage(&msg)
	if id == nil {
		user.log.Errorln("Error parsing conversationid/portalkey for like", msg.ConversationID.String())
		return
	}

	user.messageInput <- PortalMessage{*id, user, &msg, uint64(msg.CreatedAt.ToTime().Unix()), true}
}

// HandleTyping bridges an incoming GroupMe typing indicator to Matrix.
// GroupMe repeats typing events every 5 seconds while the user keeps typing.
func (user *User) HandleTyping(chat groupme.ID, userID groupme.ID) {
	if userID == user.GMID {
		return
	}
	key := user.normalizePortalKey(database.ParsePortalKey(chat.String()))
	if key == nil {
		return
	}
	portal := user.bridge.GetPortalByGMID(*key)
	if portal == nil || len(portal.MXID) == 0 {
		return
	}
	puppet := user.bridge.GetPuppetByGMID(userID)
	if puppet == nil {
		return
	}
	_, err := puppet.IntentFor(portal).UserTyping(portal.MXID, true, 8*time.Second)
	if err != nil {
		user.log.Debugfln("Failed to bridge typing indicator from %s: %v", userID, err)
	}
}

// HandleMessageUpdate bridges a GroupMe message edit to Matrix.
func (user *User) HandleMessageUpdate(message groupme.Message) {
	key := user.portalKeyFromMessage(&message)
	if key == nil {
		return
	}
	portal := user.bridge.GetPortalByGMID(*key)
	if portal == nil || len(portal.MXID) == 0 {
		return
	}
	portal.HandleMessageEdit(user, &message)
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

func (user *User) HandleMessageDeleted(message groupme.Message) {
	key := user.portalKeyFromMessage(&message)
	if key == nil {
		return
	}
	portal := user.bridge.GetPortalByGMID(*key)
	if portal == nil || len(portal.MXID) == 0 {
		return
	}
	portal.HandleGroupMeDeletion(message.ID)
}
