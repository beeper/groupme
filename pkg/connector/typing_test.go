package connector

import (
	"context"
	"testing"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
)

type activityPush struct {
	channel string
	data    map[string]interface{}
}

func (p activityPush) Channel() string              { return p.channel }
func (p activityPush) Data() map[string]interface{} { return p.data }
func (p activityPush) Ext() map[string]interface{}  { return nil }
func (p activityPush) Error() string                { return "" }

func TestIncomingTypingRoutesAndExpires(t *testing.T) {
	for _, tc := range []struct {
		name, channel, sender string
		age                   time.Duration
		wantPortal            networkid.PortalID
	}{
		{"group", "/group/88", "9", 0, "group:88"},
		{"DM", "/direct_message/9_20", "9", 0, "dm:9"},
		{"future pulse", "/group/88", "9", -time.Minute, "group:88"},
		{"old pulse", "/group/88", "9", 6 * time.Second, "group:88"},
		{"self", "/group/88", "20", 0, ""},
		{"missing sender", "/group/88", "", 0, ""},
		{"wrong DM sender", "/direct_message/9_20", "30", 0, ""},
		{"another DM", "/direct_message/9_30", "9", 0, ""},
		{"unrelated channel", "/user/20", "9", 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			push := activityPush{tc.channel, map[string]interface{}{
				"type": "typing", "user_id": tc.sender, "started": time.Now().Add(-tc.age).UnixMilli(),
			}}
			events, handled := dmTestClient().parseActivity(push)
			if !handled {
				t.Fatal("typing was not consumed")
			}
			if tc.wantPortal == "" {
				if len(events) != 0 {
					t.Fatalf("forwarded invalid typing: %+v", events)
				}
				return
			}
			if len(events) != 1 {
				t.Fatalf("expected one typing event, got %d", len(events))
			}
			evt := events[0].(*simplevent.Typing)
			if evt.PortalKey.ID != tc.wantPortal || evt.PortalKey.Receiver != "20" || string(evt.Sender.Sender) != tc.sender || evt.Timeout <= 0 || evt.Timeout > 5*time.Second {
				t.Fatalf("incorrect typing route or lifetime: %+v", evt)
			}
		})
	}
}

func TestOutgoingTypingIgnoresStopsAndRequiresConnection(t *testing.T) {
	gc := dmTestClient()
	msg := &bridgev2.MatrixTyping{Portal: testMatrixText("dm:9").Portal, Type: bridgev2.TypingTypeText}
	if err := gc.HandleMatrixTyping(context.Background(), msg); err != nil {
		t.Fatalf("stop pulse should be ignored: %v", err)
	}
	msg.IsTyping = true
	if err := gc.HandleMatrixTyping(context.Background(), msg); err == nil {
		t.Fatal("start pulse succeeded without a push connection")
	}
}
