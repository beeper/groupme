package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/beeper/groupme-lib"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
)

var (
	_ bridgev2.TypingHandlingNetworkAPI = (*GMClient)(nil)
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
		return fmt.Errorf("GroupMe push is disconnected")
	}
	return transport.SendTyping(ctx, channel, gc.Meta.Token, gc.Meta.GMID)
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
	if typ != "typing" {
		return nil, false
	}
	raw, err := json.Marshal(msg.Data())
	if err != nil {
		return nil, true
	}
	var data struct {
		UserID  groupme.ID `json:"user_id"`
		Started int64      `json:"started"`
	}
	if json.Unmarshal(raw, &data) != nil {
		return nil, true
	}
	var events []bridgev2.RemoteEvent
	switch typ {
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
		timeout := time.Until(time.UnixMilli(data.Started).Add(5 * time.Second))
		if key.ID == "" || timeout <= 0 {
			return nil, true
		}
		if timeout > 5*time.Second {
			timeout = 5 * time.Second
		}
		events = append(events, &simplevent.Typing{
			EventMeta: simplevent.EventMeta{Type: bridgev2.RemoteEventTyping, PortalKey: key, Sender: bridgev2.EventSender{Sender: MakeUserID(data.UserID)}},
			Timeout:   timeout, Type: bridgev2.TypingTypeText,
		})

	}
	return events, true
}
