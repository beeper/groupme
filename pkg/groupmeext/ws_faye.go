// mautrix-groupme - A Matrix-GroupMe puppeting bridge.
// Copyright (C) 2026 The mautrix-groupme contributors
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package groupmeext

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/rs/zerolog"

	"github.com/beeper/groupme-lib"
)

// GroupMe uses the same Faye endpoint for WebSocket and HTTP transports.
var wsPushServer = "wss" + strings.TrimPrefix(groupme.PushServer, "https")

const (
	metaHandshake   = "/meta/handshake"
	metaSubscribe   = "/meta/subscribe"
	metaUnsubscribe = "/meta/unsubscribe"
	metaConnect     = "/meta/connect"
)

// Bayeux field names match the native wire format.
type bayeuxMessage struct {
	ReqID                    string                 `json:"id,omitempty"`
	Chan                     string                 `json:"channel,omitempty"`
	Successful               bool                   `json:"successful,omitempty"`
	ClientID                 string                 `json:"clientId,omitempty"`
	SupportedConnectionTypes []string               `json:"supportedConnectionTypes,omitempty"`
	MsgData                  map[string]interface{} `json:"data,omitempty"`
	MsgAdvice                *bayeuxAdvice          `json:"advice,omitempty"`
	MsgError                 string                 `json:"error,omitempty"`
	MsgExt                   map[string]interface{} `json:"ext,omitempty"`
	ConnectionType           string                 `json:"connectionType,omitempty"`
	Subscription             string                 `json:"subscription,omitempty"`
	Version                  string                 `json:"version,omitempty"`
}

type bayeuxAdvice struct {
	Reconnect string `json:"reconnect,omitempty"`
}

// Adapt native messages to the GroupMe library's push dispatcher.
func (m *bayeuxMessage) Channel() string { return m.Chan }
func (m *bayeuxMessage) Data() map[string]interface{} {
	return m.MsgData
}
func (m *bayeuxMessage) Ext() map[string]interface{} {
	if m.MsgExt == nil {
		m.MsgExt = map[string]interface{}{}
	}
	return m.MsgExt
}
func (m *bayeuxMessage) Error() string { return m.MsgError }

// WSFayeClient implements GroupMe's Bayeux protocol over WebSocket.
type WSFayeClient struct {
	log            zerolog.Logger
	onSubscribed   func(ctx context.Context, channel string)
	onDisconnected func(err error)

	mu          sync.Mutex
	conn        *websocket.Conn
	connCtx     context.Context
	connWorkers *sync.WaitGroup
	clientID    string
	subs        map[string]*pushSubscription

	pendingMu sync.Mutex
	pending   map[string]chan *bayeuxMessage

	nextID atomic.Int64
}

// Pushes that arrive before a channel is ready are held for the current connection.
type pushSubscription struct {
	messages    chan groupme.PushMessage
	token       string
	subscribing bool
	ready       bool
	held        []*bayeuxMessage
}

var _ groupme.FayeClient = (*WSFayeClient)(nil)

// NewWSFayeClient creates a websocket-based Faye/Bayeux client for GroupMe's
// push service. Call Listen (typically via
// groupme.PushSubscription.StartListening) to begin connecting.
//
// onSubscribed runs each time a channel is subscribed on a new connection.
// That channel's pushes are delivered only after it returns. onDisconnected
// runs each time a connection attempt fails or an established connection drops.
func NewWSFayeClient(logger zerolog.Logger, onSubscribed func(ctx context.Context, channel string), onDisconnected func(err error)) *WSFayeClient {
	return &WSFayeClient{
		log:            logger.With().Str("component", "WSFayeClient").Logger(),
		onSubscribed:   onSubscribed,
		onDisconnected: onDisconnected,
		subs:           map[string]*pushSubscription{},
	}
}

func (c *WSFayeClient) nextRequestID() string {
	return strconv.FormatInt(c.nextID.Add(1), 10)
}

