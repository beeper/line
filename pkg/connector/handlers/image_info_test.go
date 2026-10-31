package handlers

import (
	"context"
	"testing"

	"maunium.net/go/mautrix/id"

	"github.com/beeper/line/pkg/line"
)

// With direct media, the event and the saved metadata (which sets the
// download's Content-Type) must keep the GIF type too.
func TestConvertImageDirectMediaKeepsGIFType(t *testing.T) {
	var saved *DirectMedia
	h := &Handler{GenerateDirectMediaURI: func(_ context.Context, _ string, dm *DirectMedia) (id.ContentURIString, any, error) {
		saved = dm
		return "mxc://example.com/gif", nil, nil
	}}
	converted, err := h.ConvertImage(context.Background(), nil, nil, line.Message{
		ID: "1",
		ContentMetadata: map[string]string{
			"OID":                "oid",
			"MEDIA_CONTENT_INFO": `{"width":480,"height":400,"animated":true,"category":"original","fileSize":3151649,"extension":"gif"}`,
		},
	}, "", nil)
	if err != nil || converted == nil {
		t.Fatalf("ConvertImage = %v, %v", converted, err)
	}
	content := converted.Parts[0].Content
	if content.Body != "image.gif" || content.Info.MimeType != "image/gif" || !content.Info.MauGIF {
		t.Errorf("event = %q %q mauGIF=%v, want image.gif image/gif fi.mau.gif", content.Body, content.Info.MimeType, content.Info.MauGIF)
	}
	if content.Info.Size != 3151649 {
		t.Errorf("event size = %d, want 3151649", content.Info.Size)
	}
	if saved == nil || saved.ContentType != "image/gif" || saved.FileName != "image.gif" {
		t.Errorf("saved metadata = %+v, want image/gif image.gif", saved)
	}
}

// GIFs from LINE arrive with MEDIA_CONTENT_INFO (capture F1–F3); Matrix should
// get them as animated GIFs, not as image.jpg (test C1, C6).
func TestLineImageInfoAnimatedGIF(t *testing.T) {
	metadata := map[string]string{
		"MEDIA_CONTENT_INFO": `{"width":480,"height":400,"animated":true,"attribute":{},"category":"original","fileSize":3151649,"extension":"gif"}`,
	}
	info, body := lineImageInfo(metadata, nil)
	if info.MimeType != "image/gif" || body != "image.gif" {
		t.Errorf("mime/body = %q/%q, want image/gif/image.gif", info.MimeType, body)
	}
	if !info.MauGIF {
		t.Error("animated GIF is not marked fi.mau.gif")
	}
	if info.Width != 480 || info.Height != 400 || info.Size != 3151649 {
		t.Errorf("info = %dx%d %d bytes, want 480x400 3151649", info.Width, info.Height, info.Size)
	}
}

func TestLineImageInfoStillGIFAndJPEG(t *testing.T) {
	still, _ := lineImageInfo(map[string]string{
		"MEDIA_CONTENT_INFO": `{"extension":"gif","animated":false,"width":800,"height":800}`,
	}, nil)
	if still.MimeType != "image/gif" || still.MauGIF {
		t.Errorf("still GIF = %q mauGIF=%v, want image/gif not fi.mau.gif", still.MimeType, still.MauGIF)
	}

	jpg, body := lineImageInfo(nil, nil)
	if jpg.MimeType != "image/jpeg" || body != "image.jpg" {
		t.Errorf("no metadata = %q/%q, want image/jpeg/image.jpg", jpg.MimeType, body)
	}
}

// The downloaded bytes decide the type when they are a known image.
func TestLineImageInfoDetectsFromData(t *testing.T) {
	gifHeader := []byte("GIF89a\x01\x00\x01\x00\x00\x00\x00;")
	info, body := lineImageInfo(map[string]string{
		"MEDIA_CONTENT_INFO": `{"extension":"jpg"}`,
	}, gifHeader)
	if info.MimeType != "image/gif" || body != "image.gif" {
		t.Errorf("detected = %q/%q, want image/gif/image.gif", info.MimeType, body)
	}
	if info.Size != len(gifHeader) {
		t.Errorf("size = %d, want %d", info.Size, len(gifHeader))
	}
}
