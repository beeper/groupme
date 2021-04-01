: n!       "  let g:stlmsg="" aml.vA       "   let g:stlmsg="explore_bufnr!=".bufnr("%")           1       "  if !exists("w:netrw_explore_bufnr")  ��      p!Q�U  ��`Q�U                  portal
}

func (bridge *Bridge) GetAllPortals() []*Portal {
	return bridge.dbPortalsToPortals(bridge.DB.Portal.GetAll())
}

func (bridge *Bridge) GetAllPortalsByJID(jid types.GroupMeID) []*Portal {
	return bridge.dbPortalsToPortals(bridge.DB.Portal.GetAllByJID(jid))
}

func (bridge *Bridge) dbPortalsToPortals(dbPortals []*database.Portal) []*Portal {
	bridge.portalsLock.Lock()
	defer bridge.portalsLock.Unlock()
	output := make([]*Portal, len(dbPortals))
	for index, dbPortal := range dbPortals {
		if dbPortal == nil {
			continue
		}
		portal, ok := bridge.portalsByJID[dbPortal.Key]
		if !ok {
			portal = bridge.loadDBPortal(dbPortal, nil)
		}
		output[index] = portal
	}
	return output
}

func (bridge *Bridge) loadDBPortal(dbPortal *database.Portal, key *database.PortalKey) *Portal {
	if dbPortal == nil {
		if key == nil {
			return nil
		}
		dbPortal = bridge.DB.Portal.New()
		dbPortal.Key = *key
		dbPortal.Insert()
	}
	portal := bridge.NewPortal(dbPortal)
	bridge.portalsByJID[portal.Key] = portal
	if len(portal.MXID) > 0 {
		bridge.portalsByMXID[portal.MXID] = portal
	}
	return portal
}

func (portal *Portal) GetUsers() []*User {
	return nil
}