func (c *WSFayeClient) registerPending(id string) chan *bayeuxMessage {
	ch := make(chan *bayeuxMessage, 1)
	c.pendingMu.Lock()
	if c.pending == nil {
		c.pending = map[string]chan *bayeuxMessage{}
	}
	c.pending[id] = ch
	c.pendingMu.Unlock()
	return ch
}

func (c *WSFayeClient) unregisterPending(id string) {
	c.pendingMu.Lock()
	delete(c.pending, id)
	c.pendingMu.Unlock()
}

func (c *WSFayeClient) takePending(id string) (chan *bayeuxMessage, bool) {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	ch, ok := c.pending[id]
	if ok {
		delete(c.pending, id)
	}
	return ch, ok
}

func (c *WSFayeClient) getClientID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.clientID
}

// Bayeux sends even a single message in a JSON array.
func (c *WSFayeClient) writeMessage(ctx context.Context, conn *websocket.Conn, msg *bayeuxMessage) error {
	return wsjson.Write(ctx, conn, []*bayeuxMessage{msg})
}

func decodeBayeuxFrame(raw []byte) ([]*bayeuxMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, nil
	}
	if trimmed[0] == '[' {
		var arr []*bayeuxMessage
		if err := json.Unmarshal(trimmed, &arr); err != nil {
			return nil, err
		}
		return arr, nil
	}
	var single bayeuxMessage
	if err := json.Unmarshal(trimmed, &single); err != nil {
		return nil, err
	}
	return []*bayeuxMessage{&single}, nil
}

// Route replies by request ID, and push events by subscription channel.
func (c *WSFayeClient) handleIncoming(ctx context.Context, m *bayeuxMessage) {
	if m == nil {
		return
	}
	if m.ReqID != "" {
		if ch, ok := c.takePending(m.ReqID); ok {
			ch <- m
			return
		}
	}

	c.mu.Lock()
	sub, ok := c.subs[m.Chan]
	held := ok && !sub.ready
	if held {
		sub.held = append(sub.held, m)
	}
	c.mu.Unlock()
	if !ok {
		if m.Chan != "" && m.Chan != metaConnect {
			c.log.Debug().Str("channel", m.Chan).Msg("No subscriber for GroupMe push channel")
		}
		return
	}
	if held {
		return
	}
	select {
	case sub.messages <- m:
	case <-ctx.Done():
	}

}

func (c *WSFayeClient) readLoop(ctx context.Context, conn *websocket.Conn) error {
	for {
		_, raw, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		msgs, err := decodeBayeuxFrame(raw)
		if err != nil {
			c.log.Warn().Err(err).Msg("Failed to decode Bayeux frame from GroupMe push websocket")
			continue
		}
		for _, m := range msgs {
			c.handleIncoming(ctx, m)
		}
	}
}

// Every reply except /meta/connect arrives promptly.
func (c *WSFayeClient) request(ctx context.Context, conn *websocket.Conn, msg *bayeuxMessage) (*bayeuxMessage, error) {
	msg.ReqID = c.nextRequestID()
	respCh := c.registerPending(msg.ReqID)

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	if err := c.writeMessage(ctx, conn, msg); err != nil {
		c.unregisterPending(msg.ReqID)
		return nil, fmt.Errorf("sending %s: %w", msg.Chan, err)
	}

	select {
	case resp := <-respCh:
		if !resp.Successful {
			return nil, fmt.Errorf("%s rejected: %s", msg.Chan, resp.MsgError)
		}
		return resp, nil
	case <-ctx.Done():
		c.unregisterPending(msg.ReqID)
		return nil, fmt.Errorf("%s timed out: %w", msg.Chan, ctx.Err())
	}
}

// The native handshake must advertise the WebSocket connection type.
func (c *WSFayeClient) handshake(ctx context.Context, conn *websocket.Conn) error {
	resp, err := c.request(ctx, conn, &bayeuxMessage{
		Chan:                     metaHandshake,
		Version:                  "1.0",
		SupportedConnectionTypes: []string{"websocket"},
	})
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.clientID = resp.ClientID
	for channel := range c.subs {
		c.subscribeLocked(channel)
	}
	return nil
}

