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
			part := convertGroupMePollEvent(context.Background(), dmTestClient().Client, msg)
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
			result, err := dmTestClient().HandleMatrixPollVote(context.Background(), msg)
			if calls != 2 || (err != nil) != tc.wantError {
				t.Fatalf("calls=%d error=%v result=%+v", calls, err, result)
			}
		})
	}
}
