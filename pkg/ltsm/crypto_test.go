package ltsm

import (
	"crypto/rand"
	"testing"

	"golang.org/x/crypto/curve25519"
)

func TestEncryptDecryptV1Roundtrip(t *testing.T) {
	// Generate two keypairs
	privA := make([]byte, 32)
	rand.Read(privA)
	pubA, _ := curve25519.X25519(privA, curve25519.Basepoint)

	privB := make([]byte, 32)
	rand.Read(privB)
	pubB, _ := curve25519.X25519(privB, curve25519.Basepoint)

	// Create channels (A->B and B->A should have same shared secret)
	chanAB, err := NewChannel(privA, pubB)
	if err != nil {
		t.Fatalf("NewChannel A->B: %v", err)
	}
	chanBA, err := NewChannel(privB, pubA)
	if err != nil {
		t.Fatalf("NewChannel B->A: %v", err)
	}

	// Verify shared secrets match
	if chanAB.SharedSecret != chanBA.SharedSecret {
		t.Fatal("shared secrets don't match")
	}

	// Encrypt with A, decrypt with B
	plaintext := []byte(`{"text":"hello from pure Go!"}`)
	ciphertext, err := chanAB.EncryptV1(plaintext)
	if err != nil {
		t.Fatalf("EncryptV1: %v", err)
	}

	decrypted, err := chanBA.DecryptV1(ciphertext)
	if err != nil {
		t.Fatalf("DecryptV1: %v", err)
	}

	if string(decrypted) != string(plaintext) {
		t.Errorf("roundtrip mismatch:\n  got:  %q\n  want: %q", string(decrypted), string(plaintext))
	}
}

func TestEncryptDecryptV2Roundtrip(t *testing.T) {
	privA := make([]byte, 32)
	rand.Read(privA)
	pubA, _ := curve25519.X25519(privA, curve25519.Basepoint)

	privB := make([]byte, 32)
	rand.Read(privB)
	pubB, _ := curve25519.X25519(privB, curve25519.Basepoint)

	chanAB, err := NewChannel(privA, pubB)
	if err != nil {
		t.Fatalf("NewChannel A->B: %v", err)
	}
	chanBA, err := NewChannel(privB, pubA)
	if err != nil {
		t.Fatalf("NewChannel B->A: %v", err)
	}

	to := "u8ae764e8e69e6bd4ecdd9b6ea0c40fce"
	from := "uf1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6"
	senderKeyID := 42
	receiverKeyID := 99
	contentType := 0
	seq := int64(1)
	plaintext := []byte(`{"text":"hello V2 from pure Go!"}`)

	ciphertext, err := chanAB.EncryptV2(to, from, senderKeyID, receiverKeyID, contentType, seq, plaintext)
	if err != nil {
		t.Fatalf("EncryptV2: %v", err)
	}

	t.Logf("V2 ciphertext: %d bytes", len(ciphertext))

	// Decrypt with the SAME parameters
	decrypted, err := chanBA.DecryptV2(to, from, senderKeyID, receiverKeyID, contentType, ciphertext)
	if err != nil {
		t.Fatalf("DecryptV2: %v", err)
	}

	if string(decrypted) != string(plaintext) {
		t.Errorf("roundtrip mismatch:\n  got:  %q\n  want: %q", string(decrypted), string(plaintext))
	}
}

func TestV1CiphertextFormat(t *testing.T) {
	priv := make([]byte, 32)
	rand.Read(priv)
	pub, _ := curve25519.X25519(priv, curve25519.Basepoint)

	ch, err := NewChannel(priv, pub)
	if err != nil {
		t.Fatalf("NewChannel: %v", err)
	}

	plaintext := []byte(`{"text":"hello crypto test"}`) // 28 bytes
	ct, err := ch.EncryptV1(plaintext)
	if err != nil {
		t.Fatalf("EncryptV1: %v", err)
	}

	// Verify format: salt(8) || encrypted(32) || MAC(16) = 56 bytes
	// 28 bytes + 4 bytes PKCS#7 padding = 32 bytes encrypted
	expectedLen := 8 + 32 + 16
	if len(ct) != expectedLen {
		t.Errorf("unexpected ciphertext length: got %d, want %d", len(ct), expectedLen)
	}

	t.Logf("V1 ciphertext: %d bytes (salt=8, body=%d, mac=16)", len(ct), len(ct)-24)
}

func TestV2CiphertextFormat(t *testing.T) {
	priv := make([]byte, 32)
	rand.Read(priv)
	pub, _ := curve25519.X25519(priv, curve25519.Basepoint)

	ch, err := NewChannel(priv, pub)
	if err != nil {
		t.Fatalf("NewChannel: %v", err)
	}

	plaintext := []byte(`{"text":"test"}`) // 15 bytes
	ct, err := ch.EncryptV2("uto", "ufrom", 1, 2, 0, 1, plaintext)
	if err != nil {
		t.Fatalf("EncryptV2: %v", err)
	}

	// Format: salt(16) || nonce(12) || ciphertext(15) || tag(16) = 59 bytes
	expectedLen := 16 + 12 + 15 + 16
	if len(ct) != expectedLen {
		t.Errorf("unexpected ciphertext length: got %d, want %d", len(ct), expectedLen)
	}

	t.Logf("V2 ciphertext: %d bytes (salt=16, nonce=12, body=%d, tag=16)", len(ct), len(ct)-44)
}

func TestProcessMIDforAAD(t *testing.T) {
	// Test MID processing - should be identity (raw UTF-8 bytes, lowercased)
	mid := "u8ae764e8e69e6bd4ecdd9b6ea0c40fce"
	result := processMIDforAAD(mid)

	if len(result) != 33 {
		t.Fatalf("expected 33 bytes, got %d", len(result))
	}

	// Output should be the same as the input MID (already lowercase)
	if string(result) != mid {
		t.Errorf("expected identity:\n  got:  %q\n  want: %q", string(result), mid)
	}

	// Test uppercase prefix gets lowercased
	midUpper := "U8ae764e8e69e6bd4ecdd9b6ea0c40fce"
	resultUpper := processMIDforAAD(midUpper)
	if resultUpper[0] != 'u' {
		t.Errorf("uppercase prefix not lowercased: got %c", resultUpper[0])
	}
}

func TestPKCS7Padding(t *testing.T) {
	tests := []struct {
		input   string
		padded  int // expected padded length
		blockSz int
	}{
		{"", 16, 16},
		{"a", 16, 16},
		{"0123456789abcdef", 32, 16}, // exactly one block -> pad to two blocks
		{"0123456789abcde", 16, 16},  // 15 bytes -> pad to 16
	}

	for _, tt := range tests {
		padded := pkcs7Pad([]byte(tt.input), tt.blockSz)
		if len(padded) != tt.padded {
			t.Errorf("pkcs7Pad(%q, %d): got len %d, want %d", tt.input, tt.blockSz, len(padded), tt.padded)
		}

		unpadded, err := pkcs7Unpad(padded, tt.blockSz)
		if err != nil {
			t.Errorf("pkcs7Unpad: %v", err)
			continue
		}
		if string(unpadded) != tt.input {
			t.Errorf("pkcs7 roundtrip: got %q, want %q", string(unpadded), tt.input)
		}
	}
}