// Starts the channel's subscription on the current connection, if there is one.
func (c *WSFayeClient) subscribeLocked(channel string) {
	sub := c.subs[channel]
	if c.clientID == "" || sub.subscribing {
		return
	}
	sub.subscribing = true
	ctx, conn, clientID, workers := c.connCtx, c.conn, c.clientID, c.connWorkers
	workers.Add(1)
	go func() {
		defer workers.Done()
		c.subscribe(ctx, conn, clientID, channel, sub)
	}()
}

func (c *WSFayeClient) isCurrent(conn *websocket.Conn, channel string, sub *pushSubscription) bool {
	return c.conn == conn && c.subs[channel] == sub
}

func (c *WSFayeClient) subscribe(ctx context.Context, conn *websocket.Conn, clientID, channel string, sub *pushSubscription) {
	for {
		msg := &bayeuxMessage{Chan: metaSubscribe, ClientID: clientID, Subscription: channel}
		msg.Ext()["access_token"] = sub.token
		msg.Ext()["timestamp"] = time.Now().Unix()
		_, err := c.request(ctx, conn, msg)
		if err == nil {
			break
		}
		c.mu.Lock()
		current := c.isCurrent(conn, channel, sub)
		c.mu.Unlock()
		if ctx.Err() != nil || !current {
			return
		}
		c.log.Warn().Err(err).Str("channel", channel).Msg("Failed to subscribe to GroupMe push channel, retrying")
		select {
		case <-time.After(2 * time.Second):
		case <-ctx.Done():
			return
		}
	}
	c.log.Debug().Str("channel", channel).Msg("Subscribed to GroupMe push channel")

	if c.onSubscribed != nil {
		c.onSubscribed(ctx, channel)
	}

	// Handlers may subscribe to a newly discovered chat while consuming these
	// frames. Never hold the subscription mutex while waiting for that consumer.
	// Keep new arrivals held until every earlier batch has been delivered.
	for {
		c.mu.Lock()
		if !c.isCurrent(conn, channel, sub) || ctx.Err() != nil {
			c.mu.Unlock()
			return
		}
		batch := sub.held
		sub.held = nil
		if len(batch) == 0 {
			sub.ready = true
			c.mu.Unlock()
			return
		}
		c.mu.Unlock()
		for _, m := range batch {
			select {
			case sub.messages <- m:
			case <-ctx.Done():
				return
			}
		}
	}
}

// Bayeux requires a continuous /meta/connect cycle alongside push delivery.
func (c *WSFayeClient) connectLoop(ctx context.Context, conn *websocket.Conn) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		id := c.nextRequestID()
		msg := &bayeuxMessage{
			ReqID:          id,
			Chan:           metaConnect,
			ClientID:       c.getClientID(),
			ConnectionType: "websocket",
		}
		respCh := c.registerPending(id)

		// Only bound the send: GroupMe holds healthy /meta/connect requests
		// open indefinitely. WebSocket pings detect transport failures.
		sendCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := c.writeMessage(sendCtx, conn, msg)
		cancel()
		if err != nil {
			c.unregisterPending(id)
			return fmt.Errorf("sending connect: %w", err)
		}

		select {
		case resp := <-respCh:
			if resp.MsgAdvice != nil {
				switch resp.MsgAdvice.Reconnect {
				case "none":
					return fmt.Errorf("server advised not to reconnect")
				case "handshake":
					return fmt.Errorf("server advised re-handshake")
				}
			}
			if !resp.Successful {
				c.log.Warn().Str("error", resp.MsgError).Msg("GroupMe push connect was not successful")
			}
		case <-ctx.Done():
			c.unregisterPending(id)
			return ctx.Err()
		}
	}
}

