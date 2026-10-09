package connector

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

	"github.com/beeper/groupme-lib"
)

var _ bridgev2.PollHandlingNetworkAPI = (*GMClient)(nil)

// Only voting on received polls is implemented. The interface also requires a
// creation handler, and the shared poll capability therefore stays partial.
func (gc *GMClient) HandleMatrixPollStart(context.Context, *bridgev2.MatrixPollStart) (*bridgev2.MatrixMessageResponse, error) {
	return nil, bridgev2.ErrPollsNotSupported
}

func (gc *GMClient) HandleMatrixPollVote(ctx context.Context, msg *bridgev2.MatrixPollVote) (*bridgev2.MatrixMessageResponse, error) {
	if msg.VoteTo == nil || msg.VoteTo.Room != msg.Portal.PortalKey {
		return nil, bridgev2.ErrUnknownPoll
	}
	portalType, groupID := ParsePortalID(msg.Portal.ID)
	meta, ok := msg.VoteTo.Metadata.(*MessageMetadata)
	if portalType != PortalTypeGroup || !ok || meta == nil || meta.PollID == "" {
		return nil, bridgev2.ErrUnknownPoll
	}
	poll, err := gc.Client.GetPoll(ctx, string(groupID), meta.PollID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch GroupMe poll: %w", err)
	}
	if poll.Status != "active" || (poll.Expiration > 0 && !poll.Expiration.ToTime().After(time.Now())) {
		return nil, pollVoteError("This GroupMe poll has ended")
	}
	answers := msg.Content.Response.Answers
	if len(answers) == 0 {
		return nil, pollVoteError("GroupMe vote withdrawal is not supported")
	}
	if poll.Type != "single" && poll.Type != "multi" {
		return nil, pollVoteError("Unsupported GroupMe poll type")
	}
	if poll.Type == "single" && len(answers) != 1 {
		return nil, pollVoteError("This GroupMe poll allows one choice")
	}
	for i, answer := range answers {
		if answer == "" || slices.Contains(answers[:i], answer) || !slices.ContainsFunc(poll.Options, func(option groupme.PollOption) bool { return option.ID == answer }) {
			return nil, pollVoteError("Unknown or repeated GroupMe poll option")
		}
	}
	result, err := gc.Client.VotePoll(ctx, string(groupID), meta.PollID, answers, poll.Type == "multi")
	if err != nil {
		return nil, fmt.Errorf("failed to vote in GroupMe poll: %w", err)
	}
	confirmed, expected := slices.Clone(result.MyVotes), slices.Clone(answers)
	slices.Sort(confirmed)
	slices.Sort(expected)
	if !slices.Equal(confirmed, expected) {
		return nil, fmt.Errorf("GroupMe did not confirm the requested poll selection")
	}
	// GroupMe returns poll state without a message ID. Use the originating
	// Matrix event to identify this action in the framework's message mapping,
	// dated before the poll so it never becomes the newest message catch-up
	// compares native IDs against.
	return &bridgev2.MatrixMessageResponse{DB: &database.Message{
		ID:        networkid.MessageID(pollVoteIDPrefix + msg.Event.ID),
		SenderID:  MakeUserID(groupme.ID(gc.Meta.GMID)),
		Timestamp: msg.VoteTo.Timestamp.Add(-time.Nanosecond),
	}}, nil
}

const pollVoteIDPrefix = "poll-vote:"

func isPollVoteID(id networkid.MessageID) bool {
	return strings.HasPrefix(string(id), pollVoteIDPrefix)
}

func pollVoteError(message string) error {
	return bridgev2.WrapErrorInStatus(errors.New(message)).WithIsCertain(true).WithErrorAsMessage()
}

func interactivePollPart(poll *groupme.PollDetail, body string) *bridgev2.ConvertedMessagePart {
	if poll.Status != "active" || (poll.Expiration > 0 && !poll.Expiration.ToTime().After(time.Now())) ||
		(poll.Type != "single" && poll.Type != "multi") || poll.Subject == "" || len(poll.Options) < 2 {
		return nil
	}
	answers := make([]event.PollOption, len(poll.Options))
	for i, option := range poll.Options {
		if option.ID == "" || option.Title == "" || slices.ContainsFunc(answers[:i], func(previous event.PollOption) bool { return previous.ID == option.ID }) {
			return nil
		}
		answers[i] = event.PollOption{ID: option.ID, MSC1767Message: event.MSC1767Message{Text: option.Title}}
	}
	maxSelections := 1
	if poll.Type == "multi" {
		maxSelections = len(answers)
	}
	return &bridgev2.ConvertedMessagePart{
		Type:    event.EventUnstablePollStart,
		Content: &event.MessageEventContent{MsgType: event.MsgText, Body: body},
		Extra: map[string]any{
			"org.matrix.msc1767.text": body,
			"org.matrix.msc3381.poll.start": event.PollStart{
				Kind: "org.matrix.msc3381.poll.disclosed", MaxSelections: maxSelections,
				Question: event.MSC1767Message{Text: poll.Subject}, Answers: answers,
			},
		},
		DBMetadata: &MessageMetadata{PollID: poll.ID},
	}
}
