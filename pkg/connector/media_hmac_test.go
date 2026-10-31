package connector

import (
	"bytes"
	"testing"
)

// Adapted from beeper/line#196.
func TestMediaHMAC(t *testing.T) {
	lc := &LineClient{}
	for _, kind := range []string{"image", "file", "audio", "video", "thumbnail"} {
		t.Run(kind, func(t *testing.T) {
			plaintext := bytes.Repeat([]byte("authenticated media"), 40000)
			encrypt := lc.encryptFileData
			if kind == "video" {
				encrypt = lc.encryptVideoData
			}
			ciphertext, key, err := encrypt(plaintext)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "thumbnail" {
				ciphertext, err = encryptThumbnail(plaintext, key)
				if err != nil {
					t.Fatal(err)
				}
			}
			decrypt, wrongMode := lc.decryptImageData, lc.decryptVideoData
			if kind == "video" {
				decrypt, wrongMode = wrongMode, decrypt
			}
			decrypted, err := decrypt(ciphertext, key)
			if err != nil || !bytes.Equal(decrypted, plaintext) {
				t.Fatalf("valid media: err=%v, equal=%v", err, bytes.Equal(decrypted, plaintext))
			}
			for _, offset := range []int{0, len(ciphertext) - 1} {
				tampered := bytes.Clone(ciphertext)
				tampered[offset] ^= 1
				if got, err := decrypt(tampered, key); err == nil || got != nil {
					t.Fatalf("tampering at %d returned plaintext or no error", offset)
				}
			}
			if got, err := wrongMode(ciphertext, key); err == nil || got != nil {
				t.Fatal("accepted incorrect HMAC mode")
			}
		})
	}
}
