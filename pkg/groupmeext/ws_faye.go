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
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
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

// wsPushServer is the websocket variant of groupme.PushServer
// ("https://push.groupme.com/faye"). Same host and path as the HTTP
// long-polling transport, per both dev.groupme.com/tutorials/push and
// https://groupme-js.github.io/GroupMeCommunityDocs/api/ws/ (verified
// 2026-09-18) -- only the scheme and the handshake's
// supportedConnectionTypes differ.
var wsPushServer = "wss" + strings.TrimPrefix(groupme.PushServer, "https")

const (
	metaHandshake = "/meta/handshake"
	metaSubscribe = "/meta/subscribe"
	metaConnect   = "/meta/connect"
)

// sustainedDegradationThreshold and degradedAlertRepeatInterval govern
// maybeLogSustainedDegradation, below.
const (
	sustainedDegradationThreshold = 5 * time.Minute
	degradedAlertRepeatInterval   = 15 * time.Minute
)

// wsDialHTTPClient is used for the WebSocket upgrade handshake, exactly
// mirroring the HTTP/1.1-forcing patch already applied to the vendored
// long-polling transport (thirdparty/wray/http_transport.go). The RFC 6455
// upgrade handshake is defined in terms of HTTP/1.1 semantics (a
// "Connection: Upgrade" request), which Go's HTTP/2 transport does not
// speak; since push.groupme.com/faye was already observed to hang/504 on
// HTTP/2 for the long-polling transport on this host, the same defensive
// fix is applied here up front rather than waiting to see if the websocket
// path reproduces the same failure mode.
var wsDialHTTPClient = &http.Client{
	Transport: &http.Transport{
		TLSNextProto: make(map[string]func(authority string, c *tls.Conn) http.RoundTripper),
	},
}

// bayeuxMessage is a Bayeux protocol message, marshaled/unmarshaled as JSON
// for the websocket transport. Field names/tags intentionally mirror
// thirdparty/wray/response.go's `message` struct so the wire format matches
// what GroupMe's push server already accepts from the long-polling path.
//
// Struct field names are chosen to avoid colliding with the PushMessage
// interface methods implemented below (Channel/Data/Ext/Error), since Go
// doesn't allow a field and a method of the same name on one type.
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
	Reconnect string  `json:"reconnect,omitempty"`
	Interval  float64 `json:"interval,omitempty"`
	Timeout   float64 `json:"timeout,omitempty"`
}

// Channel, Data, Ext, and Error implement groupme.PushMessage, which lets a
// *bayeuxMessage be handed directly to GroupMe's HandlerAll dispatch
// (via PushSubscription.channel), exactly
// like the wray-backed long-polling messages already do.
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

// WSFayeClient is a Bayeux client for GroupMe's push service
// (push.groupme.com/faye) that speaks the protocol over a WebSocket instead
// of the HTTP long-polling transport implemented by github.com/karmanyaahm/wray
// (vendored at thirdparty/wray/). GroupMe's current docs recommend
// websocket over long-polling, and the community-documented handshake
// declares `"supportedConnectionTypes":["websocket"]` -- see NOTES.md for
// the full investigation that motivated this.
//
// It implements groupme.FayeClient (Listen/WaitSubscribe), so it's a
// drop-in replacement for the wray-backed FayeClient in this package: both
// satisfy the same interface consumed by groupme.PushSubscription.
type WSFayeClient struct {
	url string
	log zerolog.Logger

	mu       sync.Mutex
	conn     *websocket.Conn
	clientID string
	subs     map[string]pushSubscription

	pendingMu sync.Mutex
	pending   map[string]chan *bayeuxMessage

	writeMu sync.Mutex
	nextID  atomic.Int64

	// connStateMu guards lastConnected/nextDegradedAt, used only by
	// markConnected/maybeLogSustainedDegradation below to detect and alert
	// on sustained (not just momentary) connection loss. Kept separate
	// from mu since it's logically independent of the actual conn/subs
	// state that mu protects.
	connStateMu    sync.Mutex
	lastConnected  time.Time
	nextDegradedAt time.Time
}

type pushSubscription struct {
	messages chan groupme.PushMessage
	token    string
}

var _ groupme.FayeClient = (*WSFayeClient)(nil)

