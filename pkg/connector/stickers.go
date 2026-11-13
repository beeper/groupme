package connector

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/beeper/groupme-lib"

	"github.com/beeper/groupme/pkg/groupmeext"
)

var _ bridgev2.StickerImportingNetworkAPI = (*GMClient)(nil)

const emojiPackURLPrefix = "groupme://emoji-pack/"

const emojiPlaceholder = "\uFFFD"

const emojiTileSize = 120

func emojiPackMetadata(pack int) *event.ImagePackMetadata {
	return &event.ImagePackMetadata{
		DisplayName: fmt.Sprintf("GroupMe emoji pack %d", pack),
		Usage:       []event.ImagePackUsage{event.ImagePackUsageSticker},
		BridgedPack: &event.BridgedStickerPack{Network: "groupme", URL: emojiPackURLPrefix + strconv.Itoa(pack)},
	}
}

func emojiStickerInfo(pack, index int, data []byte, size int) *event.FileInfo {
	return &event.FileInfo{MimeType: "image/png", Size: len(data), Width: size, Height: size, BridgedSticker: &event.BridgedSticker{
		Network: "groupme", ID: fmt.Sprintf("%d:%d", pack, index), PackURL: emojiPackURLPrefix + strconv.Itoa(pack),
	}}
}

// Pack sheets stack square tiles vertically in charmap index order.
func fetchEmojiSheet(ctx context.Context, pack int) (image.Image, int, error) {
	data, _, err := groupmeext.DownloadImage(ctx, fmt.Sprintf("https://powerups.s3.amazonaws.com/emoji/%d/keyboard.xxhdpi.120x120.png", pack))
	if err != nil {
		return nil, 0, err
	}
	sheet, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, 0, fmt.Errorf("invalid GroupMe emoji sheet: %w", err)
	} else if sheet.Bounds().Dx() == 0 {
		return sheet, 0, nil
	}
	return sheet, sheet.Bounds().Dy() / sheet.Bounds().Dx(), nil
}

func emojiTile(sheet image.Image, index int) ([]byte, int, error) {
	size := sheet.Bounds().Dx()
	tile := image.NewNRGBA(image.Rect(0, 0, size, size))
	draw.Draw(tile, tile.Bounds(), sheet, sheet.Bounds().Min.Add(image.Pt(0, index*size)), draw.Src)
	var buf bytes.Buffer
	err := png.Encode(&buf, tile)
	return buf.Bytes(), size, err
}

type cachedEmojiTile struct {
	data []byte
	size int
}

// Bounded by the catalog. Callers get copies because encrypted uploads encrypt in place.
var emojiTileCache sync.Map

func downloadEmoji(ctx context.Context, pack, index int) ([]byte, int, error) {
	if cached, ok := emojiTileCache.Load([2]int{pack, index}); ok {
		return bytes.Clone(cached.(cachedEmojiTile).data), cached.(cachedEmojiTile).size, nil
	}
	sheet, count, err := fetchEmojiSheet(ctx, pack)
	if err != nil {
		return nil, 0, err
	} else if index >= count {
		return nil, 0, fmt.Errorf("emoji index is outside the pack sheet")
	}
	data, size, err := emojiTile(sheet, index)
	if err == nil {
		emojiTileCache.Store([2]int{pack, index}, cachedEmojiTile{data, size})
	}
	return bytes.Clone(data), size, err
}

func singleEmoji(msg *groupme.Message) (pack, index int, ok bool) {
	var emoji *groupme.Attachment
	for _, att := range msg.Attachments {
		if att == nil || att.Type == groupme.Reply {
			continue
		} else if att.Type != groupme.Emoji || emoji != nil {
			return 0, 0, false
		}
		emoji = att
	}
	if emoji == nil || len(emoji.Charmap) != 1 || len(emoji.Charmap[0]) != 2 || strings.TrimSpace(msg.Text) != cmp.Or(emoji.Placeholder, emojiPlaceholder) {
		return 0, 0, false
	}
	pack, index = emoji.Charmap[0][0], emoji.Charmap[0][1]
	return pack, index, index >= 0 && index < len(groupMeEmojiNames[pack])
}

