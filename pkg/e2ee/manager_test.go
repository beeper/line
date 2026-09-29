package e2ee

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/highesttt/matrix-line-messenger/pkg/line"
	"github.com/highesttt/matrix-line-messenger/pkg/ltsm"
)

func TestV1WireHeaderUsesV1DecryptWithoutMetadata(t *testing.T) {
	m, err := NewManager()
	if err != nil {
		t.Fatal(err)
	}
	myKey, err := m.runner.KeyGenerate()
	if err != nil {
		t.Fatal(err)
	}
	myPub, err := m.runner.KeyGetPublic(myKey)
	if err != nil {
		t.Fatal(err)
	}
	const myRaw = 1
	m.myKeyID = myKey
	m.myRawKeyID = myRaw
	m.myPublicB64 = myPub
	m.peerPublic[myRaw] = myPub
	m.keyByRawID[myRaw] = myKey
	channel, err := m.runner.ChannelCreate(myKey, myPub)
	if err != nil {
		t.Fatal(err)
	}
	const plaintext = `{"text":"synthetic V1 message"}`
	chunks, err := m.encryptV1Chunks(channel, myRaw, myRaw, []byte(plaintext))
	if err != nil {
		t.Fatal(err)
	}
	msg := &line.Message{Chunks: chunks, From: "u-sender", To: "u-receiver"}
	for i := 0; i < 1800; i++ {
		got, err := m.DecryptMessageV2(msg)
		if err != nil || got != plaintext {
			t.Fatalf("V1 decrypt %d = (%q, %v)", i, got, err)
		}
	}
	msg.ContentMetadata = map[string]string{"e2eeVersion": "2"}
	if got, err := m.DecryptMessageV2(msg); err != nil || got != plaintext {
		t.Fatalf("V1 with misleading metadata = (%q, %v)", got, err)
	}

	m.groupKeys["c-test"] = map[int]int{myRaw: myKey}
	got, groupKey, err := m.DecryptGroupMessage(msg, "c-test")
	if err != nil || got != plaintext || groupKey != myRaw {
		t.Fatalf("group V1 decrypt = (%q, %d, %v)", got, groupKey, err)
	}
	salt, _ := base64.StdEncoding.DecodeString(chunks[0])
	body, _ := base64.StdEncoding.DecodeString(chunks[1])
	for _, n := range []int{4, 8} {
		repacked := append([]string(nil), chunks...)
		repacked[0] = base64.StdEncoding.EncodeToString(append(salt, body[:n]...))
		repacked[1] = base64.StdEncoding.EncodeToString(body[n:])
		repackedMsg := &line.Message{Chunks: repacked, From: "u-sender", To: "u-receiver", ContentMetadata: map[string]string{"e2eeVersion": "1"}}
		if got, err := m.DecryptMessageV2(repackedMsg); err != nil || got != plaintext {
			t.Fatalf("repacked V1 decrypt (body bytes %d) = (%q, %v)", n, got, err)
		}
	}
	fallback, err := m.EncryptMessageV2Raw("c-test", "u-sender", myKey, myPub, myRaw, myRaw, 0, []byte(plaintext))
	if err != nil {
		t.Fatalf("V2 encrypt with V1 fallback: %v", err)
	}
	header, err := base64.StdEncoding.DecodeString(fallback[0])
	if err != nil || len(header) != 8 {
		t.Fatalf("expected V1 fallback header, got %d bytes: %v", len(header), err)
	}
	if got, err := m.DecryptMessageV2(&line.Message{Chunks: fallback, From: "u-sender", To: "u-receiver"}); err != nil || got != plaintext {
		t.Fatalf("V1 fallback decrypt = (%q, %v)", got, err)
	}
	for _, first := range []string{base64.StdEncoding.EncodeToString(make([]byte, 12)), "!!!", ""} {
		invalid := *msg
		invalid.Chunks = append([]string(nil), chunks...)
		invalid.Chunks[0] = first
		if _, err := m.DecryptMessageV2(&invalid); err == nil {
			t.Fatal("invalid header was accepted")
		}
		if _, _, err := m.DecryptGroupMessage(&invalid, "c-test"); err == nil {
			t.Fatal("invalid group header was accepted")
		}
	}
}

func TestIsFatalLTSMError(t *testing.T) {
	if !isFatalLTSMError(ltsm.ErrAbort) {
		t.Fatal("direct LTSM abort was not classified as fatal")
	}
	if !isFatalLTSMError(fmt.Errorf("decrypt failed: %w", ltsm.ErrAbort)) {
		t.Fatal("wrapped LTSM abort was not classified as fatal")
	}
	if !isFatalLTSMError(fmt.Errorf("V2 failed: %w; V1 fallback failed: %w", errors.New("invalid V2 ciphertext"), ltsm.ErrAbort)) {
		t.Fatal("LTSM abort in a combined fallback error was not classified as fatal")
	}
	if isFatalLTSMError(errors.New("authentication failed")) {
		t.Fatal("ordinary decryption error was classified as fatal")
	}
}

func TestUnwrapGroupSharedKeyReturnsMissingOwnPrivateKey(t *testing.T) {
	manager := &Manager{
		peerPublic: map[int]string{1234: "creator-public-key"},
		keyByRawID: map[int]int{},
	}

	_, err := manager.UnwrapGroupSharedKey("c-group", &line.E2EEGroupSharedKey{
		CreatorKeyID:  1234,
		ReceiverKeyID: 5625926,
	})
	if !errors.Is(err, ErrMissingOwnPrivateKey) {
		t.Fatalf("err = %v, want ErrMissingOwnPrivateKey", err)
	}
}

func TestChannelFromKeyIDsReturnsMissingOwnPrivateKeyWhenPeerKeyKnown(t *testing.T) {
	manager := &Manager{
		peerPublic: map[int]string{1513671: "peer-public-key"},
		keyByRawID: map[int]int{},
	}

	_, err := manager.channelFromKeyIDs(1513671, 5920082)
	if !errors.Is(err, ErrMissingOwnPrivateKey) {
		t.Fatalf("err = %v, want ErrMissingOwnPrivateKey", err)
	}
	if !strings.Contains(err.Error(), "5920082") {
		t.Fatalf("err = %v, want missing key ID in error", err)
	}
}

func TestEncryptGroupMessageRawReturnsGroupKeyNotLoaded(t *testing.T) {
	manager := &Manager{
		sequence:       map[string]int{},
		groupKeys:      map[string]map[int]int{},
		latestGroupKey: map[string]int{},
	}

	_, err := manager.EncryptGroupMessageRaw("c-group", "u-sender", 0, []byte(`{"text":"hello"}`))
	if !errors.Is(err, ErrGroupKeyNotLoaded) {
		t.Fatalf("err = %v, want ErrGroupKeyNotLoaded", err)
	}
}
