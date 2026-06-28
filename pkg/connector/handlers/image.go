package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"strings"
	"time"

	_ "golang.org/x/image/webp"
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

	mediaCategory := lineMediaCategory(data.ContentMetadata)
	downloadOptions := lineOBSDownloadOptions(data.ContentMetadata, isPlainMedia)

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
	if isPlainMedia {
		imgData, err = client.DownloadOBSWithSIDOptions(ctx, oid, data.ID, "m", downloadOptions)
	} else {
		imgData, err = client.DownloadOBSWithOptions(ctx, oid, data.ID, downloadOptions)
	}

	// Refresh token if we get a 401
	if newClient, ok := h.tryRecoverClient(ctx, err); ok {
		client = newClient
		if isPlainMedia {
			imgData, err = client.DownloadOBSWithSIDOptions(ctx, oid, data.ID, "m", downloadOptions)
		} else {
			imgData, err = client.DownloadOBSWithOptions(ctx, oid, data.ID, downloadOptions)
		}
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

	// Upload to Matrix
	imageMedia := lineImageMediaInfo(imgData)
	if imageMedia.usedMimeFallback {
		h.Log.Debug().
			Int("size_bytes", len(imgData)).
			Msg("Falling back to JPEG MIME type for LINE image")
	}
	if imageMedia.decodeErr != nil {
		h.Log.Debug().
			Err(imageMedia.decodeErr).
			Str("mime_type", imageMedia.mimeType).
			Int("size_bytes", len(imgData)).
			Msg("Could not decode LINE image dimensions")
	}

	uploadStart := time.Now()
	mxc, file, err := intent.UploadMedia(ctx, portal.MXID, imgData, imageMedia.fileName, imageMedia.mimeType)
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

	matrixMediaURL := string(mxc)
	if file != nil && file.URL != "" {
		matrixMediaURL = string(file.URL)
	}

	h.Log.Info().
		Str("matrix_media_url", matrixMediaURL).
		Str("file_name", imageMedia.fileName).
		Str("mime_type", imageMedia.mimeType).
		Int("size", len(imgData)).
		Int("width", imageMedia.info.Width).
		Int("height", imageMedia.info.Height).
		Dur("download_duration", downloadDuration).
		Dur("decrypt_duration", decryptDuration).
		Dur("upload_duration", uploadDuration).
		Msg("Successfully uploaded image to Matrix")

	return &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{
			{
				Type:    event.EventMessage,
				Content: lineImageEventContent(mxc, file, imageMedia.fileName, imageMedia.info, relatesTo),
			},
		},
	}, nil
}
