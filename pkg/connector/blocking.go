package connector

import (
	"context"
	"fmt"
	"slices"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"

	"github.com/beeper/groupme-lib"
)

var _ bridgev2.UserBlockingNetworkAPI = (*GMClient)(nil)

func (gc *GMClient) HandleMatrixBlockUser(ctx context.Context, msg *bridgev2.MatrixBlockUser) error {
	if !gc.IsLoggedIn() {
		return bridgev2.ErrNotLoggedIn
	}
	if msg.Content.ReportSpam {
		return fmt.Errorf("GroupMe spam reporting is not supported")
	}
	kind, peer := ParsePortalID(msg.Portal.ID)
	if kind != PortalTypeDM || peer == "" {
		return fmt.Errorf("invalid GroupMe DM portal")
	}
	// Native Web uses the account and peer user IDs, not the DM conversation
	// ID: POST/DELETE /v3/blocks?user=<self>&otherUser=<peer>, without a body.
	if msg.Content.Block {
		_, err := gc.Client.CreateBlock(ctx, gc.Meta.GMID, string(peer))
		return err
	}
	return gc.Client.Unblock(ctx, gc.Meta.GMID, string(peer))
}

func (gc *GMClient) userBlocked(ctx context.Context, peer groupme.ID) *bool {
	// /blocks/between is symmetric: it is also true when the peer blocked us.
	// Only our outgoing blocks belong in bridgev2's UserBlocked field.
	blocks, err := gc.Client.IndexBlock(ctx, gc.Meta.GMID)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to fetch GroupMe blocked users")
		return nil
	}
	blocked := slices.ContainsFunc(blocks, func(block *groupme.Block) bool {
		return block != nil && string(block.UserID) == gc.Meta.GMID && block.BlockedUserID == peer
	})
	return &blocked
}

func (gc *GMClient) syncBlockedUsers(ctx context.Context) {
	blocks, err := gc.Client.IndexBlock(ctx, gc.Meta.GMID)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to sync GroupMe blocked users")
		return
	}
	previous, err := gc.Main.br.DB.Portal.GetAllBlockedDMsOf(ctx, gc.UserLogin.ID)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to get previously blocked GroupMe DMs")
		return
	}
	for _, evt := range gc.blockListEvents(blocks, previous) {
		gc.Main.br.QueueRemoteEvent(gc.UserLogin, evt)
	}
}

func (gc *GMClient) blockListEvents(blocks []*groupme.Block, previous []networkid.PortalKey) []*simplevent.ChatInfoChange {
	// GroupMe omits blocked DMs from /chats. Refresh existing portals directly
	// from /blocks, and clear blocks removed natively even if the DM is hidden.
	updates := make(map[networkid.PortalKey]bool, len(blocks)+len(previous))
	for _, key := range previous {
		updates[key] = false
	}
	for _, block := range blocks {
		if block != nil && string(block.UserID) == gc.Meta.GMID && block.BlockedUserID != "" {
			updates[gc.portalKeyForDM(block.BlockedUserID)] = true
		}
	}
	events := make([]*simplevent.ChatInfoChange, 0, len(updates))
	for key, blocked := range updates {
		events = append(events, &simplevent.ChatInfoChange{
			EventMeta:      simplevent.EventMeta{Type: bridgev2.RemoteEventChatInfoChange, PortalKey: key},
			ChatInfoChange: &bridgev2.ChatInfoChange{ChatInfo: &bridgev2.ChatInfo{UserBlocked: &blocked}},
		})
	}
	return events
}
