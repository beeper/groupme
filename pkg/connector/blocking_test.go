package connector

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

	"github.com/beeper/groupme-lib"
)

// Catch incorrect native method, account/peer IDs, body encoding, and swallowed
// provider failures. The SDK owns Matrix event handling and portal persistence.
func TestBlockUserNativeRequests(t *testing.T) {
	for _, tc := range []struct {
		name   string
		block  bool
		status int
	}{
		{"block", true, 201},
		{"unblock", false, 200},
		{"block failure", true, 403},
		{"unblock failure", false, 429},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			mockGroupMe(t, func(r *http.Request) (int, string) {
				calls++
				method := http.MethodDelete
				if tc.block {
					method = http.MethodPost
				}
				if r.Method != method || r.URL.Path != "/v3/blocks" || r.URL.Query().Get("user") != "20" || r.URL.Query().Get("otherUser") != "9" || len(r.URL.Query()) != 2 {
					t.Fatalf("wrong native block request: %s %s", r.Method, r.URL)
				}
				if r.Body != nil {
					body, err := io.ReadAll(r.Body)
					if err != nil || len(body) != 0 {
						t.Fatalf("block request must not have a body: %q, %v", body, err)
					}
				}
				if tc.block {
					return tc.status, fmt.Sprintf(`{"meta":{"code":%d},"response":{"block":{"user_id":"20","blocked_user_id":"9"}}}`, tc.status)
				}
				return tc.status, fmt.Sprintf(`{"meta":{"code":%d}}`, tc.status)
			})
			msg := &bridgev2.MatrixBlockUser{Portal: testMatrixText("dm:9").Portal, Content: &event.BeeperBlockUserEventContent{Block: tc.block}}
			err := dmTestClient().HandleMatrixBlockUser(context.Background(), msg)
			if calls != 1 || (err != nil) != (tc.status >= 300) {
				t.Fatalf("calls=%d status=%d error=%v", calls, tc.status, err)
			}
		})
	}
}

func TestNativeBlockListChangesIncludeHiddenDMs(t *testing.T) {
	gc := dmTestClient()
	previous := []networkid.PortalKey{gc.portalKeyForDM("8"), gc.portalKeyForDM("9")}
	events := gc.blockListEvents([]*groupme.Block{
		{UserID: "20", BlockedUserID: "9"},
		{UserID: "20", BlockedUserID: "10"},
		{UserID: "30", BlockedUserID: "20"},
		{UserID: "20"},
		nil,
	}, previous)
	want := map[networkid.PortalID]bool{"dm:8": false, "dm:9": true, "dm:10": true}
	if len(events) != len(want) {
		t.Fatalf("expected %d changes, got %d", len(want), len(events))
	}
	for _, evt := range events {
		blocked, ok := want[evt.PortalKey.ID]
		state := evt.ChatInfoChange.ChatInfo.UserBlocked
		if !ok || evt.PortalKey.Receiver != "20" || state == nil || *state != blocked || evt.CreatePortal {
			t.Fatalf("incorrect native block conversion: %+v", evt)
		}
		delete(want, evt.PortalKey.ID)
	}
}

func TestBlockUserRejectsUnsupportedNativeRequests(t *testing.T) {
	for _, tc := range []struct {
		name       string
		portal     networkid.PortalID
		reportSpam bool
		loggedOut  bool
	}{
		{"spam reporting", "dm:9", true, false},
		{"missing peer", "dm:", false, false},
		{"non-DM ID", "group:9", false, false},
		{"logged out", "dm:9", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mockGroupMe(t, func(r *http.Request) (int, string) {
				t.Fatal("unsupported operation made a native request")
				return 500, ""
			})
			gc := dmTestClient()
			if tc.loggedOut {
				gc.Client = nil
			}
			err := gc.HandleMatrixBlockUser(context.Background(), &bridgev2.MatrixBlockUser{
				Portal:  testMatrixText(tc.portal).Portal,
				Content: &event.BeeperBlockUserEventContent{Block: true, ReportSpam: tc.reportSpam},
			})
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

// A reverse-direction block must never be presented as one the user can undo.
// Failed lookups must leave the last known state untouched (nil, not false).
func TestBlockedStateUsesOutgoingBlockList(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		status  int
		blocked bool
		unknown bool
	}{
		{"outgoing", `{"response":{"blocks":[{"user_id":"20","blocked_user_id":"9"}]}}`, 200, true, false},
		{"incoming", `{"response":{"blocks":[{"user_id":"9","blocked_user_id":"20"}]}}`, 200, false, false},
		{"another peer", `{"response":{"blocks":[null,{"user_id":"20","blocked_user_id":"8"}]}}`, 200, false, false},
		{"empty", `{"response":{"blocks":[]}}`, 200, false, false},
		{"failure", `{"meta":{"code":503}}`, 503, false, true},
		{"malformed", `{`, 200, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mockGroupMe(t, func(r *http.Request) (int, string) {
				if r.Method != http.MethodGet || r.URL.Path != "/v3/blocks" || r.URL.Query().Get("user") != "20" || len(r.URL.Query()) != 1 {
					t.Fatalf("wrong block list request: %s %s", r.Method, r.URL)
				}
				return tc.status, tc.body
			})
			got := dmTestClient().userBlocked(context.Background(), "9")
			if (got == nil) != tc.unknown || (got != nil && *got != tc.blocked) {
				t.Fatalf("blocked=%v want blocked=%v unknown=%v", got, tc.blocked, tc.unknown)
			}
		})
	}
}
