package handlers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
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
				if err := h.uploadPreviewImage(ctx, portal, intent, info.Obs.CDN+info.Image, preview); err != nil {
					h.Log.Warn().Err(err).Str("url", requestURL).Msg("Failed to bridge link preview image")
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

func (h *Handler) uploadPreviewImage(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, imageURL string, preview *event.BeeperLinkPreview) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	if err != nil {
		return err
	}
	resp, err := h.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	mimeType := resp.Header.Get("Content-Type")
	if mimeType == "" {
		mimeType = http.DetectContentType(data)
	}
	preview.ImageURL, preview.ImageEncryption, err = intent.UploadMedia(ctx, portal.MXID, data, "", mimeType)
	if err != nil {
		return err
	}
	preview.ImageSize = event.IntOrString(len(data))
	preview.ImageType = mimeType
	return nil
}
