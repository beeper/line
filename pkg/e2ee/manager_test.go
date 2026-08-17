package e2ee

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/highesttt/matrix-line-messenger/pkg/line"
	"github.com/highesttt/matrix-line-messenger/pkg/ltsm"
)

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
