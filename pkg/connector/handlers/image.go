package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"strings"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"

	"github.com/highesttt/matrix-line-messenger/pkg/line"
)

// ConvertImage converts a LINE image message to a Matrix image message.
func (h *Handler) ConvertImage(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, data line.Message, decryptedBody string, relatesTo *event.RelatesTo) (*bridgev2.ConvertedMessage, error) {
	client := h.NewClient()
	oid := data.ContentMetadata["OID"]
	isPlainMedia := oid == ""

	// For plain media, the image is stored at r/talk/m/{messageID}
	if isPlainMedia {
		oid = data.ID
	}

	if oid == "" {
		return nil, nil
	}

	// MEDIA_CONTENT_INFO marks animated GIFs, which need the original OBS object.
	metadataAnimated := false
	var metadataWidth, metadataHeight int
	if mediaInfo := data.ContentMetadata["MEDIA_CONTENT_INFO"]; mediaInfo != "" {
		var info struct {
			Animated bool `json:"animated"`
			Width    int  `json:"width"`
			Height   int  `json:"height"`
		}
		if json.Unmarshal([]byte(mediaInfo), &info) == nil {
			metadataAnimated = info.Animated
			metadataWidth = info.Width
			metadataHeight = info.Height
		}
	}
	if thumbInfo := data.ContentMetadata["MEDIA_THUMB_INFO"]; thumbInfo != "" {
		var info struct {
			Width  int `json:"width"`
			Height int `json:"height"`
		}
		if json.Unmarshal([]byte(thumbInfo), &info) == nil {
			metadataWidth = info.Width
			metadataHeight = info.Height
		}
	}

	mediaCategory := lineMediaCategory(data.ContentMetadata)
	downloadOptions := lineOBSDownloadOptions(data.ContentMetadata, isPlainMedia)

	downloadImage := func(c *line.Client) ([]byte, error) {
		sid := "emi"
		if isPlainMedia {
			sid = "m"
		}
		if metadataAnimated {
			originalOptions := downloadOptions
			originalOptions.TID = "original"
			standardOptions := downloadOptions
			if isPlainMedia {
				standardOptions.TID = ""
			}

			if isPlainMedia {
				imgData, err := c.DownloadOBSWithSIDOptions(ctx, oid, data.ID, sid, originalOptions)
				if err == nil {
					return imgData, nil
				}
				h.Log.Debug().
					Err(err).
					Str("oid", oid).
					Str("msg_id", data.ID).
					Str("sid", sid).
					Msg("Failed to download animated image original, falling back to standard OBS path")
				return c.DownloadOBSWithSIDOptions(ctx, oid, data.ID, sid, standardOptions)
			}

			imgData, err := c.DownloadOBSWithSIDOptions(ctx, oid, data.ID, sid, standardOptions)
			if err == nil {
				return imgData, nil
			}
			h.Log.Debug().
				Err(err).
				Str("oid", oid).
				Str("msg_id", data.ID).
				Str("sid", sid).
				Msg("Failed to download encrypted animated image, falling back to original OBS path")
			return c.DownloadOBSWithSIDOptions(ctx, oid, data.ID, sid, originalOptions)
		}
		return c.DownloadOBSWithSIDOptions(ctx, oid, data.ID, sid, downloadOptions)
	}

	var imgData []byte
	var err error
	dlStart := time.Now()
	h.Log.Debug().
		Str("oid", oid).
		Str("msg_id", data.ID).
		Str("tid", downloadOptions.TID).
		Str("media_category", mediaCategory).
		Bool("has_obs_pop", downloadOptions.OBSPop != "").
		Bool("plain_media", isPlainMedia).
		Msg("Downloading image from LINE OBS")
	imgData, err = downloadImage(client)

	// Refresh token if we get a 401
	if newClient, ok := h.tryRecoverClient(ctx, err); ok {
		client = newClient
		imgData, err = downloadImage(client)
	}
	downloadDuration := time.Since(dlStart)

	if err != nil {
		h.Log.Warn().
			Err(err).
			Str("oid", oid).
			Str("msg_id", data.ID).
			Bool("plain_media", isPlainMedia).
			Dur("download_duration", downloadDuration).
			Msg("Failed to download image from OBS, sending placeholder")
		return &bridgev2.ConvertedMessage{
			Parts: []*bridgev2.ConvertedMessagePart{
				{
					Type: event.EventMessage,
					Content: &event.MessageEventContent{
						MsgType:   event.MsgNotice,
						Body:      "[Image unavailable — LINE media expired before it could be bridged]",
						RelatesTo: relatesTo,
					},
				},
			},
		}, nil
	}

	// Decrypt image if it has keyMaterial (E2EE)
	var decryptDuration time.Duration
	if decryptedBody != "" && strings.Contains(decryptedBody, "keyMaterial") {
		var decryptInfo struct {
			KeyMaterial string `json:"keyMaterial"`
			FileName    string `json:"fileName"`
		}
		if err := json.Unmarshal([]byte(decryptedBody), &decryptInfo); err == nil && decryptInfo.KeyMaterial != "" {
			decryptStart := time.Now()
			decryptedImg, err := h.DecryptMedia(imgData, decryptInfo.KeyMaterial)
			decryptDuration = time.Since(decryptStart)
			if err != nil {
				h.Log.Error().
					Err(err).
					Dur("download_duration", downloadDuration).
					Dur("decrypt_duration", decryptDuration).
					Msg("Failed to decrypt image data")
				return nil, fmt.Errorf("failed to decrypt image data: %w", err)
			}
			imgData = decryptedImg
		}
	}

	fileName := "image.jpg"
	mimeType := "image/jpeg"
	isAnimated := false

	if h.IsAnimatedGif != nil && h.IsAnimatedGif(imgData) {
		fileName = "image.gif"
		mimeType = "image/gif"
		isAnimated = true
	} else if len(imgData) >= 3 && string(imgData[0:3]) == "GIF" {
		fileName = "image.gif"
		mimeType = "image/gif"
		isAnimated = metadataAnimated
	} else if len(imgData) >= 8 && string(imgData[:8]) == "\x89PNG\r\n\x1a\n" {
		fileName = "image.png"
		mimeType = "image/png"
	} else if len(imgData) >= 12 && string(imgData[:4]) == "RIFF" && string(imgData[8:12]) == "WEBP" {
		fileName = "image.webp"
		mimeType = "image/webp"
	}

	// Upload to Matrix
	uploadStart := time.Now()
	mxc, file, err := intent.UploadMedia(ctx, portal.MXID, imgData, fileName, mimeType)
	uploadDuration := time.Since(uploadStart)
	if err != nil {
		h.Log.Error().
			Err(err).
			Int("size_bytes", len(imgData)).
			Dur("download_duration", downloadDuration).
			Dur("decrypt_duration", decryptDuration).
			Dur("upload_duration", uploadDuration).
			Msg("Failed to upload image to Matrix")
		return nil, fmt.Errorf("failed to upload image to matrix: %w", err)
	}

	msgType := event.MsgImage
	info := &event.FileInfo{
		MimeType: mimeType,
		Size:     len(imgData),
	}
	if metadataWidth > 0 && metadataHeight > 0 {
		info.Width = metadataWidth
		info.Height = metadataHeight
	} else if config, _, err := image.DecodeConfig(bytes.NewReader(imgData)); err != nil {
		h.Log.Warn().Err(err).Bool("animated", isAnimated).Msg("Failed to decode image dimensions")
	} else {
		info.Width = config.Width
		info.Height = config.Height
	}
	if isAnimated {
		info.MauGIF = true
		info.IsAnimated = true
	}

	matrixMediaURL := string(mxc)
	if file != nil && file.URL != "" {
		matrixMediaURL = string(file.URL)
	}

	h.Log.Info().
		Str("matrix_media_url", matrixMediaURL).
		Int("size", len(imgData)).
		Dur("download_duration", downloadDuration).
		Dur("decrypt_duration", decryptDuration).
		Dur("upload_duration", uploadDuration).
		Msg("Successfully uploaded image to Matrix")

	return &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{
			{
				Type: event.EventMessage,
				Content: &event.MessageEventContent{
					MsgType:   msgType,
					Body:      fileName,
					URL:       mxc,
					File:      file,
					Info:      info,
					RelatesTo: relatesTo,
				},
			},
		},
	}, nil
}

func lineMediaCategory(metadata map[string]string) string {
	if metadata == nil || metadata["MEDIA_CONTENT_INFO"] == "" {
		return ""
	}

	var info struct {
		Category string `json:"category"`
	}
	if err := json.Unmarshal([]byte(metadata["MEDIA_CONTENT_INFO"]), &info); err != nil {
		return ""
	}

	return info.Category
}

func lineOBSDownloadOptions(metadata map[string]string, isPlainMedia bool) line.OBSDownloadOptions {
	opts := line.OBSDownloadOptions{
		OBSPop: metadata["OBS_POP"],
	}
	if isPlainMedia && lineMediaCategory(metadata) == "original" {
		opts.TID = "original"
	}
	return opts
}
