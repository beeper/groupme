package groupmeext

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestInventoryPaginatesAndCancels(t *testing.T) {
	for _, kind := range []string{"groups", "chats"} {
		t.Run(kind, func(t *testing.T) {
			previous := http.DefaultTransport
			defer func() { http.DefaultTransport = previous }()
			pages := 0
			http.DefaultTransport = roundTripper(func(r *http.Request) (*http.Response, error) {
				if r.Context().Err() != nil {
					return nil, r.Context().Err()
				}
				pages++
				if r.URL.Path != "/v3/"+kind || r.URL.Query().Get("page") != strconv.Itoa(pages) || r.URL.Query().Get("per_page") != "100" {
					t.Fatalf("bad pagination request: %s", r.URL)
				}
				if r.Header.Get("X-Access-Token") != "test-token" || r.URL.Query().Has("token") {
					t.Fatal("unsafe auth")
				}
				count := 100
				if pages == 3 {
					count = 7
				}
				items := make([]map[string]string, count)
				for i := range items {
					items[i] = map[string]string{"id": fmt.Sprint((pages-1)*100 + i)}
				}
				body, _ := json.Marshal(map[string]any{"response": items, "meta": map[string]int{"code": 200}})
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}, nil
			})
			client := NewClient("test-token")
			defer client.Close()
			fetch := func(ctx context.Context) (int, error) {
				if kind == "groups" {
					v, e := client.IndexAllGroups(ctx)
					return len(v), e
				}
				v, e := client.IndexAllChats(ctx)
				return len(v), e
			}
			if count, err := fetch(context.Background()); count != 207 || err != nil || pages != 3 {
				t.Fatalf("count=%d pages=%d error=%v", count, pages, err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := fetch(ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("expected cancellation, got %v", err)
			}
		})
	}
}
