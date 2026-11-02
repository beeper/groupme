package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"

	"github.com/beeper/groupme-lib"
	"github.com/beeper/groupme/pkg/groupmeext"
)

var (
	_ bridgev2.TypingHandlingNetworkAPI      = (*GMClient)(nil)
	_ bridgev2.ReadReceiptHandlingNetworkAPI = (*GMClient)(nil)
)

func (gc *GMClient) conversationID(portalID networkid.PortalID) (groupme.ID, error) {
	kind, chatID := ParsePortalID(portalID)
	if chatID == "" {
		return "", fmt.Errorf("invalid GroupMe conversation")
	}
	switch kind {
	case PortalTypeGroup:
		return chatID, nil
	case PortalTypeDM:
		return DMConversationID(groupme.ID(gc.Meta.GMID), chatID), nil
	default:
		return "", fmt.Errorf("invalid GroupMe conversation type")
	}
}

func (gc *GMClient) portalForConversation(conversation groupme.ID) networkid.PortalKey {
	if strings.Contains(string(conversation), "+") {
		return gc.portalKeyForMessage(&groupme.Message{ConversationID: conversation})
	}
	if conversation == "" {
		return networkid.PortalKey{}
	}
	return gc.portalKeyForGroup(conversation)
}

func (gc *GMClient) HandleMatrixTyping(ctx context.Context, msg *bridgev2.MatrixTyping) error {
	if !msg.IsTyping || msg.Type != bridgev2.TypingTypeText {
		return nil
	}
	conversation, err := gc.conversationID(msg.Portal.ID)
	if err != nil {
		return err
	}
	kind, _ := ParsePortalID(msg.Portal.ID)
	channel := "/group/" + string(conversation)
	if kind == PortalTypeDM {
		channel = "/direct_message/" + strings.ReplaceAll(string(conversation), "+", "_")
	}
	gc.pushMu.Lock()
	transport := gc.pushTransport
	gc.pushMu.Unlock()
	if transport == nil {
		return gc.wrapGroupMeError(errors.New("GroupMe push is disconnected"), "not connected to GroupMe").WithIsCertain(true)
	}
	if err = transport.SendTyping(ctx, channel, gc.Meta.Token, gc.Meta.GMID); err != nil {
		return gc.wrapGroupMeError(err, "failed to send typing notification")
	}
	return nil
}

func (gc *GMClient) HandleMatrixReadReceipt(ctx context.Context, msg *bridgev2.MatrixReadReceipt) error {
	target := msg.ExactMessage
	var err error
	if target == nil {
		target, err = gc.Main.br.DB.Message.GetLastNonFakePartAtOrBeforeTime(ctx, msg.Portal.PortalKey, msg.ReadUpTo)
		if err != nil {
			return err
		}
	}
	if target != nil && target.Room != msg.Portal.PortalKey {
		return fmt.Errorf("receipt target belongs to another conversation")
	}
	target, err = gc.nearestNativeMessage(ctx, msg.Portal.PortalKey, target, false)
	if err != nil || target == nil {
		return err
	}
	if target.Timestamp.Before(msg.LastRead) {
		return nil
	}
	conversation, err := gc.conversationID(msg.Portal.ID)
	if err != nil {
		return err
	}
	if err = groupmeext.MarkRead(ctx, gc.Meta.Token, conversation, ParseMessageID(target.ID)); err != nil {
		return gc.wrapGroupMeError(err, "failed to mark as read")
	}
	gc.recordReadCheckpoint(conversation, ParseMessageID(target.ID))
	// bridgev2 invokes this for public m.read only. Implicit receipts from
	// sending a message update our checkpoint without publishing a seen event.
	if kind, _ := ParsePortalID(msg.Portal.ID); kind == PortalTypeDM && !msg.Implicit {
		if err = groupmeext.SendDMReadReceipt(ctx, gc.Meta.Token, conversation); err != nil {
			return gc.wrapGroupMeError(err, "failed to send read receipt")
		}
	}
	return nil
}

func (gc *GMClient) readCheckpointEvent(receipt groupmeext.ReadCheckpoint) *nativeReceipt {
	gc.recordReadCheckpoint(receipt.ConversationID, receipt.LastReadMessageID)
	key := gc.portalForConversation(receipt.ConversationID)
	if key.ID == "" || receipt.LastReadMessageID == "" {
		return nil
	}
	return &nativeReceipt{client: gc, Receipt: &simplevent.Receipt{
		EventMeta: simplevent.EventMeta{Type: bridgev2.RemoteEventReadReceipt, PortalKey: key,
			Sender: bridgev2.EventSender{IsFromMe: true, Sender: MakeUserID(groupme.ID(gc.Meta.GMID))}},
		LastTarget: MakeMessageID(receipt.LastReadMessageID),
	}}
}

