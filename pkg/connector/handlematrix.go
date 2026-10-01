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
	"mime"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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

// HandleMatrixMessage bridges an outgoing Matrix message to GroupMe.
//
// Plain text (and emote/notice) messages, plus outgoing image, location,
// video, and file attachments (new -- the legacy bridge never implemented
// any outgoing media at all, see NOTES.md). Video and file both needed a
// real upload API GroupMe doesn't publicly document; both were
// reverse-engineered live (a packet-capture session against the real
// GroupMe web client, then confirmed independently from this Go code
// against the live API) -- see NOTES.md "Outgoing video/file attachments"
// for the full investigation and groupmeext.UploadVideo/UploadFile for
// the resulting implementation.
func (gc *GMClient) HandleMatrixMessage(ctx context.Context, msg *bridgev2.MatrixMessage) (*bridgev2.MatrixMessageResponse, error) {
	content := msg.Content
	text := content.Body
	if content.Format == event.FormatHTML && content.FormattedBody != "" {
		text = groupmeHTMLParser.Parse(content.FormattedBody, format.NewContext(ctx))
	}
	if content.MsgType == event.MsgEmote {
		text = "/me " + text
	}

	portalType, gmid := ParsePortalID(msg.Portal.ID)
	out := &groupme.Message{
		Text: text,
		SourceGUID: uuid.NewSHA1(uuid.NameSpaceURL, []byte(strings.Join([]string{
			"mautrix-groupme", gc.Meta.GMID, string(msg.Portal.ID), string(msg.Event.ID),
		}, "\x00"))).String(),
	}

	switch content.MsgType {
	case event.MsgImage:
		attachment, err := gc.uploadMatrixImage(ctx, content)
		if err != nil {
			return nil, fmt.Errorf("failed to upload image to GroupMe: %w", err)
		}
		out.Attachments = []*groupme.Attachment{attachment}
		// content.Body is the filename (e.g. "image.jpg"), not a caption --
		// showing it as message text alongside the attachment would look
		// wrong (a stray filename above the image), so an image with no
		// separate caption gets no Text at all, matching how a plain image
		// send looks in the native GroupMe app.
		out.Text = ""

	case event.MsgLocation:
		attachment, err := matrixLocationToAttachment(content)
		if err != nil {
			return nil, fmt.Errorf("failed to convert Matrix location for GroupMe: %w", err)
		}
		out.Attachments = []*groupme.Attachment{attachment}
		// Same reasoning as image, above: content.Body for a Matrix
		// location is typically a generic client-generated description
		// (e.g. "User location"), not meaningful text worth showing
		// alongside the pin -- the attachment's own Name already carries
		// whatever description was given (see matrixLocationToAttachment).
		out.Text = ""

	case event.MsgVideo:
		attachment, err := gc.uploadMatrixVideo(ctx, content, portalType, gmid)
		if err != nil {
			return nil, fmt.Errorf("failed to upload video to GroupMe: %w", err)
		}
		out.Attachments = []*groupme.Attachment{attachment}
		out.Text = ""

	case event.MsgFile:
		// GroupMe's file-sharing feature is group-only (see
		// groupmeext.UploadFile/DownloadFile's doc comments) -- there's no
		// recipient-scoped equivalent to fall back to for a DM the way
		// video has GroupId/RecipientId, so this fails clearly instead of
		// guessing at an endpoint shape that doesn't exist.
		if portalType != PortalTypeGroup {
			return nil, fmt.Errorf("outgoing file attachments are only supported in groups, not DMs (GroupMe's file-sharing feature is group-only)")
		}
		attachment, err := gc.uploadMatrixFile(ctx, content, gmid)
		if err != nil {
			return nil, fmt.Errorf("failed to upload file to GroupMe: %w", err)
		}
		out.Attachments = []*groupme.Attachment{attachment}
		out.Text = ""
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

	return &bridgev2.MatrixMessageResponse{
		DB: &database.Message{
			ID:        MakeMessageID(sent.ID),
			MXID:      msg.Event.ID,
			Room:      msg.Portal.PortalKey,
			SenderID:  MakeUserID(groupme.ID(gc.Meta.GMID)),
			Timestamp: time.UnixMilli(msg.Event.Timestamp),
		},
	}, nil
}

// uploadMatrixImage downloads an outgoing m.image event's media from Matrix
// (handling both encrypted and unencrypted rooms via the same DownloadMedia
// call -- content.File is nil for unencrypted media, in which case
// DownloadMedia falls back to content.URL directly per its documented
// contract) and re-uploads it to GroupMe's separate image-upload host
// (thirdparty/groupme-lib/image_service.go, a local addition -- this
// bridge never supported outgoing media at all before this, encrypted or
// not), returning a ready-to-attach groupme.Attachment.
func (gc *GMClient) uploadMatrixImage(ctx context.Context, content *event.MessageEventContent) (*groupme.Attachment, error) {
	data, err := gc.Main.br.Bot.DownloadMedia(ctx, content.URL, content.File)
	if err != nil {
		return nil, fmt.Errorf("failed to download image from Matrix: %w", err)
	}

	mimeType := ""
	if content.Info != nil {
		mimeType = content.Info.MimeType
	}

	url, err := gc.Client.UploadImage(ctx, data, mimeType)
	if err != nil {
		return nil, err
	}

	return &groupme.Attachment{Type: groupme.Image, URL: url}, nil
}

// uploadMatrixVideo downloads an outgoing m.video event's media from
// Matrix and uploads it to GroupMe via groupmeext.UploadVideo, returning a
// ready-to-attach groupme.Attachment. Unlike image, this needs to know
// whether the target is a group or a DM (GroupMe's upload-session API
// wants either a group ID or a recipient ID, not a conversation ID
// generically -- see UploadVideo's doc comment) and the file's extension
// (also required by that same API; derived from the Matrix filename,
// falling back to the mimetype and then a hardcoded "mp4" if neither
// yields one).
func (gc *GMClient) uploadMatrixVideo(ctx context.Context, content *event.MessageEventContent, portalType PortalType, gmid groupme.ID) (*groupme.Attachment, error) {
	data, err := gc.Main.br.Bot.DownloadMedia(ctx, content.URL, content.File)
	if err != nil {
		return nil, fmt.Errorf("failed to download video from Matrix: %w", err)
	}

	mimeType := "video/mp4"
	if content.Info != nil && content.Info.MimeType != "" {
		mimeType = content.Info.MimeType
	}

	var groupID, recipientID string
	switch portalType {
	case PortalTypeGroup:
		groupID = gmid.String()
	case PortalTypeDM:
		recipientID = gmid.String()
	}

	renderURL, thumbnailURL, err := groupmeext.UploadVideo(ctx, gc.Meta.Token, gc.Meta.GMID, groupID, recipientID, data, videoExtension(content, mimeType), mimeType)
	if err != nil {
		return nil, err
	}

	return &groupme.Attachment{Type: groupme.Video, URL: renderURL, VideoPreviewURL: thumbnailURL}, nil
}

// videoExtension picks a file extension (no leading dot) for an outgoing
// video upload -- required by GroupMe's upload-session API
// (groupmeext.UploadVideo), which has no way to infer it server-side
// since the request just declares the extension up front rather than
// e.g. sniffing the uploaded bytes. Prefers the real Matrix filename's own
// extension; falls back to a guess from the MIME type, then to "mp4" if
// neither is available -- unconfirmed whether GroupMe cares about this
// value beyond cosmetic (the URL it hands back embeds it, see
// createUploadResponse), but there's no reason to guess less carefully
// than necessary.
func videoExtension(content *event.MessageEventContent, mimeType string) string {
	if content.Body != "" {
		if ext := strings.TrimPrefix(filepath.Ext(content.Body), "."); ext != "" {
			return strings.ToLower(ext)
		}
	}
	if exts, err := mime.ExtensionsByType(mimeType); err == nil && len(exts) > 0 {
		return strings.ToLower(strings.TrimPrefix(exts[0], "."))
	}
	return "mp4"
}

// uploadMatrixFile downloads an outgoing m.file event's media from Matrix
// and uploads it to GroupMe via groupmeext.UploadFile, returning a
// ready-to-attach groupme.Attachment. groupID is required (not just a
// generic portal/conversation ID) since GroupMe's file API is
// group-scoped -- see UploadFile's doc comment; callers must only reach
// this for a group portal (checked in HandleMatrixMessage).
//
// content.Body -- the real Matrix filename -- is passed through as-is:
// UploadFile's doc comment covers why this is the one thing that actually
// determines both the stored filename *and* mime type (GroupMe derives
// the latter from the former's extension) on GroupMe's side.
func (gc *GMClient) uploadMatrixFile(ctx context.Context, content *event.MessageEventContent, groupID groupme.ID) (*groupme.Attachment, error) {
	data, err := gc.Main.br.Bot.DownloadMedia(ctx, content.URL, content.File)
	if err != nil {
		return nil, fmt.Errorf("failed to download file from Matrix: %w", err)
	}

	mimeType := ""
	if content.Info != nil {
		mimeType = content.Info.MimeType
	}

	fileID, err := groupmeext.UploadFile(ctx, groupID, gc.Meta.Token, content.Body, data, mimeType)
	if err != nil {
		return nil, err
	}

	return &groupme.Attachment{Type: groupme.File, FileID: fileID}, nil
}

// matrixLocationToAttachment converts an outgoing m.location event's
// content.GeoURI (an RFC 5870 "geo:" URI, e.g.
// "geo:37.786971,-122.399677;u=35") into a GroupMe location attachment.
// No upload/API call needed, unlike image (or the still-unimplemented
// video/file) -- a GroupMe location is just lat/lng and a name embedded
// directly in the message JSON (see the incoming path's equivalent
// parsing in handlegroupme.go's convertGroupMeMessage, which this
// mirrors).
func matrixLocationToAttachment(content *event.MessageEventContent) (*groupme.Attachment, error) {
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
	var emoji string
	if msg.PreHandleResp != nil {
		emoji = msg.PreHandleResp.Emoji
	}
	err := gc.Client.CreateLike(ctx, conversationID, messageID, emoji)
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
