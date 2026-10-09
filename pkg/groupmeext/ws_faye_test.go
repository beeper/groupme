package groupmeext

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/beeper/groupme-lib"
)

type nativeReaction struct {
	message  groupme.Message
	userID   groupme.ID
	reaction *groupme.Reaction
}

type pushHandler struct {
	messages  []groupme.Message
	reactions []nativeReaction
	snapshots []groupme.Message
}

func (h *pushHandler) HandleTextMessage(m groupme.Message) { h.messages = append(h.messages, m) }
func (h *pushHandler) HandleError(error)                   {}
func (h *pushHandler) HandleLike(m groupme.Message)        { h.snapshots = append(h.snapshots, m) }
func (h *pushHandler) HandleReaction(msg groupme.Message, userID groupme.ID, reaction *groupme.Reaction) {
	h.reactions = append(h.reactions, nativeReaction{msg, userID, reaction})
}

// Catches dropping DM favorites, reading stale nested reactions instead of the
// authoritative sibling snapshot, and confusing missing state with removal.
func TestPushFavoriteResponses(t *testing.T) {
	for _, tc := range []struct {
		name, body    string
		wantCount     int
		wantReactions []groupme.Reaction
	}{
		{"group", `{"line":{"id":"one","group_id":"88","user_id":"9","reactions":[{"type":"unicode","code":"❤️","user_ids":["9"]}]},"reactions":[{"type":"unicode","code":"👍","user_ids":["20","30"]}]}`, 1, []groupme.Reaction{{Type: "unicode", Code: "👍", UserIDs: []string{"20", "30"}}}},
		{"DM", `{"direct_message":{"id":"one","chat_id":"9+20","user_id":"9"},"reactions":[{"type":"unicode","code":"🔥","user_ids":["20"]}]}`, 1, []groupme.Reaction{{Type: "unicode", Code: "🔥", UserIDs: []string{"20"}}}},
		{"empty overrides stale likes", `{"line":{"id":"one","group_id":"88","favorited_by":["20"]},"reactions":[]}`, 1, []groupme.Reaction{}},
		{"legacy nested reactions", `{"line":{"id":"one","group_id":"88","reactions":[{"type":"unicode","code":"👍","user_ids":["20"]}]}}`, 1, []groupme.Reaction{{Type: "unicode", Code: "👍", UserIDs: []string{"20"}}}},
		{"legacy favorites", `{"line":{"id":"one","group_id":"88","favorited_by":["20"]}}`, 1, nil},
		{"missing state", `{"line":{"id":"one","group_id":"88"}}`, 0, nil},
		{"null state", `{"line":{"id":"one","group_id":"88"},"reactions":null}`, 0, nil},
		{"missing message", `{"reactions":[]}`, 0, nil},
		{"malformed state", `{"line":{"id":"one"},"reactions":{}}`, 0, nil},
		{"malformed envelope", `[]`, 0, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var subject any
			if err := json.Unmarshal([]byte(tc.body), &subject); err != nil {
				t.Fatal(err)
			}
			handler := &pushHandler{}
			sub := groupme.NewPushSubscription(context.Background())
			sub.AddHandler(handler)
			groupme.RealTimeHandlers["favorite"](sub, "/group/88", subject)
			if len(handler.snapshots) != tc.wantCount {
				t.Fatalf("got %d snapshots, want %d", len(handler.snapshots), tc.wantCount)
			}
			if tc.wantCount == 0 {
				return
			}
			got := handler.snapshots[0]
			if got.ID != "one" || !reflect.DeepEqual(got.Reactions, tc.wantReactions) {
				t.Fatalf("incorrect snapshot: %+v", got)
			}
			if tc.name == "DM" {
				if got.ChatID != "9+20" {
					t.Fatal("lost DM conversation identity")
				}
			} else if got.GroupID != "88" {
				t.Fatal("lost group identity")
			}
			if tc.name == "legacy favorites" && !reflect.DeepEqual(got.FavoritedBy, []string{"20"}) {
				t.Fatal("lost legacy reactor")
			}
		})
	}
}