func (gc *GMClient) loadReadCheckpoints(ctx context.Context) {
	receipts, err := groupmeext.GetReadCheckpoints(ctx, gc.Meta.Token)
	if err != nil {
		gc.UserLogin.Log.Warn().Err(err).Msg("Failed to load GroupMe read checkpoints for backfill")
		return
	}
	for _, receipt := range receipts {
		gc.recordReadCheckpoint(receipt.ConversationID, receipt.LastReadMessageID)
	}
}

func (gc *GMClient) recordReadCheckpoint(conversation, lastRead groupme.ID) {
	if conversation == "" || lastRead == "" {
		return
	}
	gc.readCheckpointsMu.Lock()
	defer gc.readCheckpointsMu.Unlock()
	if gc.readCheckpoints == nil {
		gc.readCheckpoints = make(map[groupme.ID]groupme.ID)
	}
	if existing, ok := gc.readCheckpoints[conversation]; !ok || compareMessageIDs(lastRead, existing) > 0 {
		gc.readCheckpoints[conversation] = lastRead
	}
}

func (gc *GMClient) readCheckpointBehind(portalID networkid.PortalID, newest groupme.ID) *groupmeext.ReadCheckpoint {
	conversation, err := gc.conversationID(portalID)
	if err != nil {
		return nil
	}
	gc.readCheckpointsMu.Lock()
	lastRead, ok := gc.readCheckpoints[conversation]
	gc.readCheckpointsMu.Unlock()
	if !ok || compareMessageIDs(lastRead, newest) >= 0 {
		return nil
	}
	return &groupmeext.ReadCheckpoint{ConversationID: conversation, LastReadMessageID: lastRead}
}

func (gc *GMClient) syncReadCheckpoints(ctx context.Context) {
	receipts, err := groupmeext.GetReadCheckpoints(ctx, gc.Meta.Token)
	if err != nil {
		gc.UserLogin.Log.Warn().Err(err).Msg("Failed to sync GroupMe read checkpoints")
		return
	}
	for _, receipt := range receipts {
		if evt := gc.readCheckpointEvent(receipt); evt != nil {
			gc.Main.br.QueueRemoteEvent(gc.UserLogin, evt)
		}
	}
}

// These native events have no groupme-lib handler. Decode only their typed
// fields and keep routing, room creation, and Matrix delivery in bridgev2.
func (gc *GMClient) handleActivity(ctx context.Context, msg groupme.PushMessage) bool {
	events, handled := gc.parseActivity(msg)
	for _, evt := range events {
		gc.Main.br.QueueRemoteEvent(gc.UserLogin, evt)
	}
	return handled
}

