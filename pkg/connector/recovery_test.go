package connector

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/beeper/groupme-lib"
	"github.com/beeper/groupme/pkg/groupmeext"
	_ "github.com/mattn/go-sqlite3"
	"github.com/rs/zerolog"
	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

func openPollTestStore(t *testing.T, path string) *pollStore {
	t.Helper()
	db, err := dbutil.NewWithDialect("file:"+path+"?_foreign_keys=on", "sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	db.RawDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	_, err = db.Exec(ctx, `CREATE TABLE IF NOT EXISTS user_login (bridge_id TEXT NOT NULL, id TEXT NOT NULL, PRIMARY KEY(bridge_id,id))`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(ctx, `INSERT INTO user_login VALUES ('','alice'), ('','bob') ON CONFLICT DO NOTHING`)
	if err != nil {
		t.Fatal(err)
	}
	store := newPollStore(db, "", zerolog.Nop())
	if err = store.Upgrade(ctx); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestPollDeliveryRequiresCompletionForQueuedAndInlineEvents(t *testing.T) {
	for _, result := range []bridgev2.EventHandlingResult{bridgev2.EventHandlingResultQueued, bridgev2.EventHandlingResultIgnored, bridgev2.EventHandlingResultSuccess} {
		done := make(chan error, 1)
		done <- nil
		if err := waitPollDelivery(context.Background(), result, done, "message"); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan error, 1)
	if err := waitPollDelivery(context.Background(), bridgev2.EventHandlingResultIgnored, done, "filtered"); err == nil {
		t.Fatal("accepted a dropped event without a mapping check")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitPollDelivery(ctx, bridgev2.EventHandlingResultQueued, done, "queued"); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation: %v", err)
	}
	done <- io.ErrUnexpectedEOF
	if err := waitPollDelivery(context.Background(), bridgev2.EventHandlingResultQueued, done, "failed"); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("lost delivery failure: %v", err)
	}
}

func loadPollTestState(t *testing.T, db *pollStore, login networkid.UserLoginID, portal networkid.PortalID, anchor int) *pollState {
	t.Helper()
	s, err := db.load(context.Background(), login, portal, groupme.ID(strconv.Itoa(anchor)), groupme.Timestamp(1000+anchor/3))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func testPollPage(end, limit int) []*groupme.Message {
	var page []*groupme.Message
	for i := end; i >= 1 && len(page) < limit; i-- {
		page = append(page, &groupme.Message{ID: groupme.ID(strconv.Itoa(i)), CreatedAt: groupme.Timestamp(1000 + i/3), Text: fmt.Sprint(i)})
	}
	return page
}

func testPollFetch(ctx context.Context, before groupme.ID) ([]*groupme.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	n, err := strconv.Atoi(string(before))
	if err != nil {
		return nil, err
	}
	return testPollPage(n-1, 20), nil
}

func TestRecoveryResumesAcrossRestartAndSendFailure(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "poll.db")
	db := openPollTestStore(t, path)
	s := loadPollTestState(t, db, "alice", "dm:friend", 1)
	var delivered []int
	durableMappings := map[int]bool{1: true}
	failed := false
	send := func(ctx context.Context, msg *groupme.Message) error {
		n, _ := strconv.Atoi(string(msg.ID))
		if !durableMappings[n] {
			durableMappings[n] = true
			delivered = append(delivered, n)
		}
		// Simulate death after Matrix persisted the mapping, before deleting
		// the pending record. The replay must use the same native ID.
		if n == 50 && !failed {
			failed = true
			return io.ErrUnexpectedEOF
		}
		return nil
	}
	if err := recoverPollMessages(ctx, db, s, testPollPage(145, 20), 20, testPollFetch, send); err != nil {
		t.Fatal(err)
	}
	if len(delivered) != 0 {
		t.Fatal("delivered before the backwards scan reached the anchor")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = openPollTestStore(t, path)
	// A newer live event has arrived, but cannot move the recovery anchor.
	s = loadPollTestState(t, db, "alice", "dm:friend", 170)
	if s.anchorID != "1" || s.head != "145" || s.before != "66" {
		t.Fatalf("lost recovery progress: %+v", s)
	}
	for i := 0; i < 10; i++ {
		s = loadPollTestState(t, db, "alice", "dm:friend", 170)
		if s.anchorID == "170" && s.head == "" {
			break
		}
		err := recoverPollMessages(ctx, db, s, testPollPage(170, 20), 20, testPollFetch, send)
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatal(err)
		}
		if errors.Is(err, io.ErrUnexpectedEOF) {
			checkpoint := loadPollTestState(t, db, "alice", "dm:friend", 170)
			if checkpoint.anchorID != "1" {
				t.Fatal("checkpoint advanced past failed delivery")
			}
			_ = db.Close()
			db = openPollTestStore(t, path)
		}
	}
	s = loadPollTestState(t, db, "alice", "dm:friend", 170)
	if s.anchorID != "170" || s.head != "" || len(delivered) != 169 {
		t.Fatalf("incomplete recovery: state=%+v delivered=%d", s, len(delivered))
	}
	for i, n := range delivered {
		if n != i+2 {
			t.Fatalf("missing, reordered or duplicated message: at %d got %d", i, n)
		}
	}
}

func TestRecoveryFetchFailureCancellationAndNoProgress(t *testing.T) {
	for _, failure := range []string{"rate limit", "cancel", "repeated page"} {
		t.Run(failure, func(t *testing.T) {
			db := openPollTestStore(t, filepath.Join(t.TempDir(), "poll.db"))
			s := loadPollTestState(t, db, "alice", "group:g", 1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			fetch := func(ctx context.Context, before groupme.ID) ([]*groupme.Message, error) {
				calls++
				switch failure {
				case "rate limit":
					return nil, &groupme.Meta{Code: 429}
				case "cancel":
					cancel()
					return nil, ctx.Err()
				default:
					return testPollPage(100, 20), nil
				}
			}
			if err := recoverPollMessages(ctx, db, s, testPollPage(100, 20), 20, fetch, func(context.Context, *groupme.Message) error { t.Fatal("sent before completing scan"); return nil }); err == nil {
				t.Fatal("expected recovery failure")
			}
			s = loadPollTestState(t, db, "alice", "group:g", 100)
			if calls != 1 || s.anchorID != "1" || s.before != "81" || s.scanned {
				t.Fatalf("bad checkpoint after failure: %+v calls=%d", s, calls)
			}
			pending, err := db.pending(context.Background(), s, 100)
			if err != nil {
				t.Fatal(err)
			}
			if len(pending) != 20 {
				t.Fatalf("lost staged messages: %d", len(pending))
			}
		})
	}
}

func TestRecoveryGroupPagesAndDeliveryLimit(t *testing.T) {
	ctx := context.Background()
	db := openPollTestStore(t, filepath.Join(t.TempDir(), "poll.db"))
	s := loadPollTestState(t, db, "alice", "group:g", 1)
	fetched, delivered := 0, 0
	fetch := func(_ context.Context, before groupme.ID) ([]*groupme.Message, error) {
		fetched++
		n, err := strconv.Atoi(string(before))
		return testPollPage(n-1, 100), err
	}
	send := func(_ context.Context, msg *groupme.Message) error {
		delivered++
		if string(msg.ID) != strconv.Itoa(delivered+1) {
			t.Fatalf("out-of-order delivery: %s", msg.ID)
		}
		return nil
	}
	for pass, want := range []int{100, 200, 249} {
		if err := recoverPollMessages(ctx, db, s, testPollPage(250, 20), 100, fetch, send); err != nil {
			t.Fatal(err)
		}
		s = loadPollTestState(t, db, "alice", "group:g", 1)
		if delivered != want || fetched != 3 {
			t.Fatalf("pass %d: delivered %d, fetched %d", pass, delivered, fetched)
		}
		if (s.anchorID == "250") != (pass == 2) {
			t.Fatalf("incorrect checkpoint: %+v", s)
		}
	}
}

func TestRecoveryNewConversationStartsWithLatestPage(t *testing.T) {
	ctx := context.Background()
	db := openPollTestStore(t, filepath.Join(t.TempDir(), "poll.db"))
	s, err := db.load(ctx, "alice", "group:new", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	delivered := 0
	err = recoverPollMessages(ctx, db, s, testPollPage(250, 20), 100,
		func(context.Context, groupme.ID) ([]*groupme.Message, error) {
			t.Fatal("new conversation fetched older history")
			return nil, nil
		}, func(context.Context, *groupme.Message) error { delivered++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	s = loadPollTestState(t, db, "alice", "group:new", 1)
	if delivered != 20 || s.anchorID != "250" || s.head != "" {
		t.Fatalf("incorrect baseline: %+v delivered=%d", s, delivered)
	}
}

func TestRecoveryPageAndCursorAreAtomic(t *testing.T) {
	db := openPollTestStore(t, filepath.Join(t.TempDir(), "poll.db"))
	ctx := context.Background()
	s := loadPollTestState(t, db, "alice", "group:g", 1)
	_, err := db.Exec(ctx, `CREATE TRIGGER fail_checkpoint BEFORE UPDATE ON groupme_poll_state BEGIN SELECT RAISE(ABORT, 'test failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	err = recoverPollMessages(ctx, db, s, testPollPage(50, 20), 20, testPollFetch, func(context.Context, *groupme.Message) error { return nil })
	if err == nil {
		t.Fatal("expected transaction failure")
	}
	s = loadPollTestState(t, db, "alice", "group:g", 1)
	pending, err := db.pending(ctx, s, 100)
	if err != nil {
		t.Fatal(err)
	}
	if s.head != "" || s.before != "" || len(pending) != 0 {
		t.Fatal("partially committed a fetched page")
	}
}

func TestRecoveryScopesAndLoginDeletion(t *testing.T) {
	db := openPollTestStore(t, filepath.Join(t.TempDir(), "poll.db"))
	ctx := context.Background()
	for _, key := range []struct {
		login  networkid.UserLoginID
		portal networkid.PortalID
	}{{"alice", "group:1"}, {"alice", "dm:1"}, {"bob", "group:1"}} {
		s := loadPollTestState(t, db, key.login, key.portal, 1)
		s.head = "20"
		s.headTS = 1020
		s.before = "2"
		if err := db.stage(ctx, s, testPollPage(20, 19)); err != nil {
			t.Fatal(err)
		}
	}
	_, err := db.Exec(ctx, `DELETE FROM user_login WHERE bridge_id='' AND id='alice'`)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err = db.QueryRow(ctx, `SELECT COUNT(*) FROM groupme_poll_message`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 19 {
		t.Fatalf("login state leaked or crossed accounts: %d", count)
	}
}

func TestFetchPollPageUsesNativePaginationAndHandlesEmpty(t *testing.T) {
	for _, private := range []bool{false, true} {
		for _, code := range []int{200, 304, 429} {
			t.Run(fmt.Sprintf("private=%v/code=%d", private, code), func(t *testing.T) {
				previous := http.DefaultTransport
				http.DefaultTransport = loginTransport(func(r *http.Request) (*http.Response, error) {
					q := r.URL.Query()
					if q.Get("before_id") != "anchor" || q.Has("before_ID") || q.Has("token") || r.Header.Get("X-Access-Token") != "test-token" {
						t.Fatalf("incorrect request: %s", r.URL.Path)
					}
					field := "messages"
					if private {
						field = "direct_messages"
						if q.Get("other_user_id") != "chat" {
							t.Fatal("missing DM recipient")
						}
					} else if q.Get("limit") != "100" {
						t.Fatal("group page size missing")
					}
					body := fmt.Sprintf(`{"response":{"%s":[]},"meta":{"code":%d}}`, field, code)
					return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
				})
				t.Cleanup(func() { http.DefaultTransport = previous })
				gc := &GMClient{Client: groupmeext.NewClient("test-token")}
				msgs, err := gc.fetchPollPage(context.Background(), "chat", private, "anchor", 100)
				if code == 429 {
					var meta *groupme.Meta
					if !errors.As(err, &meta) || meta.Code != 429 {
						t.Fatalf("rate limit lost: %v", err)
					}
				} else if err != nil || len(msgs) != 0 {
					t.Fatalf("empty page not handled: %v", err)
				}
			})
		}
	}
}
