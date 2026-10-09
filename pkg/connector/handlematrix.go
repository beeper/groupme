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
	"fmt"
	"math"
	"mime"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"go.mau.fi/util/variationselector"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"

	"github.com/beeper/groupme-lib"

	"github.com/beeper/groupme/pkg/groupmeext"
)

var _ bridgev2.ReactionHandlingNetworkAPI = (*GMClient)(nil)

func (gc *GMClient) PreHandleMatrixReaction(ctx context.Context, msg *bridgev2.MatrixReaction) (bridgev2.MatrixReactionPreResponse, error) {
	emoji := variationselector.FullyQualify(msg.Content.RelatesTo.Key)
	if !groupme.UnicodeLikeIcons[emoji] {
		return bridgev2.MatrixReactionPreResponse{}, bridgev2.WrapErrorInStatus(
			fmt.Errorf("GroupMe does not support the %q reaction", emoji),
		).WithIsCertain(true).WithErrorAsMessage().WithErrorReason(event.MessageStatusUnsupported)
	}
	return bridgev2.MatrixReactionPreResponse{
		SenderID:     MakeUserID(groupme.ID(gc.Meta.GMID)),
		Emoji:        emoji,
		MaxReactions: 1,
	}, nil
}

func unformattedText(text string, _ format.Context) string { return text }

var groupmeHTMLParser = &format.HTMLParser{
	TabsToSpaces:           4,
	Newline:                "\n",
	HorizontalLine:         "\n---\n",
	PillConverter:          format.DefaultPillConverter,
	BoldConverter:          unformattedText,
	ItalicConverter:        unformattedText,
	StrikethroughConverter: unformattedText,
	UnderlineConverter:     unformattedText,
	MonospaceConverter:     unformattedText,
	MonospaceBlockConverter: func(code, _ string, _ format.Context) string {
		return code
	},
}

