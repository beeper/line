package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

	"github.com/highesttt/matrix-line-messenger/pkg/line"
)

type postPreviewMedia struct {
	Service   string `json:"svc"`
	SID       string `json:"sid"`
	OID       string `json:"mediaOid"`
	MediaType string `json:"mediaType"`
}

// ConvertPostNotification converts a LINE note, album, or unknown post
// notification into a readable Matrix notice, including album preview images.
func (h *Handler) ConvertPostNotification(
	ctx context.Context,
	portal *bridgev2.Portal,
	intent bridgev2.MatrixAPI,
	data line.Message,
	relatesTo *event.RelatesTo,
) (*bridgev2.ConvertedMessage, error) {
	serviceType := strings.ToUpper(strings.TrimSpace(data.ContentMetadata["serviceType"]))
	preview := strings.TrimSpace(data.ContentMetadata["text"])
	albumName := strings.TrimSpace(data.ContentMetadata["albumName"])

	var body strings.Builder
	switch serviceType {
	case "GB":
		body.WriteString("You received a LINE note.")
	case "AB":
		if albumName == "" {
			body.WriteString("You received a LINE album update.")
		} else {
			body.WriteString("LINE album update: ")
			body.WriteString(albumName)
		}
	default:
		body.WriteString("You received a LINE post notification.")
		if preview == "" {
			preview = albumName
		}
	}

	if preview != "" {
		body.WriteString("\n\nPreview:\n")
		body.WriteString(preview)
	}

	content := &event.MessageEventContent{
		MsgType:   event.MsgNotice,
		Body:      body.String(),
		RelatesTo: relatesTo,
	}

	converted := &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{
			{
				Type:    event.EventMessage,
				Content: content,
			},
		},
	}
	if serviceType != "AB" {
		return converted, nil
	}

	previewMedias, parseErr := parseAlbumPreviewMedias(data.ContentMetadata)
	if parseErr != nil {
		h.Log.Warn().
			Err(parseErr).
			Str("msg_id", data.ID).
			Msg("Failed to parse LINE album preview media metadata")
	}
	if len(previewMedias) == 0 {
		return converted, nil
	}
	if h.NewClient == nil || intent == nil || portal == nil {
		return nil, errors.New("album preview conversion requires LINE and Matrix media clients")
	}

	client := h.NewClient()
	for index, media := range previewMedias {
		imageData, err := h.downloadOBSResource(ctx, client, media.Service, media.SID, media.OID)
		if newClient, ok := h.tryRecoverClient(ctx, err); ok {
			client = newClient
			imageData, err = h.downloadOBSResource(ctx, client, media.Service, media.SID, media.OID)
		}
		if errors.Is(err, line.ErrOBSObjectNotFound) {
			h.Log.Warn().
				Str("msg_id", data.ID).
				Str("media_oid", media.OID).
				Msg("LINE album preview image expired before it could be bridged")
			continue
		} else if err != nil {
			return nil, fmt.Errorf(
				"%w: failed to download LINE album preview %q: %w",
				bridgev2.ErrIgnoringRemoteEvent,
				media.OID,
				err,
			)
		}

		mimeType, extension := albumPreviewImageType(imageData)
		if mimeType == "" {
			h.Log.Warn().
				Str("msg_id", data.ID).
				Str("media_oid", media.OID).
				Msg("Ignoring LINE album preview with unsupported image data")
			continue
		}
		fileName := fmt.Sprintf("album-image-%d.%s", index+1, extension)
		mxc, file, err := intent.UploadMedia(ctx, portal.MXID, imageData, fileName, mimeType)
		if err != nil {
			return nil, fmt.Errorf("failed to upload LINE album preview to Matrix: %w", err)
		}

		converted.Parts = append(converted.Parts, &bridgev2.ConvertedMessagePart{
			ID:   networkid.PartID(fmt.Sprintf("album-image-%d", index+1)),
			Type: event.EventMessage,
			Content: &event.MessageEventContent{
				MsgType: event.MsgImage,
				Body:    fileName,
				URL:     mxc,
				File:    file,
				Info: &event.FileInfo{
					MimeType: mimeType,
					Size:     len(imageData),
				},
				RelatesTo: relatesTo,
			},
		})
	}
	return converted, nil
}

func parseAlbumPreviewMedias(metadata map[string]string) ([]postPreviewMedia, error) {
	if metadata == nil {
		return nil, nil
	}

	var parsed []postPreviewMedia
	var parseErr error
	if raw := strings.TrimSpace(metadata["previewMedias"]); raw != "" {
		parseErr = json.Unmarshal([]byte(raw), &parsed)
		if parseErr != nil {
			parsed = nil
		}
	}

	seen := make(map[string]struct{}, len(parsed))
	medias := make([]postPreviewMedia, 0, len(parsed))
	for _, media := range parsed {
		media.Service = strings.ToLower(strings.TrimSpace(media.Service))
		media.SID = strings.ToLower(strings.TrimSpace(media.SID))
		media.OID = strings.TrimSpace(media.OID)
		media.MediaType = strings.ToUpper(strings.TrimSpace(media.MediaType))
		if media.Service != "album" || media.SID != "a" || media.OID == "" || media.MediaType != "I" {
			continue
		}
		if _, duplicate := seen[media.OID]; duplicate {
			continue
		}
		seen[media.OID] = struct{}{}
		medias = append(medias, media)
	}

	// LINE duplicates the first preview in the top-level metadata. Only use it
	// when previewMedias was absent, malformed, or had no supported images.
	if len(medias) == 0 {
		oid := strings.TrimSpace(metadata["mediaOid"])
		mediaType := strings.ToUpper(strings.TrimSpace(metadata["mediaType"]))
		if oid != "" && mediaType == "I" {
			medias = append(medias, postPreviewMedia{
				Service:   "album",
				SID:       "a",
				OID:       oid,
				MediaType: mediaType,
			})
		}
	}
	return medias, parseErr
}

func albumPreviewImageType(data []byte) (mimeType, extension string) {
	switch http.DetectContentType(data) {
	case "image/jpeg":
		return "image/jpeg", "jpg"
	case "image/png":
		return "image/png", "png"
	case "image/gif":
		return "image/gif", "gif"
	case "image/webp":
		return "image/webp", "webp"
	default:
		return "", ""
	}
}
