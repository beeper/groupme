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

package connector

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"github.com/beeper/groupme-lib"
)

const defaultPollIntervalSeconds = 60
const minPollIntervalSeconds = 30
const pollBackoffDuration = 3 * time.Minute

// pollInterval returns the configured poll interval, clamped to a sane
// minimum and defaulted if unset.
func (gc *GMClient) pollInterval() time.Duration {
	secs := gc.Main.Config.Poll.IntervalSeconds
	if secs <= 0 {
		secs = defaultPollIntervalSeconds
	}
	if secs < minPollIntervalSeconds {
		secs = minPollIntervalSeconds
	}
	return time.Duration(secs) * time.Second
}

// pollMessages runs until ctx is cancelled (see Disconnect in client.go),
// periodically polling every known chat for new messages via REST.
func (gc *GMClient) pollMessages(ctx context.Context) {
	if !gc.Main.Config.Poll.Enabled {
		gc.UserLogin.Log.Info().Msg("REST message polling disabled by config")
		return
	}

	interval := gc.pollInterval()
	log := gc.UserLogin.Log.With().Str("action", "message poll").Dur("interval", interval).Logger()
	log.Info().Msg("Starting GroupMe REST message polling loop")

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		gc.pollOnce(ctx, log)
		select {
		case <-ctx.Done():
			log.Debug().Msg("Stopping GroupMe REST message polling loop")
			return
		case <-ticker.C:
		}
	}
}

// pollTarget is one chat to poll in a pollOnce pass -- just enough to call
// pollChat once the list is built and staggering can begin.
type pollTarget struct {
	chatID  groupme.ID
	private bool
}

// pollOnce runs a single poll pass over every known group and DM chat.
//
// Requests are staggered to avoid bursts. Recovery can extend a pass beyond
// the configured interval; passes never overlap.
func (gc *GMClient) pollOnce(ctx context.Context, log zerolog.Logger) {
	var targets []pollTarget

	groups, err := gc.Client.IndexAllGroups(ctx)
	if err != nil {
		log.Err(err).Msg("Failed to list groups while polling for new messages")
	} else {
		for _, group := range groups {
			if group == nil || len(group.ID) == 0 {
				continue
			}
			targets = append(targets, pollTarget{chatID: group.ID, private: false})
		}
	}

	if ctx.Err() != nil {
		return
	}

	chats, err := gc.Client.IndexAllChats(ctx)
	if err != nil {
		log.Err(err).Msg("Failed to list DM chats while polling for new messages")
	} else {
		for _, chat := range chats {
			if chat == nil || len(chat.OtherUser.ID) == 0 {
				continue
			}
			targets = append(targets, pollTarget{chatID: chat.OtherUser.ID, private: true})
		}
	}

	if len(targets) == 0 {
		return
	}

	// Leave some of the interval for request latency.
	delay := time.Duration(float64(gc.pollInterval()) * 0.8 / float64(len(targets)))

	for i, t := range targets {
		if ctx.Err() != nil {
			return
		}
		gc.pollChat(ctx, log, t.chatID, t.private)
		if i < len(targets)-1 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
		}
	}
}

func (gc *GMClient) fetchPollPage(ctx context.Context, chatID groupme.ID, private bool, before groupme.ID, limit int) ([]*groupme.Message, error) {
	var messages []*groupme.Message
	var err error
	if private {
		var resp groupme.IndexDirectMessagesResponse
		resp, err = gc.Client.IndexDirectMessages(ctx, chatID.String(), &groupme.IndexDirectMessagesQuery{BeforeID: before})
		messages = resp.Messages
	} else {
		var resp groupme.IndexMessagesResponse
		resp, err = gc.Client.IndexMessages(ctx, chatID, &groupme.IndexMessagesQuery{BeforeID: before, Limit: limit})
		messages = resp.Messages
	}
	var meta *groupme.Meta
	if errors.As(err, &meta) && meta.Code == groupme.HTTPNotModified {
		return nil, nil
	}
	return messages, err
}

func (gc *GMClient) pollChat(ctx context.Context, log zerolog.Logger, chatID groupme.ID, private bool) {
	key := gc.portalKeyForGroup(chatID)
	pageSize := 100
	if private {
		key = gc.portalKeyForDM(chatID)
		pageSize = 20
	}
	backoffKey := groupme.ID(key.ID)
	if until, skip := gc.checkPollBackoff(backoffKey); skip {
		log.Debug().Str("portal_id", string(key.ID)).Time("backoff_until", until).Msg("Skipping rate-limited chat")
		return
	}
	// On upgrade, start from the oldest existing mapping to also recover gaps
	// left by the old latest-page poller. New logins retain the latest-20 seed.
	first, err := gc.Main.br.DB.Message.GetFirstPortalMessage(ctx, key)
	if err != nil {
		log.Err(err).Msg("Failed to get polling recovery anchor")
		return
	}
	var anchorID groupme.ID
	var anchorTS groupme.Timestamp
	if first != nil {
		anchorID = ParseMessageID(first.ID)
		anchorTS = groupme.FromTime(first.Timestamp)
	}
	s, err := gc.Main.pollDB.load(ctx, gc.UserLogin.ID, key.ID, anchorID, anchorTS)
	if err != nil {
		log.Err(err).Msg("Failed to load polling recovery state")
		return
	}
	latest, err := gc.fetchPollPage(ctx, chatID, private, "", 20)
	if err == nil {
		err = recoverPollMessages(ctx, gc.Main.pollDB, s, latest, pageSize, func(ctx context.Context, before groupme.ID) ([]*groupme.Message, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(500 * time.Millisecond):
			}
			return gc.fetchPollPage(ctx, chatID, private, before, pageSize)
		}, func(ctx context.Context, msg *groupme.Message) error { return gc.deliverPollMessage(ctx, key, msg) })
		// Keep reaction resync independent of history progress.
		for _, msg := range latest {
			if msg != nil && msg.ID != "" {
				gc.HandleLike(*msg)
			}
		}
	}
	if err != nil {
		gc.maybeBackoffPoll(backoffKey, err)
		logPollError(log, chatID, private, err)
	}
}

