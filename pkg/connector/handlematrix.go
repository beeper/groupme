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
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"go.mau.fi/util/variationselector"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"

	"github.com/beeper/groupme-lib"

	"github.com/beeper/groupme/pkg/groupmeext"
)

var (
	_ bridgev2.ReactionHandlingNetworkAPI        = (*GMClient)(nil)
	_ bridgev2.MessageRequestAcceptingNetworkAPI = (*GMClient)(nil)
)

func (gc *GMClient) PreHandleMatrixReaction(ctx context.Context, msg *bridgev2.MatrixReaction) (bridgev2.MatrixReactionPreResponse, error) {
	emoji := variationselector.FullyQualify(msg.Content.RelatesTo.Key)
	if !groupme.UnicodeLikeIcons[emoji] {
		return bridgev2.MatrixReactionPreResponse{}, permanentFailure(event.MessageStatusUnsupported, "GroupMe does not support the %q reaction", emoji)
	}
	return bridgev2.MatrixReactionPreResponse{
		SenderID:     MakeUserID(groupme.ID(gc.Meta.GMID)),
		Emoji:        emoji,
		MaxReactions: 1,
	}, nil
}

func permanentFailure(reason event.MessageStatusReason, message string, args ...any) bridgev2.MessageStatus {
	return bridgev2.WrapErrorInStatus(fmt.Errorf(message, args...)).WithIsCertain(true).WithErrorAsMessage().WithErrorReason(reason).WithStatus(event.MessageStatusFail)
}

func groupMeHTTPStatus(err error) int {
	var meta *groupme.Meta
	if errors.As(err, &meta) {
		return int(meta.Code)
	}
	var httpErr *groupmeext.HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode
	}
	return 0
}

