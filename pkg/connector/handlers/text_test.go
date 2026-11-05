package handlers

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/id"

	"github.com/beeper/line/pkg/line"
)

type linkPreviewRoundTripper func(*http.Request) (*http.Response, error)

func (f linkPreviewRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestConvertTextUploadsOnlyImageLinkPreviews(t *testing.T) {
	const pageInfo = `{"code":0,"result":{"domain":"example.com","title":"Title","summary":"Summary","image":"/preview.png","obs":{"cdn":"https://cdn.example/"}}}`
	png := []byte("\x89PNG\r\n\x1a\npreview")
	for _, tc := range []struct {
		name     string
		image    []byte
		wantURL  id.ContentURIString
		wantType string
	}{
		{"image uploaded", png, "mxc://example/", "image/png"},
		{"non-image omitted", []byte("not an image"), "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := line.NewClient("")
			client.HTTPClient.Transport = linkPreviewRoundTripper(func(req *http.Request) (*http.Response, error) {
				if !strings.Contains(req.URL.Path, "/pageinfo/get") {
					t.Fatalf("unexpected LINE request: %s", req.URL)
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(pageInfo))}, nil
			})
			matrix := &postNotificationTestMatrix{}
			uploadsBefore := matrix.uploadCount()
			var downloadedURL string
			h := &Handler{
				Log:       zerolog.New(io.Discard),
				NewClient: func() *line.Client { return client },
				DownloadPublicMedia: func(_ context.Context, _ *line.Client, resource string) ([]byte, error) {
					downloadedURL = resource
					return tc.image, nil
				},
			}
			portal := &bridgev2.Portal{Portal: &database.Portal{MXID: "!preview:example.com"}}
			converted, err := h.ConvertText(context.Background(), portal, matrix, "See https://example.com/page", nil)
			if err != nil || converted == nil || len(converted.Parts) != 1 {
				t.Fatalf("ConvertText = %#v, %v", converted, err)
			}
			content := converted.Parts[0].Content
			if content.Body != "See https://example.com/page" || len(content.BeeperLinkPreviews) != 1 {
				t.Fatalf("text content = %#v", content)
			}
			preview := content.BeeperLinkPreviews[0]
			if preview.ImageURL != tc.wantURL || preview.ImageType != tc.wantType {
				t.Fatalf("preview image = url %q type %q", preview.ImageURL, preview.ImageType)
			}
			if downloadedURL != "https://cdn.example//preview.png" {
				t.Fatalf("download URL = %q", downloadedURL)
			}
			wantUploads := uploadsBefore
			if tc.wantURL != "" {
				wantUploads++
			}
			if matrix.uploadCount() != wantUploads {
				t.Fatalf("upload count = %d, want %d", matrix.uploadCount(), wantUploads)
			}
		})
	}
}
