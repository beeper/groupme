package connector

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"maunium.net/go/mautrix/event"

	"github.com/beeper/groupme-lib"
)

func TestGroupMeEmojiConversion(t *testing.T) {
	for _, tc := range []struct {
		name, text, placeholder, want string
		charmap                       [][]int
	}{
		{"alone", "�", "�", "[smiley face]", [][]int{{1, 0}}},
		{"text", "�hi", "�", "[happy face]hi", [][]int{{1, 1}}},
		{"mixed", "😀 � 👩‍💻 �", "�", "😀 [pleased face] 👩‍💻 [heart eyes]", [][]int{{1, 2}, {18, 1}}},
		{"literal replacement", "�", "", "�", nil},
		{"different placeholder", "x\ufffcx", "\ufffc", "x[smiley face]x", [][]int{{1, 0}}},
		{"unknown emoji", "�", "�", "[GroupMe emoji 99:2]", [][]int{{99, 2}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := &groupme.Message{Text: tc.text}
			if tc.placeholder != "" {
				msg.Attachments = []*groupme.Attachment{{Type: groupme.Emoji, Placeholder: tc.placeholder, Charmap: tc.charmap}}
			}
			before, _ := json.Marshal(msg)
			converted, err := dmTestClient().convertGroupMeMessage(context.Background(), nil, nil, msg)
			if err != nil {
				t.Fatal(err)
			}
			if len(converted.Parts) != 1 || converted.Parts[0].Content.Body != tc.want || converted.Parts[0].Content.MsgType != event.MsgText {
				t.Fatalf("unexpected conversion: %+v", converted.Parts)
			}
			after, _ := json.Marshal(msg)
			if string(before) != string(after) {
				t.Fatal("conversion changed the native message snapshot")
			}
		})
	}
}

func TestGroupMeEmojiMentionOffsets(t *testing.T) {
	gc := dmTestClient()
	gc.UserLogin.UserMXID = "@tester:example.org"
	msg := &groupme.Message{
		Text: "😀 � @A � @A � <&>\n👩‍💻",
		Attachments: []*groupme.Attachment{
			{Type: groupme.Emoji, Placeholder: "�", Charmap: [][]int{{1, 0}, {1, 1}, {1, 2}}},
			{Type: groupme.Mentions, UserIDs: []groupme.ID{"20", "20"}, Loci: [][]int{{5, 2}, {10, 2}}},
		},
	}
	converted, err := gc.convertGroupMeMessage(context.Background(), nil, nil, msg)
	if err != nil {
		t.Fatal(err)
	}
	content := converted.Parts[0].Content
	wantBody := "😀 [smiley face] @A [happy face] @A [pleased face] <&>\n👩‍💻"
	wantHTML := `😀 [smiley face] <a href="https://matrix.to/#/@tester:example.org">@A</a> [happy face] <a href="https://matrix.to/#/@tester:example.org">@A</a> [pleased face] &lt;&amp;&gt;<br>👩‍💻`
	if content.Body != wantBody || content.FormattedBody != wantHTML || content.Format != event.FormatHTML {
		t.Fatalf("mention or emoji shifted: %+v", content)
	}
	if content.Mentions == nil || len(content.Mentions.UserIDs) != 1 || content.Mentions.UserIDs[0] != gc.UserLogin.UserMXID {
		t.Fatalf("lost mention target: %+v", content.Mentions)
	}
	if !reflect.DeepEqual(msg.Attachments[1].Loci, [][]int{{5, 2}, {10, 2}}) {
		t.Fatal("changed native mention offsets")
	}
}
