package connector

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
)

func TestOutgoingReadReceiptVisibility(t *testing.T) {
	for _, tc := range []struct {
		name      string
		portal    networkid.PortalID
		implicit  bool
		foreign   bool
		stale     bool
		status    int
		wantPaths []string
		wantError bool
	}{
		{"explicit DM", "dm:9", false, false, false, 200, []string{"/v4/read_receipts/9%2B20", "/v3/conversations/9+20/read_receipt"}, false},
		{"implicit DM", "dm:9", true, false, false, 200, []string{"/v4/read_receipts/9%2B20"}, false},
		{"group", "group:7", false, false, false, 200, []string{"/v4/read_receipts/7"}, false},
		{"foreign target", "dm:9", false, true, false, 200, nil, true},
		{"older read", "dm:9", false, false, true, 200, nil, false},
		{"checkpoint failure", "dm:9", false, false, false, 429, []string{"/v4/read_receipts/9%2B20"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			portal := testMatrixText(tc.portal).Portal
			target := &database.Message{ID: "123", Room: portal.PortalKey, Timestamp: time.Unix(1000, 0)}
			if tc.foreign {
				target.Room.ID = "dm:30"
			}
			msg := &bridgev2.MatrixReadReceipt{Portal: portal, ExactMessage: target, Implicit: tc.implicit}
			if tc.stale {
				msg.LastRead = target.Timestamp.Add(time.Second)
			}
			var paths []string
			mockGroupMe(t, func(r *http.Request) (int, string) {
				if r.Method != http.MethodPost {
					t.Fatalf("unexpected method: %s", r.Method)
				}
				paths = append(paths, r.URL.EscapedPath())
				return tc.status, ""
			})
			err := dmTestClient().HandleMatrixReadReceipt(context.Background(), msg)
			if (err != nil) != tc.wantError || !reflect.DeepEqual(paths, tc.wantPaths) {
				t.Fatalf("receipt paths=%v error=%v", paths, err)
			}
		})
	}
}

func TestIncomingSeenReceiptIdentity(t *testing.T) {
	for _, tc := range []struct {
		sender, chat string
		want         bool
		own          bool
	}{
		{"9", "9+20", true, false},
		{"20", "9+20", true, true},
		{"30", "9+20", false, false},
		{"9", "9+30", false, false},
	} {
		t.Run(tc.sender+"/"+tc.chat, func(t *testing.T) {
			events, handled := dmTestClient().parseActivity(activityPush{"/user/20", map[string]interface{}{
				"type": "read_receipt.create", "subject": map[string]interface{}{
					"chat_id": tc.chat, "user_id": tc.sender, "message_id": "123", "read_at": 1000,
				},
			}})
			if !handled || (len(events) == 1) != tc.want {
				t.Fatalf("unexpected receipt routing: %+v", events)
			}
			if tc.want {
				r := events[0].(*nativeReceipt)
				if r.PortalKey.ID != "dm:9" || r.PortalKey.Receiver != "20" || r.Sender.IsFromMe != tc.own || string(r.Sender.Sender) != tc.sender || r.LastTarget != "123" || r.Timestamp.Unix() != 1000 {
					t.Fatalf("lost receipt identity: %+v", r.Receipt)
				}
			}
		})
	}
}

func TestReadCheckpointSyncIsAccountScoped(t *testing.T) {
	data := map[string]interface{}{"type": "read_receipt.sync", "subject": map[string]interface{}{"receipts": []map[string]string{
		{"conversation_id": "9+20", "last_read_message_id": "123"},
		{"conversation_id": "7", "last_read_message_id": "124"},
		{"conversation_id": "9+30", "last_read_message_id": "125"},
		{"conversation_id": "8", "last_read_message_id": ""},
	}}}
	gc := dmTestClient()
	events, handled := gc.parseActivity(activityPush{"/user/20", data})
	if !handled || len(events) != 2 {
		t.Fatalf("unexpected checkpoints: %+v", events)
	}
	for i, portal := range []networkid.PortalID{"dm:9", "group:7"} {
		r := events[i].(*nativeReceipt)
		if r.PortalKey.ID != portal || r.PortalKey.Receiver != "20" || !r.Sender.IsFromMe || r.Sender.Sender != "20" {
			t.Fatalf("checkpoint belongs to the wrong account: %+v", r.Receipt)
		}
	}
	if events, handled = gc.parseActivity(activityPush{"/user/30", data}); !handled || len(events) != 0 {
		t.Fatal("accepted another account's checkpoints")
	}
}

