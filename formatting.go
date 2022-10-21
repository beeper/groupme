// mautrix-groupme - A Matrix-GroupMe puppeting bridge.
// Copyright (C) 2022 Sumner Evans, Karmanyaah Malhotra
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
	"fmt"
	"regexp"
	"strings"

	"github.com/karmanyaahm/groupme"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"
)

var italicRegex = regexp.MustCompile("([\\s>~*]|^)_(.+?)_([^a-zA-Z\\d]|$)")
var boldRegex = regexp.MustCompile("([\\s>_~]|^)\\*(.+?)\\*([^a-zA-Z\\d]|$)")
var strikethroughRegex = regexp.MustCompile("([\\s>_*]|^)~(.+?)~([^a-zA-Z\\d]|$)")
var codeBlockRegex = regexp.MustCompile("```(?:.|\n)+?```")

const mentionedGMIDsContextKey = "net.maunium.groupme.mentioned_gmids"

type Formatter struct {
	bridge *GMBridge

	matrixHTMLParser *format.HTMLParser

	waReplString   map[*regexp.Regexp]string
	waReplFunc     map[*regexp.Regexp]func(string) string
	waReplFuncText map[*regexp.Regexp]func(string) string
}

func NewFormatter(bridge *GMBridge) *Formatter {
	formatter := &Formatter{
		bridge: bridge,
		matrixHTMLParser: &format.HTMLParser{
			TabsToSpaces: 4,
			Newline:      "\n",

			PillConverter: func(displayname, mxid, eventID string, ctx format.Context) string {
				if mxid[0] == '@' {
					puppet := bridge.GetPuppetByMXID(id.UserID(mxid))
					if puppet != nil {
						gmids, ok := ctx[mentionedGMIDsContextKey].([]groupme.ID)
						if !ok {
							ctx[mentionedGMIDsContextKey] = []groupme.ID{puppet.GMID}
						} else {
							ctx[mentionedGMIDsContextKey] = append(gmids, puppet.GMID)
						}
						return "@" + puppet.PhoneNumber()
					}
				}
				return mxid
			},
			BoldConverter: func(text string, _ format.Context) string {
				return fmt.Sprintf("*%s*", text)
			},
			ItalicConverter: func(text string, _ format.Context) string {
				return fmt.Sprintf("_%s_", text)
			},
			StrikethroughConverter: func(text string, _ format.Context) string {
				return fmt.Sprintf("~%s~", text)
			},
			MonospaceConverter: func(text string, _ format.Context) string {
				return fmt.Sprintf("```%s```", text)
			},
			MonospaceBlockConverter: func(text, language string, _ format.Context) string {
				return fmt.Sprintf("```%s```", text)
			},
		},
		waReplString: map[*regexp.Regexp]string{
			italicRegex:        "$1<em>$2</em>$3",
			boldRegex:          "$1<strong>$2</strong>$3",
			strikethroughRegex: "$1<del>$2</del>$3",
		},
	}
	formatter.waReplFunc = map[*regexp.Regexp]func(string) string{
		codeBlockRegex: func(str string) string {
			str = str[3 : len(str)-3]
			if strings.ContainsRune(str, '\n') {
				return fmt.Sprintf("<pre><code>%s</code></pre>", str)
			}
			return fmt.Sprintf("<code>%s</code>", str)
		},
	}
	formatter.waReplFuncText = map[*regexp.Regexp]func(string) string{}
	return formatter
}

//func (formatter *Formatter) getMatrixInfoByJID(jid groupme.ID) (mxid id.UserID, displayname string) {
//	if user := formatter.bridge.GetUserByJID(jid); user != nil {
//		mxid = user.MXID
//		displayname = string(user.MXID)
//	} else if puppet := formatter.bridge.GetPuppetByJID(jid); puppet != nil {
//		mxid = puppet.MXID
//		displayname = puppet.Displayname
//	}
//	return
//}

//func (formatter *Formatter) ParseWhatsApp(content *event.MessageEventContent, mentionedJIDs []groupme.ID) {
//	output := html.EscapeString(content.Body)
//	for regex, replacement := range formatter.waReplString {
//		output = regex.ReplaceAllString(output, replacement)
//	}
//	for regex, replacer := range formatter.waReplFunc {
//		output = regex.ReplaceAllStringFunc(output, replacer)
//	}
//	for _, jid := range mentionedJIDs {
//		mxid, displayname := formatter.getMatrixInfoByJID(jid)
//		number := "@" + strings.Replace(jid, whatsappExt.NewUserSuffix, "", 1)
//		output = strings.Replace(output, number, fmt.Sprintf(`<a href="https://matrix.to/#/%s">%s</a>`, mxid, displayname), -1)
//		content.Body = strings.Replace(content.Body, number, displayname, -1)
//	}
//	if output != content.Body {
//		output = strings.Replace(output, "\n", "<br/>", -1)
//		content.FormattedBody = output
//		content.Format = event.FormatHTML
//		for regex, replacer := range formatter.waReplFuncText {
//			content.Body = regex.ReplaceAllStringFunc(content.Body, replacer)
//		}
//	}
//}

func (formatter *Formatter) ParseMatrix(html string) (string, []groupme.ID) {
	ctx := make(format.Context)
	result := formatter.matrixHTMLParser.Parse(html, ctx)
	mentionedJIDs, _ := ctx[mentionedGMIDsContextKey].([]groupme.ID)
	return result, mentionedJIDs
}
