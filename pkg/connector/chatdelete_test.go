package connector

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/event"
)

func TestDeleteChatScopeAndFailures(t *testing.T) {
	for _, tc := range []struct {
		name      string
		portal    networkid.PortalID
		everyone  bool
		status    int
		wantCall  bool
		wantError bool
	}{
		{"DM", "dm:9", false, 200, true, false},
		{"native failure", "dm:9", false, 429, true, true},
		{"everyone", "dm:9", true, 200, false, true},
		{"group", "group:7", false, 200, false, true},
		{"missing peer", "dm:", false, 200, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			mockGroupMe(t, func(r *http.Request) (int, string) {
				called = true
				if r.Method != http.MethodDelete || r.URL.EscapedPath() != "/v3/chats/9+20" {
					t.Fatalf("wrong deletion operation: %s %s", r.Method, r.URL.EscapedPath())
				}
				return tc.status, ""
			})
			msg := &bridgev2.MatrixDeleteChat{Portal: testMatrixText(tc.portal).Portal, Content: &event.BeeperChatDeleteEventContent{DeleteForEveryone: tc.everyone}}
			err := dmTestClient().HandleMatrixDeleteChat(context.Background(), msg)
			if called != tc.wantCall || (err != nil) != tc.wantError {
				t.Fatalf("called=%v error=%v", called, err)
			}
		})
	}
}

func TestNativeChatDeletionIsAccountScoped(t *testing.T) {
	for _, tc := range []struct {
		typ, channel, key, value string
		wantPortal               networkid.PortalID
	}{
		{"chat.deleted", "/user/20", "chat_id", "9+20", "dm:9"},
		{"chat.deleted", "/user/30", "chat_id", "9+20", ""},
		{"chat.deleted", "/user/20", "chat_id", "9+30", ""},
		{"chat.deleted", "/user/20", "chat_id", "", ""},
		{"membership.destroy", "/user/20", "id", "7", "group:7"},
		{"membership.destroy", "/group/7", "id", "7", ""},
		{"membership.destroy", "/user/20", "id", "", ""},
	} {
		t.Run(fmt.Sprintf("%s/%s/%s", tc.typ, tc.channel, tc.value), func(t *testing.T) {
			events, handled := dmTestClient().parseActivity(activityPush{tc.channel, map[string]interface{}{
				"type": tc.typ, "subject": map[string]string{tc.key: tc.value},
			}})
			if !handled {
				t.Fatal("deletion was not consumed")
			}
			if tc.wantPortal == "" {
				if len(events) != 0 {
					t.Fatalf("accepted unrelated deletion: %+v", events)
				}
				return
			}
			if len(events) != 1 {
				t.Fatalf("expected one deletion, got %d", len(events))
			}
			evt := events[0].(*simplevent.ChatDelete)
			if evt.PortalKey.ID != tc.wantPortal || evt.PortalKey.Receiver != "20" || !evt.OnlyForMe || !evt.Sender.IsFromMe || evt.CreatePortal {
				t.Fatalf("deletion has the wrong scope: %+v", evt)
			}
		})
	}
}
