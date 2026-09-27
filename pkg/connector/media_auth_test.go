package connector

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func TestMediaAuthentication(t *testing.T) {
	client := &LineClient{}

	plain := bytes.Repeat([]byte("authenticated media"), 15000)
	for _, streaming := range []bool{false, true} {
		kind := "image"
		if streaming {
			kind = "video"
		}
		var encrypted []byte
		var key string
		var err error
		if streaming {
			encrypted, key, err = client.encryptVideoData(plain)
		} else {
			encrypted, key, err = client.encryptFileData(plain)
		}
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := client.decryptMediaData(encrypted, key, kind)
		if err != nil || !bytes.Equal(decoded, plain) {
			t.Fatalf("valid media rejected, streaming=%v: %v", streaming, err)
		}
		for _, pos := range []int{0, 131072, len(encrypted) - 33, len(encrypted) - 1} {
			damaged := bytes.Clone(encrypted)
			damaged[pos] ^= 1
			decoded, err = client.decryptMediaData(damaged, key, kind)
			if err == nil || decoded != nil {
				t.Fatalf("unauthenticated plaintext returned at %d streaming=%v", pos, streaming)
			}
		}
		if streaming {
			forged := append(generateChunkHashes(encrypted[:len(encrypted)-32]), encrypted[len(encrypted)-32:]...)
			if output, err := client.decryptMediaData(forged, key, "video"); err == nil || output != nil {
				t.Fatal("chunk hash substitution accepted")
			}
		}
		wrongKey := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{42}, 32))
		if decoded, err = client.decryptMediaData(encrypted, wrongKey, kind); err == nil || decoded != nil {
			t.Fatal("wrong key accepted")
		}
		for _, size := range []int{0, 31, 32, len(encrypted) - 1} {
			if decoded, err = client.decryptMediaData(encrypted[:size], key, kind); err == nil || decoded != nil {
				t.Fatalf("truncated ciphertext accepted: %d", size)
			}
		}
	}
}
