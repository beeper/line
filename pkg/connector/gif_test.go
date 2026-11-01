package connector

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"testing"

	"maunium.net/go/mautrix/event"
)

func testGIF(t *testing.T, width, height, frames int) []byte {
	t.Helper()
	palette := color.Palette{color.Transparent, color.RGBA{R: 6, G: 166, B: 74, A: 255}}
	anim := &gif.GIF{}
	for i := 0; i < frames; i++ {
		frame := image.NewPaletted(image.Rect(0, 0, width, height), palette)
		frame.SetColorIndex(i%width, 0, 1)
		anim.Image = append(anim.Image, frame)
		anim.Delay = append(anim.Delay, 10)
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, anim); err != nil {
		t.Fatalf("encode test GIF: %v", err)
	}
	return buf.Bytes()
}

func decodeContentInfo(t *testing.T, metadata map[string]string) map[string]any {
	t.Helper()
	var info map[string]any
	if err := json.Unmarshal([]byte(metadata["MEDIA_CONTENT_INFO"]), &info); err != nil {
		t.Fatalf("MEDIA_CONTENT_INFO is not JSON: %v", err)
	}
	return info
}

// The LINE Chrome Extension sends a GIF's real pixel size and original file
// size in MEDIA_CONTENT_INFO (capture D2, D8, D9).
func TestImageMediaMetadataEncryptedMatchesChrome(t *testing.T) {
	data := testGIF(t, 960, 360, 3)
	metadata := imageMediaMetadata(data, "gif", false)

	info := decodeContentInfo(t, metadata)
	if info["width"] != float64(960) || info["height"] != float64(360) {
		t.Errorf("width/height = %v/%v, want 960/360", info["width"], info["height"])
	}
	if info["fileSize"] != float64(len(data)) {
		t.Errorf("fileSize = %v, want the original size %d", info["fileSize"], len(data))
	}
	if info["animated"] != true || info["extension"] != "gif" || info["category"] != "original" {
		t.Errorf("unexpected MEDIA_CONTENT_INFO %v", info)
	}
	if metadata["FILE_SIZE"] != itoa(len(data)) {
		t.Errorf("FILE_SIZE = %q, want the original size %d", metadata["FILE_SIZE"], len(data))
	}
	for _, key := range []string{"FILE_NAME", "contentType"} {
		if _, ok := metadata[key]; ok {
			t.Errorf("metadata has %s, which the Chrome extension does not send", key)
		}
	}
}

// In plain chats the Chrome extension sends MEDIA_CONTENT_INFO only (capture D7).
func TestImageMediaMetadataPlainIsContentInfoOnly(t *testing.T) {
	metadata := imageMediaMetadata(testGIF(t, 480, 480, 2), "gif", true)
	if len(metadata) != 1 || metadata["MEDIA_CONTENT_INFO"] == "" {
		t.Fatalf("plain metadata = %v, want MEDIA_CONTENT_INFO only", metadata)
	}
}

// A single-frame GIF is sent with animated: false (capture D3); non-GIF images
// carry no animated flag.
func TestOriginalImageInfoAnimatedFlag(t *testing.T) {
	still := originalImageInfo(testGIF(t, 800, 800, 1), "gif")
	if still.Animated == nil || *still.Animated {
		t.Errorf("single-frame GIF animated = %v, want false", still.Animated)
	}
	moving := originalImageInfo(testGIF(t, 32, 32, 4), "gif")
	if moving.Animated == nil || !*moving.Animated {
		t.Errorf("multi-frame GIF animated = %v, want true", moving.Animated)
	}
	if moving.Width != 32 || moving.Height != 32 {
		t.Errorf("tiny GIF size = %dx%d, want 32x32", moving.Width, moving.Height)
	}
	if jpg := originalImageInfo([]byte("not an image"), "jpg"); jpg.Animated != nil {
		t.Errorf("JPEG has animated flag %v, want none", *jpg.Animated)
	}
}

// Thumbnails keep the aspect ratio with the longest side at most 384 and are
// never scaled up (capture D8: 384x144, D9: 144x384, E6: 32x32).
func TestGenerateThumbnailMatchesChromeSizes(t *testing.T) {
	cases := []struct{ w, h, wantW, wantH int }{
		{960, 360, 384, 144},
		{360, 960, 144, 384},
		{480, 480, 384, 384},
		{32, 32, 32, 32},
	}
	for _, c := range cases {
		_, w, h, err := generateThumbnail(testGIF(t, c.w, c.h, 2))
		if err != nil {
			t.Fatalf("%dx%d: %v", c.w, c.h, err)
		}
		if w != c.wantW || h != c.wantH {
			t.Errorf("%dx%d thumbnail = %dx%d, want %dx%d", c.w, c.h, w, h, c.wantW, c.wantH)
		}
	}
}

// Transparent areas become white, not black, in the JPEG thumbnail LINE shows
// before a GIF plays.
func TestGenerateThumbnailTransparentIsWhite(t *testing.T) {
	thumb, _, _, err := generateThumbnail(testGIF(t, 64, 64, 2))
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(thumb))
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, _ := img.At(32, 40).RGBA()
	if r>>8 < 240 || g>>8 < 240 || b>>8 < 240 {
		t.Errorf("transparent pixel = (%d,%d,%d), want white", r>>8, g>>8, b>>8)
	}
}

// GIF pickers send fi.mau.gif; LINE takes GIFs up to 20 MB (capture E1–E3).
func TestCapabilitiesAdvertiseGIFs(t *testing.T) {
	caps := (&LineClient{}).GetCapabilities(t.Context(), nil)
	gifCaps, ok := caps.File[event.CapMsgGIF]
	if !ok {
		t.Fatal("no fi.mau.gif capability")
	}
	if gifCaps.MaxSize != maxGIFSize {
		t.Errorf("GIF MaxSize = %d, want %d", gifCaps.MaxSize, maxGIFSize)
	}
	if gifCaps.MimeTypes["image/gif"] != event.CapLevelFullySupported {
		t.Errorf("image/gif support = %v, want fully supported", gifCaps.MimeTypes["image/gif"])
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
