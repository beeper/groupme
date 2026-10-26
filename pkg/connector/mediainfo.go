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
	"mime"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"

	"maunium.net/go/mautrix/event"

	"github.com/beeper/groupme-lib"
)

// GroupMe image URLs name the file after its dimensions and format, e.g.
// i.groupme.com/498x273.gif.<hash> or m.groupme.com/uploads/<id>/200x154.original.gif.
var imageNamePattern = regexp.MustCompile(`^(\d+)x(\d+)\.(?:original\.)?([A-Za-z0-9]+)`)

func imageInfoFromURL(rawURL string) *event.FileInfo {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}
	match := imageNamePattern.FindStringSubmatch(path.Base(parsed.Path))
	if match == nil {
		return nil
	}
	info := &event.FileInfo{MimeType: mime.TypeByExtension("." + strings.ToLower(match[3]))}
	info.Width, _ = strconv.Atoi(match[1])
	info.Height, _ = strconv.Atoi(match[2])
	return info
}

func sameMediaURL(a, b string) bool {
	if a == b {
		return true
	}
	pa, errA := url.Parse(a)
	pb, errB := url.Parse(b)
	return errA == nil && errB == nil && pa.Host != "" && strings.EqualFold(pa.Host, pb.Host) && pa.Path == pb.Path
}

// GroupMe appends media URLs and document notices to the text for older
// clients; its own clients hide them when the attachment is shown.
func visibleText(text string, attachments []*groupme.Attachment) string {
	for _, att := range attachments {
		if att == nil {
			continue
		}
		switch att.Type {
		case groupme.Image, groupme.Video:
			trimmed := strings.TrimSpace(text)
			last := trimmed[strings.LastIndexAny(trimmed, " \t\n")+1:]
			if att.URL != "" && last != "" && sameMediaURL(last, att.URL) {
				text = strings.TrimSpace(strings.TrimSuffix(trimmed, last))
			}
		case groupme.File:
			if strings.HasPrefix(text, "Shared a document:") {
				text = ""
			}
		}
	}
	return text
}
