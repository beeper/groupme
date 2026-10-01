package connector

import (
	"context"
	"fmt"
	"sort"

	"github.com/beeper/groupme-lib"
)

const recoveryPagesPerPoll = 3
const recoveryMessagesPerPoll = 100

type pollPageFetcher func(context.Context, groupme.ID) ([]*groupme.Message, error)
type pollMessageSender func(context.Context, *groupme.Message) error

func newestFirst(messages []*groupme.Message) ([]*groupme.Message, error) {
	result := make([]*groupme.Message, 0, len(messages))
	for _, msg := range messages {
		if msg == nil || msg.ID == "" {
			return nil, fmt.Errorf("poll response contains a message without an ID")
		}
		result = append(result, msg)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].CreatedAt != result[j].CreatedAt {
			return result[i].CreatedAt > result[j].CreatedAt
		}
		if len(result[i].ID) != len(result[j].ID) {
			return len(result[i].ID) > len(result[j].ID)
		}
		return result[i].ID > result[j].ID
	})
	return result, nil
}

// Stage the gap before delivering it so a backwards-only DM API can still
// deliver oldest first, with bounded memory and durable pagination progress.
func recoverPollMessages(ctx context.Context, db *pollStore, s *pollState, latest []*groupme.Message, pageSize int, fetch pollPageFetcher, send pollMessageSender) error {
	if s.head == "" {
		page, err := newestFirst(latest)
		if err != nil {
			return err
		}
		if len(page) == 0 || page[0].ID == s.anchorID {
			return nil
		}
		s.head, s.headTS = page[0].ID, page[0].CreatedAt
		if err = stagePollPage(ctx, db, s, page, 20); err != nil {
			return err
		}
	}
	for n := 0; !s.scanned && n < recoveryPagesPerPoll; n++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		page, err := fetch(ctx, s.before)
		if err != nil {
			return err
		}
		page, err = newestFirst(page)
		if err != nil {
			return err
		}
		if len(page) > 0 && page[len(page)-1].ID == s.before {
			return fmt.Errorf("GroupMe pagination did not advance")
		}
		if err = stagePollPage(ctx, db, s, page, pageSize); err != nil {
			return err
		}
	}
	if !s.scanned {
		return nil
	}
	messages, err := db.pending(ctx, s, recoveryMessagesPerPoll)
	if err != nil {
		return err
	}
	for _, msg := range messages {
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = send(ctx, msg); err != nil {
			return err
		}
		if err = db.acknowledge(ctx, s, msg.ID); err != nil {
			return err
		}
	}
	return db.finish(ctx, s)
}

func stagePollPage(ctx context.Context, db *pollStore, s *pollState, page []*groupme.Message, pageSize int) error {
	staged := make([]*groupme.Message, 0, len(page))
	s.scanned = s.anchorID == "" || len(page) < pageSize
	for _, msg := range page {
		if msg.ID == s.anchorID || msg.CreatedAt < s.anchorTS {
			s.scanned = true
			continue
		}
		staged = append(staged, msg)
	}
	if len(page) > 0 {
		s.before = page[len(page)-1].ID
	}
	return db.stage(ctx, s, staged)
}
