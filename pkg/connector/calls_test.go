package connector

import (
	"context"
	"encoding/json"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"

	"github.com/beeper/groupme-lib"
)

const nativeCallStart = `{"id":"123","group_id":"88","created_at":1793134919,"user_id":"9","sender_id":"9","sender_type":"system","system":true,"name":"Caller","text":"Caller has started a call","attachments":[],"event":{"type":"group.call.started","data":{"meeting_id":"https://api.scheduler.teams.microsoft.com/teamsforlife/123","user":{"id":"9","nickname":"Caller"}}}}`
const nativeCallEnd = `{"id":"124","group_id":"88","created_at":1793134957,"user_id":"system","sender_id":"system","sender_type":"system","system":true,"name":"system","text":"Call has ended","attachments":[],"event":{"type":"group.call.ended","data":{"call_duration":38794,"user":{"id":"system","nickname":"system"}}}}`

type callPushHandler struct {
	t        *testing.T
	messages []groupme.Message
}

func (h *callPushHandler) HandleError(err error) { h.t.Fatal(err) }
func (h *callPushHandler) HandleTextMessage(msg groupme.Message) {
	h.messages = append(h.messages, msg)
}

func TestCallSystemMessages(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
		modify  func(*groupme.Message)
		sender  bridgev2.EventSender
		msgType event.MessageType
	}{
		{"call start", nativeCallStart, nil, bridgev2.EventSender{Sender: "9"}, event.MsgText},
		{"own call start", nativeCallStart, func(msg *groupme.Message) {
			msg.Event.Data = json.RawMessage(`{"meeting_id":"https://api.scheduler.teams.microsoft.com/teamsforlife/123","user":{"id":"20","nickname":"Self"}}`)
		}, bridgev2.EventSender{Sender: "20", IsFromMe: true}, event.MsgText},
		{"system sender with caller", nativeCallStart, func(msg *groupme.Message) {
			msg.UserID = "system"
		}, bridgev2.EventSender{Sender: "9"}, event.MsgText},
		{"call end", nativeCallEnd, nil, bridgev2.EventSender{}, event.MsgNotice},
		{"missing caller", nativeCallStart, func(msg *groupme.Message) {
			msg.Event.Data = json.RawMessage(`{"meeting_id":"https://api.scheduler.teams.microsoft.com/teamsforlife/123"}`)
		}, bridgev2.EventSender{}, event.MsgNotice},
		{"system caller", nativeCallStart, func(msg *groupme.Message) {
			msg.Event.Data = json.RawMessage(`{"user":{"id":"system","nickname":"system"}}`)
		}, bridgev2.EventSender{}, event.MsgNotice},
		{"malformed caller", nativeCallStart, func(msg *groupme.Message) {
			msg.Event.Data = json.RawMessage(`{"user":[]}`)
		}, bridgev2.EventSender{}, event.MsgNotice},
		{"unrelated system event", nativeCallStart, func(msg *groupme.Message) {
			msg.Event.Type = "group.name_change"
		}, bridgev2.EventSender{}, event.MsgNotice},
		{"text without event", nativeCallStart, func(msg *groupme.Message) {
			msg.Event = nil
		}, bridgev2.EventSender{}, event.MsgNotice},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var subject map[string]any
			if err := json.Unmarshal([]byte(tc.payload), &subject); err != nil {
				t.Fatal(err)
			}
			handler := &callPushHandler{t: t}
			sub := groupme.NewPushSubscription(context.Background())
			sub.AddHandler(handler)
			groupme.RealTimeHandlers["line.create"](sub, "/user/20", subject)
			if len(handler.messages) != 1 {
				t.Fatalf("call push delivered %d messages", len(handler.messages))
			}
			msg := handler.messages[0]
			if tc.modify != nil {
				tc.modify(&msg)
			}
			gc := dmTestClient()
			remote := gc.makeRemoteMessage(msg)
			if remote.GetSender() != tc.sender {
				t.Errorf("sender = %+v, want %+v", remote.GetSender(), tc.sender)
			}
			converted, err := gc.convertGroupMeMessage(context.Background(), nil, nil, &msg)
			if err != nil {
				t.Fatal(err)
			}
			if len(converted.Parts) != 1 || converted.Parts[0].Content.MsgType != tc.msgType || converted.Parts[0].Content.Body != msg.Text {
				t.Fatalf("incorrect call conversion: %+v", converted.Parts)
			}
		})
	}
}
