package handlers

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"image"
	"image/draw"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"strings"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"

	"github.com/beeper/line/pkg/line"
)

// ConvertSticker converts a LINE sticker message to a Matrix sticker or text fallback.
func (h *Handler) ConvertSticker(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, data line.Message, relatesTo *event.RelatesTo) (*bridgev2.ConvertedMessage, error) {
	stkID := data.ContentMetadata["STKID"]
	stkTxt := data.ContentMetadata["STKTXT"]
	stkOpt := data.ContentMetadata["STKOPT"]
	if stkTxt == "" {
		stkTxt = "[Sticker]"
	}
	var stickerMessage string
	if stkOpt == "CT" {
		stickerMessage = strings.TrimSpace(data.ContentMetadata["STK_MESSAGE"])
	}

	if stkID != "" {
		baseURL := lineStickerBaseURL(stkID, data.ContentMetadata["STKHASH"])
		stickerURL := baseURL + "/sticker.png"
		isAnimated := false
		switch stkOpt {
		case "A", "AS":
			stickerURL, isAnimated = baseURL+"/sticker_animation.png", true
		case "P", "PS":
			stickerURL, isAnimated = baseURL+"/sticker_popup.png", true
		case "T":
			stickerURL = baseURL + "/base/sticker.png"
		case "CT":
			stickerURL = baseURL + "/base/plus/sticker.png"
		}

		var nameOverlay []byte
		if stkOpt == "T" {
			nameOverlay = h.downloadStickerNameOverlay(ctx, stkID, data.ContentMetadata["STK_IMG_TXT"])
			if nameOverlay == nil {
				stickerURL = baseURL + "/sticker.png"
			}
		}

		stkData, mimeType, err := h.downloadStickerImage(ctx, stickerURL)
		if err == nil && nameOverlay != nil {
			stkData, err = h.overlayStickerNameText(stkID, stkData, nameOverlay)
			mimeType = "image/png"
		}
		if fallbackURL := lineStickerBaseURL(stkID, "") + "/sticker.png"; err != nil && stickerURL != fallbackURL {
			isAnimated = false
			stkData, mimeType, err = h.downloadStickerImage(ctx, fallbackURL)
		}
		if err != nil {
			h.Log.Warn().Err(err).Str("stk_id", stkID).Msg("Failed to download sticker")
		} else {
			if isAnimated {
				stkData = forceAPNGLoop(stkData)
			}
			if mimeType == "" {
				mimeType = "image/png"
			}

			var ext string
			switch mimeType {
			case "image/jpeg", "image/jpg":
				ext = "jpg"
			case "image/webp":
				ext = "webp"
			case "image/gif":
				ext = "gif"
			case "image/png":
				ext = "png"
			default:
				ext = "png"
			}

			bodyName := "sticker." + ext

			info := &event.FileInfo{
				MimeType:       mimeType,
				Size:           len(stkData),
				BridgedSticker: &event.BridgedSticker{Network: "line", ID: stkID},
			}
			if packID := data.ContentMetadata["STKPKGID"]; packID != "" {
				info.BridgedSticker.PackURL = "https://store.line.me/stickershop/product/" + packID
			}
			stickerInfo := map[string]any{"id": stkID}
			for key, field := range map[string]string{
				"STKPKGID":   "package_id",
				"STKVER":     "version",
				"STKPKGNAME": "pack_name",
			} {
				if value := data.ContentMetadata[key]; value != "" {
					stickerInfo[field] = value
				}
			}
			if _, ok := data.ContentMetadata["STKOPT"]; ok {
				stickerInfo["animated"] = isAnimated
				stickerInfo["sound"] = strings.Contains(stkOpt, "S")
			}
			if cfg, _, err := image.DecodeConfig(bytes.NewReader(stkData)); err == nil {
				info.Width = cfg.Width * 2 / 3
				info.Height = cfg.Height * 2 / 3
			}

			mxc, file, err := intent.UploadMedia(ctx, portal.MXID, stkData, bodyName, mimeType)
			if err != nil {
				h.Log.Warn().Err(err).Msg("Failed to upload sticker to Matrix")
			} else {
				parts := []*bridgev2.ConvertedMessagePart{{
					Type:  event.EventSticker,
					Extra: map[string]any{"fi.mau.line.sticker": stickerInfo},
					Content: &event.MessageEventContent{
						Body:      bodyName,
						URL:       mxc,
						File:      file,
						Info:      info,
						RelatesTo: relatesTo,
					},
				}}
				if stickerMessage != "" {
					parts = append(parts, &bridgev2.ConvertedMessagePart{
						ID:   "message",
						Type: event.EventMessage,
						Content: &event.MessageEventContent{
							MsgType:   event.MsgText,
							Body:      stickerMessage,
							RelatesTo: relatesTo,
						},
					})
				}
				return &bridgev2.ConvertedMessage{Parts: parts}, nil
			}
		}
	}

	if stickerMessage != "" {
		stkTxt = stickerMessage
	}
	return &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{
			{
				Type: event.EventMessage,
				Content: &event.MessageEventContent{
					MsgType:   event.MsgText,
					Body:      stkTxt,
					RelatesTo: relatesTo,
				},
			},
		},
	}, nil
}