// Check transport liveness independently of Bayeux message traffic.
func (c *WSFayeClient) pingLoop(ctx context.Context, conn *websocket.Conn) error {
	const interval = 30 * time.Second
	const pingTimeout = 10 * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
			err := conn.Ping(pingCtx)
			cancel()
			if err != nil {
				return fmt.Errorf("websocket ping failed: %w", err)
			}
		}
	}
}

// Run one native connection and join its workers before reconnecting.
// A successful handshake lets Listen reset the reconnect backoff.
func (c *WSFayeClient) connectAndRun(ctx context.Context) (handshook bool, err error) {
	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	conn, _, err := websocket.Dial(dialCtx, wsPushServer, nil)
	cancel()
	if err != nil {
		return false, fmt.Errorf("dialing %s: %w", wsPushServer, err)
	}
	conn.SetReadLimit(1 << 20) // 1MiB; Bayeux/GroupMe push frames are small JSON

	runCtx, cancelRun := context.WithCancel(ctx)
	var workers sync.WaitGroup

	c.mu.Lock()
	c.conn, c.connCtx, c.connWorkers, c.clientID = conn, runCtx, &workers, ""
	c.mu.Unlock()
	defer func() {
		cancelRun()
		c.mu.Lock()
		c.conn, c.clientID = nil, ""
		for _, sub := range c.subs {
			sub.subscribing, sub.ready, sub.held = false, false, nil
		}
		c.mu.Unlock()
		conn.CloseNow()
		workers.Wait()
	}()

	readErrCh := make(chan error, 1)
	workers.Add(1)
	go func() { defer workers.Done(); readErrCh <- c.readLoop(runCtx, conn) }()

	if err := c.handshake(runCtx, conn); err != nil {
		return false, fmt.Errorf("handshake: %w", err)
	}
	c.log.Info().Str("client_id", c.getClientID()).Msg("GroupMe push websocket handshake succeeded")

	connectErrCh := make(chan error, 1)
	workers.Add(1)
	go func() { defer workers.Done(); connectErrCh <- c.connectLoop(runCtx, conn) }()

	pingErrCh := make(chan error, 1)
	workers.Add(1)
	go func() { defer workers.Done(); pingErrCh <- c.pingLoop(runCtx, conn) }()

	select {
	case <-ctx.Done():
		return true, ctx.Err()
	case err := <-readErrCh:
		return true, fmt.Errorf("read loop: %w", err)
	case err := <-connectErrCh:
		return true, fmt.Errorf("connect loop: %w", err)
	case err := <-pingErrCh:
		return true, fmt.Errorf("ping loop: %w", err)
	}
}

func (c *WSFayeClient) Listen(ctx context.Context) {
	backoff := time.Second
	const maxBackoff = 60 * time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		handshook, err := c.connectAndRun(ctx)
		if ctx.Err() != nil {
			return
		}
		if handshook {
			// Reset backoff after an established connection drops.
			backoff = time.Second
		}
		c.log.Warn().Err(err).Dur("retry_in", backoff).Msg("GroupMe push websocket disconnected, reconnecting")
		if c.onDisconnected != nil {
			c.onDisconnected(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func (c *WSFayeClient) Subscribe(channel, token string, msgChannel chan groupme.PushMessage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Inventory and message discovery can name the same chat repeatedly. Keep
	// its pending frames and readiness instead of starting another subscription.
	if sub := c.subs[channel]; sub != nil && sub.token == token && sub.messages == msgChannel {
		return
	}
	c.subs[channel] = &pushSubscription{messages: msgChannel, token: token}
	c.subscribeLocked(channel)
}

func (c *WSFayeClient) Unsubscribe(ctx context.Context, channel string) error {
	c.mu.Lock()
	sub, ok := c.subs[channel]
	delete(c.subs, channel)
	conn, clientID := c.conn, c.clientID
	subscribed := ok && sub.subscribing
	c.mu.Unlock()
	if !subscribed || clientID == "" {
		return nil
	}
	_, err := c.request(ctx, conn, &bayeuxMessage{Chan: metaUnsubscribe, ClientID: clientID, Subscription: channel})
	return err
}