func (gc *GMClient) parseActivity(msg groupme.PushMessage) ([]bridgev2.RemoteEvent, bool) {
	typ, _ := msg.Data()["type"].(string)
	if typ != "typing" && typ != "read_receipt.create" && typ != "read_receipt.sync" && typ != "chat.deleted" && typ != "membership.destroy" {
		return nil, false
	}
	raw, err := json.Marshal(msg.Data())
	if err != nil {
		return nil, true
	}
	var data struct {
		UserID  groupme.ID `json:"user_id"`
		Subject struct {
			ID        groupme.ID                  `json:"id"`
			ChatID    groupme.ID                  `json:"chat_id"`
			MessageID groupme.ID                  `json:"message_id"`
			UserID    groupme.ID                  `json:"user_id"`
			ReadAt    groupme.Timestamp           `json:"read_at"`
			Receipts  []groupmeext.ReadCheckpoint `json:"receipts"`
		} `json:"subject"`
	}
	if json.Unmarshal(raw, &data) != nil {
		return nil, true
	}
	var events []bridgev2.RemoteEvent
	switch typ {
	case "chat.deleted":
		if msg.Channel() == groupme.UserChannel(groupme.ID(gc.Meta.GMID)) {
			if evt := chatDeleteEvent(gc.portalKeyForMessage(&groupme.Message{ChatID: data.Subject.ChatID})); evt != nil {
				events = append(events, evt)
			}
		}
	case "membership.destroy":
		if msg.Channel() == groupme.UserChannel(groupme.ID(gc.Meta.GMID)) && data.Subject.ID != "" {
			events = append(events, chatDeleteEvent(gc.portalKeyForGroup(data.Subject.ID)))
		}
	case "typing":
		if data.UserID == "" || string(data.UserID) == gc.Meta.GMID {
			return nil, true
		}
		var key networkid.PortalKey
		if conversation, ok := strings.CutPrefix(msg.Channel(), "/direct_message/"); ok {
			key = gc.portalForConversation(groupme.ID(strings.ReplaceAll(conversation, "_", "+")))
			_, peer := ParsePortalID(key.ID)
			if peer != data.UserID {
				return nil, true
			}
		} else if group, ok := strings.CutPrefix(msg.Channel(), "/group/"); ok {
			key = gc.portalKeyForGroup(groupme.ID(group))
		}
		if key.ID == "" {
			return nil, true
		}
		events = append(events, &simplevent.Typing{
			EventMeta: simplevent.EventMeta{Type: bridgev2.RemoteEventTyping, PortalKey: key, Sender: bridgev2.EventSender{Sender: MakeUserID(data.UserID)}},
			Timeout:   5 * time.Second, Type: bridgev2.TypingTypeText,
		})
	case "read_receipt.create":
		subject := data.Subject
		key := gc.portalKeyForMessage(&groupme.Message{ChatID: subject.ChatID})
		_, peer := ParsePortalID(key.ID)
		if key.ID == "" || subject.MessageID == "" || (subject.UserID != peer && string(subject.UserID) != gc.Meta.GMID) {
			return nil, true
		}
		if string(subject.UserID) == gc.Meta.GMID {
			if conversation, err := gc.conversationID(key.ID); err == nil {
				gc.recordReadCheckpoint(conversation, subject.MessageID)
			}
		}
		events = append(events, &nativeReceipt{client: gc, Receipt: &simplevent.Receipt{
			EventMeta: simplevent.EventMeta{Type: bridgev2.RemoteEventReadReceipt, PortalKey: key,
				Sender: bridgev2.EventSender{IsFromMe: string(subject.UserID) == gc.Meta.GMID, Sender: MakeUserID(subject.UserID)}, Timestamp: subject.ReadAt.ToTime()},
			LastTarget: MakeMessageID(subject.MessageID),
		}})
	case "read_receipt.sync":
		if msg.Channel() != groupme.UserChannel(groupme.ID(gc.Meta.GMID)) {
			return nil, true
		}
		for _, receipt := range data.Subject.Receipts {
			if evt := gc.readCheckpointEvent(receipt); evt != nil {
				events = append(events, evt)
			}
		}
	}
	return events, true
}

type nativeReceipt struct {
	*simplevent.Receipt
	client *GMClient
}

// Web's read cursor includes hidden message.update and message.deleted records.
// Translate that native cursor to the preceding visible message inside the
// portal queue; those records have no Matrix message or mapping.
func (r *nativeReceipt) PreHandle(ctx context.Context, portal *bridgev2.Portal) {
	lookup := func(id groupme.ID) bool {
		mapped, err := portal.Bridge.DB.Message.GetLastPartByID(ctx, portal.Receiver, MakeMessageID(id))
		return err == nil && mapped != nil && !mapped.HasFakeMXID() && mapped.Room == portal.PortalKey
	}
	if lookup(ParseMessageID(r.LastTarget)) {
		return
	}
	var before groupme.ID
	foundHidden := false
	for page := 0; page < 2; page++ {
		messages, err := r.client.fetchMessagePage(ctx, portal.ID, before, "", 100)
		if err != nil {
			r.client.UserLogin.Log.Warn().Err(err).Msg("Failed to resolve GroupMe receipt on a hidden record")
			return
		}
		if len(messages) == 0 {
			return
		}
		for _, msg := range messages {
			if !foundHidden {
				comparison := compareMessageIDs(msg.ID, ParseMessageID(r.LastTarget))
				if comparison > 0 {
					continue
				}
				if comparison < 0 || !isHiddenRecord(msg) {
					return
				}
				foundHidden = true
				continue
			}
			if isHiddenRecord(msg) {
				continue
			}
			if lookup(msg.ID) {
				r.LastTarget = MakeMessageID(msg.ID)
				return
			}
		}
		before = messages[len(messages)-1].ID
	}
}
