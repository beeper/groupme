package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"

	"github.com/beeper/groupme-lib"
	"github.com/beeper/groupme/pkg/groupmeext"
)

var _ bridgev2.EditHandlingNetworkAPI = (*GMClient)(nil)

func nativeContent(msg *groupme.Message) *groupmeext.MessageContent {
	attachments := msg.Attachments
	if attachments == nil {
		attachments = []*groupme.Attachment{}
	}
	return &groupmeext.MessageContent{Text: msg.Text, Attachments: attachments}
}

func (gc *GMClient) HandleMatrixEdit(ctx context.Context, msg *bridgev2.MatrixEdit) error {
	target := msg.EditTarget
	if target.Room != msg.Portal.PortalKey || string(target.SenderID) != gc.Meta.GMID || isPollVoteID(target.ID) {
		return permanentFailure(event.MessageStatusNoPermission, "only your own messages in this GroupMe conversation can be edited")
	}
	if time.Since(target.Timestamp) > 15*time.Minute {
		return bridgev2.ErrEditTargetTooOld
	}
	kind, chatID := ParsePortalID(msg.Portal.ID)
	if chatID == "" || (kind != PortalTypeGroup && kind != PortalTypeDM) {
		return fmt.Errorf("invalid GroupMe portal")
	}
	meta, _ := target.Metadata.(*MessageMetadata)
	if meta == nil {
		meta = &MessageMetadata{}
	}
	if meta.PollID != "" {
		return permanentFailure(event.MessageStatusUnsupported, "GroupMe polls cannot be edited as messages")
	}
	original := meta.NativeContent
	if original == nil {
		// Older mappings predate attachment preservation. Read the native message
		// before editing rather than dropping its reply or media attachments.
		var before groupme.ID
		for original == nil {
			page, err := gc.fetchMessagePage(ctx, msg.Portal.ID, before, "", 100)
			if err != nil {
				return wrapGroupMeError(err, "failed to load the original message").WithIsCertain(true)
			}
			if len(page) == 0 {
				break
			}
			for _, native := range page {
				if MakeMessageID(native.ID) == target.ID {
					original = nativeContent(native)
					break
				}
			}
			before = page[len(page)-1].ID
			if compareMessageIDs(before, ParseMessageID(target.ID)) <= 0 {
				break
			}
		}
		if original == nil {
			return permanentFailure(event.MessageStatusGenericError, "could not find the original GroupMe message to edit")
		}
	}
	text := msg.Content.Body
	switch msg.Content.MsgType {
	case event.MsgText, event.MsgNotice, event.MsgEmote:
	case event.MsgImage, event.MsgVideo, event.MsgFile:
		// Caption changes preserve the original media. Reject replacement media
		// instead of silently accepting an edit GroupMe cannot represent.
		previous, err := gc.Main.br.Bot.GetEvent(ctx, msg.Portal.MXID, target.MXID)
		if err != nil {
			return fmt.Errorf("read original media before caption edit: %w", err)
		}
		old := previous.Content.AsMessage()
		if old.MsgType != msg.Content.MsgType || old.URL != msg.Content.URL || !reflect.DeepEqual(old.File, msg.Content.File) {
			return permanentFailure(event.MessageStatusUnsupported, "GroupMe supports editing captions, not replacing media")
		}
		text = msg.Content.GetCaption()
	default:
		return bridgev2.ErrUnsupportedMessageType
	}
	if msg.Content.Format == event.FormatHTML && msg.Content.FormattedBody != "" {
		text = groupmeHTMLParser.Parse(msg.Content.FormattedBody, format.NewContext(ctx))
	}
	if msg.Content.MsgType == event.MsgEmote {
		text = "/me " + text
	}
	text = strings.TrimSpace(text)
	if utf8.RuneCountInString(text) > 1000 {
		return permanentFailure(event.MessageStatusUnsupported, "GroupMe messages are limited to 1000 characters")
	}
	if text == "" {
		return permanentFailure(event.MessageStatusUnsupported, "GroupMe edits require non-empty text")
	}
	content := groupmeext.MessageContent{Text: text, Attachments: []*groupme.Attachment{}}
	for _, attachment := range original.Attachments {
		// Text-relative mention and custom emoji offsets no longer describe the
		// edited plaintext. Media and reply identity must remain intact.
		if attachment != nil && string(attachment.Type) != "mentions" && string(attachment.Type) != "emoji" {
			content.Attachments = append(content.Attachments, attachment)
		}
	}
	updated, err := groupmeext.EditMessage(ctx, gc.Meta.Token, kind == PortalTypeDM, chatID, ParseMessageID(target.ID), content)
	if err != nil {
		return wrapGroupMeError(err, "failed to edit message")
	}
	if updated.UserID != groupme.ID(gc.Meta.GMID) || gc.portalKeyForMessage(&updated.Message) != msg.Portal.PortalKey {
		return wrapGroupMeError(errors.New("GroupMe edit response belongs to another sender or conversation"), "unexpected response from GroupMe")
	}
	meta.NativeContent = nativeContent(&updated.Message)
	meta.EditedAt = updated.UpdatedAt
	target.Metadata = meta
	return nil
}

func (gc *GMClient) handleMessageUpdate(msg groupme.Message) bool {
	evt, handled := gc.messageUpdateEvent(msg)
	if evt != nil {
		gc.Main.br.QueueRemoteEvent(gc.UserLogin, evt)
	}
	return handled
}

