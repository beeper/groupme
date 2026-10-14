package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

	"github.com/beeper/groupme-lib"
	"github.com/beeper/groupme/pkg/groupmeext"
)

func TestRecentEditsSkipIntermediateRevisions(t *testing.T) {
	for _, portalID := range []networkid.PortalID{"group:7", "dm:9"} {
		for _, current := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/current=%v", portalID, current), func(t *testing.T) {
				gc := dmTestClient()
				portal := testMatrixText(portalID).Portal
				field, identity := "messages", `"group_id":"7"`
				if portalID == "dm:9" {
					field, identity = "direct_messages", `"chat_id":"9+20"`
				}
				mockGroupMe(t, func(r *http.Request) (int, string) {
					messages := []string{
						fmt.Sprintf(`{"id":"123","user_id":"9",%s,"text":"latest"}`, identity),
						fmt.Sprintf(`{"id":"200","user_id":"system",%s,"event":{"type":"message.update","data":{"message_id":"123","sender_id":"9","updated_at":1000,"message":{"text":"intermediate","attachments":[]}}}}`, identity),
						fmt.Sprintf(`{"id":"201","user_id":"system",%s,"event":{"type":"message.update","data":{"message_id":"124","sender_id":"9","updated_at":1000,"message":{"text":"latest","attachments":[]}}}}`, identity),
						fmt.Sprintf(`{"id":"202","user_id":"system",%s,"event":{"type":"message.update","data":{"message_id":"123","sender_id":"9","updated_at":1000,"message":{"text":"latest","attachments":[]}}}}`, identity),
					}
					return 200, fmt.Sprintf(`{"response":{"%s":[%s]},"meta":{"code":200}}`, field, strings.Join(messages, ","))
				})
				messages, err := gc.fetchMessagePage(context.Background(), portalID, "", "", 100)
				if err != nil {
					t.Fatal(err)
				}
				existing := make(map[networkid.MessageID]*database.Message)
				for _, id := range []networkid.MessageID{"123", "124"} {
					text := "original"
					if current {
						text = "latest"
					}
					converted, err := gc.convertGroupMeMessage(context.Background(), portal, nil, &groupme.Message{Text: text})
					if err != nil {
						t.Fatal(err)
					}
					existing[id] = &database.Message{ID: id, Metadata: converted.Parts[0].DBMetadata}
				}
				for reconnect := 0; reconnect < 2; reconnect++ {
					events := gc.recentEditEvents(messages)
					if len(events) != 2 || events[0].ID != "202" || events[1].ID != "201" {
						t.Fatalf("expected only the latest revision for each message: %+v", events)
					}
					bridged := 0
					for _, evt := range events {
						old := existing[evt.TargetMessage]
						result, err := evt.ConvertEdit(context.Background(), portal, nil, []*database.Message{old})
						if err != nil {
							t.Fatal(err)
						}
						for _, part := range result.ModifiedParts {
							if !part.DontBridge {
								bridged++
								if part.Content.Body != "latest" {
									t.Fatalf("replayed intermediate content: %s", part.Content.Body)
								}
							}
						}
						if old.Metadata.(*MessageMetadata).EditRevisionID != groupme.ID(evt.ID) {
							t.Fatal("latest revision was not persisted")
						}
					}
					wantBridged := 0
					if !current && reconnect == 0 {
						wantBridged = 2
					}
					if bridged != wantBridged {
						t.Fatalf("reconnect %d bridged %d edits, want %d", reconnect, bridged, wantBridged)
					}
				}
			})
		}
	}
}

func TestEditTargetsOriginalPartAndSuppressesEcho(t *testing.T) {
	gc := &GMClient{Main: &GMConnector{}}
	portal := &bridgev2.Portal{Portal: &database.Portal{PortalKey: networkid.PortalKey{ID: "group:88", Receiver: "9"}}}
	old := &database.Message{ID: "123", PartID: "", Metadata: &MessageMetadata{}}
	u := &groupmeext.MessageUpdate{RevisionID: "125", MessageID: "123", SenderID: "20", UpdatedAt: 1791916613, Message: groupmeext.MessageContent{Text: "edited", Attachments: []*groupme.Attachment{}}}
	result, err := gc.convertEdit(context.Background(), portal, nil, []*database.Message{old}, u)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ModifiedParts) != 1 || result.ModifiedParts[0].Part != old || result.ModifiedParts[0].Content.Body != "edited" || result.AddedParts != nil || len(result.DeletedParts) != 0 {
		t.Fatalf("edit did not preserve original mapping: %+v", result)
	}
	// A same-second update still has a strictly ordered native revision ID.
	u.RevisionID = "124"
	u.Message.Text = "older"
	result, err = gc.convertEdit(context.Background(), portal, nil, []*database.Message{old}, u)
	if err != nil || len(result.ModifiedParts) != 0 {
		t.Fatalf("same-second stale revision was applied: %+v, %v", result, err)
	}
	// The native echo of our successful send must persist its revision without
	// producing a second Matrix edit for identical content.
	u.RevisionID = "126"
	u.Message.Text = "edited"
	result, err = gc.convertEdit(context.Background(), portal, nil, []*database.Message{old}, u)
	if err != nil || len(result.ModifiedParts) != 1 || !result.ModifiedParts[0].DontBridge || old.Metadata.(*MessageMetadata).EditRevisionID != "126" {
		t.Fatalf("echo wasn't suppressed while persisting revision: %+v, %v", result, err)
	}
	// Older native updates must not revert a more recent edit.
	u.RevisionID = "127"
	u.UpdatedAt--
	result, err = gc.convertEdit(context.Background(), portal, nil, []*database.Message{old}, u)
	if err != nil || len(result.ModifiedParts) != 0 {
		t.Fatalf("stale edit was applied: %+v, %v", result, err)
	}
}

func TestMatrixEditRetainsReplyAndDropsStaleMentionOffsets(t *testing.T) {
	gc := dmTestClient()
	original := testMatrixText("dm:9")
	msg := &bridgev2.MatrixEdit{MatrixEventBase: original.MatrixEventBase,
		EditTarget: &database.Message{ID: "123", SenderID: "20", Room: original.Portal.PortalKey, Timestamp: time.Now(),
			Metadata: &MessageMetadata{NativeContent: &groupmeext.MessageContent{Text: "before", Attachments: []*groupme.Attachment{
				{Type: groupme.Reply, ReplyID: "122", BaseReplyID: "122", UserID: "9"},
				{Type: "mentions"},
			}}}},
	}
	msg.Content = &event.MessageEventContent{MsgType: event.MsgText, Body: "after"}
	mockGroupMe(t, func(r *http.Request) (int, string) {
		if r.Method != "PUT" || r.URL.Path != "/v4/direct_messages/9/messages/123" {
			t.Fatalf("unexpected edit route: %s %s", r.Method, r.URL.Path)
		}
		var content groupmeext.MessageContent
		if err := json.NewDecoder(r.Body).Decode(&content); err != nil {
			t.Fatal(err)
		}
		if content.Text != "after" || len(content.Attachments) != 1 || content.Attachments[0].ReplyID != "122" {
			t.Fatalf("lost reply or retained stale text offsets: %+v", content)
		}
		return 200, `{"response":{"message":{"id":"123","user_id":"20","recipient_id":"9","conversation_id":"9+20","text":"after","attachments":[{"type":"reply","reply_id":"122"}],"updated_at":1791916613}}}`
	})
	if err := gc.HandleMatrixEdit(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if msg.EditTarget.Metadata.(*MessageMetadata).EditedAt != 1791916613 {
		t.Fatal("native revision not saved in mapping metadata")
	}
}
