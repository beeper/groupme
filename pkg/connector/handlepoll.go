package connector

import (
	"context"
	"errors"
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

// GroupMe polls require an expiration, even though Matrix polls stay open until ended.
func (gc *GMClient) HandleMatrixPollStart(ctx context.Context, msg *bridgev2.MatrixPollStart) (*bridgev2.MatrixMessageResponse, error) {
	portalType, groupID := ParsePortalID(msg.Portal.ID)
	if portalType != PortalTypeGroup {
		return nil, permanentFailure(event.MessageStatusUnsupported, "GroupMe polls can only be created in groups")
	}
	start := msg.Content.PollStart
	request := &groupme.CreatePollRequest{
		Subject:    start.Question.GetText(),
		Options:    make([]groupme.PollOption, len(start.Answers)),
		Expiration: groupme.Timestamp(time.Now().Add(14*24*time.Hour - time.Minute).Unix()),
		Visibility: "public",
		Type:       "single",
	}
	if start.MaxSelections > 1 {
		request.Type = "multi"
	}
	for i, answer := range start.Answers {
		if answer.ID == "" || slices.ContainsFunc(start.Answers[:i], func(previous event.PollOption) bool { return previous.ID == answer.ID }) {
			return nil, permanentFailure(event.MessageStatusUnsupported, "Poll answer IDs must be non-empty and unique")
		}
		request.Options[i].Title = answer.GetText()
	}
	poll, messageID, err := gc.Client.CreatePoll(ctx, string(groupID), request)
	if err != nil {
		status := wrapGroupMeError(err, "failed to create poll")
		var meta *groupme.Meta
		if status.Status == event.MessageStatusFail && errors.As(err, &meta) && len(meta.Errors) > 0 {
			status = status.WithMessage(strings.Join(meta.Errors, "; "))
		}
		return nil, status
	}
	if len(poll.Options) != len(start.Answers) {
		return nil, wrapGroupMeError(errors.New("GroupMe returned a different number of poll options"), "unexpected response from GroupMe")
	}
	answerIDs := make(map[string]string, len(start.Answers))
	for i, answer := range start.Answers {
		answerIDs[answer.ID] = poll.Options[i].ID
	}
	return &bridgev2.MatrixMessageResponse{DB: &database.Message{
		ID:        MakeMessageID(messageID),
		SenderID:  MakeUserID(groupme.ID(gc.Meta.GMID)),
		Timestamp: poll.CreatedAt.ToTime(),
		Metadata:  &MessageMetadata{PollID: poll.ID, PollAnswerIDs: answerIDs},
	}}, nil
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
		return nil, wrapGroupMeError(err, "failed to load poll").WithIsCertain(true)
	}
	if poll.Status != "active" || (poll.Expiration > 0 && !poll.Expiration.ToTime().After(time.Now())) {
		return nil, pollVoteError("This GroupMe poll has ended")
	}
	answers := slices.Clone(msg.Content.Response.Answers)
	if meta.PollAnswerIDs != nil {
		for i, answer := range answers {
			optionID, ok := meta.PollAnswerIDs[answer]
			if !ok {
				return nil, pollVoteError("Unknown GroupMe poll answer")
			}
			answers[i] = optionID
		}
	}
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
		return nil, wrapGroupMeError(err, "failed to vote in poll")
	}
	confirmed, expected := slices.Clone(result.MyVotes), slices.Clone(answers)
	slices.Sort(confirmed)
	slices.Sort(expected)
	if !slices.Equal(confirmed, expected) {
		return nil, wrapGroupMeError(errors.New("GroupMe did not confirm the requested poll selection"), "unexpected response from GroupMe")
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
