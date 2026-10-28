package connector

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"

	"github.com/beeper/groupme-lib"
)

const pollResponse = `{"response":{"poll":{"data":{"id":"poll-1","conversation_id":"88","subject":"Choose","status":"active","type":"single","visibility":"anonymous","expiration":4000000000,"options":[{"id":"1","title":"A"},{"id":"2","title":"B"}]},"user_votes":["2"]}},"meta":{"code":200}}`

func TestPollConversionRemoteResponses(t *testing.T) {
	for _, tc := range []struct {
		name, response string
		status         int
		selections     int
	}{
		{"single", pollResponse, 200, 1},
		{"multi", strings.Replace(pollResponse, `"type":"single"`, `"type":"multi"`, 1), 200, 2},
		{"wrong poll", strings.Replace(pollResponse, `"id":"poll-1"`, `"id":"other"`, 1), 200, 0},
		{"ended", strings.Replace(pollResponse, `"status":"active"`, `"status":"past"`, 1), 200, 0},
		{"missing option ID", strings.Replace(pollResponse, `"id":"2",`, "", 1), 200, 0},
		{"rate limited", `{"meta":{"code":429}}`, 429, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mockGroupMe(t, func(req *http.Request) (int, string) {
				if req.Method != "GET" || req.URL.Path != "/v3/poll/88/poll-1" {
					t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
				}
				return tc.status, tc.response
			})
			msg := &groupme.Message{GroupID: "88", Event: &groupme.Event{Type: "poll.created", Data: json.RawMessage(`{"conversation":{"id":"88"},"poll":{"id":"poll-1","subject":"Choose"}}`)}}
			part := pollTestClient(t).convertGroupMePollEvent(context.Background(), msg)
			if part == nil {
				t.Fatal("poll disappeared")
			}
			if tc.selections == 0 {
				if part.Type != event.EventMessage || !strings.Contains(part.Content.Body, "Choose") || part.DBMetadata != nil {
					t.Fatalf("invalid remote response produced an interactive poll: %+v", part)
				}
				return
			}
			poll, ok := part.Extra["org.matrix.msc3381.poll.start"].(event.PollStart)
			if !ok || part.Type != event.EventUnstablePollStart || poll.MaxSelections != tc.selections || len(poll.Answers) != 2 || poll.Answers[1].ID != "2" || part.DBMetadata.(*MessageMetadata).PollID != "poll-1" {
				t.Fatalf("incorrect native poll conversion: %+v", part)
			}
		})
	}
}

func TestPollVoteRemoteResponses(t *testing.T) {
	for _, tc := range []struct {
		name, kind, response string
		answers              []string
		status               int
		wantError            bool
	}{
		{"single", "single", pollResponse, []string{"2"}, 200, false},
		{"single user_vote envelope", "single", strings.Replace(pollResponse, `"user_votes":["2"]`, `"user_vote":"2"`, 1), []string{"2"}, 200, false},
		{"multiple", "multi", strings.Replace(pollResponse, `"user_votes":["2"]`, `"user_votes":["2","1"]`, 1), []string{"1", "2"}, 200, false},
		{"multiple server error", "multi", `{"meta":{"code":500,"errors":[]}}`, []string{"1", "2"}, 500, true},
		{"wrong selection", "single", strings.Replace(pollResponse, `"user_votes":["2"]`, `"user_votes":["1"]`, 1), []string{"2"}, 200, true},
		{"wrong conversation", "single", strings.Replace(pollResponse, `"conversation_id":"88"`, `"conversation_id":"99"`, 1), []string{"2"}, 200, true},
		{"malformed", "single", `{`, []string{"2"}, 200, true},
		{"rejected", "single", `{"meta":{"code":403,"errors":["poll ended"]}}`, []string{"2"}, 403, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			mockGroupMe(t, func(req *http.Request) (int, string) {
				calls++
				if calls == 1 {
					if req.Method != "GET" || req.URL.Path != "/v3/poll/88/poll-1" {
						t.Fatalf("unexpected poll fetch: %s %s", req.Method, req.URL.Path)
					}
					return 200, strings.Replace(pollResponse, `"type":"single"`, `"type":"`+tc.kind+`"`, 1)
				}
				if req.Method != "POST" {
					t.Fatalf("unexpected method %s", req.Method)
				}
				if tc.kind == "single" {
					if req.URL.Path != "/v3/poll/88/poll-1/2" {
						t.Fatalf("wrong native choice path: %s", req.URL.Path)
					}
				} else {
					body, _ := io.ReadAll(req.Body)
					if req.URL.Path != "/v3/poll/88/poll-1" || string(body) != `{"votes":["1","2"]}` || req.Header.Get("Content-Type") != "application/json" {
						t.Fatalf("wrong multiple choice request: %s %s", req.URL.Path, body)
					}
				}
				return tc.status, tc.response
			})
			base := testMatrixText("group:88")
			msg := &bridgev2.MatrixPollVote{MatrixMessage: *base, VoteTo: &database.Message{Room: base.Portal.PortalKey, Metadata: &MessageMetadata{PollID: "poll-1"}}, Content: &event.PollResponseEventContent{Response: event.PollResponse{Answers: tc.answers}}}
			client := pollTestClient(t)
			result, err := client.HandleMatrixPollVote(context.Background(), msg)
			if calls != 2 || (err != nil) != tc.wantError {
				t.Fatalf("calls=%d error=%v result=%+v", calls, err, result)
			}
			if err == nil {
				stored, saveErr := client.Main.br.DB.UserLogin.GetByID(context.Background(), client.UserLogin.ID)
				if saveErr != nil {
					t.Fatal(saveErr)
				}
				want := slices.Clone(tc.answers)
				slices.Sort(want)
				if !slices.Equal(stored.Metadata.(*UserLoginMetadata).ActivePolls.polls["poll-1"].Selections["20"], want) || result.DB.ID != "poll-vote:$outgoing-test" || result.DB.Timestamp != msg.VoteTo.Timestamp.Add(-time.Nanosecond) {
					t.Fatalf("vote selection or mapping was not preserved: %+v", result.DB)
				}
			}
		})
	}
}