func (gc *GMClient) deliverPollMessage(ctx context.Context, key networkid.PortalKey, msg *groupme.Message) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	done := make(chan error, 1)
	evt := gc.makeRemoteMessage(*msg)
	evt.PortalKey = key
	evt.MutateContextFunc = func(eventCtx context.Context) context.Context { return zerolog.Ctx(eventCtx).WithContext(ctx) }
	evt.PostHandleFunc = func(eventCtx context.Context, portal *bridgev2.Portal) {
		parts, err := gc.Main.br.DB.Message.GetAllPartsByID(eventCtx, gc.UserLogin.ID, MakeMessageID(msg.ID))
		if err == nil && len(parts) == 0 {
			err = fmt.Errorf("message %s has no durable Matrix mapping", msg.ID)
		}
		done <- err
	}
	result := gc.Main.br.QueueRemoteEvent(gc.UserLogin, evt)
	return waitPollDelivery(ctx, result, done, msg.ID)
}

func waitPollDelivery(ctx context.Context, result bridgev2.EventHandlingResult, done <-chan error, messageID groupme.ID) error {
	if !result.Success {
		if result.Error != nil {
			return result.Error
		}
		return fmt.Errorf("failed to queue message %s for polling recovery", messageID)
	}
	if !result.Queued {
		select {
		case err := <-done:
			return err
		default:
			return fmt.Errorf("polling recovery did not process message %s", messageID)
		}
	}
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// logPollError logs a polling failure, downgrading GroupMe's "304 Not
// Modified" response (its documented way of saying "no messages found for
// this since_id/after_id filter", i.e. simply nothing new) to debug level
// instead of treating a normal empty poll as an error.
func logPollError(log zerolog.Logger, chatID groupme.ID, private bool, err error) {
	var meta *groupme.Meta
	if errors.As(err, &meta) {
		switch meta.Code {
		case groupme.HTTPNotModified:
			log.Debug().Str("chat_id", chatID.String()).Bool("private", private).Msg("No new messages")
			return
		case groupme.HTTPTooManyRequests, groupme.HTTPEnhanceYourCalm:
			// Logged at WARN, not ERR: maybeBackoffPoll (below) already
			// handles this by skipping the chat for a while, so a 429 here
			// is an expected, self-mitigated condition, not a failure that
			// needs attention the way an ERR-level log implies (including
			// to the health-check alerting, which greps for ERR/FATAL).
			log.Warn().Str("chat_id", chatID.String()).Bool("private", private).
				Msg("Rate limited polling this chat, backing off")
			return
		}
	}
	log.Err(err).Str("chat_id", chatID.String()).Bool("private", private).Msg("Failed to poll for new messages")
}

// checkPollBackoff reports whether chatID is currently backing off after a
// previous 429 (see maybeBackoffPoll), and if so, until when.
func (gc *GMClient) checkPollBackoff(chatID groupme.ID) (until time.Time, skip bool) {
	gc.pollBackoffMu.Lock()
	defer gc.pollBackoffMu.Unlock()
	if gc.pollBackoff == nil {
		return time.Time{}, false
	}
	until, ok := gc.pollBackoff[chatID.String()]
	if !ok || time.Now().After(until) {
		return time.Time{}, false
	}
	return until, true
}

// maybeBackoffPoll records a backoff for chatID if err indicates GroupMe
// rate-limited the request (429, or the older/undocumented-in-practice 420
// "Enhance Your Calm" this library's original author expected instead --
// handled the same way defensively, though only 429 has actually been
// observed live). Polling for that chat is skipped for pollBackoffDuration
// afterward (checkPollBackoff, above) instead of being retried on the very
// next tick regardless.
func (gc *GMClient) maybeBackoffPoll(chatID groupme.ID, err error) {
	var meta *groupme.Meta
	if !errors.As(err, &meta) {
		return
	}
	if meta.Code != groupme.HTTPTooManyRequests && meta.Code != groupme.HTTPEnhanceYourCalm {
		return
	}
	gc.pollBackoffMu.Lock()
	defer gc.pollBackoffMu.Unlock()
	if gc.pollBackoff == nil {
		gc.pollBackoff = make(map[string]time.Time)
	}
	gc.pollBackoff[chatID.String()] = time.Now().Add(pollBackoffDuration)
}
