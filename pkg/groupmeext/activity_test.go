package groupmeext

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// The paths, methods, and envelope shapes below were captured from GroupMe Web
// on 2026-10-13. In particular, the two receipt routes encode '+' differently.
func TestNativeActivityRequests(t *testing.T) {
	previous := activityHTTPClient
	t.Cleanup(func() { activityHTTPClient = previous })
	tests := []struct {
		name, method, path, body, response string
		call                               func() error
	}{
		{"group edit", "PUT", "/v4/groups/88/messages/123", `{"text":"edited","attachments":[]}`, `{"response":{"message":{"id":"123","updated_at":1791916613}}}`, func() error {
			_, err := EditMessage(context.Background(), "test-token", false, "88", "123", MessageContent{Text: "edited"})
			return err
		}},
		{"DM edit", "PUT", "/v4/direct_messages/20/messages/123", `{"text":"edited","attachments":[]}`, `{"response":{"message":{"id":"123","updated_at":1791916613}}}`, func() error {
			_, err := EditMessage(context.Background(), "test-token", true, "20", "123", MessageContent{Text: "edited"})
			return err
		}},
		{"own read checkpoint", "POST", "/v4/read_receipts/9%2B20", `{"last_read_message_id":"123"}`, "", func() error { return MarkRead(context.Background(), "test-token", "9+20", "123") }},
		{"DM delete for self", "DELETE", "/v3/chats/9+20", "", "", func() error { return DeleteDM(context.Background(), "test-token", "9+20") }},
		{"DM seen receipt", "POST", "/v3/conversations/9+20/read_receipt", "", "", func() error { return SendDMReadReceipt(context.Background(), "test-token", "9+20") }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			activityHTTPClient = &http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
				body, _ := io.ReadAll(r.Body)
				if r.Method != tc.method || r.URL.EscapedPath() != tc.path || string(body) != tc.body {
					t.Fatalf("unexpected native request: %s %s %s", r.Method, r.URL.EscapedPath(), body)
				}
				if r.Header.Get("X-Access-Token") != "test-token" || r.URL.RawQuery != "" {
					t.Fatal("incorrect authentication")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(tc.response))}, nil
			})}
			if err := tc.call(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMessageUpdateEnvelope(t *testing.T) {
	frames, err := decodeBayeuxFrame([]byte(`[{"channel":"/user/9","data":{"type":"line.create","subject":{"id":"124","group_id":"88","user_id":"system","system":true,"event":{"type":"message.update","data":{"message_id":"123","sender_id":"20","updated_at":1791916613,"message":{"text":"edited","attachments":[]}}}}}}]`))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(frames[0].Data()["subject"])
	var subject struct {
		Event struct {
			Data MessageUpdate `json:"data"`
		} `json:"event"`
	}
	if err = json.Unmarshal(raw, &subject); err != nil {
		t.Fatal(err)
	}
	u := subject.Event.Data
	if u.MessageID != "123" || u.SenderID != "20" || u.UpdatedAt != 1791916613 || u.Message.Text != "edited" || u.Message.Attachments == nil {
		t.Fatalf("lost edit identity or complete content: %+v", u)
	}
}
