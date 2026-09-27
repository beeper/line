package handlers

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"io"
	"net/http"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/highesttt/matrix-line-messenger/pkg/line"
)

type incomingStickerTransport func(*http.Request) (*http.Response, error)

func (f incomingStickerTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type incomingStickerMatrix struct{ bridgev2.MatrixAPI }

func (m incomingStickerMatrix) UploadMedia(context.Context, id.RoomID, []byte, string, string) (id.ContentURIString, *event.EncryptedFileInfo, error) {
	return "mxc://test/sticker", nil, nil
}

func TestIncomingStickerUsesNativeMatrixEvent(t *testing.T) {
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 4, 3))); err != nil {
		t.Fatal(err)
	}
	h := &Handler{HTTPClient: &http.Client{Transport: incomingStickerTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/stickershop/v1/sticker/100/android/sticker.png" {
			t.Fatal(r.URL)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"image/png"}},
			Body:       io.NopCloser(bytes.NewReader(data.Bytes())),
		}, nil
	})}}
	portal := &bridgev2.Portal{Portal: &database.Portal{MXID: "!room:test"}}
	msg := line.Message{ContentMetadata: map[string]string{"STKID": "100", "STKTXT": "waving cat"}}
	converted, err := h.ConvertSticker(t.Context(), portal, incomingStickerMatrix{}, msg, nil)
	if err != nil {
		t.Fatal(err)
	}
	part := converted.Parts[0]
	content := part.Content
	if part.Type != event.EventSticker || content.MsgType != "" || content.Body != "waving cat" ||
		content.URL != "mxc://test/sticker" || content.Info.Width != 4 || content.Info.Height != 3 {
		t.Fatalf("incorrect sticker event: %+v", part)
	}
}