// Catches dropping modern reaction pushes, using the message author as the
// reactor, and treating removal as an empty snapshot for every participant.
func TestPushReactionResponses(t *testing.T) {
	for _, tc := range []struct {
		name, eventType, body, wantEmoji string
		wantCount                        int
	}{
		{"add", "like.create", `{"line":{"id":"one","group_id":"88","user_id":"9","favorited_by":["20","30"]},"user_id":"20","user_reaction":{"type":"unicode","code":"👍","user_ids":["20"]},"reactions":[{"type":"unicode","code":"🔥","user_ids":["30"]},{"type":"unicode","code":"👍","user_ids":["20"]}]}`, "👍", 1},
		{"change DM", "like.create", `{"direct_message":{"id":"one","chat_id":"9+20","user_id":"9"},"user_id":"20","user_reaction":{"type":"unicode","code":"🔥","user_ids":["20"]}}`, "🔥", 1},
		{"remove DM", "like.delete", `{"direct_message":{"id":"one","chat_id":"9+20","user_id":"9","favorited_by":["30"]},"user_id":"20"}`, "", 1},
		{"remove only reactor", "like.delete", `{"line":{"id":"one","group_id":"88","user_id":"9","favorited_by":["30"]},"user_id":"20"}`, "", 1},
		{"missing reactor", "like.create", `{"line":{"id":"one"},"user_reaction":{"type":"unicode","code":"👍"}}`, "", 0},
		{"missing target", "like.delete", `{"user_id":"20"}`, "", 0},
		{"missing reaction", "like.create", `{"line":{"id":"one"},"user_id":"20"}`, "", 0},
		{"custom emoji", "like.create", `{"line":{"id":"one"},"user_id":"20","user_reaction":{"type":"emoji","pack_id":1,"pack_index":2}}`, "", 0},
		{"malformed", "like.delete", `[]`, "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var subject any
			if err := json.Unmarshal([]byte(tc.body), &subject); err != nil {
				t.Fatal(err)
			}
			handler := &pushHandler{}
			sub := groupme.NewPushSubscription(context.Background())
			sub.AddHandler(handler)
			groupme.RealTimeHandlers[tc.eventType](sub, "/user/9", subject)
			if len(handler.reactions) != tc.wantCount {
				t.Fatalf("got %d reaction changes, want %d", len(handler.reactions), tc.wantCount)
			}
			if tc.wantCount == 0 {
				return
			}
			got := handler.reactions[0]
			if got.userID != "20" || got.message.ID != "one" || got.message.UserID != "9" {
				t.Fatalf("lost native reactor or target identity: %+v", got)
			}
			if tc.wantEmoji == "" {
				if got.reaction != nil || len(got.message.FavoritedBy) != 1 || got.message.FavoritedBy[0] != "30" {
					t.Fatal("removal lost the remaining reactor or was parsed as an addition")
				}
			} else if got.reaction == nil || got.reaction.Code != tc.wantEmoji {
				t.Fatalf("lost native emoji: %+v", got.reaction)
			}
			if (tc.name == "change DM" || tc.name == "remove DM") && got.message.ChatID != "9+20" {
				t.Fatal("lost DM conversation identity")
			}
		})
	}
}

func TestPushMessageResponses(t *testing.T) {
	for _, tc := range []struct {
		name, body   string
		wantMessages int
	}{
		{"message", `{"id":"one","user_id":"9","group_id":"88","text":"hello"}`, 1},
		{"DM", `{"id":"two","user_id":"9","chat_id":"9+20","text":"hello"}`, 1},
		{"system poll", `{"id":"three","user_id":"system","group_id":"88","event":{"type":"poll.created","data":{}}}`, 1},
		{"missing identity", `{"text":"invalid"}`, 0},
		{"malformed payload", `[]`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var subject any
			if err := json.Unmarshal([]byte(tc.body), &subject); err != nil {
				t.Fatal(err)
			}
			handler := &pushHandler{}
			sub := groupme.NewPushSubscription(context.Background())
			sub.AddHandler(handler)
			groupme.RealTimeHandlers["line.create"](sub, "/user/20", subject)
			if len(handler.messages) != tc.wantMessages {
				t.Fatalf("got %d native messages", len(handler.messages))
			}
			if tc.name == "DM" && handler.messages[0].ConversationID != "9+20" {
				t.Fatal("lost native DM conversation")
			}
		})
	}
}
