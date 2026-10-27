package connector

import (
	"encoding/json"
	"slices"
	"testing"

	"maunium.net/go/mautrix/event"

	"github.com/beeper/groupme-lib"
)

func TestVoiceNoteNativePeakFormats(t *testing.T) {
	for name, peaks := range map[string]string{
		"array":          `[0,0.125,0.5,1,0]`,
		"group string":   `"[0,0.125,0.5,1,0]"`,
		"live DM string": `"0.0,0.125,0.5,1.0,0.0"`,
	} {
		t.Run(name, func(t *testing.T) {
			content := &event.MessageEventContent{}
			addVoiceNoteInfo(content, &groupme.Attachment{
				Type: groupme.Audio, Duration: 3, Peaks: json.RawMessage(peaks),
			})
			if content.MSC3245Voice == nil || content.Info.Duration != 3000 || content.MSC1767Audio.Duration != 3000 {
				t.Fatal("voice flag or duration missing")
			}
			if !slices.Equal(content.MSC1767Audio.Waveform, []int{0, 128, 512, 1024, 0}) {
				t.Fatalf("unexpected waveform: %v", content.MSC1767Audio.Waveform)
			}
		})
	}
}
