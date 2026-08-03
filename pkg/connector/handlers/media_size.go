package handlers

import (
	"strconv"
	"strings"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
)

const (
	BeeperMaxFileSize          = 100 * 1024 * 1024
	encryptedMediaSizeOverhead = 32
	oversizedFileBody          = "This file exceeds Beeper's 100MB file size limit. Open LINE to view it."
)

func (h *Handler) oversizedMediaNoticeFromMetadata(metadata map[string]string, relatesTo *event.RelatesTo) *bridgev2.ConvertedMessage {
	size, err := strconv.ParseInt(strings.TrimSpace(metadata["FILE_SIZE"]), 10, 64)
	// E2EE media stored in OBS may include a 32-byte HMAC in FILE_SIZE. Keep
	// borderline values on the authoritative post-decryption size-check path.
	if err != nil || size <= BeeperMaxFileSize+encryptedMediaSizeOverhead {
		return nil
	}
	return h.oversizedMediaNotice(size, "metadata", relatesTo)
}

func (h *Handler) oversizedMediaNotice(size int64, sizeSource string, relatesTo *event.RelatesTo) *bridgev2.ConvertedMessage {
	if size <= BeeperMaxFileSize {
		return nil
	}
	h.Log.Warn().
		Int64("size_bytes", size).
		Int("limit_bytes", BeeperMaxFileSize).
		Str("size_source", sizeSource).
		Msg("Skipping oversized LINE media upload")
	return &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{
			{
				Type: event.EventMessage,
				Content: &event.MessageEventContent{
					MsgType:   event.MsgNotice,
					Body:      oversizedFileBody,
					RelatesTo: relatesTo,
				},
			},
		},
	}
}
