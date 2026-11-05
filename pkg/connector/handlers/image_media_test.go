package handlers

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/jpeg"
	"image/png"
	"testing"
)

func TestLineImageMediaInfo(t *testing.T) {
	for _, format := range []string{"jpeg", "png"} {
		t.Run(format, func(t *testing.T) {
			var buf bytes.Buffer
			img := image.NewRGBA(image.Rect(0, 0, 23, 17))
			var err error
			if format == "jpeg" {
				err = jpeg.Encode(&buf, img, nil)
			} else {
				err = png.Encode(&buf, img)
			}
			if err != nil {
				t.Fatal(err)
			}
			got := lineImageMediaInfo(buf.Bytes())
			if got.mimeType != "image/"+format || got.info.Width != 23 || got.info.Height != 17 || got.info.Size != buf.Len() || got.info.MimeType != got.mimeType || got.usedMimeFallback || got.decodeErr != nil {
				t.Fatalf("incorrect image metadata: %+v, info=%+v", got, got.info)
			}
		})
	}
}

func TestLineImageBrands(t *testing.T) {
	for _, tt := range []struct{ major, compatible, want string }{
		{"heic", "", "image/heic"}, {"heif", "", "image/heif"},
		{"mif1", "heic", "image/heic"}, {"mif1", "avif", "image/avif"},
		{"avif", "", "image/avif"},
	} {
		t.Run(tt.major+tt.compatible, func(t *testing.T) {
			data := make([]byte, 20)
			binary.BigEndian.PutUint32(data, 20)
			copy(data[4:], "ftyp")
			copy(data[8:], tt.major)
			copy(data[16:], tt.compatible)
			got := lineImageMediaInfo(data)
			if got.mimeType != tt.want || got.usedMimeFallback || got.decodeErr != nil {
				t.Fatalf("unexpected metadata: %+v", got)
			}
			for _, size := range []uint32{0, 12, 1024, 0xffffffff} {
				binary.BigEndian.PutUint32(data, size)
				if got := lineImageMediaInfo(data); !got.usedMimeFallback {
					t.Fatalf("accepted invalid ftyp size %d", size)
				}
			}
		})
	}
}

func TestLineImageExtensions(t *testing.T) {
	for mimeType, want := range map[string]string{
		"image/jpeg": "jpg", "image/png; charset=binary": "png", "image/gif": "gif",
		"image/webp": "webp", "image/heic": "heic", "image/heif": "heif", "image/avif": "avif",
		"image/bmp": "bmp", "image/tiff": "tiff", "image/svg+xml": "svg", "image/x-icon": "ico", "unknown": "jpg",
	} {
		if got := imageExtensionForMIME(mimeType); got != want {
			t.Errorf("%s: got %s, want %s", mimeType, got, want)
		}
	}
}
