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

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/util/variationselector"
)

const formatterContextAllowedMentionsKey = "com.beeper.groupme.allowed_mentions"

func (br *GMBridge) pillConverter(displayname, mxid, eventID string, ctx format.Context) string {
	// GroupMe only supports user mentions.
	if len(mxid) == 0 || mxid[0] != '@' {
		return displayname
	}

	return fmt.Sprintf("@%s", displayname)
}

var matrixHTMLParser = &format.HTMLParser{
	TabsToSpaces:   4,
	Newline:        "\n",
	HorizontalLine: "\n---\n",
}

func (portal *Portal) parseMatrixHTML(content *event.MessageEventContent) string {
	if content.Format == event.FormatHTML && len(content.FormattedBody) > 0 {
		return variationselector.FullyQualify(matrixHTMLParser.Parse(content.FormattedBody, format.NewContext()))
	} else {
		return variationselector.FullyQualify(content.Body)
	}
}
