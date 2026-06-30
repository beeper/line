package ltsm

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"golang.org/x/crypto/curve25519"
)

// TestGenerateV2TestVector generates a complete V2 encryption test vector
// with all intermediate values exposed for cross-validation.
func TestGenerateV2TestVector(t *testing.T) {
	// Use deterministic keys so the test vector is reproducible
	senderPriv, _ := hex.DecodeString("a546e36bf0527c9d3b16154b82465edd62144c0ac1fc5a18506a2244ba449ac4")
	receiverPriv, _ := hex.DecodeString("4b66e9d4d1b4673c5ad22691957d6af5c11b6421e0ea01d42ca4169e7918ba0d")

	senderPub, _ := curve25519.X25519(senderPriv, curve25519.Basepoint)
	receiverPub, _ := curve25519.X25519(receiverPriv, curve25519.Basepoint)

	t.Logf("Sender private key:   %s", hex.EncodeToString(senderPriv))
	t.Logf("Sender public key:    %s", hex.EncodeToString(senderPub))
	t.Logf("Receiver private key: %s", hex.EncodeToString(receiverPriv))
	t.Logf("Receiver public key:  %s", hex.EncodeToString(receiverPub))

	// Create channels
	senderChan, err := NewChannel(senderPriv, receiverPub)
	if err != nil {
		t.Fatalf("NewChannel sender->receiver: %v", err)
	}
	receiverChan, err := NewChannel(receiverPriv, senderPub)
	if err != nil {
		t.Fatalf("NewChannel receiver->sender: %v", err)
	}

	t.Logf("Shared secret:        %s", hex.EncodeToString(senderChan.SharedSecret[:]))

	if senderChan.SharedSecret != receiverChan.SharedSecret {
		t.Fatal("shared secrets don't match")
	}

	to := "u8ae764e8e69e6bd4ecdd9b6ea0c40fce"
	from := "uf1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6"
	senderKeyID := 42
	receiverKeyID := 99
	contentType := 0
	seq := int64(1)
	plaintext := []byte(`{"text":"hello from pure Go V2 test vector"}`)

	// Encrypt V2
	ciphertext, err := senderChan.EncryptV2(to, from, senderKeyID, receiverKeyID, contentType, seq, plaintext)
	if err != nil {
		t.Fatalf("EncryptV2: %v", err)
	}

	// Decrypt with receiver to verify
	decrypted, err := receiverChan.DecryptV2(to, from, senderKeyID, receiverKeyID, contentType, ciphertext)
	if err != nil {
		t.Fatalf("DecryptV2: %v", err)
	}
	if string(decrypted) != string(plaintext) {
		t.Fatalf("roundtrip mismatch: got %q, want %q", string(decrypted), string(plaintext))
	}
	t.Logf("V2 roundtrip OK: %s", string(decrypted))

	// Parse ciphertext components
	salt := ciphertext[:16]
	nonce := ciphertext[16:28]
	sealed := ciphertext[28:]
	encBody := sealed[:len(sealed)-16]
	gcmTag := sealed[len(sealed)-16:]

	// Derive key for verification
	kh := sha256.New()
	kh.Write(senderChan.SharedSecret[:])
	kh.Write(salt)
	kh.Write([]byte("Key"))
	derivedKey := kh.Sum(nil)

	// Build AAD for verification
	aad := buildAADv2(to, from, senderKeyID, receiverKeyID, contentType)

	t.Logf("\n--- V2 Test Vector ---")
	t.Logf("Sender private key:   %s", hex.EncodeToString(senderPriv))
	t.Logf("Sender public key:    %s", hex.EncodeToString(senderPub))
	t.Logf("Receiver private key: %s", hex.EncodeToString(receiverPriv))
	t.Logf("Receiver public key:  %s", hex.EncodeToString(receiverPub))
	t.Logf("Shared secret:        %s", hex.EncodeToString(senderChan.SharedSecret[:]))
	t.Logf("To MID:               %s", to)
	t.Logf("From MID:             %s", from)
	t.Logf("Sender Key ID:        %d", senderKeyID)
	t.Logf("Receiver Key ID:      %d", receiverKeyID)
	t.Logf("Content Type:         %d", contentType)
	t.Logf("Sequence:             %d", seq)
	t.Logf("Plaintext:            %s", string(plaintext))
	t.Logf("Plaintext hex:        %s", hex.EncodeToString(plaintext))
	t.Logf("Ciphertext hex:       %s", hex.EncodeToString(ciphertext))
	t.Logf("  Salt (16):          %s", hex.EncodeToString(salt))
	t.Logf("  Nonce (12):         %s", hex.EncodeToString(nonce))
	t.Logf("  Encrypted body:     %s", hex.EncodeToString(encBody))
	t.Logf("  GCM Tag (16):       %s", hex.EncodeToString(gcmTag))
	t.Logf("Derived AES key:      %s", hex.EncodeToString(derivedKey))
	t.Logf("AAD (%d bytes):       %s", len(aad), hex.EncodeToString(aad))

	// Also do a V1 test vector for cross-validation
	v1ct, err := senderChan.EncryptV1(plaintext)
	if err != nil {
		t.Fatalf("EncryptV1: %v", err)
	}
	v1dec, err := receiverChan.DecryptV1(v1ct)
	if err != nil {
		t.Fatalf("DecryptV1: %v", err)
	}
	if string(v1dec) != string(plaintext) {
		t.Fatalf("V1 roundtrip mismatch")
	}

	v1salt := v1ct[:8]
	v1body := v1ct[8 : len(v1ct)-16]
	v1mac := v1ct[len(v1ct)-16:]

	t.Logf("\n--- V1 Test Vector ---")
	t.Logf("V1 ciphertext hex:    %s", hex.EncodeToString(v1ct))
	t.Logf("  Salt (8):           %s", hex.EncodeToString(v1salt))
	t.Logf("  Encrypted body:     %s", hex.EncodeToString(v1body))
	t.Logf("  MAC (16):           %s", hex.EncodeToString(v1mac))

	// Output JSON test vector
	type TestVectorJSON struct {
		SenderPrivateKey   string `json:"senderPrivateKey"`
		SenderPublicKey    string `json:"senderPublicKey"`
		ReceiverPrivateKey string `json:"receiverPrivateKey"`
		ReceiverPublicKey  string `json:"receiverPublicKey"`
		SharedSecret       string `json:"sharedSecret"`
		ToMID              string `json:"toMID"`
		FromMID            string `json:"fromMID"`
		SenderKeyID        int    `json:"senderKeyID"`
		ReceiverKeyID      int    `json:"receiverKeyID"`
		ContentType        int    `json:"contentType"`
		Sequence           int64  `json:"sequence"`
		Plaintext          string `json:"plaintext"`
		PlaintextHex       string `json:"plaintextHex"`
		V2                 struct {
			Ciphertext    string `json:"ciphertext"`
			Salt          string `json:"salt"`
			Nonce         string `json:"nonce"`
			EncryptedBody string `json:"encryptedBody"`
			GCMTag        string `json:"gcmTag"`
			DerivedAESKey string `json:"derivedAESKey"`
			AAD           string `json:"aad"`
			AADLength     int    `json:"aadLength"`
		} `json:"v2"`
		V1 struct {
			Ciphertext    string `json:"ciphertext"`
			Salt          string `json:"salt"`
			EncryptedBody string `json:"encryptedBody"`
			MAC           string `json:"mac"`
		} `json:"v1"`
	}

	vec := TestVectorJSON{
		SenderPrivateKey:   hex.EncodeToString(senderPriv),
		SenderPublicKey:    hex.EncodeToString(senderPub),
		ReceiverPrivateKey: hex.EncodeToString(receiverPriv),
		ReceiverPublicKey:  hex.EncodeToString(receiverPub),
		SharedSecret:       hex.EncodeToString(senderChan.SharedSecret[:]),
		ToMID:              to,
		FromMID:            from,
		SenderKeyID:        senderKeyID,
		ReceiverKeyID:      receiverKeyID,
		ContentType:        contentType,
		Sequence:           seq,
		Plaintext:          string(plaintext),
		PlaintextHex:       hex.EncodeToString(plaintext),
	}
	vec.V2.Ciphertext = hex.EncodeToString(ciphertext)
	vec.V2.Salt = hex.EncodeToString(salt)
	vec.V2.Nonce = hex.EncodeToString(nonce)
	vec.V2.EncryptedBody = hex.EncodeToString(encBody)
	vec.V2.GCMTag = hex.EncodeToString(gcmTag)
	vec.V2.DerivedAESKey = hex.EncodeToString(derivedKey)
	vec.V2.AAD = hex.EncodeToString(aad)
	vec.V2.AADLength = len(aad)

	vec.V1.Ciphertext = hex.EncodeToString(v1ct)
	vec.V1.Salt = hex.EncodeToString(v1salt)
	vec.V1.EncryptedBody = hex.EncodeToString(v1body)
	vec.V1.MAC = hex.EncodeToString(v1mac)

	jsonBytes, _ := json.MarshalIndent(vec, "", "  ")
	t.Logf("\n--- JSON Test Vector ---\n%s", string(jsonBytes))

	// Write to file
	if err := os.WriteFile("/tmp/ltsm_v2_test_vector.json", jsonBytes, 0644); err != nil {
		t.Logf("Warning: could not write test vector file: %v", err)
	} else {
		t.Logf("Test vector written to /tmp/ltsm_v2_test_vector.json")
	}
}

