package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/beeper/groupme-lib"
	"github.com/beeper/groupme/pkg/groupmeext"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func mockGroupMe(t *testing.T, handle func(*http.Request) (int, string)) {
	t.Helper()
	previous := http.DefaultTransport
	http.DefaultTransport = loginTransport(func(r *http.Request) (*http.Response, error) {
		if err := r.Context().Err(); err != nil {
			return nil, err
		}
		if r.URL.Host != "api.groupme.com" || r.Header.Get("X-Access-Token") != "test-token" || r.URL.Query().Has("token") {
			t.Fatal("unexpected destination or authentication")
		}
		status, body := handle(r)
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = previous })
}

func dmTestClient() *GMClient {
	return &GMClient{
		Main:      &GMConnector{},
		Client:    groupmeext.NewClient("test-token"),
		Meta:      &UserLoginMetadata{GMID: "20", Token: "test-token"},
		UserLogin: &bridgev2.UserLogin{UserLogin: &database.UserLogin{ID: "20"}},
	}
}

func testMatrixText(portal networkid.PortalID) *bridgev2.MatrixMessage {
	return &bridgev2.MatrixMessage{MatrixEventBase: bridgev2.MatrixEventBase[*event.MessageEventContent]{
		Event:   &event.Event{ID: "$outgoing-test", Timestamp: 1234567890000},
		Content: &event.MessageEventContent{MsgType: event.MsgText, Body: "DM test"},
		Portal:  &bridgev2.Portal{Portal: &database.Portal{PortalKey: networkid.PortalKey{ID: portal, Receiver: "20"}}},
	}}
}

func TestDMIncomingAndEchoUseSamePortal(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		fromMe     bool
	}{
		{"incoming push", `{"id":"incoming","user_id":"9","recipient_id":"20","chat_id":"9+20","text":"hello"}`, false},
		{"outgoing push echo", `{"id":"outgoing","user_id":"20","recipient_id":"9","chat_id":"9+20","text":"hello"}`, true},
		{"incoming REST", `{"id":"incoming","user_id":"9","recipient_id":"20","conversation_id":"9+20","text":"hello"}`, false},
		{"outgoing REST", `{"id":"outgoing","user_id":"20","recipient_id":"9","conversation_id":"9+20","text":"hello"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gc := dmTestClient()
			var msg groupme.Message
			if err := json.Unmarshal([]byte(tc.body), &msg); err != nil {
				t.Fatal(err)
			}
			evt := gc.makeRemoteMessage(msg)
			if evt.PortalKey != (networkid.PortalKey{ID: "dm:9", Receiver: "20"}) || evt.Sender.IsFromMe != tc.fromMe || evt.Sender.Sender != networkid.UserID(msg.UserID) {
				t.Fatalf("wrong portal or sender: %+v", evt.EventMeta)
			}
			converted, err := evt.ConvertMessageFunc(context.Background(), nil, nil, evt.Data)
			if err != nil || len(converted.Parts) != 1 || converted.Parts[0].Content.Body != "hello" {
				t.Fatalf("wrong message conversion: %+v, %v", converted, err)
			}
			gc.UserLogin.ID = "other-login"
			if gc.makeRemoteMessage(msg).PortalKey.Receiver != "other-login" {
				t.Fatal("portal crossed login ownership")
			}
		})
	}
}

func TestDMSendAcceptsPendingRequestAndMapsResponse(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(fmt.Sprint(pending), func(t *testing.T) {
			var calls []string
			mockGroupMe(t, func(r *http.Request) (int, string) {
				calls = append(calls, r.Method+" "+r.URL.Path)
				switch r.URL.Path {
				case "/v3/chats/9+20":
					return 200, fmt.Sprintf(`{"response":{"requires_approval":%v}}`, pending)
				case "/v3/chats/9+20/approve":
					return 200, `{"meta":{"code":200}}`
				case "/v3/direct_messages":
					var body struct {
						Message groupme.Message `json:"direct_message"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					if r.Method != "POST" || body.Message.RecipientID != "9" || body.Message.GroupID != "" || body.Message.Text != "DM test" || body.Message.SourceGUID == "" {
						t.Fatalf("incorrect DM request: %+v", body.Message)
					}
					return 201, `{"response":{"direct_message":{"id":"native-dm"}},"meta":{"code":201}}`
				default:
					t.Fatalf("unexpected request: %s", r.URL.Path)
					return 500, ""
				}
			})
			msg := testMatrixText("dm:9")
			result, err := dmTestClient().HandleMatrixMessage(context.Background(), msg)
			if err != nil {
				t.Fatal(err)
			}
			want := "GET /v3/chats/9+20,POST /v3/direct_messages"
			if pending {
				want = "GET /v3/chats/9+20,POST /v3/chats/9+20/approve,POST /v3/direct_messages"
			}
			if strings.Join(calls, ",") != want {
				t.Fatalf("incorrect approval/send order: %v", calls)
			}
			if result.DB.ID != "native-dm" || result.DB.SenderID != "20" {
				t.Fatalf("incorrect mapping: %+v", result.DB)
			}
		})
	}
}