func (gc *GMClient) emojiStickerPart(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, msg *groupme.Message) (*bridgev2.ConvertedMessagePart, error) {
	pack, index, ok := singleEmoji(msg)
	if !ok {
		return nil, nil
	}
	data, size, err := downloadEmoji(ctx, pack, index)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Int("pack", pack).Int("index", index).Msg("Failed to get GroupMe emoji sticker image, bridging it as text")
		return nil, nil
	}
	mxc, file, err := intent.UploadMedia(ctx, portal.MXID, data, "sticker.png", "image/png")
	if err != nil {
		return nil, fmt.Errorf("upload GroupMe emoji sticker: %w", err)
	}
	content := &event.MessageEventContent{Body: groupMeEmojiName(pack, index), Info: emojiStickerInfo(pack, index, data, size)}
	if file != nil {
		content.File = file
	} else {
		content.URL = mxc
	}
	return &bridgev2.ConvertedMessagePart{Type: event.EventSticker, Content: content}, nil
}

// GroupMe web offers every catalog pack to all users and sends a sticker as a lone pack emoji.
func nativeEmojiSticker(info *event.FileInfo) *groupme.Attachment {
	if info == nil || info.BridgedSticker == nil || info.BridgedSticker.Network != "groupme" {
		return nil
	}
	rawPack, rawIndex, _ := strings.Cut(info.BridgedSticker.ID, ":")
	pack, packErr := strconv.Atoi(rawPack)
	index, indexErr := strconv.Atoi(rawIndex)
	if packErr != nil || indexErr != nil || index < 0 || index >= len(groupMeEmojiNames[pack]) {
		return nil
	}
	return &groupme.Attachment{Type: groupme.Emoji, Placeholder: emojiPlaceholder, Charmap: groupme.IntPairs{{pack, index}}}
}

func (gc *GMClient) ListImagePacks(ctx context.Context) ([]*event.ImagePackMetadata, error) {
	packs := make([]*event.ImagePackMetadata, 0, len(groupMeEmojiNames))
	for _, pack := range slices.Sorted(maps.Keys(groupMeEmojiNames)) {
		packs = append(packs, emojiPackMetadata(pack))
	}
	return packs, nil
}

func (gc *GMClient) DownloadImagePack(ctx context.Context, packURL string) (*bridgev2.ImportedImagePack, error) {
	rawPack, ok := strings.CutPrefix(packURL, emojiPackURLPrefix)
	pack, err := strconv.Atoi(rawPack)
	if !ok || err != nil || groupMeEmojiNames[pack] == nil {
		return nil, mautrix.MNotFound.WithMessage("Unsupported GroupMe emoji pack")
	}
	imported := &bridgev2.ImportedImagePack{
		Shortcode: fmt.Sprintf("groupme_%d", pack),
		Content:   &event.ImagePackEventContent{Metadata: *emojiPackMetadata(pack), Images: make(map[string]*event.ImagePackImage, len(groupMeEmojiNames[pack]))},
	}
	var sheet image.Image
	var count int
	for index := range groupMeEmojiNames[pack] {
		dbKey := database.Key(fmt.Sprintf("stickercache:groupme:%d:%d", pack, index))
		mxc := id.ContentURIString(gc.Main.br.DB.KV.Get(ctx, dbKey))
		if mxc == "" {
			if sheet == nil {
				if sheet, count, err = fetchEmojiSheet(ctx, pack); err != nil {
					return nil, fmt.Errorf("failed to download GroupMe emoji pack: %w", err)
				}
			}
			if index >= count {
				break
			}
			data, _, err := emojiTile(sheet, index)
			if err != nil {
				return nil, fmt.Errorf("failed to crop GroupMe emoji %d:%d: %w", pack, index, err)
			}
			mxc, _, err = gc.Main.br.Bot.UploadMedia(ctx, "", data, fmt.Sprintf("%d_%d.png", pack, index), "image/png")
			if err != nil {
				return nil, fmt.Errorf("failed to upload GroupMe emoji %d:%d: %w", pack, index, err)
			}
			gc.Main.br.DB.KV.Set(ctx, dbKey, string(mxc))
		}
		if index == 0 {
			imported.Content.Metadata.AvatarURL = mxc
		}
		imported.Content.Images[fmt.Sprintf("groupme_%d_%d", pack, index)] = &event.ImagePackImage{
			URL: mxc, Body: groupMeEmojiName(pack, index), Info: emojiStickerInfo(pack, index, nil, emojiTileSize),
		}
	}
	if len(imported.Content.Images) == 0 {
		return nil, mautrix.MNotFound.WithMessage("GroupMe emoji pack is empty")
	}
	return imported, nil
}