// TestGenerateV2DeterministicVector generates a V2 vector with a deterministic
// salt and nonce (by encrypting manually) for truly reproducible test vectors.
func TestGenerateV2DeterministicVector(t *testing.T) {
	// Deterministic keys
	senderPriv, _ := hex.DecodeString("a546e36bf0527c9d3b16154b82465edd62144c0ac1fc5a18506a2244ba449ac4")
	receiverPriv, _ := hex.DecodeString("4b66e9d4d1b4673c5ad22691957d6af5c11b6421e0ea01d42ca4169e7918ba0d")

	senderPub, _ := curve25519.X25519(senderPriv, curve25519.Basepoint)
	receiverPub, _ := curve25519.X25519(receiverPriv, curve25519.Basepoint)

	senderChan, err := NewChannel(senderPriv, receiverPub)
	if err != nil {
		t.Fatalf("NewChannel: %v", err)
	}
	receiverChan, err := NewChannel(receiverPriv, senderPub)
	if err != nil {
		t.Fatalf("NewChannel: %v", err)
	}

	if senderChan.SharedSecret != receiverChan.SharedSecret {
		t.Fatal("shared secrets mismatch")
	}

	to := "u8ae764e8e69e6bd4ecdd9b6ea0c40fce"
	from := "uf1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6"
	senderKeyID := 42
	receiverKeyID := 99
	contentType := 0
	seq := int64(1)
	plaintext := []byte(`{"text":"hello from pure Go V2 test vector"}`)

	// Deterministic salt and nonce
	salt, _ := hex.DecodeString("0102030405060708090a0b0c0d0e0f10")
	nonce := make([]byte, 12)
	binary.BigEndian.PutUint64(nonce[:8], uint64(seq))
	// nonce[8:12] = deterministic random part
	nonce[8] = 0xaa
	nonce[9] = 0xbb
	nonce[10] = 0xcc
	nonce[11] = 0xdd

	// Derive key
	kh := sha256.New()
	kh.Write(senderChan.SharedSecret[:])
	kh.Write(salt)
	kh.Write([]byte("Key"))
	aesKey := kh.Sum(nil)

	// Build AAD
	aad := buildAADv2(to, from, senderKeyID, receiverKeyID, contentType)

	// AES-256-GCM encrypt
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		t.Fatalf("aes.NewCipher: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("cipher.NewGCM: %v", err)
	}
	sealed := gcm.Seal(nil, nonce, plaintext, aad)

	// Assemble ciphertext: salt(16) || nonce(12) || sealed
	ciphertext := make([]byte, 0, 16+12+len(sealed))
	ciphertext = append(ciphertext, salt...)
	ciphertext = append(ciphertext, nonce...)
	ciphertext = append(ciphertext, sealed...)

	encBody := sealed[:len(sealed)-16]
	gcmTag := sealed[len(sealed)-16:]

	// Verify decrypt
	decrypted, err := receiverChan.DecryptV2(to, from, senderKeyID, receiverKeyID, contentType, ciphertext)
	if err != nil {
		t.Fatalf("DecryptV2 failed: %v", err)
	}
	if string(decrypted) != string(plaintext) {
		t.Fatalf("roundtrip mismatch: got %q, want %q", string(decrypted), string(plaintext))
	}

	t.Logf("V2 Deterministic Test Vector")
	t.Logf("============================")
	t.Logf("Sender private key:   %s", hex.EncodeToString(senderPriv))
	t.Logf("Sender public key:    %s", hex.EncodeToString(senderPub))
	t.Logf("Receiver private key: %s", hex.EncodeToString(receiverPriv))
	t.Logf("Receiver public key:  %s", hex.EncodeToString(receiverPub))
	t.Logf("Shared secret:        %s", hex.EncodeToString(senderChan.SharedSecret[:]))
	t.Logf("To MID:               %s", to)
	t.Logf("From MID:             %s", from)
	t.Logf("Sender Key ID:        %d", senderKeyID)
	t.Logf("Receiver Key ID:      %d", receiverKeyID)
	t.Logf("Content Type:         %d", contentType)
	t.Logf("Sequence:             %d", seq)
	t.Logf("Plaintext:            %s", string(plaintext))
	t.Logf("Plaintext hex:        %s", hex.EncodeToString(plaintext))
	t.Logf("")
	t.Logf("Ciphertext hex:       %s", hex.EncodeToString(ciphertext))
	t.Logf("  Salt (16):          %s", hex.EncodeToString(salt))
	t.Logf("  Nonce (12):         %s", hex.EncodeToString(nonce))
	t.Logf("  Encrypted body:     %s", hex.EncodeToString(encBody))
	t.Logf("  GCM Tag (16):       %s", hex.EncodeToString(gcmTag))
	t.Logf("Derived AES key:      %s", hex.EncodeToString(aesKey))
	t.Logf("AAD (%d bytes):       %s", len(aad), hex.EncodeToString(aad))
	t.Logf("AAD breakdown:")
	t.Logf("  processed_to (33):   %s", hex.EncodeToString(aad[:33]))
	t.Logf("  processed_from (33): %s", hex.EncodeToString(aad[33:66]))
	t.Logf("  senderKeyId (4):     %s", hex.EncodeToString(aad[66:70]))
	t.Logf("  receiverKeyId (4):   %s", hex.EncodeToString(aad[70:74]))
	t.Logf("  version (4):         %s", hex.EncodeToString(aad[74:78]))
	t.Logf("  contentType (4):     %s", hex.EncodeToString(aad[78:82]))

	// Output JSON
	type DeterministicVector struct {
		SenderPrivateKey   string `json:"senderPrivateKey"`
		SenderPublicKey    string `json:"senderPublicKey"`
		ReceiverPrivateKey string `json:"receiverPrivateKey"`
		ReceiverPublicKey  string `json:"receiverPublicKey"`
		SharedSecret       string `json:"sharedSecret"`
		ToMID              string `json:"toMID"`
		FromMID            string `json:"fromMID"`
		SenderKeyID        int    `json:"senderKeyID"`
		ReceiverKeyID      int    `json:"receiverKeyID"`
		ContentType        int    `json:"contentType"`
		Sequence           int64  `json:"sequence"`
		Plaintext          string `json:"plaintext"`
		PlaintextHex       string `json:"plaintextHex"`
		Salt               string `json:"salt"`
		Nonce              string `json:"nonce"`
		DerivedAESKey      string `json:"derivedAESKey"`
		AAD                string `json:"aad"`
		AADLength          int    `json:"aadLength"`
		EncryptedBody      string `json:"encryptedBody"`
		GCMTag             string `json:"gcmTag"`
		Ciphertext         string `json:"ciphertext"`
		CiphertextLength   int    `json:"ciphertextLength"`
	}

	vec := DeterministicVector{
		SenderPrivateKey:   hex.EncodeToString(senderPriv),
		SenderPublicKey:    hex.EncodeToString(senderPub),
		ReceiverPrivateKey: hex.EncodeToString(receiverPriv),
		ReceiverPublicKey:  hex.EncodeToString(receiverPub),
		SharedSecret:       hex.EncodeToString(senderChan.SharedSecret[:]),
		ToMID:              to,
		FromMID:            from,
		SenderKeyID:        senderKeyID,
		ReceiverKeyID:      receiverKeyID,
		ContentType:        contentType,
		Sequence:           seq,
		Plaintext:          string(plaintext),
		PlaintextHex:       hex.EncodeToString(plaintext),
		Salt:               hex.EncodeToString(salt),
		Nonce:              hex.EncodeToString(nonce),
		DerivedAESKey:      hex.EncodeToString(aesKey),
		AAD:                hex.EncodeToString(aad),
		AADLength:          len(aad),
		EncryptedBody:      hex.EncodeToString(encBody),
		GCMTag:             hex.EncodeToString(gcmTag),
		Ciphertext:         hex.EncodeToString(ciphertext),
		CiphertextLength:   len(ciphertext),
	}

	jsonBytes, _ := json.MarshalIndent(vec, "", "  ")
	fmt.Println(string(jsonBytes))

	// Write to file
	if err := os.WriteFile("/tmp/ltsm_v2_deterministic_vector.json", jsonBytes, 0644); err != nil {
		t.Logf("Warning: could not write test vector file: %v", err)
	} else {
		t.Logf("Test vector written to /tmp/ltsm_v2_deterministic_vector.json")
	}
}