func lineStickerBaseURL(stickerID, hash string) string {
	if hash != "" {
		return fmt.Sprintf("https://stickershop.line-scdn.net/stickershop/v2/sticker/%s/%s/android", stickerID, url.PathEscape(hash))
	}
	return fmt.Sprintf("https://stickershop.line-scdn.net/stickershop/v1/sticker/%s/android", stickerID)
}

func (h *Handler) downloadStickerImage(ctx context.Context, stickerURL string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, stickerURL, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := h.HTTPClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("sticker download returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	return data, resp.Header.Get("Content-Type"), err
}

func (h *Handler) downloadStickerNameOverlay(ctx context.Context, stickerID, text string) []byte {
	if text == "" {
		return nil
	}
	overlayURL := fmt.Sprintf("https://stickershop.line-scdn.net/stickershop/v1/sticker/%s/android/overlay/name/%s/sticker.png", stickerID, url.PathEscape(text))
	data, _, err := h.downloadStickerImage(ctx, overlayURL)
	if err != nil {
		h.Log.Warn().Err(err).Str("stk_id", stickerID).Msg("Failed to download sticker name overlay")
		return nil
	}
	return data
}

func (h *Handler) overlayStickerNameText(stickerID string, baseData, overlayData []byte) ([]byte, error) {
	base, err := png.Decode(bytes.NewReader(baseData))
	if err != nil {
		h.Log.Warn().Err(err).Str("stk_id", stickerID).Msg("Failed to decode sticker base image")
		return nil, err
	}
	overlay, err := png.Decode(bytes.NewReader(overlayData))
	if err != nil {
		h.Log.Warn().Err(err).Str("stk_id", stickerID).Msg("Failed to decode sticker name overlay")
		return nil, err
	}
	combined := image.NewNRGBA(base.Bounds())
	draw.Draw(combined, combined.Bounds(), base, base.Bounds().Min, draw.Src)
	draw.Draw(combined, combined.Bounds(), overlay, overlay.Bounds().Min, draw.Over)
	var buf bytes.Buffer
	if err = png.Encode(&buf, combined); err != nil {
		h.Log.Warn().Err(err).Str("stk_id", stickerID).Msg("Failed to encode sticker with name overlay")
		return nil, err
	}
	return buf.Bytes(), nil
}

// forceAPNGLoop sets the APNG loop count to 0 (infinite) in an acTL chunk.
func forceAPNGLoop(data []byte) []byte {
	if len(data) < 8 || string(data[:8]) != "\x89PNG\r\n\x1a\n" {
		return data
	}

	offset := 8
	for offset < len(data) {
		if offset+8 > len(data) {
			break
		}
		length := binary.BigEndian.Uint32(data[offset : offset+4])
		chunkType := string(data[offset+4 : offset+8])
		if uint64(length)+12 > uint64(len(data)-offset) {
			break
		}

		if chunkType == "acTL" {
			if length >= 8 && offset+8+8 <= len(data) {
				binary.BigEndian.PutUint32(data[offset+8+4:offset+8+8], 0)

				crc := crc32.NewIEEE()
				crc.Write(data[offset+4 : offset+8+int(length)])
				newCRC := crc.Sum32()

				binary.BigEndian.PutUint32(data[offset+8+int(length):offset+8+int(length)+4], newCRC)
			}
			break
		}

		offset += 4 + 4 + int(length) + 4
	}
	return data
}
