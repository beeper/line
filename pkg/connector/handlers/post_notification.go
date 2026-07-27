package handlers

import (
	"strings"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"

	"github.com/highesttt/matrix-line-messenger/pkg/line"
)

// ConvertPostNotification converts a LINE note, album, or unknown post
// notification into a readable Matrix notice.
func (*Handler) ConvertPostNotification(data line.Message, relatesTo *event.RelatesTo) (*bridgev2.ConvertedMessage, error) {
	serviceType := strings.ToUpper(strings.TrimSpace(data.ContentMetadata["serviceType"]))
	preview := strings.TrimSpace(data.ContentMetadata["text"])
	albumName := strings.TrimSpace(data.ContentMetadata["albumName"])
	postURL := strings.TrimSpace(data.ContentMetadata["postEndUrl"])

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
	if postURL != "" {
		body.WriteString("\n\nOpen in LINE: ")
		body.WriteString(postURL)
	} else {
		body.WriteString("\n\nOpen LINE for full details.")
	}

	return &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{
			{
				Type: event.EventMessage,
				Content: &event.MessageEventContent{
					MsgType:   event.MsgNotice,
					Body:      body.String(),
					RelatesTo: relatesTo,
				},
			},
		},
	}, nil
}
