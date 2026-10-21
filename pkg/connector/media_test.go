package connector

import (
	"bytes"
	"testing"
)

func TestStripJPEGExif(t *testing.T) {
	segment := func(marker byte, payload string) []byte {
		n := len(payload) + 2
		return append([]byte{0xFF, marker, byte(n >> 8), byte(n)}, payload...)
	}
	soi := []byte{0xFF, 0xD8}
	jfif := segment(0xE0, "JFIF\x00")
	exif := segment(0xE1, "Exif\x00\x00MM\x00*GPS")
	xmp := segment(0xE1, "http://ns.adobe.com/xap/1.0/\x00")
	icc := segment(0xE2, "ICC_PROFILE\x00")
	scan := []byte{0xFF, 0xDA, 0x00, 0x02, 0x12, 0x34, 0xFF, 0xD9}

	join := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
	input := join(soi, jfif, exif, xmp, icc, scan)
	want := join(soi, jfif, xmp, icc, scan)
	if got := stripJPEGExif(input); !bytes.Equal(got, want) {
		t.Fatalf("stripJPEGExif() = %x, want %x", got, want)
	}

	truncated := join(soi, segment(0xE1, "Exif\x00\x00")[:5])
	if got := stripJPEGExif(truncated); !bytes.Equal(got, truncated) {
		t.Fatalf("malformed input should be returned unchanged")
	}
}
