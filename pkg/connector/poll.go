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
	"sort"
	"time"

	"github.com/rs/zerolog"

	"github.com/beeper/groupme-lib"
)

// This file implements REST-API polling as a resilient fallback/replacement
// for GroupMe's real-time Faye/Bayeux push connection (see handlegroupme.go
// and NOTES.md "Faye/Bayeux push connection reliability"). That connection
// has been observed failing persistently (504 Gateway Timeout on every
// handshake attempt, for over an hour of continuous retries against a live
// account) -- with no other way to learn about new messages, that meant
// nothing synced into Matrix at all: not messages from other people, and
// not even the logged-in user's own messages sent from the native GroupMe
// app.
//
// Design summary (see NOTES.md for the full writeup):
//   - Runs as its own goroutine per Connect() call (see client.go), on a
//     timer, independent of whether the Faye connection is up.
//   - Each tick re-lists the user's groups and DM chats via the same REST
//     calls used by the initial sync (gc.Client.IndexAllGroups /
//     IndexAllChats, see sync.go) -- so newly created chats are picked up
//     automatically, without needing a separately maintained chat list.
//   - For each chat, ONE request fetches the most recent page of messages
//     (no since/after cursor -- see pollChat's doc comment for why this
//     is both simpler and necessary to avoid rate limiting, not just an
//     optimization); both new messages and reaction/like changes are
//     bridged from that single response.
//   - New messages are fed through GMClient.HandleTextMessage -- the exact
//     same conversion path used for live Faye push messages -- so polled
//     messages are bridged identically to pushed ones, including the
//     sender's own messages (GroupMe's message-list API returns those too,
//     with the same UserID/RecipientID shape as push payloads, so
//     HandleTextMessage's existing IsFromMe/portal-routing logic just
//     works without any special-casing here).
//   - Safe to run alongside a working Faye connection: bridgev2 core
//     dedupes incoming messages by ID before doing anything with them
//     (Portal.handleRemoteMessage -> DB.Message.GetAllPartsByID), so if
//     both Faye and polling observe the same message, whichever arrives
//     first wins and the other is a silent no-op.

// defaultPollIntervalSeconds is used when the config doesn't specify one
// (or specifies an invalid value).
//
// Confirmed live that GroupMe's rate limiting is real, not theoretical:
// with ~80 chats, a 20s interval, and even just one request per chat per
// tick (~4 req/s sustained, all fired back-to-back at the top of each
// tick), GroupMe returned "Error Code 429" on the majority of chats
// within minutes (362 and 500 occurrences logged across two consecutive
// runs). Disabling polling entirely immediately stopped all 429s (0
// logged), confirming polling -- not resync/push traffic -- was the
// cause. 60s cuts steady-state request rate 3x versus the previous
// default; combined with staggering requests across the interval instead
// of firing them all at once (pollOnce, below) and backing off
// per-chat on an actual 429 (pollChat, below) rather than just retrying
// on the next tick regardless, this is meant to stay well clear of
// whatever GroupMe's actual limit is instead of just reducing how often
// it gets hit.
const defaultPollIntervalSeconds = 60

// minPollIntervalSeconds is a floor on the configured interval, regardless
// of what the config says, so a typo (e.g. "1" instead of "10") can't turn
// this into a tight request loop against GroupMe's API.
const minPollIntervalSeconds = 30

// pollBackoffDuration is how long a chat is skipped after it gets a 429,
// before poll requests to it resume. Deliberately longer than the poll
// interval itself so a rate-limited chat doesn't just get hit again on
// the very next tick.
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
		select {
		case <-ctx.Done():
			log.Debug().Msg("Stopping GroupMe REST message polling loop")
			return
		case <-ticker.C:
		}
		gc.pollOnce(ctx, log)
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
// Requests are staggered evenly across (most of) the poll interval rather
// than fired back-to-back -- confirmed live that GroupMe's rate limiting
// reacts to bursty request patterns, not just total volume (see
// defaultPollIntervalSeconds' doc comment). The stagger delay is computed
// from the interval and chat count so total time spent here stays safely
// within one tick even for an account with many chats, leaving headroom
// for the requests themselves and per-chat backoff skips (which are
// effectively free/instant).
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

	// Spread requests across 80% of the interval, leaving the remaining
	// 20% as headroom (request latency, GC pauses, etc.) so this pass
	// reliably finishes before the next tick would fire (which, with a
	// standard time.Ticker, would just be silently dropped if this run
	// were still in progress -- fine for correctness, but defeats the
	// point of staggering if it happened routinely).
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