// A GroupMe error response means the request was rejected; transport errors and
// 5xx responses leave the outcome unknown.
func (gc *GMClient) wrapGroupMeError(err error, message string) bridgev2.MessageStatus {
	status, reason, certain := event.MessageStatusRetriable, event.MessageStatusNetworkError, false
	switch code := groupMeHTTPStatus(err); {
	case code == http.StatusTooManyRequests:
		message, certain = "GroupMe is rate limiting requests, try again later", true
	case code == http.StatusUnauthorized:
		gc.rejectToken(err)
		message, certain = "your GroupMe session expired, please log in again", true
	case code == http.StatusForbidden:
		status, reason, message, certain = event.MessageStatusFail, event.MessageStatusNoPermission, "GroupMe doesn't allow this", true
	case code == http.StatusRequestTimeout || code == http.StatusTooEarly:
	case code >= 400 && code < 500:
		status, reason, message, certain = event.MessageStatusFail, event.MessageStatusGenericError, "GroupMe rejected the request", true
	}
	return bridgev2.WrapErrorInStatus(err).WithStatus(status).WithErrorReason(reason).WithIsCertain(certain).WithMessage(message)
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
		return nil, permanentFailure(event.MessageStatusUnsupported, "GroupMe replies must target a message in the same conversation").WithSendNotice(true)
	}
	if msg.ReplyTo != nil && isPollVoteID(msg.ReplyTo.ID) {
		return nil, permanentFailure(event.MessageStatusUnsupported, "GroupMe replies cannot target poll votes").WithSendNotice(true)
	}
	content := msg.Content
	portalType, gmid := ParsePortalID(msg.Portal.ID)
	if gmid == "" || (portalType != PortalTypeGroup && portalType != PortalTypeDM) {
		return nil, fmt.Errorf("invalid GroupMe portal")
	}
	if content.MsgType == event.MsgAudio && content.MSC3245Voice == nil {
		// GroupMe only has voice notes; other audio isn't in the room capabilities.
		return nil, bridgev2.ErrUnsupportedMessageType
	}
	text := content.Body
	switch content.MsgType {
	case event.MsgImage, event.MsgVideo, event.MsgFile:
		text = content.GetCaption()
	case event.MsgAudio:
		text = ""
	}
	fmtCtx := format.NewContext(ctx)
	if text != "" && content.Format == event.FormatHTML && content.FormattedBody != "" {
		text = gc.htmlParser().Parse(content.FormattedBody, fmtCtx)
	}
	if content.MsgType == event.MsgEmote {
		text = "/me " + text
	}
	text, mentions := takeMentions(text, fmtCtx)
	if utf8.RuneCountInString(text) > 1000 {
		return nil, permanentFailure(event.MessageStatusUnsupported, "GroupMe messages are limited to 1000 characters").WithSendNotice(true)
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
	case event.MsgImage, event.MsgVideo, event.MsgAudio, event.MsgFile:
		attachment, err := gc.uploadMatrixMedia(ctx, content, portalType, gmid)
		if err != nil {
			return nil, err
		}
		out.Attachments = []*groupme.Attachment{attachment}
	case event.MsgLocation:
		attachment, err := matrixLocationToAttachment(content)
		if err != nil {
			return nil, bridgev2.WrapErrorInStatus(err).WithMessage("invalid location").WithIsCertain(true).WithSendNotice(true).WithErrorReason(event.MessageStatusUnsupported).WithStatus(event.MessageStatusFail)
		}
		out.Attachments = []*groupme.Attachment{attachment}
		out.Text = "" // The pin's name already contains the description.

	default:
		return nil, bridgev2.ErrUnsupportedMessageType
	}

	if mentions != nil && out.Text != "" {
		out.Attachments = append(out.Attachments, mentions)
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
		sent, err = gc.Client.CreateDirectMessage(ctx, out)
	default:
		return nil, fmt.Errorf("unknown portal type for %s", msg.Portal.ID)
	}
	if err != nil {
		return nil, gc.wrapGroupMeError(err, "failed to send message")
	}
	if sent == nil || sent.ID == "" {
		return nil, gc.wrapGroupMeError(errors.New("GroupMe send response did not include a message ID"), "unexpected response from GroupMe")
	}
	if (sent.UserID != "" && string(sent.UserID) != gc.Meta.GMID) ||
		(sent.GroupID != "" && sent.GroupID != out.GroupID) ||
		(sent.RecipientID != "" && sent.RecipientID != out.RecipientID) {
		return nil, gc.wrapGroupMeError(errors.New("GroupMe send response belongs to another sender or conversation"), "unexpected response from GroupMe")
	}
	dbMessage := &database.Message{
		ID:       MakeMessageID(sent.ID),
		Metadata: &MessageMetadata{NativeContent: nativeContent(out)},
		SenderID: MakeUserID(groupme.ID(gc.Meta.GMID)),
	}
	if sent.CreatedAt > 0 {
		dbMessage.Timestamp = sent.CreatedAt.ToTime()
	}
	return &bridgev2.MatrixMessageResponse{DB: dbMessage}, nil
}

// uploadMatrixMedia shares the Matrix download and byte limit checks. GroupMe
// has separate image, media-session, and group-file upload APIs.
func (gc *GMClient) uploadMatrixMedia(ctx context.Context, content *event.MessageEventContent, portalType PortalType, gmid groupme.ID) (*groupme.Attachment, error) {
	data, err := gc.Main.br.Bot.DownloadMedia(ctx, content.URL, content.File)
	if err != nil {
		return nil, fmt.Errorf("%w from Matrix: %w", bridgev2.ErrMediaDownloadFailed, err)
	}
	limit := MaxFileSize
	if content.MsgType == event.MsgFile {
		limit = MaxDocumentSize
	}
	if len(data) > limit {
		return nil, permanentFailure(event.MessageStatusUnsupported, "GroupMe attachments are limited to %.0f MB", float64(limit)/1e6).WithSendNotice(true)
	}
	mimeType := ""
	if content.Info != nil {
		mimeType = content.Info.MimeType
	}
	switch content.MsgType {
	case event.MsgImage:
		url, err := gc.Client.UploadImage(ctx, data, mimeType)
		if err != nil {
			return nil, gc.wrapGroupMeError(err, "failed to upload media").WithIsCertain(true)
		}
		return &groupme.Attachment{Type: groupme.Image, URL: url}, nil
	case event.MsgVideo, event.MsgAudio:
		var extension string
		mimeType, extension = mediaUploadType(content, mimeType)
		req := groupmeext.UploadRequest{SenderId: gc.Meta.GMID, Extension: extension}
		if portalType == PortalTypeDM {
			req.RecipientId = string(gmid)
		} else {
			req.GroupId = string(gmid)
		}
		if content.MsgType == event.MsgVideo && content.Info != nil {
			req.Width, req.Height = content.Info.Width, content.Info.Height
		}
		uploaded, err := groupmeext.UploadMedia(ctx, gc.Meta.Token, req, data, mimeType)
		if err != nil {
			return nil, gc.wrapGroupMeError(err, "failed to upload media").WithIsCertain(true)
		}
		if content.MsgType == event.MsgAudio {
			return voiceNoteAttachment(content, uploaded), nil
		}
		return &groupme.Attachment{Type: groupme.Video, URL: uploaded.RenderURL, VideoPreviewURL: uploaded.ThumbnailURL}, nil
	case event.MsgFile:
		fileID, err := groupmeext.UploadFile(ctx, gmid, gc.Meta.Token, content.GetFileName(), data, mimeType)
		if err != nil {
			return nil, gc.wrapGroupMeError(err, "failed to upload media").WithIsCertain(true)
		}
		return &groupme.Attachment{Type: groupme.File, FileID: fileID}, nil
	default:
		return nil, bridgev2.ErrUnsupportedMessageType
	}
}

