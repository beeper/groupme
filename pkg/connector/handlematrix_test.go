package connector

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"

	"github.com/beeper/groupme-lib"
)

func TestSendTextConvertsFormattingToPlaintext(t *testing.T) {
	for _, tc := range []struct {
		name, body, html, want string
		msgType                event.MessageType
	}{
		{"bold", "Beeper bridge test reply", "<strong>Beeper bridge test reply</strong>", "Beeper bridge test reply", event.MsgText},
		{"literal markers", "**literal asterisks**", "", "**literal asterisks**", event.MsgText},
		{"emote", "waves", "<em>waves</em>", "/me waves", event.MsgEmote},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			mockGroupMe(t, func(r *http.Request) (int, string) {
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
				return 201, `{"response":{"message":{"id":"sent-message"}},"meta":{"code":201}}`
			})
			gc := dmTestClient()
			content := &event.MessageEventContent{MsgType: tc.msgType, Body: tc.body}
			if tc.html != "" {
				content.Format = event.FormatHTML
				content.FormattedBody = tc.html
			}
			msg := testMatrixText("group:test-group")
			msg.Content = content
			result, err := gc.HandleMatrixMessage(context.Background(), msg)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || result.DB.ID != MakeMessageID("sent-message") {
				t.Fatalf("unexpected send result: calls=%d, message=%+v", calls, result.DB)
			}
		})
	}
}

func TestSendReactionPreservesEmoji(t *testing.T) {
	for _, tc := range []struct{ key, want string }{
		{"👍\ufe0f", "👍"},
		{"👍", "👍"},
		{"❤", "❤️"},
		{"❤️", "❤️"},
		{"🔥\ufe0f", "🔥"},
		{"🦄", ""},
	} {
		t.Run(tc.key, func(t *testing.T) {
			calls := 0
			mockGroupMe(t, func(r *http.Request) (int, string) {
				calls++
				if r.Method != http.MethodPost || r.URL.Path != "/v3/messages/test-group/test-message/like" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				var request struct {
					LikeIcon struct{ Type, Code string } `json:"like_icon"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Fatal(err)
				}
				if request.LikeIcon.Type != "unicode" || request.LikeIcon.Code != tc.want {
					t.Errorf("sent reaction %+v, want unicode %q", request.LikeIcon, tc.want)
				}
				return 200, `{"meta":{"code":200}}`
			})
			gc := dmTestClient()
			msg := &bridgev2.MatrixReaction{
				MatrixEventBase: bridgev2.MatrixEventBase[*event.ReactionEventContent]{
					Content: &event.ReactionEventContent{RelatesTo: event.RelatesTo{Key: tc.key}},
					Portal:  testMatrixText("group:test-group").Portal,
				},
				TargetMessage: &database.Message{ID: MakeMessageID("test-message")},
			}
			pre, err := gc.PreHandleMatrixReaction(context.Background(), msg)
			if tc.want == "" {
				if err == nil || calls != 0 {
					t.Fatal("unsupported reaction must fail before sending")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			msg.PreHandleResp = &pre
			if _, err = gc.HandleMatrixReaction(context.Background(), msg); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("sent %d reactions, want one", calls)
			}
		})
	}
}
