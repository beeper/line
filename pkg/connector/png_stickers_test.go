package connector

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"os/exec"
	"slices"
	"testing"
)

func testAPNG(t *testing.T) []byte {
	t.Helper()
	out := []byte("\x89PNG\r\n\x1a\n")
	chunk := func(kind string, data []byte) {
		out = binary.BigEndian.AppendUint32(out, uint32(len(data)))
		out = append(out, kind...)
		out = append(out, data...)
		crc := crc32.NewIEEE()
		crc.Write([]byte(kind))
		crc.Write(data)
		out = binary.BigEndian.AppendUint32(out, crc.Sum32())
	}
	var frames [2][]byte
	for i := range frames {
		img := image.NewNRGBA(image.Rect(0, 0, 8, 8))
		img.SetNRGBA(2+i, 2, color.NRGBA{R: 255, A: 255})
		var b bytes.Buffer
		png.Encode(&b, img)
		frames[i] = b.Bytes()
	}
	chunk("IHDR", frames[0][16:29])
	control := make([]byte, 8)
	binary.BigEndian.PutUint32(control, 2)
	chunk("acTL", control)
	seq := uint32(0)
	for i, data := range frames {
		fc := make([]byte, 26)
		binary.BigEndian.PutUint32(fc, seq)
		seq++
		binary.BigEndian.PutUint32(fc[4:], 8)
		binary.BigEndian.PutUint32(fc[8:], 8)
		binary.BigEndian.PutUint16(fc[20:], uint16(i+1))
		binary.BigEndian.PutUint16(fc[22:], 10)
		chunk("fcTL", fc)
		for pos := 8; pos < len(data); {
			n := int(binary.BigEndian.Uint32(data[pos:]))
			if string(data[pos+4:pos+8]) == "IDAT" {
				payload := data[pos+8 : pos+8+n]
				if i == 0 {
					chunk("IDAT", payload)
				} else {
					fd := binary.BigEndian.AppendUint32(nil, seq)
					seq++
					chunk("fdAT", append(fd, payload...))
				}
			}
			pos += n + 12
		}
	}
	chunk("IEND", nil)
	return out
}

func checkTransparentGIF(t *testing.T, data []byte) *gif.GIF {
	t.Helper()
	g, err := gif.DecodeAll(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Image) < 2 || g.LoopCount != 0 {
		t.Fatalf("not looping animation: frames=%d loop=%d", len(g.Image), g.LoopCount)
	}
	a := uint32(0)
	if image.Pt(0, 0).In(g.Image[0].Bounds()) {
		_, _, _, a = g.Image[0].At(0, 0).RGBA()
	}
	if a != 0 {
		t.Fatalf("transparent corner flattened: alpha=%d pixel=%v bounds=%v", a, g.Image[0].At(0, 0), g.Image[0].Bounds())
	}
	return g
}

func TestPNGStickerConversion(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg required")
	}
	var static bytes.Buffer
	img := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	img.SetNRGBA(2, 2, color.NRGBA{R: 255, A: 255})
	png.Encode(&static, img)
	for _, data := range [][]byte{static.Bytes(), testAPNG(t)} {
		converted, err := preparePNGSticker(t.Context(), data, true)
		if err != nil {
			t.Fatal(err)
		}
		g := checkTransparentGIF(t, converted)
		if len(g.Image) != 2 {
			t.Fatal("frame count", len(g.Image))
		}
	}
	original, err := preparePNGSticker(t.Context(), static.Bytes(), false)
	if err != nil || !bytes.Equal(original, static.Bytes()) {
		t.Fatal("ordinary PNG changed", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := preparePNGSticker(ctx, testAPNG(t), true); err == nil {
		t.Fatal("ignored cancellation")
	}
	fixture := testAPNG(t)
	for i := 8; i < len(fixture); i++ {
		_, _ = pngAnimation(fixture[:i])
	}
}

func TestAPNGTrailingChunksIgnored(t *testing.T) {
	valid := testAPNG(t)

	trailing := append(bytes.Clone(valid), 0, 0, 0, 0, 'f', 'c', 'T', 'L', 0, 0, 0, 0)
	animated, err := pngAnimation(trailing)
	if err != nil || !animated {
		t.Fatal("valid APNG rejected", err)
	}
	if !slices.Equal(pngFrameDelays(trailing), pngFrameDelays(valid)) {
		t.Fatal("parsed trailing bytes")
	}
}
