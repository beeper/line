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
			name:     "heif",
			data:     []byte("\x00\x00\x00\x18ftypmif1\x00\x00\x00\x00"),
			fileName: "image.heif",
			mimeType: "image/heif",
		},
		{
			name:     "avif",
			data:     []byte("\x00\x00\x00\x18ftypavif\x00\x00\x00\x00"),
			fileName: "image.avif",
			mimeType: "image/avif",
		},
		{
			name:     "bmp",
			data:     []byte("BM line image data"),
			fileName: "image.bmp",
			mimeType: "image/bmp",
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
			media := lineImageMediaInfo(tt.data)

			if media.fileName != tt.fileName {
				t.Fatalf("unexpected file name: got %q, want %q", media.fileName, tt.fileName)
			}
			if media.mimeType != tt.mimeType {
				t.Fatalf("unexpected mime type: got %q, want %q", media.mimeType, tt.mimeType)
			}
			if media.info == nil {
				t.Fatal("expected file info")
			}
			if media.info.MimeType != tt.mimeType {
				t.Fatalf("unexpected info mime type: got %q, want %q", media.info.MimeType, tt.mimeType)
			}
			if media.info.Size != len(tt.data) {
				t.Fatalf("unexpected info size: got %d, want %d", media.info.Size, len(tt.data))
			}
			if media.info.Width != tt.width || media.info.Height != tt.height {
				t.Fatalf("unexpected dimensions: got %dx%d, want %dx%d", media.info.Width, media.info.Height, tt.width, tt.height)
			}
		})
	}
}

func TestLineImageMediaInfoFallbackState(t *testing.T) {
	tests := []struct {
		name             string
		data             []byte
		usedMimeFallback bool
		hasDecodeErr     bool
	}{
		{
			name: "jpeg",
			data: testJPEG(t, 2, 2),
		},
		{
			name:             "unknown",
			data:             []byte("not an image"),
			usedMimeFallback: true,
		},
		{
			name: "heif without decoder",
			data: []byte("\x00\x00\x00\x18ftypmif1\x00\x00\x00\x00"),
		},
		{
			name:         "truncated jpeg",
			data:         []byte{0xff, 0xd8, 0xff},
			hasDecodeErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			media := lineImageMediaInfo(tt.data)

			if media.usedMimeFallback != tt.usedMimeFallback {
				t.Fatalf("unexpected fallback state: got %t, want %t", media.usedMimeFallback, tt.usedMimeFallback)
			}
			if (media.decodeErr != nil) != tt.hasDecodeErr {
				t.Fatalf("unexpected decode error state: got %v, want error %t", media.decodeErr, tt.hasDecodeErr)
			}
		})
	}
}

func TestImageExtensionForMIME(t *testing.T) {
	tests := []struct {
		mimeType  string
		extension string
	}{
		{mimeType: "image/jpeg", extension: "jpg"},
		{mimeType: "image/png", extension: "png"},
		{mimeType: "image/gif", extension: "gif"},
		{mimeType: "image/webp", extension: "webp"},
		{mimeType: "image/heic", extension: "heic"},
		{mimeType: "image/heif", extension: "heif"},
		{mimeType: "image/avif", extension: "avif"},
		{mimeType: "image/bmp", extension: "bmp"},
		{mimeType: "image/tiff", extension: "tiff"},
		{mimeType: "image/x-icon", extension: "ico"},
		{mimeType: "image/vnd.microsoft.icon", extension: "ico"},
		{mimeType: "image/png; charset=binary", extension: "png"},
		{mimeType: "image/svg+xml", extension: "svg"},
		{mimeType: "unknown/unknown", extension: "jpg"},
	}

	for _, tt := range tests {
		t.Run(tt.mimeType, func(t *testing.T) {
			if extension := imageExtensionForMIME(tt.mimeType); extension != tt.extension {
				t.Fatalf("unexpected extension: got %q, want %q", extension, tt.extension)
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