func TestCreatedPollAnswerIDs(t *testing.T) {
	for _, tc := range []struct {
		name, kind              string
		ids, votes, nativeVotes []string
	}{
		{"single", "single", []string{"1", "2"}, []string{"2"}, []string{"9"}},
		{"multi", "multi", []string{"a7f", "q2x", "k9z"}, []string{"a7f", "k9z"}, []string{"7", "11"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := event.PollStart{Question: event.MSC1767Message{Text: "Choose"}, MaxSelections: 1}
			native := groupme.PollDetail{ID: "poll-1", ConversationID: "88", CreatedAt: 1793204604, Expiration: 4000000000, Status: "active", Type: tc.kind}
			for i, id := range tc.ids {
				start.Answers = append(start.Answers, event.PollOption{ID: id, MSC1767Message: event.MSC1767Message{Text: id}})
				native.Options = append(native.Options, groupme.PollOption{ID: []string{"7", "9", "11"}[i], Title: id})
			}
			if tc.kind == "multi" {
				start.MaxSelections = 3
			}
			calls := 0
			mockGroupMe(t, func(r *http.Request) (int, string) {
				calls++
				switch calls {
				case 1:
					var request groupme.CreatePollRequest
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Fatal(err)
					}
					lifetime := time.Until(request.Expiration.ToTime())
					if r.Method != "POST" || r.URL.Path != "/v3/poll/88" || request.Subject != "Choose" || request.Type != tc.kind || request.Visibility != "public" || lifetime > 14*24*time.Hour-time.Minute || lifetime < 14*24*time.Hour-2*time.Minute {
						t.Fatalf("unexpected create request: %+v", request)
					}
					for i, option := range request.Options {
						if option.ID != "" || option.Title != tc.ids[i] {
							t.Fatalf("unexpected option: %+v", option)
						}
					}
				case 2:
					if r.Method != "GET" || r.URL.Path != "/v3/poll/88/poll-1" {
						t.Fatalf("unexpected fetch: %s %s", r.Method, r.URL.Path)
					}
				case 3:
					if r.Method != "POST" {
						t.Fatal(r.Method)
					}
					if tc.kind == "single" {
						if r.URL.Path != "/v3/poll/88/poll-1/9" {
							t.Fatal(r.URL.Path)
						}
					} else {
						var body struct {
							Votes []string `json:"votes"`
						}
						_ = json.NewDecoder(r.Body).Decode(&body)
						if r.URL.Path != "/v3/poll/88/poll-1" || !slices.Equal(body.Votes, tc.nativeVotes) {
							t.Fatalf("wrong vote: %s %+v", r.URL.Path, body)
						}
					}
				default:
					t.Fatal("unexpected request")
				}
				response, _ := json.Marshal(map[string]any{"response": map[string]any{"poll": map[string]any{"data": native, "user_votes": tc.nativeVotes}, "message": map[string]any{"id": "announcement-1"}}})
				return 201, string(response)
			})
			client := pollTestClient(t)
			base := testMatrixText("group:88")
			created, err := client.HandleMatrixPollStart(context.Background(), &bridgev2.MatrixPollStart{MatrixMessage: *base, Content: &event.PollStartEventContent{PollStart: start}})
			if err != nil {
				t.Fatal(err)
			}
			if created.DB.ID != "announcement-1" {
				t.Fatalf("wrong echo identity: %s", created.DB.ID)
			}
			persisted, err := json.Marshal(created.DB.Metadata)
			if err != nil {
				t.Fatal(err)
			}
			var metadata MessageMetadata
			if err = json.Unmarshal(persisted, &metadata); err != nil {
				t.Fatal(err)
			}
			if metadata.PollID != "poll-1" || len(metadata.PollAnswerIDs) != len(tc.ids) {
				t.Fatalf("incorrect persisted mapping: %s", persisted)
			}
			created.DB.Room = base.Portal.PortalKey
			created.DB.Metadata = &metadata
			vote := &bridgev2.MatrixPollVote{MatrixMessage: *base, VoteTo: created.DB, Content: &event.PollResponseEventContent{Response: event.PollResponse{Answers: tc.votes}}}
			if _, err = client.HandleMatrixPollVote(context.Background(), vote); err != nil {
				t.Fatal(err)
			}
			if calls != 3 || !slices.Equal(vote.Content.Response.Answers, tc.votes) {
				t.Fatalf("calls=%d, mutated Matrix answers: %v", calls, vote.Content.Response.Answers)
			}
		})
	}
}
