package handlers

import (
	"context"
	"fmt"
	"html"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/beeper/line/pkg/line"
)

// ConvertContact converts a LINE contact share (contentType 13) to a Matrix notice
// that links the shared user's ghost when one is known.
func (h *Handler) ConvertContact(data line.Message, ghostMXID id.UserID, relatesTo *event.RelatesTo) (*bridgev2.ConvertedMessage, error) {
	displayName := data.ContentMetadata["displayName"]
	if displayName == "" {
		displayName = "Unknown"
	}
	content := &event.MessageEventContent{
		MsgType:   event.MsgNotice,
		Body:      fmt.Sprintf("LINE contact shared: %s", displayName),
		Mentions:  &event.Mentions{},
		RelatesTo: relatesTo,
	}
	if ghostMXID != "" {
		content.Format = event.FormatHTML
		content.FormattedBody = fmt.Sprintf(`LINE contact shared: <a href="%s">%s</a>`, html.EscapeString(ghostMXID.URI().MatrixToURL()), html.EscapeString(displayName))
	}
	return &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{{Type: event.EventMessage, Content: content}},
	}, nil
}

// ConvertDeviceContact converts a device/phone contact shared via ORGCONTP metadata
// (contentType 0 with vCard data) to a Matrix file or notice.
func (h *Handler) ConvertDeviceContact(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, data line.Message, unwrappedText string, relatesTo *event.RelatesTo) (*bridgev2.ConvertedMessage, error) {
	displayName := data.ContentMetadata["displayName"]
	if displayName == "" {
		displayName = "Unknown"
	}
	vcard := data.ContentMetadata["vCard"]
	if vcard == "" {
		// No vCard data - fall back to text notice
		body := fmt.Sprintf("Shared contact: %s", displayName)
		if unwrappedText != "" {
			body += "\n" + unwrappedText
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

	fileName := displayName + ".vcf"
	vcardBytes := []byte(vcard)
	mxc, file, err := intent.UploadMedia(ctx, portal.MXID, vcardBytes, fileName, "text/vcard")
	if err != nil {
		h.Log.Warn().Err(err).Msg("Failed to upload device contact vCard")
		return &bridgev2.ConvertedMessage{
			Parts: []*bridgev2.ConvertedMessagePart{
				{
					Type: event.EventMessage,
					Content: &event.MessageEventContent{
						MsgType:   event.MsgNotice,
						Body:      fmt.Sprintf("Shared contact: %s", displayName),
						RelatesTo: relatesTo,
					},
				},
			},
		}, nil
	}

	return &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{
			{
				Type: event.EventMessage,
				Content: &event.MessageEventContent{
					MsgType:  event.MsgFile,
					Body:     fileName,
					URL:      mxc,
					File:     file,
					FileName: fileName,
					Info: &event.FileInfo{
						MimeType: "text/vcard",
						Size:     len(vcardBytes),
					},
					RelatesTo: relatesTo,
				},
			},
		},
	}, nil
}
