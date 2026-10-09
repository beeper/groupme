package connector

import (
	"context"
	"strings"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/mediaproxy"

	"github.com/beeper/groupme-lib"
	"github.com/beeper/groupme/pkg/groupmeext"
)

var _ bridgev2.DirectMediableNetwork = (*GMConnector)(nil)

func (gc *GMConnector) SetUseDirectMedia() {
	gc.useDirectMedia = true
}

// Versioned, NUL-separated native identifiers. Image and video URLs come from
// message attachments, not upload sessions. No expiry was observed in those
// URLs, but their lifetime is not guaranteed and this path does not refresh them.
// Credentials stay in login metadata; the framework signs and serves media IDs.
//
//	1i: image URL
//	1v: login ID, video URL
//	1f: login ID, group ID, file ID
func (gc *GMConnector) directMediaURI(ctx context.Context, fields ...string) (id.ContentURIString, error) {
	mediaID := networkid.MediaID(strings.Join(fields, "\x00"))
	if _, err := parseDirectMediaID(mediaID); err != nil {
		return "", err
	}
	return gc.br.Matrix.GenerateContentURI(ctx, mediaID)
}

func parseDirectMediaID(mediaID networkid.MediaID) ([]string, error) {
	fields := strings.Split(string(mediaID), "\x00")
	for _, field := range fields {
		if field == "" {
			return nil, mediaproxy.ErrInvalidMediaIDSyntax
		}
	}
	switch fields[0] {
	case "1i":
		if len(fields) == 2 && groupmeext.ValidateMediaURL(fields[1]) == nil {
			return fields, nil
		}
	case "1v":
		if len(fields) == 3 && groupmeext.ValidateMediaURL(fields[2]) == nil {
			return fields, nil
		}
	case "1f":
		if len(fields) == 4 && !strings.ContainsAny(fields[2]+fields[3], "/?#%") {
			return fields, nil
		}
	}
	return nil, mediaproxy.ErrInvalidMediaIDSyntax
}

func (gc *GMConnector) Download(ctx context.Context, mediaID networkid.MediaID, _ map[string]string) (mediaproxy.GetMediaResponse, error) {
	fields, err := parseDirectMediaID(mediaID)
	if err != nil {
		return nil, err
	}
	var token string
	if fields[0] != "1i" {
		login := gc.br.GetCachedUserLoginByID(networkid.UserLoginID(fields[1]))
		if login == nil {
			return nil, mautrix.MNotFound.WithMessage("GroupMe media login not found")
		}
		meta, ok := login.Metadata.(*UserLoginMetadata)
		if !ok || meta == nil || meta.Token == "" {
			return nil, mautrix.MNotFound.WithMessage("GroupMe media login is not connected")
		}
		token = meta.Token
	}
	var data []byte
	var mime string
	switch fields[0] {
	case "1i":
		data, mime, err = groupmeext.DownloadImage(ctx, fields[1])
	case "1v":
		data, mime, err = groupmeext.DownloadVideo(ctx, fields[2], token)
	case "1f":
		data, _, mime, err = groupmeext.DownloadFile(ctx, groupme.ID(fields[2]), fields[3], token)
	}
	if err != nil {
		// A Matrix response error also keeps the framework from logging the
		// full media ID (which contains a native attachment URL).
		return nil, mautrix.MUnknown.WithMessage("Failed to download GroupMe media: %v", err)
	}
	response := mediaproxy.GetMediaResponseRawData(data)
	if mime != "" {
		response.ContentType = mime
	}
	return response, nil
}

func (gc *GMClient) directMediaContent(ctx context.Context, msg *groupme.Message, att *groupme.Attachment) (*event.MessageEventContent, error) {
	var fields []string
	content := &event.MessageEventContent{}
	switch att.Type {
	case groupme.Image:
		fields = []string{"1i", att.URL}
		content.MsgType, content.Body = event.MsgImage, "image"
	case groupme.Video:
		fields = []string{"1v", string(gc.UserLogin.ID), att.URL}
		content.MsgType, content.Body = event.MsgVideo, "video"
	case groupme.File:
		fields = []string{"1f", string(gc.UserLogin.ID), string(msg.GroupID), att.FileID}
		meta, err := groupmeext.GetFileMetadata(ctx, msg.GroupID, att.FileID, gc.Meta.Token)
		if err != nil {
			return nil, err
		}
		content.MsgType, content.Body = event.MsgFile, meta.FileName
		content.Info = &event.FileInfo{MimeType: meta.Mime, Size: meta.FileSize}
	default:
		return nil, nil
	}
	var err error
	content.URL, err = gc.Main.directMediaURI(ctx, fields...)
	if err != nil {
		return nil, err
	}
	return content, nil
}
