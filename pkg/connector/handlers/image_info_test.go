package handlers

import "testing"

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
