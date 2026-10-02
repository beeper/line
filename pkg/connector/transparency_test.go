package connector

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func TestTransparentPreviewPreservesAlpha(t *testing.T) {
	for _, size := range []image.Point{{8, 6}, {2560, 1280}, {1, 2560}} {
		t.Run(size.String(), func(t *testing.T) {
			src := image.NewNRGBA(image.Rect(0, 0, size.X, size.Y))
			src.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 128})
			var encoded bytes.Buffer
			if err := png.Encode(&encoded, src); err != nil {
				t.Fatal(err)
			}
			data, w, h, err := generateThumbnail(encoded.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			decoded, format, err := image.Decode(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			if format != "png" || w < 1 || h < 1 || w > 1280 || h > 1280 {
				t.Fatalf("format=%s dimensions=%dx%d", format, w, h)
			}
			_, _, _, alpha := decoded.At(w-1, h-1).RGBA()
			if alpha != 0 {
				t.Fatalf("transparent pixel alpha=%d", alpha)
			}
			if size.X == 8 {
				_, _, _, a := decoded.At(0, 0).RGBA()
				if a != 128*257 {
					t.Fatalf("partial alpha changed: %d", a)
				}
			}
		})
	}
}

func TestOpaquePreviewRemainsJPEG(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	for y := range 2 {
		for x := range 2 {
			src.SetNRGBA(x, y, color.NRGBA{R: 255, A: 255})
		}
	}
	var encoded bytes.Buffer
	png.Encode(&encoded, src)
	data, _, _, err := generateThumbnail(encoded.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	_, format, err := image.Decode(bytes.NewReader(data))
	if err != nil || format != "jpeg" {
		t.Fatalf("format=%s err=%v", format, err)
	}
}

func TestEncryptedImageOriginalAndPreviewPreserveAlpha(t *testing.T) {
	var original bytes.Buffer
	png.Encode(&original, image.NewNRGBA(image.Rect(0, 0, 8, 6)))
	client := &LineClient{}
	for _, data := range [][]byte{original.Bytes(), testGIF(t, 2)} {
		encrypted, key, err := client.encryptFileData(data)
		if err != nil {
			t.Fatal(err)
		}
		decrypted, err := client.decryptImageData(encrypted, key)
		if err != nil || !bytes.Equal(data, decrypted) {
			t.Fatal("encrypted original changed", err)
		}
		preview, _, _, err := generateThumbnail(data)
		if err != nil {
			t.Fatal(err)
		}
		ciphertext, err := encryptThumbnail(preview, key)
		if err != nil {
			t.Fatal(err)
		}
		decodedPreview, err := client.decryptImageData(ciphertext, key)
		if err != nil || !bytes.Equal(preview, decodedPreview) {
			t.Fatal("encrypted preview changed", err)
		}
		if bytes.Equal(data, original.Bytes()) {
			img, err := png.Decode(bytes.NewReader(decodedPreview))
			if err != nil {
				t.Fatal(err)
			}
			_, _, _, alpha := img.At(0, 0).RGBA()
			if alpha != 0 {
				t.Fatal("encrypted preview lost transparency")
			}
		}
	}
}
