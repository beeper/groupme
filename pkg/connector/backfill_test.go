package connector

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"maunium.net/go/mautrix/bridgev2/networkid"

	"github.com/beeper/groupme-lib"
)

func TestHistoryRemoteResponses(t *testing.T) {
	for _, dm := range []bool{false, true} {
		for _, tc := range []struct {
			name      string
			status    int
			messages  string
			wantError bool
		}{
			{"page", 200, `[{"id":"98","user_id":"9","group_id":"7","recipient_id":"20"},{"id":"99","user_id":"20","group_id":"7","recipient_id":"9"}]`, false},
			{"empty", 304, `[]`, false},
			{"rate limit", 429, `[]`, true},
			{"missing message", 200, `[null]`, true},
			{"missing ID", 200, `[{}]`, true},
			{"wrong conversation", 200, `[{"id":"99","user_id":"8","group_id":"8","recipient_id":"20"}]`, true},
			{"nonadvancing", 200, `[{"id":"100","user_id":"9","group_id":"7","recipient_id":"20"}]`, true},
		} {
			t.Run(fmt.Sprintf("dm=%v/%s", dm, tc.name), func(t *testing.T) {
				portalID := networkid.PortalID("group:7")
				field := "messages"
				messages := tc.messages
				if dm {
					portalID, field = "dm:9", "direct_messages"
					messages = strings.ReplaceAll(strings.ReplaceAll(messages, `"group_id":"7",`, ""), `"group_id":"8",`, "")
				}
				mockGroupMe(t, func(r *http.Request) (int, string) {
					q := r.URL.Query()
					if q.Get("before_id") != "100" || q.Has("after_id") {
						t.Fatal("incorrect history cursor")
					}
					if dm && q.Get("other_user_id") != "9" || !dm && q.Get("limit") != "100" {
						t.Fatal("incorrect history request")
					}
					return tc.status, fmt.Sprintf(`{"response":{"%s":%s},"meta":{"code":%d}}`, field, messages, tc.status)
				})
				page, err := dmTestClient().fetchMessagePage(context.Background(), portalID, "100", "", 100)
				if (err != nil) != tc.wantError {
					t.Fatalf("unexpected error: %v", err)
				}
				if tc.name == "page" && (len(page) != 2 || page[0].ID != "99" || page[1].ID != "98") {
					t.Fatal("incorrect page order")
				}
				if tc.status == 429 {
					var meta *groupme.Meta
					if !errors.As(err, &meta) || meta.Code != 429 {
						t.Fatal("lost rate-limit response")
					}
				}
			})
		}
	}
}
