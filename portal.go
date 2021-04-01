// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2020 Tulir Asokan
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

package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io/ioutil"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/crypto/attachment"

	"github.com/Rhymen/go-whatsapp"
	"github.com/gabriel-vasile/mimetype"
	"github.com/karmanyaahm/groupme"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/pushrules"

	"github.com/karmanyaahm/matrix-groupme-go/database"
	"github.com/karmanyaahm/matrix-groupme-go/groupmeExt"
	"github.com/karmanyaahm/matrix-groupme-go/types"
	whatsappExt "github.com/karmanyaahm/matrix-groupme-go/whatsapp-ext"
)

func (bridge *Bridge) GetPortalByMXID(mxid id.RoomID) *Portal {
	bridge.portalsLock.Lock()
	defer bridge.portalsLock.Unlock()
	portal, ok := bridge.portalsByMXID[mxid]
	if !ok {
		return bridge.loadDBPortal(bridge.DB.Portal.GetByMXID(mxid), nil)
	}
	return portal
}

func (bridge *Bridge) GetPortalByJID(key database.PortalKey) *Portal {
	bridge.portalsLock.Lock()
	defer bridge.portalsLock.Unlock()
	portal, ok := bridge.portalsByJID[key]
	if !ok {
		return bridge.loadDBPortal(bridge.DB.Portal.JID(msgID)

	newLikes := newReactions(reactions, ppl)
	removeLikes := oldReactions(reactions, ppl)

	var eventID id.EventID
	if len(newLikes) > 0 {
		message := portal.bridge.DB.Message.GetByJID(portal.Key, msgID)
		if message == nil {
			portal.log.Errorln("Received reaction for unknown message", msgID)
			return
		}
		eventID = message.MXID
	}
	for _, i := range reactions {
		fmt.Printf("%+v, ", i.User)
	}
	fmt.Println(newLikes, ppl)

	for _, jid := range newLikes {
		intent := portal.getReactionIntent(jid)
		resp, err := portal.sendReaction(intent, eventID, "❤")
		if err != nil {
			portal.log.Errorln("Something wrong with sending reaction", msgID, jid, err)
			continue
		}

		newReaction := portal.bridge.DB.Reaction.New()
		newReaction.MXID = resp.EventID
		newReaction.MessageJID = msgID
		newReaction.MessageMXID = eventID
		newReaction.UserMXID = portal.bridge.GetPuppetByJID(jid).MXID

		newReaction.Insert()

	}

	for _, reaction := range removeLikes {
		if len(reaction.User.JID) == 0 {
			portal.log.Warnln("Reaction user state wrong", reaction.MXID, msgID)
			continue
		}
		intent := portal.getReactionIntent(reaction.User.JID)
		_, err := intent.RedactEvent(portal.MXID, reaction.MXID)
		if err != nil {
			portal.log.Errorln("Something wrong with reaction redaction", reaction.MXID)
			continue
		}
		reaction.Delete()

	}
}

func oldReactions(a []*database.Reaction, b []string) (ans []*database.Reaction) {
	for _, i := range a {
		flag := false
		for _, j := range b {
			if i.User.JID == j {
				flag = true
				break
			}
		}
		if !flag {
			ans = append(ans, i)
		}
	}

	return
}

func newReactions(a []*database.Reaction, b []string) (ans []string) {
	for _, j := range b {
		flag := false
		for _, i := range a {
			if i.User.JID == j {
				flag = true
				break
			}
		}
		if !flag {
			ans = append(ans, j)
		}
	}

	return
}

func (portal *Portal) HandleLocationMessage(source *User, message whatsapp.LocationMessage) {
	//	intent := portal.startHandling(source, message.Info)
	//	if intent == nil {
	//		return
	//	}
	//
	//	url := message.Url
	//	if len(url) == 0 {
	//		url = fmt.Sprintf("https://maps.google.com/?q=%.5f,%.5f", message.DegreesLatitude, message.DegreesLongitude)
	//	}
	//	name := message.Name
	//	if len(name) == 0 {
	//		latChar := 'N'
	//		if message.DegreesLatitude < 0 {
	//			latChar = 'S'
	//		}
	//		longChar := 'E'
	//		if message.DegreesLongitude < 0 {
	//			longChar = 'W'
	//		}
	//		name = fmt.Sprintf("%.4f° %c %.4f° %c", math.Abs(message.DegreesLatitude), latChar, math.Abs(message.DegreesLongitude), longChar)
	//	}
	//
	//	content := &event.MessageEventContent{
	//		MsgType:       event.MsgLocation,
	//		Body:          fmt.Sprintf("Location: %s\n%s\n%s", name, message.Address, url),
	//		Format:        event.FormatHTML,
	//		FormattedBody: fmt.Sprintf("Location: <a href='%s'>%s</a><br>%s", url, name, message.Address),
	//		GeoURI:        fmt.Sprintf("geo:%.5f,%.5f", message.DegreesLatitude, message.DegreesLongitude),
	//	}
	//
	//	if len(message.JpegThumbnail) > 0 {
	//		thumbnailMime := http.DetectContentType(message.JpegThumbnail)
	//		uploadedThumbnail, _ := intent.UploadBytes(message.JpegThumbnail, thumbnailMime)
	//		if uploadedThumbnail != nil {
	//			cfg, _, _ := image.DecodeConfig(bytes.NewReader(message.JpegThumbnail))
	//			content.Info = &event.FileInfo{
	//				ThumbnailInfo: &event.FileInfo{
	//					Size:     len(message.JpegThumbnail),
	//					Width:    cfg.Width,
	//					Height:   cfg.Height,
	//					MimeType: thumbnailMime,
	//				},
	//				ThumbnailURL: uploadedThumbnail.ContentURI.CUString(),
	//			}
	//		}
	//	}
	//
	//	portal.SetReply(content, message.ContextInfo)
	//
	//	_, _ = intent.UserTyping(portal.MXID, false, 0)
	//	resp, err := portal.sendMessage(intent, event.EventMessage, content, int64(message.Info.Timestamp*1000))
	//	if err != nil {
	//		portal.log.Errorfln("Failed to handle message %s: %v", message.Info.Id, err)
	//		return
	//	}
	//	portal.finishHandling(source, message.Info.Source, resp.EventID)
	//}

	//func (portal *Portal) HandleContactMessage(source *User, message whatsapp.ContactMessage) {
	//	intent := portal.startHandling(source, message.Info)
	//	if intent == nil {
	//		return
	//	}
	//
	//	fileName := fmt.Sprintf("%s.vcf", message.DisplayName)
	//	data := []byte(message.Vcard)
	//	mimeType := "text/vcard"
	//	data, uploadMimeType, file := portal.encryptFile(data, mimeType)
	//
	//	uploadResp, err := intent.UploadBytesWithName(data, uploadMimeType, fileName)
	//	if err != nil {
	//		portal.log.Errorfln("Failed to upload vcard of %s: %v", message.DisplayName, err)
	//		return
	//	}
	//
	//	content := &event.MessageEventContent{
	//		Body:    fileName,
	//		MsgType: event.MsgFile,
	//		File:    file,
	//		Info: &event.FileInfo{
	//			MimeType: mimeType,
	//			Size:     len(message.Vcard),
	//		},
	//	}
	//	if content.File != nil {
	//		content.File.URL = uploadResp.ContentURI.CUString()
	//	} else {
	//		content.URL = uploadResp.ContentURI.CUString()
	//	}
	//
	//	portal.SetReply(content, message.ContextInfo)
	//
	//	_, _ = intent.UserTyping(portal.MXID, false, 0)
	//	resp, err := portal.sendMessage(intent, event.EventMessage, content, int64(message.Info.Timestamp*1000))
	//	if err != nil {
	//		portal.log.Errorfln("Failed to handle message %s: %v", message.Info.Id, err)
	//		return
	//	}
	//	portal.finishHandling(source, message.Info.Source, resp.EventID)
}

func (portal *Portal) sendMediaBridgeFailure(source *User, intent *appservice.IntentAPI, message groupme.Message, bridgeErr error) {
	portal.log.Errorfln("Failed to bridge media for %s: %v", message.UserID.String(), bridgeErr)
	resp, err := portal.sendMessage(intent, event.EventMessage, &event.MessageEventContent{
		MsgType: event.MsgNotice,
		Body:    "Failed to bridge media",
	}, int64(message.CreatedAt.ToTime().Unix()*1000))
	if err != nil {
		portal.log.Errorfln("Failed to send media download error message for %s: %v", message.UserID.String(), err)
	} else {
		portal.finishHandling(source, &message, resp.EventID)
	}
}

func (portal *Portal) encryptFile(data []byte, mimeType string) ([]byte, string, *event.EncryptedFileInfo) {
	if !portal.Encrypted {
		return data, mimeType, nil
	}

	file := &event.EncryptedFileInfo{
		EncryptedFile: *attachment.NewEncryptedFile(),
		URL:           "",
	}
	return file.Encrypt(data), "application/octet-stream", file
}

func (portal *Portal) tryKickUser(userID id.UserID, intent *appservice.IntentAPI) error {
	_, err := intent.KickUser(portal.MXID, &mautrix.ReqKickUser{UserID: userID})
	if err != nil {
		httpErr, ok := err.(mautrix.HTTPError)
		if ok && httpErr.RespError != nil && httpErr.RespError.ErrCode == "M_FORBIDDEN" {
			_, err = portal.MainIntent().KickUser(portal.MXID, &mautrix.ReqKickUser{UserID: userID})
		}
	}
	return err
}

func (portal *Portal) removeUser(isSameUser bool, kicker *appservice.IntentAPI, target id.UserID, targetIntent *appservice.IntentAPI) {
	if !isSameUser || targetIntent == nil {
		err := portal.tryKickUser(target, kicker)
		if err != nil {
			portal.log.Warnfln("Failed to kick %s from %s: %v", target, portal.MXID, err)
			if targetIntent != nil {
				_, _ = targetIntent.LeaveRoom(portal.MXID)
			}
		}
	} else {
		_, err := targetIntent.LeaveRoom(portal.MXID)
		if err != nil {
			portal.log.Warnfln("Failed to leave portal as %s: %v", target, err)
			_, _ = portal.MainIntent().KickUser(portal.MXID, &mautrix.ReqKickUser{UserID: target})
		}
	}
}

func (portal *Portal) HandleWhatsAppKick(senderJID string, jids []string) {
	sender := portal.bridge.GetPuppetByJID(senderJID)
	senderIntent := sender.IntentFor(portal)
	for _, jid := range jids {
		puppet := portal.bridge.GetPuppetByJID(jid)
		portal.removeUser(puppet.JID == sender.JID, senderIntent, puppet.MXID, puppet.DefaultIntent())

		user := portal.bridge.GetUserByJID(jid)
		if user != nil {
			var customIntent *appservice.IntentAPI
			if puppet.CustomMXID == user.MXID {
				customIntent = puppet.CustomIntent()
			}
			portal.removeUser(puppet.JID == sender.JID, senderIntent, user.MXID, customIntent)
		}
	}
}

func (portal *Portal) HandleWhatsAppInvite(senderJID string, jids []string) {
	senderIntent := portal.MainIntent()
	if senderJID != "unknown" {
		sender := portal.bridge.GetPuppetByJID(senderJID)
		senderIntent = sender.IntentFor(portal)
	}
	for _, jid := range jids {
		puppet := portal.bridge.GetPuppetByJID(jid)
		_, err := senderIntent.InviteUser(portal.MXID, &mautrix.ReqInviteUser{UserID: puppet.MXID})
		if err != nil {
			portal.log.Warnfln("Failed to invite %s as %s: %v", puppet.MXID, senderIntent.UserID, err)
		}
		err = puppet.DefaultIntent().EnsureJoined(portal.MXID)
		if err != nil {
			portal.log.Errorfln("Failed to ensure %s is joined: %v", puppet.MXID, err)
		}
	}
}

type base struct {
	download func() ([]byte, error)
	info     whatsapp.MessageInfo
	context  whatsapp.ContextInfo
	mimeType string
}

type mediaMessage struct {
	base

	thumbnail     []byte
	caption       string
	fileName      string
	length        uint32
	sendAsSticker bool
}

func (portal *Portal) uploadWithRetry(intent *appservice.IntentAPI, data []byte, mimeType string, retries int) (*mautrix.RespMediaUpload, error) {
	for ; ; retries-- {
		uploaded, err := intent.UploadBytes(data, mimeType)
		if isGatewayError(err) {
			portal.log.Warnfln("Got gateway error trying to upload media, retrying in %d seconds", int(BadGatewaySleep.Seconds()))
			time.Sleep(BadGatewaySleep)
		} else {
			return uploaded, err
		}
	}
}

func makeMessageID() *string {
	b := make([]byte, 10)
	rand.Read(b)
	str := strings.ToUpper(hex.EncodeToString(b))
	return &str
}

func (portal *Portal) downloadThumbnail(content *event.MessageEventContent, id id.EventID) []byte {
	if len(content.GetInfo().ThumbnailURL) == 0 {
		return nil
	}
	mxc, err := content.GetInfo().ThumbnailURL.Parse()
	if err != nil {
		portal.log.Errorln("Malformed thumbnail URL in %s: %v", id, err)
	}
	thumbnail, err := portal.MainIntent().DownloadBytes(mxc)
	if err != nil {
		portal.log.Errorln("Failed to download thumbnail in %s: %v", id, err)
		return nil
	}
	thumbnailType := http.DetectContentType(thumbnail)
	var img image.Image
	switch thumbnailType {
	case "image/png":
		img, err = png.Decode(bytes.NewReader(thumbnail))
	case "image/gif":
		img, err = gif.Decode(bytes.NewReader(thumbnail))
	case "image/jpeg":
		return thumbnail
	default:
		return nil
	}
	var buf bytes.Buffer
	err = jpeg.Encode(&buf, img, &jpeg.Options{
		Quality: jpeg.DefaultQuality,
	})
	if err != nil {
		portal.log.Errorln("Failed to re-encode thumbnail in %s: %v", id, err)
		return nil
	}
	return buf.Bytes()
}

func (portal *Portal) convertGifToVideo(gif []byte) ([]byte, error) {
	dir, err := ioutil.TempDir("", "gif-convert-*")
	if err != nil {
		return nil, fmt.Errorf("failed to make temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	inputFile, err := os.OpenFile(filepath.Join(dir, "input.gif"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("failed open input file: %w", err)
	}
	_, err = inputFile.Write(gif)
	if err != nil {
		_ = inputFile.Close()
		return nil, fmt.Errorf("failed to write gif to input file: %w", err)
	}
	_ = inputFile.Close()

	outputFileName := filepath.Join(dir, "output.mp4")
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "warning",
		"-f", "gif", "-i", inputFile.Name(),
		"-pix_fmt", "yuv420p", "-c:v", "libx264", "-movflags", "+faststart",
		"-filter:v", "crop='floor(in_w/2)*2:floor(in_h/2)*2'",
		outputFileName)
	vcLog := portal.log.Sub("VideoConverter").WithDefaultLevel(log.LevelWarn)
	cmd.Stdout = vcLog
	cmd.Stderr = vcLog

	err = cmd.Run()
	if err != nil {
		return nil, fmt.Errorf("failed to run ffmpeg: %w", err)
	}
	outputFile, err := os.OpenFile(filepath.Join(dir, "output.mp4"), os.O_RDONLY, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to open output file: %w", err)
	}
	defer func() {
		_ = outputFile.Close()
		_ = os.Remove(outputFile.Name())
	}()
	mp4, err := ioutil.ReadAll(outputFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read mp4 from output file: %w", err)
	}
	return mp4, nil
}

func (portal *Portal) preprocessMatrixMedia(sender *User, relaybotFormatted bool, content *event.MessageEventContent, eventID id.EventID, mediaType whatsapp.MediaType) *MediaUpload {
	// var caption string
	// var mentionedJIDs []types.GroupMeID
	// if relaybotFormatted {
	// 	caption, mentionedJIDs = portal.bridge.Formatter.ParseMatrix(content.FormattedBody)
	// }

	// var file *event.EncryptedFileInfo
	// rawMXC := content.URL
	// if content.File != nil {
	// 	file = content.File
	// 	rawMXC = file.URL
	// }
	// mxc, err := rawMXC.Parse()
	// if err != nil {
	// 	portal.log.Errorln("Malformed content URL in %s: %v", eventID, err)
	// 	return nil
	// }
	// data, err := portal.MainIntent().DownloadBytes(mxc)
	// if err != nil {
	// 	portal.log.Errorfln("Failed to download media in %s: %v", eventID, err)
	// 	return nil
	// }
	// if file != nil {
	// 	data, err = file.Decrypt(data)
	// 	if err != nil {
	// 		portal.log.Errorfln("Failed to decrypt media in %s: %v", eventID, err)
	// 		return nil
	// 	}
	// }
	// if mediaType == whatsapp.MediaVideo && content.GetInfo().MimeType == "image/gif" {
	// 	data, err = portal.convertGifToVideo(data)
	// 	if err != nil {
	// 		portal.log.Errorfln("Failed to convert gif to mp4 in %s: %v", eventID, err)
	// 		return nil
	// 	}
	// 	content.Info.MimeType = "video/mp4"
	// }

	// url, mediaKey, fileEncSHA256, fileSHA256, fileLength, err := sender.Conn.Upload(bytes.NewReader(data), mediaType)
	// if err != nil {
	// 	portal.log.Errorfln("Failed to upload media in %s: %v", eventID, err)
	// 	return nil
	// }

	// return &MediaUpload{
	// 	Caption:       caption,
	// 	MentionedJIDs: mentionedJIDs,
	// 	URL:           url,
	// 	MediaKey:      mediaKey,
	// 	FileEncSHA256: fileEncSHA256,
	// 	FileSHA256:    fileSHA256,
	// 	FileLength:    fileLength,
	// 	Thumbnail:     portal.downloadThumbnail(content, eventID),
	// }
	return nil
}

type MediaUpload struct {
	Caption       string
	MentionedJIDs []types.GroupMeID
	URL           string
	MediaKey      []byte
	FileEncSHA256 []byte
	FileSHA256    []byte
	FileLength    uint64
	Thumbnail     []byte
}

func (portal *Portal) sendMatrixConnectionError(sender *User, eventID id.EventID) bool {
	if !sender.HasSession() {
		portal.log.Debugln("Ignoring event", eventID, "from", sender.MXID, "as user has no session")
		return true
	} else if !sender.IsConnected() {
		portal.log.Debugln("Ignoring event", eventID, "from", sender.MXID, "as user is not connected")
		inRoom := ""
		if portal.IsPrivateChat() {
			inRoom = " in your management room"
		}
		reconnect := fmt.Sprintf("Use `%s reconnect`%s to reconnect.", portal.bridge.Config.Bridge.CommandPrefix, inRoom)
		if sender.IsLoginInProgress() {
			reconnect = "You have a login attempt in progress, please wait."
		}
		msg := format.RenderMarkdown("\u26a0 You are not connected to WhatsApp, so your message was not bridged. "+reconnect, true, false)
		msg.MsgType = event.MsgNotice
		_, err := portal.sendMainIntentMessage(msg)
		if err != nil {
			portal.log.Errorln("Failed to send bridging failure message:", err)
		}
		return true
	}
	return false
}

func (portal *Portal) addRelaybotFormat(sender *User, content *event.MessageEventContent) bool {
	member := portal.MainIntent().Member(portal.MXID, sender.MXID)
	if len(member.Displayname) == 0 {
		member.Displayname = string(sender.MXID)
	}

	if content.Format != event.FormatHTML {
		content.FormattedBody = strings.Replace(html.EscapeString(content.Body), "\n", "<br/>", -1)
		content.Format = event.FormatHTML
	}
	data, err := portal.bridge.Config.Bridge.Relaybot.FormatMessage(content, sender.MXID, member)
	if err != nil {
		portal.log.Errorln("Failed to apply relaybot format:", err)
	}
	content.FormattedBody = data
	return true
}

func (portal *Portal) convertMatrixMessage(sender *User, evt *event.Event) ([]*groupme.Message, *User) {
	content, ok := evt.Content.Parsed.(*event.MessageEventContent)
	if !ok {
		portal.log.Debugfln("Failed to handle event %s: unexpected parsed content type %T", evt.ID, evt.Content.Parsed)
		return nil, sender
	}

	//ts := uint64(evt.Timestamp / 1000)
	//status := waProto.WebMessageInfo_ERROR
	//fromMe := true
	//	info := &waProto.WebMessageInfo{
	//		Key: &waProto.MessageKey{
	//			FromMe:    &fromMe,
	//			Id:        makeMessageID(),
	//			RemoteJid: &portal.Key.JID,
	//		},
	//		MessageTimestamp: &ts,
	//		Message:          &waProto.Message{},
	//		Status:           &status,
	//	}
	//
	info := groupme.Message{
		SourceGUID: evt.ID.String(), //TODO Figure out for multiple messages
		GroupID:    groupme.ID(portal.Key.JID),
	}
	replyToID := content.GetReplyTo()
	if len(replyToID) > 0 {
		//		content.RemoveReplyFallback()
		//		msg := portal.bridge.DB.Message.GetByMXID(replyToID)
		//		if msg != nil && msg.Content != nil {
		//			ctxInfo.StanzaId = &msg.JID
		//			ctxInfo.Participant = &msg.Sender
		//			ctxInfo.QuotedMessage = msg.Content
		//		}
	}
	relaybotFormatted := false
	if sender.NeedsRelaybot(portal) {
		if !portal.HasRelaybot() {
			if sender.HasSession() {
				portal.log.Debugln("Database says", sender.MXID, "not in chat and no relaybot, but trying to send anyway")
			} else {
				portal.log.Debugln("Ignoring message from", sender.MXID, "in chat with no relaybot")
				return nil, sender
			}
		} else {
			relaybotFormatted = portal.addRelaybotFormat(sender, content)
			sender = portal.bridge.Relaybot
		}
	}
	if evt.Type == event.EventSticker {
		content.MsgType = event.MsgImage
	} else if content.MsgType == event.MsgImage && content.GetInfo().MimeType == "image/gif" {
		content.MsgType = event.MsgVideo
	}

	switch content.MsgType {
	case event.MsgText, event.MsgEmote, event.MsgNotice:
		text := content.Body
		if content.Format == event.FormatHTML {
			text, _ = portal.bridge.Formatter.ParseMatrix(content.FormattedBody)
			//TODO mentions
		}
		if content.MsgType == event.MsgEmote && !relaybotFormatted {
			text = "/me " + text
		}
		info.Text = text

	//	if ctxInfo.StanzaId != nil || ctxInfo.MentionedJid != nil {
	//		info.Message.ExtendedTextMessage = &waProto.ExtendedTextMessage{
	//			Text:        &text,
	//			ContextInfo: ctxInfo,
	//		}
	// }
	//else {
	//			info.Message.Conversation = &text
	//		}
	//	 case event.MsgImage:
	//	 	media := portal.preprocessMatrixMedia(sender, relaybotFormatted, content, evt.ID, whatsapp.MediaImage)
	//	 	if media == nil {
	//	 		return nil, sender
	//	 	}
	//	 	ctxInfo.MentionedJid = media.MentionedJIDs
	//	 	info.Message.ImageMessage = &waProto.ImageMessage{
	//	 		ContextInfo:   ctxInfo,
	//	 		Caption:       &media.Caption,
	//	 		JpegThumbnail: media.Thumbnail,
	//	 		Url:           &media.URL,
	//	 		MediaKey:      media.MediaKey,
	//	 		Mimetype:      &content.GetInfo().MimeType,
	//	 		FileEncSha256: media.FileEncSHA256,
	//	 		FileSha256:    media.FileSHA256,
	//	 		FileLength:    &media.FileLength,
	//	 	}
	//	 case event.MsgVideo:
	//	 	gifPlayback := content.GetInfo().MimeType == "image/gif"
	//	 	media := portal.preprocessMatrixMedia(sender, relaybotFormatted, content, evt.ID, whatsapp.MediaVideo)
	//	 	if media == nil {
	//	 		return nil, sender
	//	 	}
	//	 	duration := uint32(content.GetInfo().Duration)
	//	 	ctxInfo.MentionedJid = media.MentionedJIDs
	//	 	info.Message.VideoMessage = &waProto.VideoMessage{
	//	 		ContextInfo:   ctxInfo,
	//	 		Caption:       &media.Caption,
	//	 		JpegThumbnail: media.Thumbnail,
	//	 		Url:           &media.URL,
	//	 		MediaKey:      media.MediaKey,
	//	 		Mimetype:      &content.GetInfo().MimeType,
	//	 		GifPlayback:   &gifPlayback,
	//	 		Seconds:       &duration,
	//	 		FileEncSha256: media.FileEncSHA256,
	//	 		FileSha256:    media.FileSHA256,
	//	 		FileLength:    &media.FileLength,
	//	 	}
	//	 case event.MsgAudio:
	//	 	media := portal.preprocessMatrixMedia(sender, relaybotFormatted, content, evt.ID, whatsapp.MediaAudio)
	//	 	if media == nil {
	//	 		return nil, sender
	//	 	}
	//	 	duration := uint32(content.GetInfo().Duration)
	//	 	info.Message.AudioMessage = &waProto.AudioMessage{
	//	 		ContextInfo:   ctxInfo,
	//	 		Url:           &media.URL,
	//	 		MediaKey:      media.MediaKey,
	//	 		Mimetype:      &content.GetInfo().MimeType,
	//	 		Seconds:       &duration,
	//	 		FileEncSha256: media.FileEncSHA256,
	//	 		FileSha256:    media.FileSHA256,
	//	 		FileLength:    &media.FileLength,
	//	 	}
	//	 case event.MsgFile:
	//	 	media := portal.preprocessMatrixMedia(sender, relaybotFormatted, content, evt.ID, whatsapp.MediaDocument)
	//	 	if media == nil {
	//	 		return nil, sender
	//	 	}
	//	 	info.Message.DocumentMessage = &waProto.DocumentMessage{
	//	 		ContextInfo:   ctxInfo,
	//	 		Url:           &media.URL,
	//	 		Title:         &content.Body,
	//	 		FileName:      &content.Body,
	//	 		MediaKey:      media.MediaKey,
	//	 		Mimetype:      &content.GetInfo().MimeType,
	//	 		FileEncSha256: media.FileEncSHA256,
	//	 		FileSha256:    media.FileSHA256,
	//	 		FileLength:    &media.FileLength,
	//	 	}
	default:
		portal.log.Debugln("Unhandled Matrix event %s: unknown msgtype %s", evt.ID, content.MsgType)
		return nil, sender
	}
	return []*groupme.Message{&info}, sender
}

func (portal *Portal) wasMessageSent(sender *User, id string) bool {
	// _, err := sender.Conn.LoadMessagesAfter(portal.Key.JID, id, true, 0)
	// if err != nil {
	// 	if err != whatsapp.ErrServerRespondedWith404 {
	// 		portal.log.Warnfln("Failed to check if message was bridged without response: %v", err)
	// 	}
	// 	return false
	// }
	return true
}

func (portal *Portal) sendErrorMessage(message string) id.EventID {
	resp, err := portal.sendMainIntentMessage(event.MessageEventContent{
		MsgType: event.MsgNotice,
		Body:    fmt.Sprintf("\u26a0 Your message may not have been bridged: %v", message),
	})
	if err != nil {
		portal.log.Warnfln("Failed to send bridging error message:", err)
		return ""
	}
	return resp.EventID
}

func (portal *Portal) sendDeliveryReceipt(eventID id.EventID) {
	if portal.bridge.Config.Bridge.DeliveryReceipts {
		err := portal.bridge.Bot.MarkRead(portal.MXID, eventID)
		if err != nil {
			portal.log.Debugfln("Failed to send delivery receipt for %s: %v", eventID, err)
		}
	}
}

var timeout = errors.New("message sending timed out")

func (portal *Portal) HandleMatrixMessage(sender *User, evt *event.Event) {
	if !portal.HasRelaybot() && ((portal.IsPrivateChat() && sender.JID != portal.Key.Receiver) ||
		portal.sendMatrixConnectionError(sender, evt.ID)) {
		return
	}
	portal.log.Debugfln("Received event %s", evt.ID)
	info, sender := portal.convertMatrixMessage(sender, evt)
	if info == nil {
		return
	}
	for _, i := range info {
		portal.log.Debugln("Sending event", evt.ID, "to WhatsApp", info[0].ID)

		var err error
		i, err = portal.sendRaw(sender, evt, info[0], false) //TODO deal with multiple messages for longer messages
		if err != nil {
			portal.log.Warnln("Unable to handle message from Matrix", evt.ID)
			//TODO handle deleted room and such
		} else {

			portal.markHandled(sender, i, evt.ID)
		}
	}

}

func (portal *Portal) sendRaw(sender *User, evt *event.Event, info *groupme.Message, isRetry bool) (*groupme.Message, error) {

	m, err := sender.Client.CreateMessage(context.TODO(), info.GroupID, info)
	id := ""
	if m != nil {
		id = m.ID.String()
	}
	if err != nil {
		portal.log.Warnln(err, id, info.GroupID.String())
	}
	if isRetry && err != nil {
		m, err = sender.Client.CreateMessage(context.TODO(), info.GroupID, info)
	}
	if err != nil {
		return nil, err
	}
	return m, nil
	// errChan := make(chan error, 1)
	// go sender.Conn.SendRaw(info, errChan)

	// var err error
	// var errorEventID id.EventID
	// select {
	// case err = <-errChan:
	// 	var statusResp whatsapp.StatusResponse
	// 	if !isRetry && errors.As(err, &statusResp) && statusResp.Status == 599 {
	// 		portal.log.Debugfln("599 status response sending %s to WhatsApp (%+v), retrying...", evt.ID, statusResp)
	// 		errorEventID = portal.sendErrorMessage(fmt.Sprintf("%v. The bridge will retry in 5 seconds.", err))
	// 		time.Sleep(5 * time.Second)
	// 		portal.sendRaw(sender, evt, info, true)
	// 	}
	// case <-time.After(time.Duration(portal.bridge.Config.Bridge.ConnectionTimeout) * time.Second):
	// 	if portal.bridge.Config.Bridge.FetchMessageOnTimeout && portal.wasMessageSent(sender, info.Key.GetId()) {
	// 		portal.log.Debugln("Matrix event %s was bridged, but response didn't arrive within timeout")
	// 		portal.sendDeliveryReceipt(evt.ID)
	// 	} else {
	// 		portal.log.Warnfln("Response when bridging Matrix event %s is taking long to arrive", evt.ID)
	// 		errorEventID = portal.sendErrorMessage(timeout.Error())
	// 	}
	// 	err = <-errChan
	// }
	// if err != nil {
	// 	portal.log.Errorfln("Error handling Matrix event %s: %v", evt.ID, err)
	// 	var statusResp whatsapp.StatusResponse
	// 	if errors.As(err, &statusResp) && statusResp.Status == 599 {
	// 		portal.log.Debugfln("599 status response data: %+v", statusResp)
	// 	}
	// 	portal.sendErrorMessage(err.Error())
	// } else {
	// 	portal.log.Debugfln("Handled Matrix event %s", evt.ID)
	// 	portal.sendDeliveryReceipt(evt.ID)
	// }
	// if errorEventID != "" {
	// 	_, err = portal.MainIntent().RedactEvent(portal.MXID, errorEventID)
	// 	if err != nil {
	// 		portal.log.Warnfln("Failed to redact timeout warning message %s: %v", errorEventID, err)
	// 	}
	// }
}

func (portal *Portal) HandleMatrixRedaction(sender *User, evt *event.Event) {
	// if portal.IsPrivateChat() && sender.JID != portal.Key.Receiver {
	// 	return
	// }

	// msg := portal.bridge.DB.Message.GetByMXID(evt.Redacts)
	// if msg == nil || msg.Sender != sender.JID {
	// 	return
	// }

	// ts := uint64(evt.Timestamp / 1000)
	// status := waProto.WebMessageInfo_PENDING
	// protoMsgType := waProto.ProtocolMessage_REVOKE
	// fromMe := true
	// info := &waProto.WebMessageInfo{
	// 	Key: &waProto.MessageKey{
	// 		FromMe:    &fromMe,
	// 		Id:        makeMessageID(),
	// 		RemoteJid: &portal.Key.JID,
	// 	},
	// 	MessageTimestamp: &ts,
	// 	Message: &waProto.Message{
	// 		ProtocolMessage: &waProto.ProtocolMessage{
	// 			Type: &protoMsgType,
	// 			Key: &waProto.MessageKey{
	// 				FromMe:    &fromMe,
	// 				Id:        &msg.JID,
	// 				RemoteJid: &portal.Key.JID,
	// 			},
	// 		},
	// 	},
	// 	Status: &status,
	// }
	// errChan := make(chan error, 1)
	// go sender.Conn.SendRaw(info, errChan)

	// var err error
	// select {
	// case err = <-errChan:
	// case <-time.After(time.Duration(portal.bridge.Config.Bridge.ConnectionTimeout) * time.Second):
	// 	portal.log.Warnfln("Response when bridging Matrix redaction %s is taking long to arrive", evt.ID)
	// 	err = <-errChan
	// }
	// if err != nil {
	// 	portal.log.Errorfln("Error handling Matrix redaction %s: %v", evt.ID, err)
	// } else {
	// 	portal.log.Debugln("Handled Matrix redaction %s of %s", evt.ID, evt.Redacts)
	// 	portal.sendDeliveryReceipt(evt.ID)
	// }
}

func (portal *Portal) Delete() {
	portal.Portal.Delete()
	portal.bridge.portalsLock.Lock()
	delete(portal.bridge.portalsByJID, portal.Key)
	if len(portal.MXID) > 0 {
		delete(portal.bridge.portalsByMXID, portal.MXID)
	}
	portal.bridge.portalsLock.Unlock()
}

func (portal *Portal) GetMatrixUsers() ([]id.UserID, error) {
	members, err := portal.MainIntent().JoinedMembers(portal.MXID)
	if err != nil {
		return nil, fmt.Errorf("failed to get member list: %w", err)
	}
	var users []id.UserID
	for userID := range members.Joined {
		_, isPuppet := portal.bridge.ParsePuppetMXID(userID)
		if !isPuppet && userID != portal.bridge.Bot.UserID {
			users = append(users, userID)
		}
	}
	return users, nil
}

func (portal *Portal) CleanupIfEmpty() {
	users, err := portal.GetMatrixUsers()
	if err != nil {
		portal.log.Errorfln("Failed to get Matrix user list to determine if portal needs to be cleaned up: %v", err)
		return
	}

	if len(users) == 0 {
		portal.log.Infoln("Room seems to be empty, cleaning up...")
		portal.Delete()
		portal.Cleanup(false)
	}
}

func (portal *Portal) Cleanup(puppetsOnly bool) {
	if len(portal.MXID) == 0 {
		return
	}
	if portal.IsPrivateChat() {
		_, err := portal.MainIntent().LeaveRoom(portal.MXID)
		if err != nil {
			portal.log.Warnln("Failed to leave private chat portal with main intent:", err)
		}
		return
	}
	intent := portal.MainIntent()
	members, err := intent.JoinedMembers(portal.MXID)
	if err != nil {
		portal.log.Errorln("Failed to get portal members for cleanup:", err)
		return
	}
	for member := range members.Joined {
		if member == intent.UserID {
			continue
		}
		puppet := portal.bridge.GetPuppetByMXID(member)
		if puppet != nil {
			_, err = puppet.DefaultIntent().LeaveRoom(portal.MXID)
			if err != nil {
				portal.log.Errorln("Error leaving as puppet while cleaning up portal:", err)
			}
		} else if !puppetsOnly {
			_, err = intent.KickUser(portal.MXID, &mautrix.ReqKickUser{UserID: member, Reason: "Deleting portal"})
			if err != nil {
				portal.log.Errorln("Error kicking user while cleaning up portal:", err)
			}
		}
	}
	_, err = intent.LeaveRoom(portal.MXID)
	if err != nil {
		portal.log.Errorln("Error leaving with main intent while cleaning up portal:", err)
	}
}

func (portal *Portal) HandleMatrixLeave(sender *User) {
	if portal.IsPrivateChat() {
		portal.log.Debugln("User left private chat portal, cleaning up and deleting...")
		portal.Delete()
		portal.Cleanup(false)
		return
	} else {
		// TODO should we somehow deduplicate this call if this leave was sent by the bridge?
		err := sender.Client.RemoveFromGroup(sender.JID, portal.Key.JID)
		if err != nil {
			portal.log.Errorfln("Failed to leave group as %s: %v", sender.MXID, err)
			return
		}
		portal.CleanupIfEmpty()
	}
}

func (portal *Portal) HandleMatrixKick(sender *User, evt *event.Event) {
	// puppet := portal.bridge.GetPuppetByMXID(id.UserID(evt.GetStateKey()))
	// if puppet != nil {
	// 	resp, err := sender.Conn.RemoveMember(portal.Key.JID, []string{puppet.JID})
	// 	if err != nil {
	// 		portal.log.Errorfln("Failed to kick %s from group as %s: %v", puppet.JID, sender.MXID, err)
	// 		return
	// 	}
	// 	portal.log.Infoln("Kick %s response: %s", puppet.JID, <-resp)
	// }
}

func (portal *Portal) HandleMatrixInvite(sender *User, evt *event.Event) {
	// puppet := portal.bridge.GetPuppetByMXID(id.UserID(evt.GetStateKey()))
	// if puppet != nil {
	// 	resp, err := sender.Conn.AddMember(portal.Key.JID, []string{puppet.JID})
	// 	if err != nil {
	// 		portal.log.Errorfln("Failed to add %s to group as %s: %v", puppet.JID, sender.MXID, err)
	// 		return
	// 	}
	// 	portal.log.Infoln("Add %s response: %s", puppet.JID, <-resp)
	// }
}
