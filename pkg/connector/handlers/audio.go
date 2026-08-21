package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"

	"github.com/highesttt/matrix-line-messenger/pkg/line"
)

// ConvertAudio converts a LINE audio message to a Matrix audio message.
func (h *Handler) ConvertAudio(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, data line.Message, decryptedBody string, relatesTo *event.RelatesTo) (*bridgev2.ConvertedMessage, error) {
	if oversized := h.oversizedMediaNoticeFromMetadata(data.ContentMetadata, relatesTo); oversized != nil {
		return oversized, nil
	}

	client := h.NewClient()
	oid := data.ContentMetadata["OID"]
	isPlainMedia := oid == ""

	// If OID is not in ContentMetadata, check decrypted body (E2EE path)
	if oid == "" && decryptedBody != "" && strings.Contains(decryptedBody, "OID") {
		var decryptInfo struct {
			OID         string `json:"OID"`
			KeyMaterial string `json:"keyMaterial"`
		}
		if err := json.Unmarshal([]byte(decryptedBody), &decryptInfo); err == nil && decryptInfo.OID != "" {
			oid = decryptInfo.OID
			isPlainMedia = false
		}
	}

	// For plain media, the audio is stored at r/talk/m/{messageID}
	if isPlainMedia {
		oid = data.ID
	}

	if oid == "" {
		return nil, nil
	}

	sid := "ema"
	if isPlainMedia {
		sid = "m"
	}
	downloadOptions := lineOBSDownloadOptions(data.ContentMetadata, isPlainMedia)
	talkMetaMessageID := obsTalkMetaMessageID(data.ID, isPlainMedia)
	audioData, err := client.DownloadOBSWithSIDOptions(ctx, oid, talkMetaMessageID, sid, downloadOptions)

	if newClient, ok := h.tryRecoverClient(ctx, client, err); ok {
		client = newClient
		audioData, err = client.DownloadOBSWithSIDOptions(ctx, oid, talkMetaMessageID, sid, downloadOptions)
	}
	h.handleFinalAuthError(ctx, client, err)

	if err != nil {
		h.Log.Warn().
			Err(err).
			Str("oid", oid).
			Str("msg_id", data.ID).
			Bool("plain_media", isPlainMedia).
			Msg("Failed to download audio from OBS")
		return mediaDownloadFailure("Audio", err, relatesTo)
	}

	audioData, err = h.decryptDownloadedMedia(audioData, decryptedBody, data.ContentMetadata, "audio")
	if err != nil {
		h.Log.Error().Err(err).Msg("Failed to decrypt audio data")
		return nil, err
	}

	if oversized := h.oversizedMediaNotice(int64(len(audioData)), "downloaded", relatesTo); oversized != nil {
		return oversized, nil
	}

	var duration int
	if durationStr := data.ContentMetadata["DURATION"]; durationStr != "" {
		if d, err := strconv.Atoi(durationStr); err == nil {
			duration = d
		}
	}

	mxc, file, err := intent.UploadMedia(ctx, portal.MXID, audioData, "audio.m4a", "audio/mp4")
	if err != nil {
		return nil, fmt.Errorf("failed to upload audio to matrix: %w", err)
	}

	audioInfo := &event.FileInfo{
		MimeType: "audio/mp4",
		Size:     len(audioData),
	}
	if duration > 0 {
		audioInfo.Duration = duration
	}

	return &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{
			{
				Type: event.EventMessage,
				Content: &event.MessageEventContent{
					MsgType:   event.MsgAudio,
					Body:      "audio.m4a",
					URL:       mxc,
					File:      file,
					Info:      audioInfo,
					RelatesTo: relatesTo,
				},
			},
		},
	}, nil
}
