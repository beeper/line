package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"

	"github.com/highesttt/matrix-line-messenger/pkg/line"
)

// Handler provides dependencies needed by content type conversion functions.
type Handler struct {
	Log        zerolog.Logger
	HTTPClient *http.Client

	// RecoverToken attempts to restore a valid session by refreshing or re-logging in.
	RecoverToken      func(ctx context.Context) error
	ShouldRecover     func(ctx context.Context, err error) bool
	IsRefreshRequired func(err error) bool
	IsLoggedOut       func(err error) bool
	HandleLoggedOut   func(ctx context.Context, err error)

	// NewClient creates a new LINE API client with the current access token.
	NewClient func() *line.Client

	// DecryptMedia decrypts E2EE encrypted media data using the given key material.
	DecryptMedia func(data []byte, keyMaterial string) ([]byte, error)
}

func obsTalkMetaMessageID(messageID string, isPlainMedia bool) string {
	if isPlainMedia {
		return ""
	}
	return messageID
}

func mediaDownloadFailure(kind string, err error, relatesTo *event.RelatesTo) (*bridgev2.ConvertedMessage, error) {
	if !errors.Is(err, line.ErrOBSObjectNotFound) {
		// Keep ambiguous OBS failures retryable. Returning ErrIgnoringRemoteEvent
		// prevents bridgev2 from posting a generic error notice, while omitting a
		// converted message means the remote event isn't stored as successfully
		// bridged and can be retried by a later backfill.
		return nil, fmt.Errorf("%w: failed to download %s from LINE OBS: %w", bridgev2.ErrIgnoringRemoteEvent, strings.ToLower(kind), err)
	}
	return &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{
			{
				Type: event.EventMessage,
				Content: &event.MessageEventContent{
					MsgType:   event.MsgNotice,
					Body:      fmt.Sprintf("[%s unavailable — LINE media expired before it could be bridged]", kind),
					RelatesTo: relatesTo,
				},
			},
		},
	}, nil
}

// tryRecoverClient attempts token recovery on auth errors and returns a fresh client.
// Returns (newClient, true) on success, (nil, false) if recovery was not needed or failed.
func (h *Handler) tryRecoverClient(ctx context.Context, err error) (*line.Client, bool) {
	if err == nil {
		return nil, false
	}
	if h.IsLoggedOut(err) {
		if h.HandleLoggedOut != nil {
			h.HandleLoggedOut(ctx, err)
		}
		return nil, false
	}
	if h.ShouldRecover != nil {
		if !h.ShouldRecover(ctx, err) {
			return nil, false
		}
	} else if !line.IsUnauthorizedStatus(err) && !h.IsRefreshRequired(err) {
		return nil, false
	}
	if errRecover := h.RecoverToken(ctx); errRecover != nil {
		h.Log.Warn().Err(errRecover).Msg("Failed to recover token for media download")
		return nil, false
	}
	return h.NewClient(), true
}
