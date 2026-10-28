package connector

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/beeper/groupme-lib"
)

type stickerMatrix struct {
	bridgev2.MatrixAPI
	bridgev2.MatrixConnector
	t *testing.T
}

func (m stickerMatrix) UploadMedia(_ context.Context, _ id.RoomID, data []byte, _, mime string) (id.ContentURIString, *event.EncryptedFileInfo, error) {
	if string(data) != "GIF89a" || mime != "image/gif" {
		m.t.Fatal("sticker upload lost image data or MIME type")
	}
	return "mxc://test/sticker", nil, nil
}

func (m stickerMatrix) GenerateContentURI(_ context.Context, mediaID networkid.MediaID) (id.ContentURIString, error) {
	if _, err := parseDirectMediaID(mediaID); err != nil {
		m.t.Fatal(err)
	}
	return "mxc://test/sticker", nil
}

func TestStickerPushAndHistory(t *testing.T) {
	const source = "https://media4.giphy.com/media/example/200w.gif"
	for _, attachment := range []string{
		`{"type":"postprocessing","queues":["linked_image"]}`,
		`{"type":"linked_image","url":"https://i.groupme.com/200x200.gif.example"}`,
	} {
		for _, direct := range []bool{false, true} {
			var msg groupme.Message
			if err := json.Unmarshal([]byte(`{"text":"`+source+`","attachments":[`+attachment+`]}`), &msg); err != nil {
				t.Fatal(err)
			}
			previous := http.DefaultTransport
			http.DefaultTransport = loginTransport(func(r *http.Request) (*http.Response, error) {
				if r.Header.Get("Cookie") != "" || r.Header.Get("X-Access-Token") != "" {
					t.Fatal("sticker download included credentials")
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"image/gif"}}, Body: io.NopCloser(strings.NewReader("GIF89a"))}, nil
			})
			matrix := stickerMatrix{t: t}
			gc := &GMClient{Main: &GMConnector{br: &bridgev2.Bridge{Matrix: matrix}, useDirectMedia: direct}}
			portal := &bridgev2.Portal{Portal: &database.Portal{MXID: "!test:test"}}
			converted, err := gc.convertGroupMeMessage(context.Background(), portal, matrix, &msg)
			http.DefaultTransport = previous
			if err != nil {
				t.Fatal(err)
			}
			if len(converted.Parts) != 1 || converted.Parts[0].Type != event.EventMessage {
				t.Fatalf("unexpected sticker parts: %+v", converted)
			}
			content := converted.Parts[0].Content
			if content.MsgType != event.MsgImage || content.URL != "mxc://test/sticker" || content.Info.MimeType != "image/gif" || content.Body != source {
				t.Fatalf("sticker is not a renderable image: %+v", content)
			}
			if string(msg.Attachments[0].Type) == "image" {
				t.Fatal("conversion changed native attachment metadata")
			}
		}
	}
}