// NewWSFayeClient creates a websocket-based Faye/Bayeux client for GroupMe's
// push service. Call Listen (typically via
// groupme.PushSubscription.StartListening) to begin connecting.
func NewWSFayeClient(logger zerolog.Logger) *WSFayeClient {
	return &WSFayeClient{
		url:  wsPushServer,
		log:  logger.With().Str("component", "WSFayeClient").Logger(),
		subs: map[string]pushSubscription{},
		// Starts the "how long has this been down" clock at construction,
		// not the zero value -- otherwise a connection that fails on its
		// very first attempt would immediately look like it's been down
		// for decades and skip straight past sustainedDegradationThreshold
		// instead of getting the same grace period a later failure would.
		lastConnected: time.Now(),
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

func (c *WSFayeClient) getConn() *websocket.Conn {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn
}

// writeMessage sends a single Bayeux message as a one-element JSON array
// (the Bayeux spec envelope is always an array of message objects,
// regardless of transport -- see thirdparty/wray's HTTP transport, which
// does the same over POST bodies).
func (c *WSFayeClient) writeMessage(ctx context.Context, conn *websocket.Conn, msg *bayeuxMessage) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
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

// handleIncoming routes one decoded Bayeux message either to a pending
// request waiter (handshake/subscribe/connect responses, correlated by id)
// or to a subscribed channel's message chan (actual push events), which
// PushSubscription.StartListening reads from and dispatches into
// RealTimeHandlers/HandlerAll -- the same path the long-polling transport
// uses.
func (c *WSFayeClient) handleIncoming(ctx context.Context, m *bayeuxMessage) {
	if m.ReqID != "" {
		if ch, ok := c.takePending(m.ReqID); ok {
			ch <- m
			return
		}
	}

	c.mu.Lock()
	sub, ok := c.subs[m.Chan]
	c.mu.Unlock()
	if !ok {
		if m.Chan != "" && m.Chan != metaConnect {
			c.log.Debug().Str("channel", m.Chan).Msg("No subscriber for GroupMe push channel")
		}
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

// handshake performs the Bayeux handshake over an already-dialed websocket
// connection. Per the community-documented protocol
// (https://groupme-js.github.io/GroupMeCommunityDocs/api/ws/, verified
// 2026-09-18), supportedConnectionTypes must be ["websocket"] here (as
// opposed to the ["long-polling"] wray/HTTP path uses).
func (c *WSFayeClient) handshake(ctx context.Context, conn *websocket.Conn) error {
	id := c.nextRequestID()
	msg := &bayeuxMessage{
		ReqID:                    id,
		Chan:                     metaHandshake,
		Version:                  "1.0",
		SupportedConnectionTypes: []string{"websocket"},
	}
	respCh := c.registerPending(id)

	hsCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	if err := c.writeMessage(hsCtx, conn, msg); err != nil {
		c.unregisterPending(id)
		return fmt.Errorf("sending handshake: %w", err)
	}

	select {
	case resp := <-respCh:
		if !resp.Successful {
			return fmt.Errorf("handshake rejected: %s", resp.MsgError)
		}
		c.mu.Lock()
		c.clientID = resp.ClientID
		c.mu.Unlock()
		return nil
	case <-hsCtx.Done():
		c.unregisterPending(id)
		return fmt.Errorf("handshake timed out: %w", hsCtx.Err())
	}
}

func (c *WSFayeClient) subscribeOnce(ctx context.Context, channel string) error {
	conn := c.getConn()
	if conn == nil {
		return fmt.Errorf("not connected")
	}

	id := c.nextRequestID()
	msg := &bayeuxMessage{
		ReqID:        id,
		Chan:         metaSubscribe,
		ClientID:     c.getClientID(),
		Subscription: channel,
	}
	c.mu.Lock()
	subscription, ok := c.subs[channel]
	c.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown subscription")
	}
	msg.Ext()["access_token"] = subscription.token
	msg.Ext()["timestamp"] = time.Now().Unix()

	respCh := c.registerPending(id)

	subCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	if err := c.writeMessage(subCtx, conn, msg); err != nil {
		c.unregisterPending(id)
		return fmt.Errorf("sending subscribe: %w", err)
	}

	select {
	case resp := <-respCh:
		if !resp.Successful {
			return fmt.Errorf("subscribe to %s rejected: %s", channel, resp.MsgError)
		}
		return nil
	case <-subCtx.Done():
		c.unregisterPending(id)
		return fmt.Errorf("subscribe to %s timed out: %w", channel, subCtx.Err())
	}
}

// resubscribeAll re-sends /meta/subscribe for every channel WaitSubscribe
// has ever been called for, after a fresh handshake (e.g. following a
// reconnect). Each subscription is retried independently and indefinitely,
// mirroring wray's resubscribeAll behavior for the long-polling transport.
func (c *WSFayeClient) resubscribeAll(ctx context.Context, workers *sync.WaitGroup) {
	c.mu.Lock()
	channels := make([]string, 0, len(c.subs))
	for ch := range c.subs {
		channels = append(channels, ch)
	}
	c.mu.Unlock()

	for _, channel := range channels {
		channel := channel
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				if ctx.Err() != nil {
					return
				}
				if err := c.subscribeOnce(ctx, channel); err != nil {
					c.log.Warn().Err(err).Str("channel", channel).Msg("Failed to (re)subscribe to GroupMe push channel, retrying")
					select {
					case <-time.After(2 * time.Second):
						continue
					case <-ctx.Done():
						return
					}
				}
				c.log.Debug().Str("channel", channel).Msg("Subscribed to GroupMe push channel")
				return
			}
		}()
	}
}

// connectLoop implements Bayeux's /meta/connect keep-alive cycle: as soon as
// one connect request gets a response, the next is sent immediately. Unlike
// the long-polling transport, the server doesn't need to hold this open to
// deliver messages (those arrive as independent frames on the same
// websocket at any time -- see handleIncoming/readLoop), but /meta/connect
// is still how Bayeux clients signal liveness and receive `advice`
// (e.g. being told to re-handshake), so it's kept running per spec.
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

		// Only the *send* is time-bounded -- Bayeux's /meta/connect is a
		// long-hold by design (the server is meant to sit on it until
		// there's something to report), and GroupMe's actual hold duration
		// isn't documented, so there is no sane fixed deadline for "no
		// response yet" that wouldn't eventually be too short. Waiting
		// indefinitely for a *reply* isn't a bug: an unresponsive-forever
		// connection is still detected, just via the independent
		// websocket-level ping in connectAndRun (real transport liveness)
		// and via readLoop's conn.Read erroring on an actually-dead
		// connection, rather than by guessing how long GroupMe is allowed
		// to take. Previously this used a 45s deadline on the wait itself
		// and treated hitting it as fatal, tearing down and fully
		// re-dialing/re-handshaking/re-subscribing the entire connection
		// every time -- confirmed live to fire on a healthy connection
		// roughly every 45 seconds, i.e. GroupMe routinely takes longer
		// than 45s to respond to a connect and that's normal, not a
		// failure.
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

// pingLoop is the real liveness check for the connection: an active
// websocket-protocol ping/pong on a fixed interval, independent of Bayeux
// message traffic entirely. A ping failure (no pong within its own
// timeout) is genuine evidence the transport is dead -- e.g. a "zombie"
// connection that looks open but has silently stopped delivering anything,
// which plain TCP doesn't always surface quickly on its own. This is what
// should trigger a reconnect; a quiet /meta/connect (connectLoop, above)
// should not.
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

// markConnected records that the websocket just completed a successful
// Bayeux handshake, resetting the "how long has this been down" clock that
// maybeLogSustainedDegradation checks. Also clears any pending repeat-alert
// cooldown, so a *new* degradation period (after a real recovery) gets its
// own prompt alert instead of inheriting the previous period's cooldown.
func (c *WSFayeClient) markConnected() {
	c.connStateMu.Lock()
	c.lastConnected = time.Now()
	c.nextDegradedAt = time.Time{}
	c.connStateMu.Unlock()
}

// maybeLogSustainedDegradation logs an Error-level line if the websocket
// has not completed a successful handshake in over
// sustainedDegradationThreshold. Deliberately Error, unlike every other
// reconnect-related log line in this file (Listen, connectLoop, etc. all
// log at Warn): a single reconnect is expected and self-healing and
// shouldn't page anyone, but *sustained* inability to reconnect at all is
// exactly the kind of thing worth surfacing -- the host's health-check
// alerting (see NOTES.md "Health-check/alerting system") greps for
// Error/Fatal/panic-level log lines specifically so this reaches it
// without any changes needed on that side.
//
// Note this doesn't mean messages are being lost: the REST polling
// fallback (pkg/connector/poll.go) is fully independent of this and keeps
// delivering everything, just delayed up to the poll interval (60s by
// default) instead of near-instant. This alert is about *that*
// degradation being worth knowing about, not data loss.
//
// Throttled to degradedAlertRepeatInterval so a prolonged outage doesn't
// spam an alert every single backoff cycle (which can be as short as 1s);
// it still repeats periodically rather than alerting only once, so a
// multi-hour outage isn't just a single alert easy to miss or forget.
func (c *WSFayeClient) maybeLogSustainedDegradation() {
	c.connStateMu.Lock()
	downFor := time.Since(c.lastConnected)
	shouldLog := downFor >= sustainedDegradationThreshold && time.Now().After(c.nextDegradedAt)
	if shouldLog {
		c.nextDegradedAt = time.Now().Add(degradedAlertRepeatInterval)
	}
	c.connStateMu.Unlock()
	if shouldLog {
		c.log.Error().Dur("down_for", downFor).
			Msg("GroupMe push websocket has not reconnected in over 5 minutes; real-time delivery is degraded, falling back to REST polling (messages still arrive, delayed up to the poll interval)")
	}
}

// connectAndRun dials one websocket connection, handshakes, resubscribes,
// and runs the read/connect loops until either fails or the connection
// drops. It always returns a non-nil error (Listen treats every return as
// "reconnect after a backoff"); handshook reports whether the connection
// got far enough to be genuinely up, so Listen can reset its backoff.
func (c *WSFayeClient) connectAndRun(ctx context.Context) (handshook bool, err error) {
	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	conn, _, err := websocket.Dial(dialCtx, c.url, &websocket.DialOptions{
		HTTPClient: wsDialHTTPClient,
	})
	cancel()
	if err != nil {
		return false, fmt.Errorf("dialing %s: %w", c.url, err)
	}
	conn.SetReadLimit(1 << 20) // 1MiB; Bayeux/GroupMe push frames are small JSON

	runCtx, cancelRun := context.WithCancel(ctx)
	var workers sync.WaitGroup

	c.mu.Lock()
	c.conn = conn
	c.clientID = ""
	c.mu.Unlock()
	defer func() {
		cancelRun()
		conn.CloseNow()
		workers.Wait()
		c.mu.Lock()
		c.conn = nil
		c.clientID = ""
		c.mu.Unlock()
	}()

	readErrCh := make(chan error, 1)
	workers.Add(1)
	go func() { defer workers.Done(); readErrCh <- c.readLoop(runCtx, conn) }()

	if err := c.handshake(runCtx, conn); err != nil {
		return false, fmt.Errorf("handshake: %w", err)
	}
	c.log.Info().Str("client_id", c.clientID).Msg("GroupMe push websocket handshake succeeded")
	c.markConnected()
	// The connection was up right until this returns, so that's when the
	// "how long has this been down" clock should start -- not at the
	// handshake. Without this, the first disconnect after hours of healthy
	// uptime measured the whole uptime as downtime and fired the sustained
	// degradation alert immediately (seen live: down_for of 4-8 hours logged
	// the same millisecond as the disconnect itself).
	defer c.markConnected()

	c.resubscribeAll(runCtx, &workers)

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
			// A connection that actually came up resets the backoff, so a
			// routine drop after hours of uptime reconnects in ~1s rather
			// than inheriting the 60s cap from some earlier failure streak.
			backoff = time.Second
		}
		c.maybeLogSustainedDegradation()
		c.log.Warn().Err(err).Dur("retry_in", backoff).Msg("GroupMe push websocket disconnected, reconnecting")
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

func (c *WSFayeClient) WaitSubscribe(ctx context.Context, channel, token string, msgChannel chan groupme.PushMessage) error {
	c.mu.Lock()
	c.subs[channel] = pushSubscription{messages: msgChannel, token: token}
	c.mu.Unlock()
	for {
		attemptCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := c.waitConnectedAndSubscribe(attemptCtx, channel)
		cancel()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		c.log.Warn().Err(err).Str("channel", channel).Msg("Failed to subscribe to GroupMe push channel, retrying")
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func (c *WSFayeClient) waitConnectedAndSubscribe(ctx context.Context, channel string) error {
	for {
		if c.getConn() != nil && c.getClientID() != "" {
			return c.subscribeOnce(ctx, channel)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for websocket connection: %w", ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}
