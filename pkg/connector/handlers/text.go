package handlers

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"

	"github.com/beeper/line/pkg/line"
)

// ConvertText converts a LINE text message to a Matrix text message, including link previews.
func (h *Handler) ConvertText(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, unwrappedText string, relatesTo *event.RelatesTo) (*bridgev2.ConvertedMessage, error) {
	client := h.NewClient()

	content := &event.MessageEventContent{
		MsgType:   event.MsgText,
		Body:      unwrappedText,
		RelatesTo: relatesTo,
	}

	urlRegex := regexp.MustCompile(`(https?://)?([a-zA-Z0-9.-]+\.[a-zA-Z]{2,})(/[^\s]*)?`)
	if match := urlRegex.FindString(unwrappedText); match != "" {
		match = strings.TrimRight(match, ".,;:!?")
		requestURL := match
		if !strings.HasPrefix(match, "http") {
			requestURL = "https://" + match
		}
		if info, err := client.GetPageInfo(requestURL); err == nil {
			preview := &event.BeeperLinkPreview{
				MatchedURL: match,
				LinkPreview: event.LinkPreview{
					Title:        info.Title,
					Description:  info.Summary,
					CanonicalURL: info.Domain,
				},
			}
			if info.Image != "" && info.Obs.CDN != "" {
				if err := h.uploadPreviewImage(ctx, client, portal, intent, info.Obs.CDN+info.Image, preview); err != nil {
					h.Log.Warn().Msg("Failed to bridge link preview image")
				}
			}
			content.BeeperLinkPreviews = []*event.BeeperLinkPreview{preview}
		}
	}

	return &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{
			{
				Type:    event.EventMessage,
				Content: content,
			},
		},
	}, nil
}

func (h *Handler) uploadPreviewImage(ctx context.Context, client *line.Client, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, imageURL string, preview *event.BeeperLinkPreview) error {
	var data []byte
	var err error
	if h.DownloadPublicMedia != nil {
		data, err = h.DownloadPublicMedia(ctx, client, imageURL)
	} else {
		data, err = client.DownloadOBSPublicResource(ctx, imageURL)
	}
	if err != nil {
		return err
	}
	mimeType := http.DetectContentType(data)
	if !strings.HasPrefix(mimeType, "image/") {
		return fmt.Errorf("unexpected content type %q", mimeType)
	}
	preview.ImageURL, preview.ImageEncryption, err = intent.UploadMedia(ctx, portal.MXID, data, "", mimeType)
	if err != nil {
		return err
	}
	preview.ImageSize = event.IntOrString(len(data))
	preview.ImageType = mimeType
	return nil
}
