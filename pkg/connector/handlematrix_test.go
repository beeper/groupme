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

func TestSendTextConvertsFormattingToPlaintext(t *testing.T) {
	for _, tc := range []struct {
		name, body, html, want string
		msgType                event.MessageType
	}{
		{"bold", "Beeper bridge test reply", "<strong>Beeper bridge test reply</strong>", "Beeper bridge test reply", event.MsgText},
		{"beeper bold fallback", "**Bold test two**", "<strong>Bold test two</strong>", "Bold test two", event.MsgText},
		{"literal markers", "**literal asterisks**", "", "**literal asterisks**", event.MsgText},
		{"literal markers in HTML", "**literal asterisks**", "<p>**literal asterisks**</p>", "**literal asterisks**", event.MsgText},
		{"nested formatting", "**bold _and italic_** ~~deleted~~ `code`", "<strong>bold <em>and italic</em></strong> <del>deleted</del> <code>code</code>", "bold and italic deleted code", event.MsgText},
		{"multiline link", "First line\nExample: https://example.com/", "First line<br>Example: <a href=\"https://example.com/\">website</a>", "First line\nExample: website (https://example.com/)", event.MsgText},
		{"code block", "```\na * b\nc\n```", "<pre><code>a * b\nc</code></pre>", "a * b\nc", event.MsgText},
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
			previous := http.DefaultTransport
			http.DefaultTransport = loginTransport(func(r *http.Request) (*http.Response, error) {
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
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"meta":{"code":200}}`))}, nil
			})
			t.Cleanup(func() { http.DefaultTransport = previous })
			gc := &GMClient{Client: groupmeext.NewClient("test-token"), Meta: &UserLoginMetadata{GMID: "self"}}
			msg := &bridgev2.MatrixReaction{
				MatrixEventBase: bridgev2.MatrixEventBase[*event.ReactionEventContent]{
					Content: &event.ReactionEventContent{RelatesTo: event.RelatesTo{Key: tc.key}},
					Portal:  &bridgev2.Portal{Portal: &database.Portal{PortalKey: networkid.PortalKey{ID: "group:test-group", Receiver: "self"}}},
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
			if pre.Emoji != tc.want || pre.MaxReactions != 1 {
				t.Fatalf("unexpected reaction metadata: %+v", pre)
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
