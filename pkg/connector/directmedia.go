package connector

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/mediaproxy"

	"github.com/highesttt/matrix-line-messenger/pkg/connector/handlers"
)

const (
	lineDirectMediaIDVersion   = 1
	lineDirectMediaIDMaxLength = 64
)

type MessageMetadata struct {
	DirectMedia *handlers.DirectMedia `json:"direct_media,omitempty"`
}

type lineDirectMediaID struct {
	UserLoginID networkid.UserLoginID
	MessageID   networkid.MessageID
}

var _ bridgev2.DirectMediableNetwork = (*LineConnector)(nil)

func (lc *LineConnector) SetUseDirectMedia() {
	lc.directMedia.Store(true)
}

func makeLineDirectMediaID(userLoginID networkid.UserLoginID, messageID networkid.MessageID) (networkid.MediaID, error) {
	login, message := []byte(userLoginID), []byte(messageID)
	if len(login) == 0 || len(login) > lineDirectMediaIDMaxLength || len(message) == 0 || len(message) > lineDirectMediaIDMaxLength {
		return nil, fmt.Errorf("invalid LINE direct media ID components")
	}
	mediaID := append([]byte{lineDirectMediaIDVersion, byte(len(login))}, login...)
	mediaID = append(mediaID, byte(len(message)))
	return append(mediaID, message...), nil
}

func parseLineDirectMediaID(mediaID networkid.MediaID) (*lineDirectMediaID, error) {
	if len(mediaID) < 4 || mediaID[0] != lineDirectMediaIDVersion {
		return nil, fmt.Errorf("invalid LINE direct media ID")
	}
	loginLength := int(mediaID[1])
	if loginLength == 0 || loginLength > lineDirectMediaIDMaxLength || len(mediaID) < loginLength+3 {
		return nil, fmt.Errorf("invalid LINE direct media login ID")
	}
	messageLength := int(mediaID[loginLength+2])
	if messageLength == 0 || messageLength > lineDirectMediaIDMaxLength || len(mediaID) != loginLength+messageLength+3 {
		return nil, fmt.Errorf("invalid LINE direct media message ID")
	}
	return &lineDirectMediaID{
		UserLoginID: networkid.UserLoginID(mediaID[2 : loginLength+2]),
		MessageID:   networkid.MessageID(mediaID[loginLength+3:]),
	}, nil
}

func (lc *LineConnector) Download(ctx context.Context, mediaID networkid.MediaID, _ map[string]string) (mediaproxy.GetMediaResponse, error) {
	parsed, err := parseLineDirectMediaID(mediaID)
	if err != nil {
		return nil, err
	}
	if lc.br == nil || lc.br.DB == nil {
		return nil, fmt.Errorf("LINE bridge database is unavailable")
	}
	login := lc.br.GetCachedUserLoginByID(parsed.UserLoginID)
	if login == nil || login.Client == nil || !login.Client.IsLoggedIn() {
		return nil, bridgev2.ErrNotLoggedIn
	}
	client, ok := login.Client.(*LineClient)
	if !ok {
		return nil, fmt.Errorf("unexpected LINE client type %T", login.Client)
	}
	message, err := lc.br.DB.Message.GetFirstPartByID(ctx, parsed.UserLoginID, parsed.MessageID)
	if err != nil {
		return nil, fmt.Errorf("failed to load LINE direct media message: %w", err)
	}
	if message == nil {
		return nil, fmt.Errorf("message not found")
	}
	metadata, ok := message.Metadata.(*MessageMetadata)
	if !ok || metadata.DirectMedia == nil {
		return nil, fmt.Errorf("message does not have LINE direct media metadata")
	}
	data, err := client.newMessageHandler().DownloadDirectMedia(ctx, string(parsed.MessageID), metadata.DirectMedia)
	if err != nil {
		return nil, err
	}
	if len(data) > handlers.BeeperMaxFileSize {
		return nil, fmt.Errorf("%w: LINE %s is %.2f MiB", bridgev2.ErrMediaTooLarge, metadata.DirectMedia.Kind, float64(len(data))/(1024*1024))
	}
	contentType := metadata.DirectMedia.ContentType
	if contentType == "" {
		contentType = http.DetectContentType(data)
	}
	return &mediaproxy.GetMediaResponseData{
		Reader: io.NopCloser(bytes.NewReader(data)), ContentType: contentType, ContentLength: int64(len(data)),
	}, nil
}
