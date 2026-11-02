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
	"html"
	"slices"
	"strings"
	"unicode/utf16"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"

	"github.com/beeper/groupme-lib"
)

// GroupMe mention loci are UTF-16 offsets into the plain text.

const (
	mentionStart       = '\uE000'
	mentionEnd         = '\uE001'
	mentionsContextKey = "fi.mau.groupme.mentions"
)

func (gc *GMClient) htmlParser() *format.HTMLParser {
	parser := *groupmeHTMLParser
	parser.PillConverter = gc.convertPill
	return &parser
}

func (gc *GMClient) convertPill(displayname, mxid, eventID string, ctx format.Context) string {
	if eventID == "" && strings.HasPrefix(mxid, "@") {
		if userID := gc.mentionedUser(id.UserID(mxid)); userID != "" {
			ids, _ := ctx.ReturnData[mentionsContextKey].([]groupme.ID)
			ctx.ReturnData[mentionsContextKey] = append(ids, userID)
			return string(mentionStart) + "@" + strings.TrimPrefix(displayname, "@") + string(mentionEnd)
		}
	}
	return format.DefaultPillConverter(displayname, mxid, eventID, ctx)
}

func (gc *GMClient) mentionedUser(mxid id.UserID) groupme.ID {
	if mxid == gc.UserLogin.UserMXID {
		return groupme.ID(gc.Meta.GMID)
	}
	if userID, ok := gc.Main.br.Matrix.ParseGhostMXID(mxid); ok {
		return ParseUserID(userID)
	}
	return ""
}

func takeMentions(text string, ctx format.Context) (string, *groupme.Attachment) {
	ids, _ := ctx.ReturnData[mentionsContextKey].([]groupme.ID)
	if len(ids) == 0 {
		return text, nil
	}
	var out strings.Builder
	mentions := &groupme.Attachment{Type: groupme.Mentions}
	offset, start := 0, -1
	for _, r := range text {
		switch {
		case r == mentionStart:
			start = offset
		case r == mentionEnd:
			if start >= 0 && len(mentions.UserIDs) < len(ids) {
				mentions.UserIDs = append(mentions.UserIDs, ids[len(mentions.UserIDs)])
				mentions.Loci = append(mentions.Loci, []int{start, offset - start})
			}
			start = -1
		default:
			out.WriteRune(r)
			offset += utf16.RuneLen(r)
		}
	}
	if len(mentions.UserIDs) == 0 {
		return out.String(), nil
	}
	return out.String(), mentions
}

func (gc *GMClient) applyTextAttachments(content *event.MessageEventContent, attachments []*groupme.Attachment) {
	body := content.Body
	content.Body = groupMeEmojiReplacer(attachments)(body)
	var mentions *groupme.Attachment
	for _, att := range attachments {
		if att != nil && att.Type == groupme.Mentions {
			mentions = att
			break
		}
	}
	if mentions == nil || len(mentions.UserIDs) == 0 {
		return
	}
	type span struct {
		start, end int
		user       groupme.ID
	}
	text := utf16.Encode([]rune(body))
	spans := make([]span, 0, len(mentions.UserIDs))
	for i, user := range mentions.UserIDs {
		if i >= len(mentions.Loci) || len(mentions.Loci[i]) != 2 || user == "" {
			continue
		}
		start, length := mentions.Loci[i][0], mentions.Loci[i][1]
		if start >= 0 && length > 0 && start+length <= len(text) {
			spans = append(spans, span{start, start + length, user})
		}
	}
	slices.SortFunc(spans, func(a, b span) int { return a.start - b.start })
	replaceEmoji := groupMeEmojiReplacer(attachments)
	escape := func(units []uint16) string {
		return strings.ReplaceAll(html.EscapeString(replaceEmoji(string(utf16.Decode(units)))), "\n", "<br>")
	}
	var out strings.Builder
	content.Mentions = &event.Mentions{}
	prev := 0
	for _, s := range spans {
		if s.start < prev {
			continue
		}
		mxid := gc.UserLogin.UserMXID
		if string(s.user) != gc.Meta.GMID {
			mxid = gc.Main.br.Matrix.FormatGhostMXID(MakeUserID(s.user))
		}
		out.WriteString(escape(text[prev:s.start]))
		out.WriteString(`<a href="` + html.EscapeString(mxid.URI().MatrixToURL()) + `">`)
		out.WriteString(escape(text[s.start:s.end]))
		out.WriteString("</a>")
		content.Mentions.Add(mxid)
		prev = s.end
	}
	if prev == 0 {
		content.Mentions = nil
		return
	}
	out.WriteString(escape(text[prev:]))
	content.Format = event.FormatHTML
	content.FormattedBody = out.String()
}
