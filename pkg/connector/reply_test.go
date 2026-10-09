package connector

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

	"github.com/beeper/groupme-lib"
)

func TestReplySendRetainsRelationAndAttachment(t *testing.T) {
	for _, portal := range []networkid.PortalID{"group:9", "dm:9"} {
		for _, location := range []bool{false, true} {
			t.Run(string(portal)+"/"+map[bool]string{false: "text", true: "location"}[location], func(t *testing.T) {
				mockGroupMe(t, func(r *http.Request) (int, string) {
					var body map[string]groupme.Message
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					for _, m := range body {
						want := 1
						if location {
							want = 2
							if m.Attachments[0].Type != groupme.Location {
								t.Fatal("lost location attachment")
							}
						}
						if len(m.Attachments) != want {
							t.Fatalf("wrong attachments: %+v", m.Attachments)
						}
						att := m.Attachments[want-1]
						if att.Type != groupme.Reply || att.ReplyID != "parent" || att.BaseReplyID != "parent" || att.UserID != "9" {
							t.Fatalf("wrong reply contract: %+v", att)
						}
					}
					return 201, `{"response":{"message":{"id":"sent"},"direct_message":{"id":"sent"}},"meta":{"code":201}}`
				})
				msg := testMatrixText(portal)
				msg.ReplyTo = &database.Message{ID: "parent", SenderID: "9", Room: msg.Portal.PortalKey}
				if location {
					msg.Content = &event.MessageEventContent{MsgType: event.MsgLocation, Body: "Test location", GeoURI: "geo:51.5,-0.1"}
				}
				if _, err := dmTestClient().HandleMatrixMessage(context.Background(), msg); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestReplyConversionUsesImmediateParent(t *testing.T) {
	msg := &groupme.Message{Text: "reply", Attachments: []*groupme.Attachment{
		nil,
		{Type: groupme.Reply, ReplyID: "immediate-parent", BaseReplyID: "thread-root", UserID: "9"},
		{Type: groupme.Location, Latitude: "51.5", Longitude: "-0.1", Name: "Test location"},
	}}
	converted, err := (&GMClient{Main: &GMConnector{}}).convertGroupMeMessage(context.Background(), nil, nil, msg)
	if err != nil {
		t.Fatal(err)
	}
	if converted.ReplyTo == nil || converted.ReplyTo.MessageID != "immediate-parent" || len(converted.Parts) != 2 || converted.Parts[0].Content.MsgType != event.MsgLocation || converted.Parts[1].Content.Body != "reply" {
		t.Fatalf("reply lost relation or content: %+v", converted)
	}
}
