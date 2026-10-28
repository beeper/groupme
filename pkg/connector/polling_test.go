package connector

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"

	"github.com/beeper/groupme-lib"
)

func pollTestClient(t *testing.T) *GMClient {
	t.Helper()
	gc := dmTestClient()
	db, err := dbutil.NewWithDialect(filepath.Join(t.TempDir(), "polls.db")+"?_foreign_keys=on", "sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	bridgeDB := database.New("", gc.Main.GetDBMetaTypes(), db)
	ctx := context.Background()
	if err = bridgeDB.Upgrade(ctx); err != nil {
		t.Fatal(err)
	}
	gc.Main.br = &bridgev2.Bridge{DB: bridgeDB}
	gc.UserLogin.Bridge = gc.Main.br
	gc.UserLogin.Metadata = gc.Meta
	gc.UserLogin.UserMXID = "@test:example.com"
	if err = bridgeDB.User.Insert(ctx, &database.User{MXID: gc.UserLogin.UserMXID}); err != nil {
		t.Fatal(err)
	}
	if err = bridgeDB.UserLogin.Insert(ctx, gc.UserLogin.UserLogin); err != nil {
		t.Fatal(err)
	}
	return gc
}

func TestPollSelectionChanges(t *testing.T) {
	poll := &groupme.PollDetail{Options: []groupme.PollOption{
		{ID: "2", VoterIDs: []string{"a", "b"}},
		{ID: "1", VoterIDs: []string{"a"}},
	}}
	current := pollSelections(poll)
	want := map[string][]string{"a": {"1", "2"}, "b": {"2"}}
	if !reflect.DeepEqual(current, want) {
		t.Fatalf("wrong selections: %v", current)
	}
	previous := map[string][]string{"a": {"1", "2"}, "b": {"1"}, "gone": {"2"}}
	changed := changedPollSelections(previous, current)
	if !reflect.DeepEqual(changed, map[string][]string{"b": {"2"}, "gone": {}}) {
		t.Fatalf("wrong changes or missing withdrawal: %v", changed)
	}
	for _, choice := range []string{"1", "2", "1"} {
		next := map[string][]string{"b": {choice}}
		if !reflect.DeepEqual(changedPollSelections(map[string][]string{"b": current["b"]}, next), next) {
			t.Fatal("lost repeated selection")
		}
		current = next
	}
	if len(changedPollSelections(current, current)) != 0 {
		t.Fatal("unchanged selection was repeated")
	}
}

func TestPollResponseAnswerIDs(t *testing.T) {
	for _, tc := range []struct {
		name            string
		mapping         map[string]string
		selection, want []string
		wantError       bool
	}{
		{"native", nil, []string{"7", "9"}, []string{"7", "9"}, false},
		{"matrix", map[string]string{"1": "7", "2": "9"}, []string{"9"}, []string{"2"}, false},
		{"withdrawal", map[string]string{"1": "7"}, nil, []string{}, false},
		{"unknown option", map[string]string{"1": "7"}, []string{"9"}, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			part, err := pollResponsePart(&database.Message{MXID: "$poll", Metadata: &MessageMetadata{PollAnswerIDs: tc.mapping}}, tc.selection)
			if (err != nil) != tc.wantError {
				t.Fatal(err)
			}
			if err != nil {
				return
			}
			if part.Type != event.EventUnstablePollResponse || part.Content.RelatesTo.Type != event.RelReference || part.Content.RelatesTo.EventID != "$poll" {
				t.Fatalf("wrong response relation: %+v", part)
			}
			encoded, _ := json.Marshal(part.Extra)
			var response event.PollResponseEventContent
			if err = json.Unmarshal(encoded, &response); err != nil || !reflect.DeepEqual(response.Response.Answers, tc.want) {
				t.Fatalf("wrong answers: %s (%v)", encoded, err)
			}
		})
	}
}

func TestPollTrackingStops(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		expiration   groupme.Timestamp
		httpStatus   int
		wantTracked  bool
		wantRequests int
	}{
		{"active", "active", 4000000000, 200, true, 1},
		{"ended", "past", 4000000000, 200, false, 1},
		{"unknown status", "unknown", 4000000000, 200, false, 1},
		{"expired", "active", 1, 200, false, 0},
		{"not found", "active", 4000000000, 404, false, 1},
		{"forbidden", "active", 4000000000, 403, false, 1},
		{"rate limited", "active", 4000000000, 429, false, 1},
		{"server error", "active", 4000000000, 500, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gc := pollTestClient(t)
			gc.Meta.ActivePolls = map[string]*ActivePoll{"poll-1": {GroupID: "88", Expiration: tc.expiration, Selections: map[string][]string{}}}
			calls := 0
			mockGroupMe(t, func(r *http.Request) (int, string) {
				calls++
				body, _ := json.Marshal(map[string]any{"response": map[string]any{"poll": map[string]any{"data": groupme.PollDetail{ID: "poll-1", ConversationID: "88", Status: tc.status, Expiration: tc.expiration}}}})
				return tc.httpStatus, string(body)
			})
			gc.refreshPoll(context.Background(), "poll-1")
			stored, err := gc.Main.br.DB.UserLogin.GetByID(context.Background(), gc.UserLogin.ID)
			if err != nil {
				t.Fatal(err)
			}
			tracked := stored.Metadata.(*UserLoginMetadata).ActivePolls["poll-1"] != nil
			if tc.httpStatus >= 500 {
				tracked = gc.Meta.ActivePolls["poll-1"] != nil
			}
			if calls != tc.wantRequests || tracked != tc.wantTracked {
				t.Fatalf("requests=%d tracked=%v", calls, tracked)
			}
		})
	}
	if pollIsGone(errors.New("network failure")) || pollIsActive(&groupme.PollDetail{Status: "active"}, time.Now()) {
		t.Fatal("network failures or missing expiration were misclassified")
	}
}

func TestPollFetchOverlappingMatrixVote(t *testing.T) {
	gc := pollTestClient(t)
	tracked := &ActivePoll{GroupID: "88", Expiration: 4000000000, Selections: map[string][]string{}}
	gc.Meta.ActivePolls = map[string]*ActivePoll{"poll-1": tracked}
	mockGroupMe(t, func(r *http.Request) (int, string) {
		gc.pollsMu.Lock()
		tracked.Selections["20"] = []string{"2"}
		tracked.revision++
		gc.pollsMu.Unlock()
		return 200, pollResponse
	})
	gc.refreshPoll(context.Background(), "poll-1")
	if !reflect.DeepEqual(tracked.Selections, map[string][]string{"20": {"2"}}) {
		t.Fatal("stale poll fetch overwrote a Matrix vote")
	}
	tracked.voting = true
	mockGroupMe(t, func(r *http.Request) (int, string) {
		t.Fatal("fetched poll while a Matrix vote was in flight")
		return 0, ""
	})
	gc.refreshPoll(context.Background(), "poll-1")
}
