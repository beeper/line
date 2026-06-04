package handlers

import (
	"context"
	"encoding/json"
	"fmt"
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

	downloadOptions := line.OBSDownloadOptions{
		OBSPop: data.ContentMetadata["OBS_POP"],
	}
	if isPlainMedia && lineMediaCategory(data.ContentMetadata) == "original" {
		downloadOptions.TID = "original"
	}

	var imgData []byte
	var err error
	dlStart := time.Now()
	h.Log.Debug().
		Str("oid", oid).
		Str("msg_id", data.ID).
		Str("tid", downloadOptions.TID).
		Bool("has_obs_pop", downloadOptions.OBSPop != "").
		Bool("plain_media", isPlainMedia).
		Interface("content_metadata", data.ContentMetadata).
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
	uploadStart := time.Now()
	mxc, file, err := intent.UploadMedia(ctx, portal.MXID, imgData, "image.jpg", "image/jpeg")
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

	h.Log.Info().
		Str("mxc", mxc.ParseOrIgnore().String()).
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
					MsgType:   event.MsgImage,
					Body:      "image.jpg",
					URL:       mxc,
					File:      file,
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