// The media upload session requires an extension before it receives any bytes.
func mediaUploadType(content *event.MessageEventContent, mimeType string) (string, string) {
	if content.MsgType == event.MsgAudio {
		mimeType = cmp.Or(mimeType, "audio/mp4")
	} else {
		mimeType = cmp.Or(mimeType, "video/mp4")
	}
	if ext := strings.TrimPrefix(filepath.Ext(content.GetFileName()), "."); ext != "" {
		return mimeType, strings.ToLower(ext)
	}
	if content.MsgType == event.MsgAudio {
		// mime lists audio/mp4 extensions alphabetically, so .f4a would come first.
		return mimeType, "m4a"
	}
	if exts, err := mime.ExtensionsByType(mimeType); err == nil && len(exts) > 0 {
		return mimeType, strings.ToLower(strings.TrimPrefix(exts[0], "."))
	}
	return mimeType, "mp4"
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

// Bridgev2 invokes this for explicit acceptance or before replying to a request.
func (gc *GMClient) HandleMatrixAcceptMessageRequest(ctx context.Context, msg *bridgev2.MatrixAcceptMessageRequest) error {
	portalType, otherUser := ParsePortalID(msg.Portal.ID)
	if portalType != PortalTypeDM {
		return permanentFailure(event.MessageStatusUnsupported, "GroupMe message requests are only supported in DMs")
	}
	conversationID := DMConversationID(groupme.ID(gc.Meta.GMID), otherUser)
	if err := groupmeext.ApproveChat(ctx, gc.Meta.Token, string(conversationID)); err != nil {
		return gc.wrapGroupMeError(err, "failed to accept message request")
	}
	return nil
}

// HandleMatrixReaction bridges a Matrix reaction to a GroupMe "like".
func (gc *GMClient) HandleMatrixReaction(ctx context.Context, msg *bridgev2.MatrixReaction) (*database.Reaction, error) {
	portalType, gmid := ParsePortalID(msg.Portal.ID)
	conversationID := gmid
	if portalType == PortalTypeDM {
		conversationID = DMConversationID(groupme.ID(gc.Meta.GMID), gmid)
	}
	if isPollVoteID(msg.TargetMessage.ID) {
		return nil, permanentFailure(event.MessageStatusUnsupported, "GroupMe reactions cannot target poll votes").WithSendNotice(true)
	}
	messageID := ParseMessageID(msg.TargetMessage.ID)
	err := gc.Client.CreateLike(ctx, conversationID, messageID, msg.PreHandleResp.Emoji)
	if err != nil {
		return nil, gc.wrapGroupMeError(err, "failed to send reaction")
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
		return gc.wrapGroupMeError(err, "failed to remove reaction")
	}
	return nil
}
