package connector

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

	"github.com/beeper/groupme-lib"
	"github.com/beeper/groupme/pkg/groupmeext"
)

func TestSendTextUsesPlaintextBody(t *testing.T) {
	for _, tc := range []struct {
		name, body, html, want string
		msgType                event.MessageType
	}{
		{"bold", "Beeper bridge test reply", "<strong>Beeper bridge test reply</strong>", "Beeper bridge test reply", event.MsgText},
		{"literal markers", "**literal asterisks**", "", "**literal asterisks**", event.MsgText},
		{"multiline link", "First line\nExample: https://example.com/", "First line<br>Example: <a href=\"https://example.com/\">website</a>", "First line\nExample: https://example.com/", event.MsgText},
		{"emote", "waves", "<em>waves</em>", "/me waves", event.MsgEmote},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			previous := http.DefaultTransport
			http.DefaultTransport = loginTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != http.MethodPost || r.URL.Path != "/v3/groups/test-group/messages" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				var request struct {
					Message groupme.Message `json:"message"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Fatal(err)
				}
				if request.Message.Text != tc.want {
					t.Errorf("sent text %q, want %q", request.Message.Text, tc.want)
				}
				return &http.Response{StatusCode: http.StatusCreated, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"response":{"message":{"id":"sent-message"}},"meta":{"code":201}}`))}, nil
			})
			t.Cleanup(func() { http.DefaultTransport = previous })
			gc := &GMClient{Client: groupmeext.NewClient("test-token"), Meta: &UserLoginMetadata{GMID: "self"}}
			content := &event.MessageEventContent{MsgType: tc.msgType, Body: tc.body}
			if tc.html != "" {
				content.Format = event.FormatHTML
				content.FormattedBody = tc.html
			}
			msg := &bridgev2.MatrixMessage{MatrixEventBase: bridgev2.MatrixEventBase[*event.MessageEventContent]{
				Event:   &event.Event{ID: "$matrix-message", Timestamp: 1234567890000},
				Content: content,
				Portal:  &bridgev2.Portal{Portal: &database.Portal{PortalKey: networkid.PortalKey{ID: "group:test-group", Receiver: "self"}}},
			}}
			result, err := gc.HandleMatrixMessage(context.Background(), msg)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || result.DB.ID != MakeMessageID("sent-message") || result.DB.MXID != msg.Event.ID || result.DB.Room != msg.Portal.PortalKey {
				t.Fatalf("unexpected send result: calls=%d, message=%+v", calls, result.DB)
			}
		})
	}
}
