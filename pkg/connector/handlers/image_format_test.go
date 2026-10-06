package handlers

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/gif"
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

type imageTestTransport func(*http.Request) (*http.Response, error)

func (f imageTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type incomingImageMatrix struct {
	bridgev2.MatrixAPI
	data       []byte
	name, mime string
}

func (m *incomingImageMatrix) UploadMedia(_ context.Context, _ id.RoomID, data []byte, name, mime string) (id.ContentURIString, *event.EncryptedFileInfo, error) {
	m.data, m.name, m.mime = bytes.Clone(data), name, mime
	return "mxc://test/original", nil, nil
}

func TestIncomingImageKeepsOriginalFormat(t *testing.T) {
	var pngData, gifData bytes.Buffer
	png.Encode(&pngData, image.NewNRGBA(image.Rect(0, 0, 4, 3)))
	first := image.NewPaletted(image.Rect(0, 0, 4, 3), color.Palette{color.Transparent, color.White})
	second := image.NewPaletted(first.Bounds(), first.Palette)
	second.SetColorIndex(1, 1, 1)
	gif.EncodeAll(&gifData, &gif.GIF{Image: []*image.Paletted{first, second}, Delay: []int{10, 20}, LoopCount: 0})
	for _, tc := range []struct {
		ext  string
		data []byte
	}{{"png", pngData.Bytes()}, {"gif", gifData.Bytes()}} {
		t.Run(tc.ext, func(t *testing.T) {
			h := &Handler{HTTPClient: &http.Client{Transport: imageTestTransport(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(tc.data))}, nil
			})}}
			h.NewClient = func() *line.Client {
				c := line.NewClient("test")
				c.HTTPClient = h.HTTPClient
				c.OBSClient = h.HTTPClient
				return c
			}
			matrix := &incomingImageMatrix{}
			converted, err := h.ConvertImage(t.Context(), &bridgev2.Portal{Portal: &database.Portal{MXID: "!room:test"}}, matrix, line.Message{ID: "original", ContentMetadata: map[string]string{"DOWNLOAD_URL": "/r/test/original"}}, "", nil)
			if err != nil {
				t.Fatal(err)
			}
			content := converted.Parts[0].Content
			if matrix.name != "image."+tc.ext || matrix.mime != "image/"+tc.ext || !bytes.Equal(matrix.data, tc.data) {
				t.Fatalf("upload changed format: %s %s", matrix.name, matrix.mime)
			}
			if content.Info.MimeType != matrix.mime || content.Body != matrix.name || content.Info.Width != 4 || content.Info.Height != 3 || content.Info.Size != len(tc.data) {
				t.Fatalf("wrong content: %+v", content)
			}
		})
	}
}

func TestDirectImageFormat(t *testing.T) {
	for _, tc := range []struct {
		metadata   map[string]string
		name, mime string
	}{
		{map[string]string{"MEDIA_CONTENT_INFO": `{"category":"original","extension":"gif","animated":true}`, "FILE_NAME": "wrong.jpg"}, "image.gif", "image/gif"},
		{map[string]string{"MEDIA_CONTENT_INFO": `{"extension":"png"}`}, "image.png", "image/png"},
		{map[string]string{"FILE_NAME": "transparent.art.PNG"}, "image.png", "image/png"},
		{nil, "image.jpg", "image/jpeg"},
	} {
		name, mime, err := directMediaContent("image", "", tc.metadata)
		if err != nil || name != tc.name || mime != tc.mime {
			t.Fatalf("got %q %q %v", name, mime, err)
		}
	}
}
