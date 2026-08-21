package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"

	"github.com/highesttt/matrix-line-messenger/pkg/line"
)

// ConvertVideo converts a LINE video message to a Matrix video message.
func (h *Handler) ConvertVideo(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, data line.Message, decryptedBody string, relatesTo *event.RelatesTo) (*bridgev2.ConvertedMessage, error) {
	if oversized := h.oversizedMediaNoticeFromMetadata(data.ContentMetadata, relatesTo); oversized != nil {
		return oversized, nil
	}

	client := h.NewClient()
	oid := data.ContentMetadata["OID"]
	isPlainMedia := oid == ""

	if oid == "" && decryptedBody != "" && strings.Contains(decryptedBody, "OID") {
		var decryptInfo struct {
			OID         string `json:"OID"`
			KeyMaterial string `json:"keyMaterial"`
			FileName    string `json:"fileName"`
		}
		if err := json.Unmarshal([]byte(decryptedBody), &decryptInfo); err == nil && decryptInfo.OID != "" {
			oid = decryptInfo.OID
			isPlainMedia = false
		}
	}

	// For plain media, the video is stored at r/talk/m/{messageID}
	if isPlainMedia {
		oid = data.ID
	}

	if oid == "" {
		return nil, nil
	}

	sid := "emv"
	if isPlainMedia {
		sid = "m"
	}
	downloadOptions := lineOBSDownloadOptions(data.ContentMetadata, isPlainMedia)
	talkMetaMessageID := obsTalkMetaMessageID(data.ID, isPlainMedia)
	dlStart := time.Now()
	videoData, err := client.DownloadOBSWithSIDOptions(ctx, oid, talkMetaMessageID, sid, downloadOptions)

	if newClient, ok := h.tryRecoverClient(ctx, client, err); ok {
		client = newClient
		videoData, err = client.DownloadOBSWithSIDOptions(ctx, oid, talkMetaMessageID, sid, downloadOptions)
	}
	h.handleFinalAuthError(ctx, client, err)

	if err != nil {
		h.Log.Warn().
			Err(err).
			Str("oid", oid).
			Str("msg_id", data.ID).
			Bool("plain_media", isPlainMedia).
			Dur("download_duration", time.Since(dlStart)).
			Msg("Failed to download video from OBS")
		return mediaDownloadFailure("Video", err, relatesTo)
	}

	videoData, err = h.decryptDownloadedMedia(videoData, decryptedBody, data.ContentMetadata, "video")
	if err != nil {
		h.Log.Error().Err(err).Msg("Failed to decrypt video data")
		return nil, err
	}

	if oversized := h.oversizedMediaNotice(int64(len(videoData)), "downloaded", relatesTo); oversized != nil {
		return oversized, nil
	}

	fileName := data.ContentMetadata["FILE_NAME"]

	if fileName == "" && decryptedBody != "" && strings.Contains(decryptedBody, "fileName") {
		var decryptInfo struct {
			FileName string `json:"fileName"`
		}
		if err := json.Unmarshal([]byte(decryptedBody), &decryptInfo); err == nil && decryptInfo.FileName != "" {
			fileName = decryptInfo.FileName
		}
	}

	if fileName == "" {
		fileName = "video.mp4"
	}

	mimeType := "video/mp4"
	if strings.HasSuffix(strings.ToLower(fileName), ".webm") {
		mimeType = "video/webm"
	}

	mxc, file, err := intent.UploadMedia(ctx, portal.MXID, videoData, fileName, mimeType)
	if err != nil {
		h.Log.Error().Err(err).Int("size_bytes", len(videoData)).Msg("Failed to upload video to Matrix")
		return nil, fmt.Errorf("failed to upload video to matrix: %w", err)
	}

	h.Log.Info().
		Str("mxc", mxc.ParseOrIgnore().String()).
		Str("file_name", fileName).
		Int("size", len(videoData)).
		Dur("download_duration", time.Since(dlStart)).
		Msg("Successfully uploaded video to Matrix")

	var duration int
	if durationStr := data.ContentMetadata["DURATION"]; durationStr != "" {
		if d, err := strconv.Atoi(durationStr); err == nil {
			duration = d
		}
	}

	videoInfo := &event.FileInfo{
		MimeType: mimeType,
		Size:     len(videoData),
	}
	if duration > 0 {
		videoInfo.Duration = duration
	}

	return &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{
			{
				Type: event.EventMessage,
				Content: &event.MessageEventContent{
					MsgType:   event.MsgVideo,
					Body:      fileName,
					URL:       mxc,
					File:      file,
					Info:      videoInfo,
					RelatesTo: relatesTo,
				},
			},
		},
	}, nil
}
