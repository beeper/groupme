package connector

import (
	"fmt"
	"strings"

	"github.com/beeper/groupme-lib"
)

// Each occurrence of the placeholder consumes one [pack, row] entry, even
// when text is rendered in separate segments around UTF-16 mention loci.
func groupMeEmojiReplacer(attachments []*groupme.Attachment) func(string) string {
	var emoji *groupme.Attachment
	for _, att := range attachments {
		if att != nil && att.Type == groupme.Emoji {
			emoji = att
			break
		}
	}
	if emoji == nil || emoji.Placeholder == "" {
		return func(text string) string { return text }
	}
	index := 0
	return func(text string) string {
		var out strings.Builder
		for index < len(emoji.Charmap) {
			before, after, found := strings.Cut(text, emoji.Placeholder)
			if !found {
				break
			}
			out.WriteString(before)
			pair := emoji.Charmap[index]
			name := "GroupMe emoji"
			if len(pair) == 2 {
				names := groupMeEmojiNames[pair[0]]
				if pair[1] >= 0 && pair[1] < len(names) {
					name = names[pair[1]]
				} else {
					name = fmt.Sprintf("GroupMe emoji %d:%d", pair[0], pair[1])
				}
			}
			out.WriteString("[" + name + "]")
			text = after
			index++
		}
		out.WriteString(text)
		return out.String()
	}
}
