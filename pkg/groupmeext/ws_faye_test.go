package groupmeext

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/beeper/groupme-lib"
	"github.com/coder/websocket"
	"github.com/rs/zerolog"
)

type subscriptionSeen struct{ channel, token, connection string }

func TestPushAccountIsolationAcrossReconnect(t *testing.T) {
	seen := make(chan subscriptionSeen, 32)
	var nextID atomic.Int64
	var connections sync.Map
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		connectionID := fmt.Sprint(nextID.Add(1))
		connections.Store(connectionID, conn)
		defer connections.Delete(connectionID)
		for {
			_, data, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			messages, err := decodeBayeuxFrame(data)
			if err != nil {
				t.Error(err)
				return
			}
			for _, msg := range messages {
				response := &bayeuxMessage{ReqID: msg.ReqID, Chan: msg.Chan, Successful: true, ClientID: connectionID}
				if msg.Chan == metaSubscribe {
					token, _ := msg.Ext()["access_token"].(string)
					seen <- subscriptionSeen{msg.Subscription, token, connectionID}
				}
				if msg.Chan == metaConnect {
					time.Sleep(10 * time.Millisecond)
				}
				out, _ := json.Marshal([]*bayeuxMessage{response})
				if err := conn.Write(r.Context(), websocket.MessageText, out); err != nil {
					return
				}
			}
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	var workers sync.WaitGroup
	defer func() { cancel(); workers.Wait() }()
	for _, account := range []string{"alice", "bob"} {
		client := NewWSFayeClient(zerolog.Nop())
		client.url = "ws" + strings.TrimPrefix(server.URL, "http")
		workers.Add(2)
		go func() { defer workers.Done(); client.Listen(ctx) }()
		go func() {
			defer workers.Done()
			_ = client.WaitSubscribe(ctx, "/user/"+account, account+"-token", make(chan groupme.PushMessage, 1))
		}()
	}
	observed := map[string]map[string]bool{}
	deadline := time.After(8 * time.Second)
	for len(observed["/user/alice"]) < 2 || len(observed["/user/bob"]) < 2 {
		select {
		case sub := <-seen:
			if sub.token != strings.TrimPrefix(sub.channel, "/user/")+"-token" {
				t.Fatalf("cross-account token: %+v", sub)
			}
			if observed[sub.channel] == nil {
				observed[sub.channel] = map[string]bool{}
			}
			if !observed[sub.channel][sub.connection] {
				observed[sub.channel][sub.connection] = true
				if len(observed[sub.channel]) == 1 {
					if conn, ok := connections.Load(sub.connection); ok {
						conn.(*websocket.Conn).CloseNow()
					}
				}
			}
		case <-deadline:
			t.Fatalf("reconnect did not restore both accounts: %v", observed)
		}
	}
}

type pushHandler struct{ messages chan groupme.Message }

func (h *pushHandler) HandleTextMessage(m groupme.Message) { h.messages <- m }
func (h *pushHandler) HandleError(error)                   {}

type batchTransport struct {
	client   *WSFayeClient
	messages []*bayeuxMessage
	ready    chan struct{}
}

func (b *batchTransport) Listen(ctx context.Context) {
	select {
	case <-b.ready:
	case <-ctx.Done():
		return
	}
	for _, msg := range b.messages {
		b.client.handleIncoming(ctx, msg)
	}
	<-ctx.Done()
}
func (b *batchTransport) WaitSubscribe(ctx context.Context, channel, token string, messages chan groupme.PushMessage) error {
	b.client.subs[channel] = pushSubscription{messages: messages, token: token}
	close(b.ready)
	return nil
}

func TestPushBatchOrderAndUnknownEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	sub := groupme.NewPushSubscription(ctx)
	handler := &pushHandler{messages: make(chan groupme.Message, 600)}
	sub.AddHandler(handler)
	transport := &batchTransport{client: NewWSFayeClient(zerolog.Nop()), ready: make(chan struct{})}
	transport.messages = append(transport.messages,
		&bayeuxMessage{Chan: "/user/alice", MsgData: map[string]interface{}{"type": 42}},
		&bayeuxMessage{Chan: "/user/alice", MsgData: map[string]interface{}{"type": "future.event", "subject": map[string]interface{}{}}},
	)
	for i := 0; i < 600; i++ {
		transport.messages = append(transport.messages, &bayeuxMessage{Chan: "/user/alice", MsgData: map[string]interface{}{"type": "line.create", "subject": map[string]interface{}{"id": fmt.Sprint(i), "group_id": "group", "user_id": "bob"}}})
	}
	sub.StartListening(ctx, transport)
	defer func() { cancel(); sub.Wait() }()
	if err := sub.SubscribeToUser(ctx, "alice", "alice-token"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 600; i++ {
		select {
		case msg := <-handler.messages:
			if string(msg.ID) != fmt.Sprint(i) {
				t.Fatalf("message %d arrived as %s", i, msg.ID)
			}
		case <-time.After(time.Second):
			t.Fatalf("message %d missing", i)
		}
	}
}

func TestPushCancellationDuringRetryAndBlockedDelivery(t *testing.T) {
	client := NewWSFayeClient(zerolog.Nop())
	ctx, cancel := context.WithCancel(context.Background())
	client.subs["blocked"] = pushSubscription{messages: make(chan groupme.PushMessage)}
	done := make(chan struct{})
	go func() { client.handleIncoming(ctx, &bayeuxMessage{Chan: "blocked"}); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("delivery ignored cancellation")
	}
	if err := client.WaitSubscribe(ctx, "/user/alice", "alice-token", make(chan groupme.PushMessage)); err != context.Canceled {
		t.Fatalf("subscription ignored cancellation: %v", err)
	}

	attempt := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case attempt <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client.url = "ws" + strings.TrimPrefix(server.URL, "http")
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	done = make(chan struct{})
	go func() { client.Listen(ctx); close(done) }()
	<-attempt
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("retry loop ignored cancellation")
	}
}
