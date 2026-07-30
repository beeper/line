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

	// RecoverClient classifies auth errors using the client that made the failed
	// request, and returns a client that may be used for one retry.
	RecoverClient func(ctx context.Context, failedClient *line.Client, err error) (*line.Client, error)

	// NewClient creates a new LINE API client with the current access token.
	NewClient func() *line.Client

	// DownloadOBSResource overrides non-talk OBS downloads in tests.
	DownloadOBSResource func(ctx context.Context, client *line.Client, service, sid, oid string) ([]byte, error)

	// DownloadAlbumPreview overrides album thumbnail downloads in tests.
	DownloadAlbumPreview func(ctx context.Context, client *line.Client, oid, chatID, albumID string) ([]byte, error)

	// DecryptMedia decrypts E2EE encrypted media data using the given key material.
	DecryptMedia func(data []byte, keyMaterial string) ([]byte, error)
}

func (h *Handler) downloadAlbumPreview(ctx context.Context, client *line.Client, oid, chatID, albumID string) ([]byte, error) {
	if h.DownloadAlbumPreview != nil {
		return h.DownloadAlbumPreview(ctx, client, oid, chatID, albumID)
	}
	return client.DownloadAlbumPreview(ctx, oid, chatID, albumID)
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
func (h *Handler) tryRecoverClient(ctx context.Context, failedClient *line.Client, err error) (*line.Client, bool) {
	if err == nil || h.RecoverClient == nil {
		return nil, false
	}
	recoveredClient, errRecover := h.RecoverClient(ctx, failedClient, err)
	if errRecover != nil {
		h.Log.Warn().Err(errRecover).Msg("Failed to recover token for media download")
		return nil, false
	}
	return recoveredClient, recoveredClient != nil
}

// handleFinalAuthError applies forced-logout handling to the one allowed retry
// without requesting another retry. Source-aware recovery will ignore the error
// if another token rotation already made the retry client stale.
func (h *Handler) handleFinalAuthError(ctx context.Context, failedClient *line.Client, err error) {
	if !line.IsLoggedOut(err) || h.RecoverClient == nil {
		return
	}
	if _, errRecover := h.RecoverClient(ctx, failedClient, err); errRecover != nil {
		h.Log.Warn().Err(errRecover).Msg("Failed to handle LINE logout after media retry")
	}
}
