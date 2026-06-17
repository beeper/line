package handlers

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func TestLineImageMediaInfo(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		fileName string
		mimeType string
		width    int
		height   int
	}{
		{
			name:     "jpeg",
			data:     testJPEG(t, 23, 17),
			fileName: "image.jpg",
			mimeType: "image/jpeg",
			width:    23,
			height:   17,
		},
		{
			name:     "png",
			data:     testPNG(t, 19, 11),
			fileName: "image.png",
			mimeType: "image/png",
			width:    19,
			height:   11,
		},
		{
			name:     "heic",
			data:     []byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00"),
			fileName: "image.heic",
			mimeType: "image/heic",
		},
		{
			name:     "fallback",
			data:     []byte("not an image"),
			fileName: "image.jpg",
			mimeType: "image/jpeg",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fileName, mimeType, info := lineImageMediaInfo(tt.data)

			if fileName != tt.fileName {
				t.Fatalf("unexpected file name: got %q, want %q", fileName, tt.fileName)
			}
			if mimeType != tt.mimeType {
				t.Fatalf("unexpected mime type: got %q, want %q", mimeType, tt.mimeType)
			}
			if info == nil {
				t.Fatal("expected file info")
			}
			if info.MimeType != tt.mimeType {
				t.Fatalf("unexpected info mime type: got %q, want %q", info.MimeType, tt.mimeType)
			}
			if info.Size != len(tt.data) {
				t.Fatalf("unexpected info size: got %d, want %d", info.Size, len(tt.data))
			}
			if info.Width != tt.width || info.Height != tt.height {
				t.Fatalf("unexpected dimensions: got %dx%d, want %dx%d", info.Width, info.Height, tt.width, tt.height)
			}
		})
	}
}

func TestLineImageEventContentIncludesFileInfo(t *testing.T) {
	relatesTo := &event.RelatesTo{}
	info := &event.FileInfo{MimeType: "image/jpeg", Size: 123, Width: 10, Height: 12}

	content := lineImageEventContent(id.ContentURIString("mxc://example/image"), nil, "image.jpg", info, relatesTo)

	if content.MsgType != event.MsgImage {
		t.Fatalf("unexpected msgtype: got %q, want %q", content.MsgType, event.MsgImage)
	}
	if content.Body != "image.jpg" {
		t.Fatalf("unexpected body: got %q", content.Body)
	}
	if content.Info != info {
		t.Fatal("expected image file info to be attached to content")
	}
	if content.RelatesTo != relatesTo {
		t.Fatal("expected relates_to to be preserved")
	}
}

func testJPEG(t *testing.T, width, height int) []byte {
	t.Helper()

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, testImage(width, height), nil); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.Bytes()
}

func testPNG(t *testing.T, width, height int) []byte {
	t.Helper()

	var buf bytes.Buffer
	if err := png.Encode(&buf, testImage(width, height)); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func testImage(width, height int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 0x80, A: 0xff})
		}
	}
	return img
}