func TestSendRejectsEmptySuccessAndPreservesFailures(t *testing.T) {
	for _, portal := range []networkid.PortalID{"group:9", "dm:9"} {
		for _, tc := range []struct {
			name     string
			code     int
			response string
		}{
			{"null", 201, "null"}, {"missing message", 201, "{}"}, {"missing ID", 201, `{"message":{},"direct_message":{}}`},
			{"rate limit", 429, "null"}, {"unauthorized", 401, "null"},
			{"wrong sender", 201, `{"message":{"id":"sent","user_id":"99"},"direct_message":{"id":"sent","user_id":"99"}}`},
			{"wrong conversation", 201, `{"message":{"id":"sent","group_id":"8"},"direct_message":{"id":"sent","recipient_id":"8"}}`},
		} {
			t.Run(string(portal)+"/"+tc.name, func(t *testing.T) {
				mockGroupMe(t, func(r *http.Request) (int, string) {
					if r.Method == "GET" {
						return 200, `{"response":{"requires_approval":false}}`
					}
					return tc.code, fmt.Sprintf(`{"response":%s,"meta":{"code":%d}}`, tc.response, tc.code)
				})
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("send panicked on provider response: %v", r)
					}
				}()
				result, err := dmTestClient().HandleMatrixMessage(context.Background(), testMatrixText(portal))
				if err == nil || result != nil {
					t.Fatal("bad send was accepted as delivered")
				}
				if tc.code >= 400 {
					var meta *groupme.Meta
					if !errors.As(err, &meta) || int(meta.Code) != tc.code {
						t.Fatalf("lost provider failure: %v", err)
					}
				}
			})
		}
	}
}

func TestDMPendingApprovalFailureStopsSend(t *testing.T) {
	mockGroupMe(t, func(r *http.Request) (int, string) {
		switch r.URL.Path {
		case "/v3/chats/9+20":
			return 200, `{"response":{"requires_approval":true}}`
		case "/v3/chats/9+20/approve":
			return 429, `{"error":"private response data"}`
		default:
			t.Error("sent DM before required approval succeeded")
			return 201, `{"response":{"direct_message":{"id":"sent"}},"meta":{"code":201}}`
		}
	})
	result, err := dmTestClient().HandleMatrixMessage(context.Background(), testMatrixText("dm:9"))
	if result != nil || err == nil || strings.Contains(err.Error(), "private response data") {
		t.Fatalf("approval failure was lost or exposed response data: %v", err)
	}
}

func TestDMCanceledSendMakesNoRequest(t *testing.T) {
	mockGroupMe(t, func(r *http.Request) (int, string) {
		t.Fatal("canceled send made a request")
		return 500, ""
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := dmTestClient().HandleMatrixMessage(ctx, testMatrixText("dm:9"))
	if result != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation was not propagated: %v", err)
	}
}

func TestSendRetryKeepsNativeRequestIdentity(t *testing.T) {
	var guids []string
	mockGroupMe(t, func(r *http.Request) (int, string) {
		if r.Method == "GET" {
			return 200, `{"response":{"requires_approval":false}}`
		}
		var body map[string]groupme.Message
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		for _, msg := range body {
			guids = append(guids, msg.SourceGUID)
		}
		return 201, `{"response":{"message":{"id":"sent"},"direct_message":{"id":"sent"}},"meta":{"code":201}}`
	})
	for _, portal := range []networkid.PortalID{"group:9", "dm:9"} {
		for _, eventID := range []id.EventID{"$first", "$first", "$second"} {
			msg := testMatrixText(portal)
			msg.Event.ID = eventID
			if _, err := dmTestClient().HandleMatrixMessage(context.Background(), msg); err != nil {
				t.Fatal(err)
			}
		}
	}
	if guids[0] == "" || guids[0] != guids[1] || guids[0] == guids[2] || guids[3] != guids[4] || guids[3] == guids[5] || guids[0] == guids[3] {
		t.Fatal("request IDs are not stable per message and conversation")
	}
}

func TestDMReactionsUseNumericConversationOrder(t *testing.T) {
	gc := dmTestClient()
	var calls []string
	mockGroupMe(t, func(r *http.Request) (int, string) {
		calls = append(calls, r.URL.Path)
		if r.Method != "POST" {
			t.Fatal("unexpected reaction method")
		}
		return 200, `{"meta":{"code":200}}`
	})
	portal := testMatrixText("dm:9").Portal
	_, err := gc.HandleMatrixReaction(context.Background(), &bridgev2.MatrixReaction{
		MatrixEventBase: bridgev2.MatrixEventBase[*event.ReactionEventContent]{Portal: portal},
		TargetMessage:   &database.Message{ID: "native-dm"},
		PreHandleResp:   &bridgev2.MatrixReactionPreResponse{Emoji: "👍"},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = gc.HandleMatrixReactionRemove(context.Background(), &bridgev2.MatrixReactionRemove{
		MatrixEventBase: bridgev2.MatrixEventBase[*event.RedactionEventContent]{Portal: portal},
		TargetReaction:  &database.Reaction{MessageID: "native-dm"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(calls, ",") != "/v3/messages/9+20/native-dm/like,/v3/messages/9+20/native-dm/unlike" {
		t.Fatalf("wrong reaction endpoints: %v", calls)
	}
}
