package connector

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"slices"
	"time"

	"github.com/google/uuid"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/event"

	"github.com/beeper/groupme-lib"
)

func pollIsActive(poll *groupme.PollDetail, now time.Time) bool {
	return poll.Status == "active" && poll.Expiration.ToTime().After(now)
}

func pollIsGone(err error) bool {
	var meta *groupme.Meta
	return errors.As(err, &meta) && (meta.Code == http.StatusForbidden || meta.Code == http.StatusNotFound)
}

func (gc *GMClient) savePolls(ctx context.Context) {
	if err := gc.UserLogin.Save(ctx); err != nil {
		gc.UserLogin.Log.Err(err).Msg("Failed to save active GroupMe polls")
	}
}

func (gc *GMClient) trackPoll(ctx context.Context, groupID groupme.ID, messageID networkid.MessageID, poll *groupme.PollDetail) {
	if !pollIsActive(poll, time.Now()) {
		return
	}
	active := &gc.Meta.ActivePolls
	active.Lock()
	if active.polls[poll.ID] != nil {
		active.Unlock()
		return
	}
	if active.polls == nil {
		active.polls = make(map[string]*ActivePoll)
	}
	active.polls[poll.ID] = &ActivePoll{
		GroupID: groupID, MessageID: messageID, Expiration: poll.Expiration,
		Selections: make(map[string][]string),
	}
	active.Unlock()
	gc.savePolls(ctx)
}

func pollSelections(poll *groupme.PollDetail) map[string][]string {
	selections := make(map[string][]string)
	for _, option := range poll.Options {
		for _, voter := range option.VoterIDs {
			selections[voter] = append(selections[voter], option.ID)
		}
	}
	for voter, answers := range selections {
		slices.Sort(answers)
		selections[voter] = slices.Compact(answers)
	}
	return selections
}

func changedPollSelections(previous, current map[string][]string) map[string][]string {
	changed := make(map[string][]string)
	for voter, answers := range current {
		if !slices.Equal(previous[voter], answers) {
			changed[voter] = answers
		}
	}
	for voter, answers := range previous {
		if len(answers) > 0 && len(current[voter]) == 0 {
			changed[voter] = []string{}
		}
	}
	return changed
}

func pollResponsePart(message *database.Message, selection []string) (*bridgev2.ConvertedMessagePart, error) {
	answers := make([]string, len(selection))
	answerIDs := message.Metadata.(*MessageMetadata).PollAnswerIDs
	for i, optionID := range selection {
		answers[i] = optionID
		if answerIDs != nil {
			answers[i] = ""
			for answerID, nativeID := range answerIDs {
				if nativeID == optionID {
					answers[i] = answerID
					break
				}
			}
			if answers[i] == "" {
				return nil, errors.New("unknown GroupMe poll option")
			}
		}
	}
	return &bridgev2.ConvertedMessagePart{
		Type: event.EventUnstablePollResponse,
		Content: &event.MessageEventContent{RelatesTo: &event.RelatesTo{
			Type: event.RelReference, EventID: message.MXID,
		}},
		Extra: map[string]any{
			"org.matrix.msc3381.poll.response": map[string]any{"answers": answers},
		},
	}, nil
}

func (gc *GMClient) pollActivePolls(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			gc.Meta.ActivePolls.Lock()
			pollIDs := slices.Collect(maps.Keys(gc.Meta.ActivePolls.polls))
			gc.Meta.ActivePolls.Unlock()
			for _, pollID := range pollIDs {
				if ctx.Err() != nil {
					return
				}
				gc.refreshPoll(ctx, pollID)
			}
		}
	}
}

func (gc *GMClient) refreshPoll(ctx context.Context, pollID string) {
	active := &gc.Meta.ActivePolls
	active.Lock()
	tracked := active.polls[pollID]
	if tracked == nil || tracked.voting {
		active.Unlock()
		return
	}
	if !tracked.Expiration.ToTime().After(time.Now()) {
		delete(active.polls, pollID)
		active.Unlock()
		gc.savePolls(ctx)
		return
	}
	revision := tracked.revision
	active.Unlock()
	log := gc.UserLogin.Log.With().Str("poll_id", pollID).Logger()
	log.Debug().Msg("Fetching active GroupMe poll")
	poll, err := gc.Client.GetPoll(ctx, string(tracked.GroupID), pollID)
	if ctx.Err() != nil {
		return
	}
	active.Lock()
	if active.polls[pollID] != tracked || tracked.revision != revision {
		active.Unlock()
		return
	}
	if err != nil {
		gone := pollIsGone(err)
		if gone {
			delete(active.polls, pollID)
		}
		active.Unlock()
		log.Warn().Err(err).Msg("Failed to fetch active GroupMe poll")
		if gone {
			gc.savePolls(ctx)
		}
		return
	}
	changed := changedPollSelections(tracked.Selections, pollSelections(poll))
	active.Unlock()
	if poll.Visibility != "anonymous" {
		gc.syncPollSelections(ctx, tracked, changed, revision)
	}
	active.Lock()
	if tracked.revision != revision {
		active.Unlock()
		return
	}
	stopped := !pollIsActive(poll, time.Now())
	if stopped {
		delete(active.polls, pollID)
	}
	active.Unlock()
	if stopped {
		log.Debug().Msg("Stopped tracking GroupMe poll")
	}
	if stopped || len(changed) > 0 {
		gc.savePolls(ctx)
	}
}

func (gc *GMClient) syncPollSelections(ctx context.Context, tracked *ActivePoll, changed map[string][]string, revision uint64) {
	if len(changed) == 0 {
		return
	}
	message, err := gc.Main.br.DB.Message.GetPartByID(ctx, gc.UserLogin.ID, tracked.MessageID, "")
	if err != nil || message == nil {
		gc.UserLogin.Log.Warn().Err(err).Msg("Failed to get poll response target message")
		return
	}
	active := &gc.Meta.ActivePolls
	for voter, selection := range changed {
		active.Lock()
		stale := tracked.revision != revision
		active.Unlock()
		if stale || ctx.Err() != nil {
			return
		}
		part, err := pollResponsePart(message, selection)
		if err != nil {
			gc.UserLogin.Log.Warn().Err(err).Msg("Failed to convert GroupMe poll response")
			continue
		}
		result := gc.Main.br.QueueRemoteEvent(gc.UserLogin, &simplevent.PreConvertedMessage{
			EventMeta: simplevent.EventMeta{
				Type:      bridgev2.RemoteEventMessage,
				PortalKey: gc.portalKeyForGroup(tracked.GroupID),
				Sender:    bridgev2.EventSender{Sender: MakeUserID(groupme.ID(voter)), IsFromMe: voter == gc.Meta.GMID},
				Timestamp: time.Now(),
			},
			ID:   networkid.MessageID(pollVoteIDPrefix + uuid.NewString()),
			Data: &bridgev2.ConvertedMessage{Parts: []*bridgev2.ConvertedMessagePart{part}},
		})
		active.Lock()
		if result.Success && !result.Ignored && tracked.revision == revision {
			tracked.Selections[voter] = selection
		}
		active.Unlock()
	}
}
