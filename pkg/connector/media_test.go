package connector

import (
	"bytes"
	"testing"
)

func TestDecryptImageDataVerifiesHMAC(t *testing.T) {
	lc := &LineClient{}
	plaintext := []byte("file-like LINE E2EE media")

	encrypted, keyMaterial, err := lc.encryptFileData(plaintext)
	if err != nil {
		t.Fatalf("encryptFileData() error = %v", err)
	}

	decrypted, err := lc.decryptImageData(encrypted, keyMaterial)
	if err != nil {
		t.Fatalf("decryptImageData() error = %v", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("decryptImageData() = %q, want %q", decrypted, plaintext)
	}

	tamperedCiphertext := bytes.Clone(encrypted)
	tamperedCiphertext[0] ^= 0x01
	if _, err := lc.decryptImageData(tamperedCiphertext, keyMaterial); err == nil {
		t.Fatal("decryptImageData() with tampered ciphertext succeeded, want error")
	}

	tamperedMAC := bytes.Clone(encrypted)
	tamperedMAC[len(tamperedMAC)-1] ^= 0x01
	if _, err := lc.decryptImageData(tamperedMAC, keyMaterial); err == nil {
		t.Fatal("decryptImageData() with tampered HMAC succeeded, want error")
	}
}

func TestDecryptThumbnailDataVerifiesHMAC(t *testing.T) {
	lc := &LineClient{}
	plaintext := []byte("LINE E2EE thumbnail")

	_, keyMaterial, err := lc.encryptFileData([]byte("parent media"))
	if err != nil {
		t.Fatalf("encryptFileData() error = %v", err)
	}
	encrypted, err := encryptThumbnail(plaintext, keyMaterial)
	if err != nil {
		t.Fatalf("encryptThumbnail() error = %v", err)
	}

	decrypted, err := lc.decryptImageData(encrypted, keyMaterial)
	if err != nil {
		t.Fatalf("decryptImageData() error = %v", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("decryptImageData() = %q, want %q", decrypted, plaintext)
	}

	tamperedCiphertext := bytes.Clone(encrypted)
	tamperedCiphertext[0] ^= 0x01
	if _, err := lc.decryptImageData(tamperedCiphertext, keyMaterial); err == nil {
		t.Fatal("decryptImageData() with tampered thumbnail ciphertext succeeded, want error")
	}

	tamperedMAC := bytes.Clone(encrypted)
	tamperedMAC[len(tamperedMAC)-1] ^= 0x01
	if _, err := lc.decryptImageData(tamperedMAC, keyMaterial); err == nil {
		t.Fatal("decryptImageData() with tampered thumbnail HMAC succeeded, want error")
	}
}

func TestDecryptVideoDataVerifiesChunkHashHMAC(t *testing.T) {
	lc := &LineClient{}
	plaintext := bytes.Repeat([]byte("video"), 40000)

	encrypted, keyMaterial, err := lc.encryptVideoData(plaintext)
	if err != nil {
		t.Fatalf("encryptVideoData() error = %v", err)
	}

	decrypted, err := lc.decryptVideoData(encrypted, keyMaterial)
	if err != nil {
		t.Fatalf("decryptVideoData() error = %v", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Fatal("decryptVideoData() returned different plaintext")
	}

	if _, err := lc.decryptImageData(encrypted, keyMaterial); err == nil {
		t.Fatal("decryptImageData() accepted video HMAC mode, want error")
	}
	fileLikeEncrypted, fileLikeKeyMaterial, err := lc.encryptFileData(plaintext)
	if err != nil {
		t.Fatalf("encryptFileData() error = %v", err)
	}
	if _, err := lc.decryptVideoData(fileLikeEncrypted, fileLikeKeyMaterial); err == nil {
		t.Fatal("decryptVideoData() accepted file-like HMAC mode, want error")
	}

	tamperedCiphertext := bytes.Clone(encrypted)
	tamperedCiphertext[0] ^= 0x01
	if _, err := lc.decryptVideoData(tamperedCiphertext, keyMaterial); err == nil {
		t.Fatal("decryptVideoData() with tampered ciphertext succeeded, want error")
	}

	tamperedMAC := bytes.Clone(encrypted)
	tamperedMAC[len(tamperedMAC)-1] ^= 0x01
	if _, err := lc.decryptVideoData(tamperedMAC, keyMaterial); err == nil {
		t.Fatal("decryptVideoData() with tampered HMAC succeeded, want error")
	}
}