func TestReadCursorResolvesHiddenEditsToVisibleMessages(t *testing.T) {
	ctx := context.Background()
	db, err := dbutil.NewWithDialect(filepath.Join(t.TempDir(), "receipts.db")+"?_foreign_keys=on", "sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	gc := dmTestClient()
	bridgeDB := database.New("", gc.Main.GetDBMetaTypes(), db)
	if err = bridgeDB.Upgrade(ctx); err != nil {
		t.Fatal(err)
	}
	if err = bridgeDB.Ghost.Insert(ctx, &database.Ghost{ID: "9"}); err != nil {
		t.Fatal(err)
	}
	portal := testMatrixText("dm:9").Portal
	portal.Bridge = &bridgev2.Bridge{DB: bridgeDB}
	otherPortal, otherAccount := portal.PortalKey, portal.PortalKey
	otherPortal.ID, otherAccount.Receiver = "dm:30", "30"
	for _, key := range []networkid.PortalKey{portal.PortalKey, otherPortal, otherAccount} {
		if err = bridgeDB.Portal.Insert(ctx, &database.Portal{PortalKey: key}); err != nil {
			t.Fatal(err)
		}
	}
	for _, msg := range []*database.Message{
		{ID: "107", Room: otherAccount, MXID: "$other-account"},
		{ID: "105", Room: portal.PortalKey, MXID: "~fake:vote"},
		{ID: "104", Room: otherPortal, MXID: "$other-room"},
		{ID: "103", Room: portal.PortalKey, MXID: "$visible"},
	} {
		msg.SenderID = "9"
		if err = bridgeDB.Message.Insert(ctx, msg); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		target, want networkid.MessageID
		requests     int
	}{
		{"103", "103", 0},
		{"109", "103", 2},
		{"110", "110", 1},
	} {
		t.Run(string(tc.target), func(t *testing.T) {
			calls := 0
			mockGroupMe(t, func(r *http.Request) (int, string) {
				calls++
				var messages string
				switch r.URL.Query().Get("before_id") {
				case "":
					messages = `{"id":"110","user_id":"9","chat_id":"9+20"},{"id":"109","user_id":"system","chat_id":"9+20","event":{"type":"message.update"}},{"id":"108","user_id":"system","chat_id":"9+20","event":{"type":"message.update"}}`
				case "108":
					messages = `{"id":"107","user_id":"9","chat_id":"9+20"},{"id":"105","user_id":"9","chat_id":"9+20"},{"id":"104","user_id":"9","chat_id":"9+20"},{"id":"103","user_id":"9","chat_id":"9+20"}`
				default:
					t.Fatalf("unexpected history cursor: %s", r.URL.RawQuery)
				}
				return 200, fmt.Sprintf(`{"response":{"direct_messages":[%s]},"meta":{"code":200}}`, messages)
			})
			r := &nativeReceipt{client: gc, Receipt: &simplevent.Receipt{LastTarget: tc.target}}
			r.PreHandle(ctx, portal)
			if r.LastTarget != tc.want || calls != tc.requests {
				t.Fatalf("resolved cursor=%s requests=%d", r.LastTarget, calls)
			}
		})
	}
}

func TestReadCursorLimitsHistoryScan(t *testing.T) {
	ctx := context.Background()
	db, err := dbutil.NewWithDialect(filepath.Join(t.TempDir(), "receipts.db")+"?_foreign_keys=on", "sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	gc := dmTestClient()
	bridgeDB := database.New("", gc.Main.GetDBMetaTypes(), db)
	if err = bridgeDB.Upgrade(ctx); err != nil {
		t.Fatal(err)
	}
	for _, portalID := range []networkid.PortalID{"group:7", "dm:9"} {
		for _, target := range []networkid.MessageID{"1", "1000"} {
			t.Run(fmt.Sprintf("%s/target=%s", portalID, target), func(t *testing.T) {
				portal := testMatrixText(portalID).Portal
				portal.Bridge = &bridgev2.Bridge{DB: bridgeDB}
				field, identity, pageSize := "messages", `"group_id":"7"`, 100
				if portalID == "dm:9" {
					field, identity, pageSize = "direct_messages", `"chat_id":"9+20"`, 20
				}
				calls := 0
				mockGroupMe(t, func(r *http.Request) (int, string) {
					calls++
					if calls > 2 {
						t.Fatal("receipt lookup continued beyond two pages")
					}
					if calls == 2 && r.URL.Query().Get("before_id") != fmt.Sprint(1001-pageSize) {
						t.Fatalf("unexpected cursor: %s", r.URL.RawQuery)
					}
					var messages []string
					for i := 0; i < pageSize; i++ {
						id := 1000 - (calls-1)*pageSize - i
						extra := ""
						if id == 1000 {
							extra = `,"event":{"type":"message.update"}`
						}
						messages = append(messages, fmt.Sprintf(`{"id":"%d","user_id":"9",%s%s}`, id, identity, extra))
					}
					return 200, fmt.Sprintf(`{"response":{"%s":[%s]},"meta":{"code":200}}`, field, strings.Join(messages, ","))
				})
				receipt := &nativeReceipt{client: gc, Receipt: &simplevent.Receipt{LastTarget: target}}
				receipt.PreHandle(ctx, portal)
				if calls != 2 || receipt.LastTarget != target {
					t.Fatalf("unresolved receipt target=%s requests=%d", receipt.LastTarget, calls)
				}
			})
		}
	}
}