// HandleMatrixMessage converts and sends a native GroupMe message.
func (gc *GMClient) HandleMatrixMessage(ctx context.Context, msg *bridgev2.MatrixMessage) (*bridgev2.MatrixMessageResponse, error) {
	if msg.ReplyTo != nil && (msg.ReplyTo.ID == "" || msg.ReplyTo.Room != msg.Portal.PortalKey) {
		return nil, fmt.Errorf("GroupMe replies must target a message in the same conversation")
	}
	content := msg.Content
	text := content.Body
	if content.Format == event.FormatHTML && content.FormattedBody != "" {
		text = groupmeHTMLParser.Parse(content.FormattedBody, format.NewContext(ctx))
	}
	if content.MsgType == event.MsgEmote {
		text = "/me " + text
	}

	portalType, gmid := ParsePortalID(msg.Portal.ID)
	if gmid == "" || (portalType != PortalTypeGroup && portalType != PortalTypeDM) {
		return nil, fmt.Errorf("invalid GroupMe portal")
	}
	if content.MsgType == event.MsgImage || content.MsgType == event.MsgVideo || content.MsgType == event.MsgFile {
		text = content.GetCaption()
		if text != "" && content.Format == event.FormatHTML && content.FormattedBody != "" {
			text = groupmeHTMLParser.Parse(content.FormattedBody, format.NewContext(ctx))
		}
	}
	if utf8.RuneCountInString(text) > 1000 {
		return nil, fmt.Errorf("GroupMe messages are limited to 1000 characters")
	}
	out := &groupme.Message{
		Text: text,
		SourceGUID: uuid.NewSHA1(uuid.NameSpaceURL, []byte(strings.Join([]string{
			"mautrix-groupme", gc.Meta.GMID, string(msg.Portal.ID), string(msg.Event.ID),
		}, "\x00"))).String(),
	}
	if msg.InputTransactionID != "" {
		out.SourceGUID = string(msg.InputTransactionID)
	}

	switch content.MsgType {
	case event.MsgText, event.MsgNotice, event.MsgEmote:
	case event.MsgImage, event.MsgVideo, event.MsgFile:
		attachment, err := gc.uploadMatrixMedia(ctx, content, portalType, gmid)
		if err != nil {
			return nil, err
		}
		out.Attachments = []*groupme.Attachment{attachment}
	case event.MsgLocation:
		attachment, err := matrixLocationToAttachment(content)
		if err != nil {
			return nil, err
		}
		out.Attachments = []*groupme.Attachment{attachment}
		out.Text = "" // The pin's name already contains the description.

	default:
		return nil, bridgev2.ErrUnsupportedMessageType
	}

	if msg.ReplyTo != nil {
		out.Attachments = append(out.Attachments, &groupme.Attachment{
			Type:        groupme.Reply,
			ReplyID:     ParseMessageID(msg.ReplyTo.ID),
			BaseReplyID: ParseMessageID(msg.ReplyTo.ID),
			UserID:      ParseUserID(msg.ReplyTo.SenderID),
		})
	}

	var sent *groupme.Message
	var err error
	switch portalType {
	case PortalTypeGroup:
		out.GroupID = gmid
		sent, err = gc.Client.CreateMessage(ctx, gmid, out)
	case PortalTypeDM:
		out.RecipientID = gmid
		if err = gc.approveDMRequestIfPending(ctx, gmid); err != nil {
			return nil, err
		}
		sent, err = gc.Client.CreateDirectMessage(ctx, out)
	default:
		return nil, fmt.Errorf("unknown portal type for %s", msg.Portal.ID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to send message to GroupMe: %w", err)
	}
	if sent == nil || sent.ID == "" {
		return nil, fmt.Errorf("GroupMe send response did not include a message ID")
	}
	if (sent.UserID != "" && string(sent.UserID) != gc.Meta.GMID) ||
		(sent.GroupID != "" && sent.GroupID != out.GroupID) ||
		(sent.RecipientID != "" && sent.RecipientID != out.RecipientID) {
		return nil, fmt.Errorf("GroupMe send response belongs to another sender or conversation")
	}
	dbMessage := &database.Message{
		ID:       MakeMessageID(sent.ID),
		SenderID: MakeUserID(groupme.ID(gc.Meta.GMID)),
	}
	if sent.CreatedAt > 0 {
		dbMessage.Timestamp = sent.CreatedAt.ToTime()
	}
	return &bridgev2.MatrixMessageResponse{DB: dbMessage}, nil
}

// uploadMatrixMedia shares the Matrix download and byte limit checks. GroupMe
// has separate image, video-session, and group-file upload APIs.
func (gc *GMClient) uploadMatrixMedia(ctx context.Context, content *event.MessageEventContent, portalType PortalType, gmid groupme.ID) (*groupme.Attachment, error) {
	data, err := gc.Main.br.Bot.DownloadMedia(ctx, content.URL, content.File)
	if err != nil {
		return nil, fmt.Errorf("failed to download media from Matrix: %w", err)
	}
	limit := MaxFileSize
	if content.MsgType == event.MsgFile {
		limit = MaxDocumentSize
	}
	if len(data) > limit {
		return nil, fmt.Errorf("GroupMe attachment exceeds the %d-byte limit", limit)
	}
	mimeType := ""
	if content.Info != nil {
		mimeType = content.Info.MimeType
	}
	switch content.MsgType {
	case event.MsgImage:
		url, err := gc.Client.UploadImage(ctx, data, mimeType)
		return &groupme.Attachment{Type: groupme.Image, URL: url}, err
	case event.MsgVideo:
		if mimeType == "" {
			mimeType = "video/mp4"
		}
		var groupID, recipientID string
		if portalType == PortalTypeDM {
			recipientID = string(gmid)
		} else {
			groupID = string(gmid)
		}
		url, preview, err := groupmeext.UploadVideo(ctx, gc.Meta.Token, gc.Meta.GMID, groupID, recipientID, data, videoExtension(content, mimeType), mimeType)
		return &groupme.Attachment{Type: groupme.Video, URL: url, VideoPreviewURL: preview}, err
	case event.MsgFile:
		fileID, err := groupmeext.UploadFile(ctx, gmid, gc.Meta.Token, content.GetFileName(), data, mimeType)
		return &groupme.Attachment{Type: groupme.File, FileID: fileID}, err
	default:
		return nil, bridgev2.ErrUnsupportedMessageType
	}
}

// The video upload session requires an extension before it receives any bytes.
func videoExtension(content *event.MessageEventContent, mimeType string) string {
	if ext := strings.TrimPrefix(filepath.Ext(content.GetFileName()), "."); ext != "" {
		return strings.ToLower(ext)
	}
	if exts, err := mime.ExtensionsByType(mimeType); err == nil && len(exts) > 0 {
		return strings.ToLower(strings.TrimPrefix(exts[0], "."))
	}
	return "mp4"
}

// GroupMe location attachments carry latitude, longitude, and a name.
func matrixLocationToAttachment(content *event.MessageEventContent) (*groupme.Attachment, error) {
	if !strings.HasPrefix(content.GeoURI, "geo:") {
		return nil, fmt.Errorf("location must have a geo: URI")
	}
	geo := strings.TrimPrefix(content.GeoURI, "geo:")
	// RFC 5870 allows an optional altitude (three comma-separated
	// coordinates instead of two) and a ";u=<uncertainty>" parameter
	// suffix; GroupMe only has room for lat/lng, so anything past the
	// first two coordinate fields is dropped.
	geo = strings.SplitN(geo, ";", 2)[0]
	parts := strings.SplitN(geo, ",", 3)
	if len(parts) < 2 {
		return nil, fmt.Errorf("malformed geo URI %q", content.GeoURI)
	}
	lat, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return nil, fmt.Errorf("invalid latitude in geo URI %q: %w", content.GeoURI, err)
	}
	lng, err := strconv.ParseFloat(parts[1], 64)
	if err != nil {
		return nil, fmt.Errorf("invalid longitude in geo URI %q: %w", content.GeoURI, err)
	}

	if math.IsNaN(lat) || math.IsInf(lat, 0) || math.Abs(lat) > 90 || math.IsNaN(lng) || math.IsInf(lng, 0) || math.Abs(lng) > 180 {
		return nil, fmt.Errorf("invalid location coordinates")
	}
	name := content.Body
	if name == "" {
		name = "Location"
	}

	return &groupme.Attachment{
		Type:      groupme.Location,
		Latitude:  strconv.FormatFloat(lat, 'f', -1, 64),
		Longitude: strconv.FormatFloat(lng, 'f', -1, 64),
		Name:      name,
	}, nil
}

