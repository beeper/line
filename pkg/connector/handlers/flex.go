package handlers

import (
	"strings"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"

	"github.com/highesttt/matrix-line-messenger/pkg/line"
)

// ConvertFlex converts a LINE rich/Flex message to a Matrix fallback.
func (h *Handler) ConvertFlex(data line.Message, relatesTo *event.RelatesTo) (*bridgev2.ConvertedMessage, error) {
	preview := strings.TrimSpace(data.ContentMetadata["ALT_TEXT"])
	body := "LINE rich message. Check LINE for full details."
	if preview != "" {
		body += "\n\nPreview:\n" + preview
	}

	return &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{
			{
				Type: event.EventMessage,
				Content: &event.MessageEventContent{
					MsgType:   event.MsgNotice,
					Body:      body,
					RelatesTo: relatesTo,
				},
			},
		},
	}, nil
}