func (gc *GMClient) messageUpdateEvent(msg groupme.Message) (*simplevent.Message[*groupmeext.MessageUpdate], bool) {
	if msg.Event == nil || msg.Event.Type != "message.update" {
		return nil, false
	}
	var update groupmeext.MessageUpdate
	if err := json.Unmarshal(msg.Event.Data, &update); err != nil || update.MessageID == "" || update.SenderID == "" || update.UpdatedAt <= 0 {
		gc.UserLogin.Log.Warn().Msg("Ignoring malformed GroupMe message update")
		return nil, true
	}
	update.RevisionID = msg.ID
	key := gc.portalKeyForMessage(&msg)
	if key.ID == "" {
		return nil, true
	}
	return &simplevent.Message[*groupmeext.MessageUpdate]{
		EventMeta: simplevent.EventMeta{Type: bridgev2.RemoteEventEdit, PortalKey: key,
			Sender:    bridgev2.EventSender{Sender: MakeUserID(update.SenderID), IsFromMe: string(update.SenderID) == gc.Meta.GMID},
			Timestamp: update.UpdatedAt.ToTime()},
		ID: MakeMessageID(msg.ID), TargetMessage: MakeMessageID(update.MessageID), Data: &update,
		ConvertEditFunc: gc.convertEdit,
	}, true
}

func (gc *GMClient) recentEditEvents(messages []*groupme.Message) []*simplevent.Message[*groupmeext.MessageUpdate] {
	var events []*simplevent.Message[*groupmeext.MessageUpdate]
	seen := make(map[networkid.MessageID]bool)
	// History pages are newest first. Replaying intermediate revisions would
	// briefly revert messages whose backfilled content is already current.
	for _, msg := range messages {
		evt, _ := gc.messageUpdateEvent(*msg)
		if evt != nil && !seen[evt.TargetMessage] {
			seen[evt.TargetMessage] = true
			events = append(events, evt)
		}
	}
	return events
}

func (gc *GMClient) convertEdit(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, existing []*database.Message, update *groupmeext.MessageUpdate) (*bridgev2.ConvertedEdit, error) {
	result := &bridgev2.ConvertedEdit{}
	if len(existing) == 0 {
		return result, nil
	}
	if meta, ok := existing[0].Metadata.(*MessageMetadata); ok && meta != nil {
		if (meta.EditRevisionID != "" && update.RevisionID != "" && compareMessageIDs(meta.EditRevisionID, update.RevisionID) >= 0) || meta.EditedAt > update.UpdatedAt {
			return result, nil
		}
		if meta.NativeContent != nil && reflect.DeepEqual(meta.NativeContent, &update.Message) {
			// A successful outbound edit echoes through push. Persist its native
			// revision using the framework, without sending another Matrix edit.
			for _, part := range existing {
				partMeta, _ := part.Metadata.(*MessageMetadata)
				if partMeta == nil {
					partMeta = &MessageMetadata{}
					part.Metadata = partMeta
				}
				partMeta.NativeContent = &update.Message
				partMeta.EditedAt = update.UpdatedAt
				partMeta.EditRevisionID = update.RevisionID
				result.ModifiedParts = append(result.ModifiedParts, &bridgev2.ConvertedEditPart{
					Part: part, DontBridge: true, Type: event.EventMessage,
					Content: &event.MessageEventContent{MsgType: event.MsgText, Body: update.Message.Text},
				})
			}
			return result, nil
		}
	}
	msg := &groupme.Message{ID: update.MessageID, UserID: update.SenderID, Text: update.Message.Text, Attachments: update.Message.Attachments}
	if kind, chatID := ParsePortalID(portal.ID); kind == PortalTypeGroup {
		msg.GroupID = chatID
	}
	converted, err := gc.convertGroupMeMessage(ctx, portal, intent, msg)
	if err != nil {
		return nil, err
	}
	for _, part := range converted.Parts {
		meta := part.DBMetadata.(*MessageMetadata)
		meta.EditedAt = update.UpdatedAt
		meta.EditRevisionID = update.RevisionID
	}
	// Synchronous outbound sends have a single default part ID, whereas the
	// receive converter names media parts. Preserve that original mapping.
	if len(existing) == 1 && len(converted.Parts) == 1 {
		result.ModifiedParts = []*bridgev2.ConvertedEditPart{converted.Parts[0].ToEditPart(existing[0])}
		return result, nil
	}
	byPart := make(map[networkid.PartID]*database.Message, len(existing))
	for _, part := range existing {
		byPart[part.PartID] = part
	}
	if legacyFirst := byPart["attachment-0"]; legacyFirst != nil && byPart["text"] == nil {
		// Stored before the first part became the default part.
		byPart["text"], byPart[""] = byPart[""], legacyFirst
		delete(byPart, "attachment-0")
	}
	for _, part := range converted.Parts {
		part.DBMetadata.(*MessageMetadata).EditedAt = update.UpdatedAt
		if old := byPart[part.ID]; old != nil {
			result.ModifiedParts = append(result.ModifiedParts, part.ToEditPart(old))
			delete(byPart, part.ID)
		} else {
			if result.AddedParts == nil {
				result.AddedParts = &bridgev2.ConvertedMessage{ReplyTo: converted.ReplyTo}
			}
			result.AddedParts.Parts = append(result.AddedParts.Parts, part)
		}
	}
	unmatched := slices.Collect(maps.Values(byPart))
	for _, old := range existing {
		if slices.Contains(unmatched, old) {
			result.DeletedParts = append(result.DeletedParts, old)
		}
	}
	return result, nil
}