// pollChat fetches the most recent page of messages for a single chat
// (group or DM) and bridges both new messages and reaction/like changes
// from that one response. chatID is the GroupMe group ID for groups, or
// the other participant's user ID for DMs.
//
// This used to be two separate requests per chat per tick: one with a
// since/after cursor for new messages, one uncursored for reaction
// resyncing (see git history for the previous version and its reasoning).
// Both are bounded to the same 20-message page GroupMe returns by default
// (groupme.IndexMessagesQuery.Limit / the DM endpoint's fixed 20-per-page
// behavior), so merging them loses no coverage -- and for "is this
// message new", no cursor/local bookkeeping is needed at all:
// HandleTextMessage's underlying bridgev2 core already dedupes incoming
// messages by ID before doing anything observable
// (Portal.handleRemoteMessage -> DB.Message.GetAllPartsByID), so simply
// calling it for every message in the page unconditionally is already
// correct and safe, not just an approximation.
//
// Confirmed live this was necessary, not just a theoretical optimization:
// with ~80 chats and a 20s poll interval, two requests per chat per tick
// was enough sustained request volume to trip GroupMe's rate limiting
// (429s observed on the majority of chats within minutes of the
// double-request version going live).
func (gc *GMClient) pollChat(ctx context.Context, log zerolog.Logger, chatID groupme.ID, private bool) {
	if until, skip := gc.checkPollBackoff(chatID); skip {
		log.Debug().Str("chat_id", chatID.String()).Time("backoff_until", until).
			Msg("Skipping poll for chat still backing off after a 429")
		return
	}

	var msgs []*groupme.Message
	if private {
		resp, err := gc.Client.IndexDirectMessages(ctx, chatID.String(), &groupme.IndexDirectMessagesQuery{})
		if err != nil {
			gc.maybeBackoffPoll(chatID, err)
			logPollError(log, chatID, true, err)
			return
		}
		msgs = resp.Messages
	} else {
		resp, err := gc.Client.IndexMessages(ctx, chatID, &groupme.IndexMessagesQuery{Limit: 20})
		if err != nil {
			gc.maybeBackoffPoll(chatID, err)
			logPollError(log, chatID, false, err)
			return
		}
		msgs = resp.Messages
	}
	if len(msgs) == 0 {
		return
	}

	// Both endpoints are documented to return results newest-first when no
	// since/after cursor is given, and the DM endpoint's ordering isn't
	// documented as strictly one direction or the other either. Sort
	// explicitly by timestamp so messages are always bridged in
	// chronological order regardless of which case applies.
	sort.SliceStable(msgs, func(i, j int) bool {
		return msgs[i].CreatedAt.ToTime().Before(msgs[j].CreatedAt.ToTime())
	})

	for _, msg := range msgs {
		if msg == nil || len(msg.ID) == 0 {
			continue
		}
		// Same conversion path as live Faye push messages -- see
		// handlegroupme.go. Safe to call unconditionally for every message
		// in the page (not just ones newer than some cursor): bridgev2
		// core dedupes by message ID before doing anything observable, so
		// this is a no-op for anything already bridged, whether by a
		// previous poll or by Faye push delivering it first.
		gc.HandleTextMessage(*msg)
		// Reaction/like resync for the same message. GroupMe's live push
		// is the only other path that catches a like added to an
		// already-bridged message, and it's not perfectly reliable (see
		// "Faye/Bayeux push connection reliability" in NOTES.md) -- this
		// is a cheap, safe no-op via the same full-resync HandleLike
		// already uses live when FavoritedBy/Reactions haven't changed.
		gc.HandleLike(*msg)
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