// A new conversation may not have chat metadata yet. A known pending request,
// however, must be accepted before sending the reply.
func (gc *GMClient) approveDMRequestIfPending(ctx context.Context, otherUser groupme.ID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	convID := string(DMConversationID(groupme.ID(gc.Meta.GMID), otherUser))
	log := zerolog.Ctx(ctx).With().Str("conversation_id", convID).Logger()
	pending, err := groupmeext.ChatRequiresApproval(ctx, gc.Meta.Token, convID)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to check whether DM is a pending message request, sending anyway")
		return ctx.Err()
	}
	if !pending {
		return nil
	}
	if err := groupmeext.ApproveChat(ctx, gc.Meta.Token, convID); err != nil {
		return fmt.Errorf("failed to accept GroupMe message request: %w", err)
	}
	log.Info().Msg("Accepted GroupMe DM message request before replying")
	return nil
}

// HandleMatrixReaction bridges a Matrix reaction to a GroupMe "like".
func (gc *GMClient) HandleMatrixReaction(ctx context.Context, msg *bridgev2.MatrixReaction) (*database.Reaction, error) {
	portalType, gmid := ParsePortalID(msg.Portal.ID)
	conversationID := gmid
	if portalType == PortalTypeDM {
		conversationID = DMConversationID(groupme.ID(gc.Meta.GMID), gmid)
	}
	messageID := ParseMessageID(msg.TargetMessage.ID)
	err := gc.Client.CreateLike(ctx, conversationID, messageID, msg.PreHandleResp.Emoji)
	if err != nil {
		return nil, fmt.Errorf("failed to like GroupMe message: %w", err)
	}
	return &database.Reaction{}, nil
}

// HandleMatrixReactionRemove bridges removing a Matrix reaction to
// un-liking the GroupMe message.
func (gc *GMClient) HandleMatrixReactionRemove(ctx context.Context, msg *bridgev2.MatrixReactionRemove) error {
	portalType, gmid := ParsePortalID(msg.Portal.ID)
	conversationID := gmid
	if portalType == PortalTypeDM {
		conversationID = DMConversationID(groupme.ID(gc.Meta.GMID), gmid)
	}
	messageID := ParseMessageID(msg.TargetReaction.MessageID)
	err := gc.Client.DestroyLike(ctx, conversationID, messageID)
	if err != nil {
		return fmt.Errorf("failed to unlike GroupMe message: %w", err)
	}
	return nil
}
