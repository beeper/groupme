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

	"github.com/beeper/groupme-lib"
)

var _ bridgev2.BackfillingNetworkAPI = (*GMClient)(nil)

// Native IDs are decimal strings. Compare without assuming they fit in int64.
func compareMessageIDs(a, b groupme.ID) int {
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return strings.Compare(string(a), string(b))
}

// Poll votes are stored under their Matrix event ID, dated just before their
// poll. Step over them to the nearest native message in the given direction.
func (gc *GMClient) nearestNativeMessage(ctx context.Context, portal networkid.PortalKey, msg *database.Message, newer bool) (*database.Message, error) {
	var err error
	for msg != nil && isPollVoteID(msg.ID) {
		if newer {
			msg, err = gc.Main.br.DB.Message.GetFirstNonFakePartAfterTime(ctx, portal, msg.Timestamp)
		} else {
			msg, err = gc.Main.br.DB.Message.GetLastNonFakePartAtOrBeforeTime(ctx, portal, msg.Timestamp.Add(-time.Nanosecond))
		}
		if err != nil {
			return nil, fmt.Errorf("failed to find GroupMe message near poll vote: %w", err)
		}
	}
	return msg, nil
}

// fetchMessagePage normalizes GroupMe's empty-page response and returns newest
// first. Only groups support after_id (which returns oldest first remotely).
func (gc *GMClient) fetchMessagePage(ctx context.Context, portalID networkid.PortalID, before, after groupme.ID, limit int) ([]*groupme.Message, error) {
	kind, chatID := ParsePortalID(portalID)
	var messages []*groupme.Message
	var err error
	switch kind {
	case PortalTypeGroup:
		var resp groupme.IndexMessagesResponse
		resp, err = gc.Client.IndexMessages(ctx, chatID, &groupme.IndexMessagesQuery{BeforeID: before, AfterID: after, Limit: limit})
		messages = resp.Messages
	case PortalTypeDM:
		var resp groupme.IndexDirectMessagesResponse
		resp, err = gc.Client.IndexDirectMessages(ctx, string(chatID), &groupme.IndexDirectMessagesQuery{BeforeID: before})
		messages = resp.Messages
	default:
		return nil, fmt.Errorf("invalid GroupMe portal type")
	}
	var meta *groupme.Meta
	if errors.As(err, &meta) && meta.Code == groupme.HTTPNotModified {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	for _, msg := range messages {
		if msg == nil || msg.ID == "" || msg.UserID == "" {
			return nil, fmt.Errorf("GroupMe history response is missing a message or sender ID")
		}
		if gc.portalKeyForMessage(msg).ID != portalID || (kind == PortalTypeDM && msg.UserID != groupme.ID(gc.Meta.GMID) && msg.UserID != chatID && msg.UserID != "system") {
			return nil, fmt.Errorf("GroupMe history response belongs to another conversation")
		}
		if before != "" && compareMessageIDs(msg.ID, before) >= 0 || after != "" && compareMessageIDs(msg.ID, after) <= 0 {
			return nil, fmt.Errorf("GroupMe history pagination did not advance")
		}
	}
	slices.SortFunc(messages, func(a, b *groupme.Message) int { return compareMessageIDs(b.ID, a.ID) })
	return messages, nil
}

func (gc *GMClient) FetchMessages(ctx context.Context, params bridgev2.FetchMessagesParams) (*bridgev2.FetchMessagesResponse, error) {
	if params.ThreadRoot != "" {
		return nil, fmt.Errorf("GroupMe does not have message threads")
	}
	if !gc.IsLoggedIn() {
		return nil, bridgev2.ErrNotLoggedIn
	}
	count := max(params.Count, 1)
	kind, _ := ParsePortalID(params.Portal.ID)
	anchorMsg, err := gc.nearestNativeMessage(ctx, params.Portal.PortalKey, params.AnchorMessage, !params.Forward)
	if err != nil {
		return nil, err
	}
	var anchor, before, after groupme.ID
	if anchorMsg != nil {
		anchor = ParseMessageID(anchorMsg.ID)
	}
	if !params.Forward {
		before = groupme.ID(params.Cursor)
		if before == "" {
			before = anchor
		}
	} else if kind == PortalTypeGroup {
		after = anchor
	}
	var messages []*groupme.Message
	hasMore := true
	for len(messages) < count && hasMore {
		limit := min(count-len(messages), 100)
		if kind == PortalTypeDM {
			limit = 20
		}
		page, err := gc.fetchMessagePage(ctx, params.Portal.ID, before, after, limit)
		if err != nil {
			return nil, err
		}
		hasMore = len(page) == limit
		if len(page) == 0 {
			break
		}
		if after != "" {
			after = page[0].ID
		} else {
			before = page[len(page)-1].ID
		}
		for _, msg := range page {
			if params.Forward && anchor != "" && compareMessageIDs(msg.ID, anchor) <= 0 {
				hasMore = false
				continue
			}
			messages = append(messages, msg)
		}
	}
	slices.SortFunc(messages, func(a, b *groupme.Message) int { return compareMessageIDs(a.ID, b.ID) })
	resp := &bridgev2.FetchMessagesResponse{
		Forward: params.Forward, HasMore: hasMore,
		Cursor:                  networkid.PaginationCursor(before),
		AggressiveDeduplication: true,
		MarkRead:                params.AnchorMessage == nil,
	}
	for _, msg := range messages {
		sender := gc.messageSender(msg)
		intent, ok := params.Portal.GetIntentFor(ctx, sender, gc.UserLogin, bridgev2.RemoteEventMessage)
		if !ok {
			return nil, bridgev2.ErrFailedToGetIntent
		}
		converted, err := gc.convertMessage(ctx, params.Portal, intent, msg)
		if err != nil {
			return nil, err
		}
		resp.Messages = append(resp.Messages, &bridgev2.BackfillMessage{
			ConvertedMessage: converted, ID: MakeMessageID(msg.ID), Sender: sender,
			Timestamp: msg.CreatedAt.ToTime(), Reactions: gc.messageReactions(msg).ToBackfill(),
		})
	}
	return resp, nil
}