func (bridge *Bridge) NewManualPortal(key database.PortalKey) *Portal {
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

func (bridge *Bridge) NewPortal(dbPortal *database.Portal) *Portal {
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
	chat      string
	group     bool
	source    *User
	data      *groupme.Message
	timestamp uint64
}

type Portal struct {
	*database.Portal

	bridge *Bridge
	log    log.Logger

	roomCreateLock sync.Mutex

	recentlyHandled      []string
	recentlyHandledLock  sync.Mutex
	recentlyHandledIndex uint8

	backfillLock  sync.Mutex
	backfilling   bool
	lastMessageTs uint64

	privateChatBackfillInvitePuppet func()

	messages chan PortalMessage

	isPrivate   *bool
	hasRelaybot *bool
}

const MaxMessageAgeToCreatePortal = 5 * 60 // 5 minutes

func (portal *Portal) handleMessageLoop() {
	for msg := range portal.messages {
		if len(portal.MXID) == 0 {
			if msg.timestamp+MaxMessageAgeToCreatePortal < uint64(time.Now().Unix()) {
				portal.log.Debugln("Not creating portal room for incoming message as the message is too old.")
				continue
			}
			portal.log.Debugln("Creating Matrix room from incoming message")
			err := portal.CreateMatrixRoom(msg.source)
			if err != nil {
				portal.log.Errorln("Failed to create portal room:", err)
				return
			}
		}
		portal.backfillLock.Lock()
		portal.handleMessage(msg)
		portal.backfillLock.Unlock()
	}
}

func (portal *Portal) handleMessage(msg PortalMessage) {
	if len(portal.MXID) == 0 {
		portal.log.Warnln("handleMessage called even though portal.MXID is empty")
		return
	}
	portal.HandleTextMessage(msg.source, msg.data)
	portal.handleReaction(msg.data.ID.String(), msg.data.FavoritedBy)
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
	msg := portal.bridge.DB.Message.GetByJID(portal.Key, id.String())
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
	msg.JID = message.ID.String()
	msg.MXID = mxid
	msg.Timestamp = uint64(message.CreatedAt.ToTime().Unix())
	if message.UserID.String() == source.JID {
		msg.Sender = source.JID
	} else if portal.IsPrivateChat() {
		msg.Sender = portal.Key.JID
	} else {
		msg.Sender = message.ID.String()
		if len(msg.Sender) == 0 {
			println("AAAAAAAAAAAAAAAAAAAAAAAAAAIDK")
			msg.Sender = message.SenderID.String()
		}
	}
	msg.Content = &groupmeExt.Message{Message: *message}
	msg.Insert()

	portal.recentlyHandledLock.Lock()
	portal.recentlyHandled[0] = "" //FIFO queue being implemented here //TODO: is this efficent
	portal.recentlyHandled = portal.recentlyHandled[1:]
	portal.recentlyHandled = append(portal.recentlyHandled, message.ID.String())
	portal.recentlyHandledLock.Unlock()
}

func (portal *Portal) getMessageIntent(user *User, info *groupme.Message) *appservice.IntentAPI {
	if info.UserID.String() == user.GetJID() { //from me
		return portal.bridge.GetPuppetByJID(user.JID).IntentFor(portal) //TODO why is this
	} else if portal.IsPrivateChat() {
		return portal.MainIntent()
	} else if len(info.UserID.String()) == 0 {
		//	if len(info.Source.GetParticipant()) != 0 {
		//		info.SenderJid = info.Source.GetParticipant()
		//	} else {
		//		return nil
		//	}
		println("TODO weird uid stuff")
	}
	return portal.bridge.GetPuppetByJID(info.UserID.String()).IntentFor(portal)
}

func (portal *Portal) getReactionIntent(jid types.GroupMeID) *appservice.IntentAPI {
	return portal.bridge.GetPuppetByJID(jid).IntentFor(portal)

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
			portal.log.Debugfln("Not handling %s: sender is not known")
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
	participantMap := make(map[string]bool)
	for _, participant := range metadata.Members {
		participantMap[participant.UserID.String()] = true
		user := portal.bridge.GetUserByJID(participant.UserID.String())
		portal.userMXIDAction(user, portal.ensureMXIDInvited)

		puppet := portal.bridge.GetPuppetByJID(participant.UserID.String())
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
		go puppet.Sync(nil, *participant) //why nil whynot
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
		portal.AvatarURL = types.ContentURI{}
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

	portal.AvatarURL = types.ContentURI{resp.ContentURI}
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

func (portal *Portal) UpdateName(name string, setBy types.GroupMeID, updateInfo bool) bool {
	if portal.Name != name {
		intent := portal.MainIntent()
		if len(setBy) > 0 {
			intent = portal.bridge.GetPuppetByJID(setBy).IntentFor(portal)
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

func (portal *Portal) UpdateTopic(topic string, setBy types.GroupMeID, updateInfo bool) bool {
	if portal.Topic != topic {
		intent := portal.MainIntent()
		if len(setBy) > 0 {
			intent = portal.bridge.GetPuppetByJID(setBy).IntentFor(portal)
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
	group, err := user.Client.ShowGroup(context.TODO(), groupme.ID(strings.Replace(portal.Key.JID, groupmeExt.NewUserSuffix, "", 1)))
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

	if user == portal.bridge.Relaybot {
		for _, mxid := range portal.bridge.Config.Bridge.Relaybot.InviteUsers {
			fn(mxid)
		}
	} else {
		fn(user.MXID)
	}
}

func (portal *Portal) ensureMXIDInvited(mxid id.UserID) {
	err := portal.MainIntent().EnsureInvited(portal.MXID, mxid)
	if err != nil {
		portal.log.Warnfln("Failed to ensure %s is invited to %s: %v", mxid, portal.MXID, err)
	}
}

func (portal *Portal) ensureUserInvited(user *User) {
	portal.userMXIDAction(user, portal.ensureMXIDInvited)

	customPuppet := portal.bridge.GetPuppetByCustomMXID(user.MXID)
	if customPuppet != nil && customPuppet.CustomIntent() != nil {
		_ = customPuppet.CustomIntent().EnsureJoined(portal.MXID)
	}
}

func (portal *Portal) Sync(user *User, group groupme.Group) {
	portal.log.Infoln("Syncing portal for", user.MXID)

	if user.IsRelaybot {
		yes := true
		portal.hasRelaybot = &yes
	}

	err := user.Conn.SubscribeToGroup(context.TODO(), group.ID, user.Token)
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
		portal.Update()
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

func (portal *Portal) ChangeAdminStatus(jids []string, setAdmin bool) {
	levels, err := portal.MainIntent().PowerLevels(portal.MXID)
	if err != nil {
		levels = portal.GetBasePowerLevels()
	}
	newLevel := 0
	if setAdmin {
		newLevel = 50
	}
	changed := false
	for _, jid := range jids {
		puppet := portal.bridge.GetPuppetByJID(jid)
		changed = levels.EnsureUserLevel(puppet.MXID, newLevel) || changed

		user := portal.bridge.GetUserByJID(jid)
		if user != nil {
			changed = levels.EnsureUserLevel(user.MXID, newLevel) || changed
		}
	}
	if changed {
		_, err = portal.MainIntent().SetPowerLevels(portal.MXID, levels)
		if err != nil {
			portal.log.Errorln("Failed to change power levels:", err)
		}
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

func (portal *Portal) BackfillHistory(user *User, lastMessageTime uint64) error {
	if !portal.bridge.Config.Bridge.RecoverHistory {
		return nil
	}

	endBackfill := portal.beginBackfill()
	defer endBackfill()

	lastMessage := portal.bridge.DB.Message.GetLastInChat(portal.Key)
	if lastMessage == nil {
		return nil
	}
	if lastMessage.Timestamp >= lastMessageTime {
		portal.log.Debugln("Not backfilling: no new messages")
		return nil
	}

	lastMessageID := lastMessage.JID
	lastMessageFromMe := lastMessage.Sender == user.JID
	portal.log.Infoln("Backfilling history since", lastMessageID, "for", user.MXID)
	for len(lastMessageID) > 0 {
		portal.log.Debugln("Fetching 50 messages of history after", lastMessageID)
		messages, err := user.Client.LoadMessagesAfter(portal.Key.JID, lastMessageID, lastMessageFromMe, 50)
		if err != nil {
			return err
		}
		//	messages, ok := resp.Content.([]interface{})
		if len(messages) == 0 {
			portal.log.Debugfln("Didn't get more messages to backfill (resp.Content is %T)", messages)
			break
		}

		portal.handleHistory(user, messages)

		lastMessageProto := messages[len(messages)-1]
		lastMessageID = lastMessageProto.ID.String()
		lastMessageFromMe = lastMessageProto.UserID.String() == user.JID
	}
	portal.log.Infoln("Backfilling finished")
	return nil
}

func (portal *Portal) beginBackfill() func() {
	portal.backfillLock.Lock()
	portal.backfilling = true
	var privateChatPuppetInvited bool
	var privateChatPuppet *Puppet
	if portal.IsPrivateChat() && portal.bridge.Config.Bridge.InviteOwnPuppetForBackfilling && portal.Key.JID != portal.Key.Receiver {
		privateChatPuppet = portal.bridge.GetPuppetByJID(portal.Key.Receiver)
		portal.privateChatBackfillInvitePuppet = func() {
			if privateChatPuppetInvited {
				return
			}
			privateChatPuppetInvited = true
			_, _ = portal.MainIntent().InviteUser(portal.MXID, &mautrix.ReqInviteUser{UserID: privateChatPuppet.MXID})
			_ = privateChatPuppet.DefaultIntent().EnsureJoined(portal.MXID)
		}
	}
	return func() {
		portal.backfilling = false
		portal.privateChatBackfillInvitePuppet = nil
		portal.backfillLock.Unlock()
		if privateChatPuppet != nil && privateChatPuppetInvited {
			_, _ = privateChatPuppet.DefaultIntent().LeaveRoom(portal.MXID)
		}
	}
}

func (portal *Portal) disableNotifications(user *User) {
	if !portal.bridge.Config.Bridge.HistoryDisableNotifs {
		return
	}
	puppet := portal.bridge.GetPuppetByCustomMXID(user.MXID)
	if puppet == nil || puppet.customIntent == nil {
		return
	}
	portal.log.Debugfln("Disabling notifications for %s for backfilling", user.MXID)
	ruleID := fmt.Sprintf("net.maunium.silence_while_backfilling.%s", portal.MXID)
	err := puppet.customIntent.PutPushRule("global", pushrules.OverrideRule, ruleID, &mautrix.ReqPutPushRule{
		Actions: []pushrules.PushActionType{pushrules.ActionDontNotify},
		Conditions: []pushrules.PushCondition{{
			Kind:    pushrules.KindEventMatch,
			Key:     "room_id",
			Pattern: string(portal.MXID),
		}},
	})
	if err != nil {
		portal.log.Warnfln("Failed to disable notifications for %s while backfilling: %v", user.MXID, err)
	}
}

func (portal *Portal) enableNotifications(user *User) {
	if !portal.bridge.Config.Bridge.HistoryDisableNotifs {
		return
	}
	puppet := portal.bridge.GetPuppetByCustomMXID(user.MXID)
	if puppet == nil || puppet.customIntent == nil {
		return
	}
	portal.log.Debugfln("Re-enabling notifications for %s after backfilling", user.MXID)
	ruleID := fmt.Sprintf("net.maunium.silence_while_backfilling.%s", portal.MXID)
	err := puppet.customIntent.DeletePushRule("global", pushrules.OverrideRule, ruleID)
	if err != nil {
		portal.log.Warnfln("Failed to re-enable notifications for %s after backfilling: %v", user.MXID, err)
	}
}

func (portal *Portal) FillInitialHistory(user *User) error {
	if portal.bridge.Config.Bridge.InitialHistoryFill == 0 {
		return nil
	}
	endBackfill := portal.beginBackfill()
	defer endBackfill()
	if portal.privateChatBackfillInvitePuppet != nil {
		portal.privateChatBackfillInvitePuppet()
	}

	n := portal.bridge.Config.Bridge.InitialHistoryFill
	portal.log.Infoln("Filling initial history, maximum", n, "messages")
	var messages []*groupme.Message
	before := ""
	chunkNum := 1
	for n > 0 {
		count := 50
		if n < count {
			count = n
		}
		portal.log.Debugfln("Fetching chunk %d (%d messages / %d cap) before message %s", chunkNum, count, n, before)
		chunk, err := user.Client.LoadMessagesBefore(portal.Key.JID, before, count)
		if err != nil {
			return err
		}
		if len(chunk) == 0 {
			portal.log.Infoln("Chunk empty, starting handling of loaded messages")
			break
		}

		//reverses chunk to ascending order (oldest first)
		i := 0
		j := len(chunk) - 1
		for i < j {
			chunk[i], chunk[j] = chunk[j], chunk[i]
			i++
			j--
		}

		messages = append(chunk, messages...)

		portal.log.Debugfln("Fetched chunk and received %d messages", len(chunk))

		n -= len(chunk)
		before = chunk[0].ID.String()
		if len(before) == 0 {
			portal.log.Infoln("No message ID for first message, starting handling of loaded messages")
			break
		}
	}
	portal.disableNotifications(user)
	portal.handleHistory(user, messages)
	portal.enableNotifications(user)
	portal.log.Infoln("Initial history fill complete")
	return nil
}

func (portal *Portal) handleHistory(user *User, messages []*groupme.Message) {
	portal.log.Infoln("Handling", len(messages), "messages of history")
	for _, message := range messages {
		//	data, ok := rawMessage.(*groupme.Message)
		//	if !ok {
		//		portal.log.Warnln("Unexpected non-WebMessageInfo item in history response:", rawMessage)
		//		continue
		//	}
		//	data := whatsapp.ParseProtoMessage(message)
		//	if data == nil || data == whatsapp.ErrMessageTypeNotImplemented {
		//		st := message.GetMessageStubType()
		//		// Ignore some types that are known to fail
		//		if st == waProto.WebMessageInfo_CALL_MISSED_VOICE || st == waProto.WebMessageInfo_CALL_MISSED_VIDEO ||
		//			st == waProto.WebMessageInfo_CALL_MISSED_GROUP_VOICE || st == waProto.WebMessageInfo_CALL_MISSED_GROUP_VIDEO {
		//			continue
		//		}
		//		portal.log.Warnln("Message", message.GetKey().GetId(), "failed to parse during backfilling")
		//		continue
		//	}
		if portal.privateChatBackfillInvitePuppet != nil && message.UserID.String() == user.JID && portal.IsPrivateChat() {
			portal.privateChatBackfillInvitePuppet()
		}
		portal.handleMessage(PortalMessage{portal.Key.JID, portal.Key.JID == portal.Key.Receiver, user, message, uint64(message.CreatedAt.ToTime().Unix())})
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
			ID:          portal.Key.JID,
			DisplayName: portal.Name,
			AvatarURL:   portal.AvatarURL.CUString(),
		},
	}
	bridgeInfoStateKey := fmt.Sprintf("net.maunium.whatsapp://whatsapp/%s", portal.Key.JID)
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
		puppet := portal.bridge.GetPuppetByJID(portal.Key.JID)
		if portal.bridge.Config.Bridge.PrivateChatPortalMeta {
			portal.Name = puppet.Displayname
			portal.AvatarURL = puppet.AvatarURL
			portal.Avatar = puppet.Avatar
		} else {
			portal.Name = ""
		}
		portal.Topic = "WhatsApp private chat"
		//	 } else if portal.IsStatusBroadcastRoom() {
		//	 	portal.Name = "WhatsApp Status Broadcast"
		//	 	portal.Topic = "WhatsApp status updates from your contacts"
	} else {
		var err error
		metadata, err = user.Client.ShowGroup(context.TODO(), groupme.ID(portal.Key.JID))
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
				Parsed: event.RoomAvatarEventContent{URL: portal.AvatarURL.ContentURI},
			},
		})
	}

	invite := []id.UserID{user.MXID}
	if user.IsRelaybot {
		invite = portal.bridge.Config.Bridge.Relaybot.InviteUsers
	}

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
	}
	portal.MXID = resp.RoomID
	portal.Update()
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
	user.addPortalToCommunity(portal)
	if portal.IsPrivateChat() {
		puppet := user.bridge.GetPuppetByJID(portal.Key.JID)
		user.addPuppetToCommunity(puppet)

		if portal.bridge.Config.Bridge.Encryption.Default {
			err = portal.bridge.Bot.EnsureJoined(portal.MXID)
			if err != nil {
				portal.log.Errorln("Failed to join created portal with bridge bot for e2be:", err)
			}
		}

		user.UpdateDirectChats(map[id.UserID][]id.RoomID{puppet.MXID: {portal.MXID}})
	}
	err = portal.FillInitialHistory(user)
	if err != nil {
		portal.log.Errorln("Failed to fill history:", err)
	}
	return nil
}

func (portal *Portal) IsPrivateChat() bool {
	if portal.isPrivate == nil {
		val := strings.HasSuffix(portal.Key.JID, whatsappExt.NewUserSuffix)
		portal.isPrivate = &val
	}
	return *portal.isPrivate
}

func (portal *Portal) HasRelaybot() bool {
	if portal.bridge.Relaybot == nil {
		return false
	} else if portal.hasRelaybot == nil {
		val := portal.bridge.Relaybot.IsInPortal(portal.Key)
		portal.hasRelaybot = &val
	}
	return *portal.hasRelaybot
}

func (portal *Portal) IsStatusBroadcastRoom() bool {
	return portal.Key.JID == "status@broadcast"
}

func (portal *Portal) MainIntent() *appservice.IntentAPI {
	if portal.IsPrivateChat() {
		return portal.bridge.GetPuppetByJID(portal.Key.JID).DefaultIntent()
	}
	return portal.bridge.Bot
}

func (portal *Portal) SetReply(content *event.MessageEventContent, info whatsapp.ContextInfo) {
	if len(info.QuotedMessageID) == 0 {
		return
	}
	message := portal.bridge.DB.Message.GetByJID(portal.Key, info.QuotedMessageID)
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

func (portal *Portal) HandleMessageRevoke(user *User, message whatsappExt.MessageRevocation) {
	msg := portal.bridge.DB.Message.GetByJID(portal.Key, message.Id)
	if msg == nil {
		return
	}
	var intent *appservice.IntentAPI
	if message.FromMe {
		if portal.IsPrivateChat() {
			intent = portal.bridge.GetPuppetByJID(user.JID).CustomIntent()
		} else {
			intent = portal.bridge.GetPuppetByJID(user.JID).IntentFor(portal)
		}
	} else if len(message.Participant) > 0 {
		intent = portal.bridge.GetPuppetByJID(message.Participant).IntentFor(portal)
	}
	if intent == nil {
		intent = portal.MainIntent()
	}
	_, err := intent.RedactEvent(portal.MXID, msg.MXID)
	if err != nil {
		portal.log.Errorln("Failed to redact %s: %v", msg.JID, err)
		return
	}
	msg.Delete()
}

//func (portal *Portal) HandleFakeMessage(_ *User, message FakeMessage) {
//	if portal.isRecentlyHandled(message.ID) {
//		return
//	}
//
//	content := event.MessageEventContent{
//		MsgType: event.MsgNotice,
//		Body:    message.Text,
//	}
//	if message.Alert {
//		content.MsgType = event.MsgText
//	}
//	_, err := portal.sendMainIntentMessage(content)
//	if err != nil {
//		portal.log.Errorfln("Failed to handle fake message %s: %v", message.ID, err)
//		return
//	}
//
//	portal.recentlyHandledLock.Lock()
//	index := portal.recentlyHandledIndex
//	portal.recentlyHandledIndex = (portal.recentlyHandledIndex + 1) % recentlyHandledLength
//	portal.recentlyHandledLock.Unlock()
//	portal.recentlyHandled[index] = message.ID
//}

func (portal *Portal) sendMainIntentMessage(content interface{}) (*mautrix.RespSendEvent, error) {
	return portal.sendMessage(portal.MainIntent(), event.EventMessage, content, 0)
}

const MessageSendRetries = 5
const MediaUploadRetries = 5
const BadGatewaySleep = 5 * time.Second

func (portal *Portal) sendMessage(intent *appservice.IntentAPI, eventType event.Type, content interface{}, timestamp int64) (*mautrix.RespSendEvent, error) {
	return portal.sendMessageWithRetry(intent, eventType, content, timestamp, MessageSendRetries)
}
func (portal *Portal) sendReaction(intent *appservice.IntentAPI, eventID id.EventID, reaction string) (*mautrix.RespSendEvent, error) {
	return portal.sendMessage(intent, event.EventReaction, &event.ReactionEventContent{
		RelatesTo: event.RelatesTo{
			EventID: eventID,
			Type:    event.RelAnnotation,
			Key:     reaction,
		},
	}, time.Now().Unix())
}

func isGatewayError(err error) bool {
	if err == nil {
		return false
	}
	var httpErr mautrix.HTTPError
	return errors.As(err, &httpErr) && (httpErr.IsStatus(http.StatusBadGateway) || httpErr.IsStatus(http.StatusGatewayTimeout))
}

func (portal *Portal) sendMessageWithRetry(intent *appservice.IntentAPI, eventType event.Type, content interface{}, timestamp int64, retries int) (*mautrix.RespSendEvent, error) {
	for ; ; retries-- {
		resp, err := portal.sendMessageDirect(intent, eventType, content, timestamp)
		if retries > 0 && isGatewayError(err) {
			portal.log.Warnfln("Got gateway error trying to send message, retrying in %d seconds", int(BadGatewaySleep.Seconds()))
			time.Sleep(BadGatewaySleep)

		} else {
			return resp, err
		}
	}
}

func (portal *Portal) sendMessageDirect(intent *appservice.IntentAPI, eventType event.Type, content interface{}, timestamp int64) (*mautrix.RespSendEvent, error) {
	wrappedContent := event.Content{Parsed: content}
	if timestamp != 0 && intent.IsCustomPuppet {
		wrappedContent.Raw = map[string]interface{}{
			"net.maunium.whatsapp.puppet": intent.IsCustomPuppet,
		}
	}
	if portal.Encrypted && portal.bridge.Crypto != nil {
		encrypted, err := portal.bridge.Crypto.Encrypt(portal.MXID, eventType, wrappedContent)
		if err != nil {
			return nil, fmt.Errorf("failed to encrypt event: %w", err)
		}
		eventType = event.EventEncrypted
		wrappedContent.Parsed = encrypted
	}
	if timestamp == 0 {
		return intent.SendMessageEvent(portal.MXID, eventType, &wrappedContent)
	} else {
		return intent.SendMassagedMessageEvent(portal.MXID, eventType, &wrappedContent, timestamp*1000) //milliseconds
	}
}

func (portal *Portal) handleAttachment(intent *appservice.IntentAPI, attachment *groupme.Attachment, source *User, message *groupme.Message) (msg *event.MessageEventContent, sendText bool, err error) {
	sendText = true
	switch attachment.Type {
	case "image":
		imgData, mime, err := groupmeExt.DownloadImage(attachment.URL)
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
		} //TODO abstract groupme url parsing in groupmeExt
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
		vidContents, mime := groupmeExt.DownloadVideo(attachment.VideoPreviewURL, attachment.URL, source.Token)
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
		fileData, fname, fmime := groupmeExt.DownloadFile(portal.Key.JID, attachment.FileID, source.Token)
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
	default:
		portal.log.Warnln("Unable to handle groupme attachment type", attachment.Type)
		return nil, true, fmt.Errorf("Unable to handle groupme attachment type %s", attachment.Type)
	}
	return nil, true, errors.New("Unknown type")
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
		resp, err := portal.sendMessage(intent, event.EventMessage, msg, message.CreatedAt.ToTime().Unix())
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
		resp, err := portal.sendMessage(intent, event.EventMessage, content, message.CreatedAt.ToTime().Unix())
		if err != nil {
			portal.log.Errorfln("Failed to handle message %s: %v", message.ID, err)
			return
		}
		sentID = resp.EventID

	}
	portal.finishHandling(source, message, sentID)
}

func (portal *Portal) handleReaction(msgID types.GroupMeID, ppl []types.GroupMeID) {
	reactions := portal.bridge.DB.Reaction.GetBy               A 