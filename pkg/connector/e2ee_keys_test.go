package connector

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"

	"github.com/highesttt/matrix-line-messenger/pkg/line"
)

func newPeerKeyTestClient() *LineClient {
	return &LineClient{
		AccessToken: "access",
		recoverTime: time.Now(),
		UserLogin: &bridgev2.UserLogin{
			Bridge: &bridgev2.Bridge{Log: zerolog.New(io.Discard)},
		},
	}
}

func TestEnsurePeerKeyRecoversAndRetriesRefreshRequired(t *testing.T) {
	oldNewClient := newLineAPIClient
	oldNegotiate := negotiateE2EEPublicKeyWithClient
	t.Cleanup(func() {
		newLineAPIClient = oldNewClient
		negotiateE2EEPublicKeyWithClient = oldNegotiate
	})

	var newClientCalls int
	newLineAPIClient = func(token string) *line.Client {
		newClientCalls++
		return line.NewClient(token)
	}

	var negotiateCalls int
	negotiateE2EEPublicKeyWithClient = func(*line.Client, string) (*line.E2EEPublicKey, error) {
		negotiateCalls++
		if negotiateCalls == 1 {
			return nil, errAuthRequired
		}
		return &line.E2EEPublicKey{
			KeyID:     json.Number("42"),
			PublicKey: "peer-public-key",
		}, nil
	}

	lc := newPeerKeyTestClient()
	keyID, publicKey, err := lc.ensurePeerKey(context.Background(), "peer-mid")
	if err != nil {
		t.Fatalf("ensurePeerKey returned error: %v", err)
	}
	if keyID != 42 || publicKey != "peer-public-key" {
		t.Fatalf("ensurePeerKey returned keyID=%d publicKey=%q", keyID, publicKey)
	}
	if negotiateCalls != 2 {
		t.Fatalf("negotiate calls = %d, want 2", negotiateCalls)
	}
	if newClientCalls != 2 {
		t.Fatalf("new clients = %d, want 2", newClientCalls)
	}
}

func TestEnsurePeerKeyByIDRecoversAndRetriesRefreshRequired(t *testing.T) {
	oldNewClient := newLineAPIClient
	oldGetKey := getE2EEPublicKeyWithClient
	t.Cleanup(func() {
		newLineAPIClient = oldNewClient
		getE2EEPublicKeyWithClient = oldGetKey
	})

	var newClientCalls int
	newLineAPIClient = func(token string) *line.Client {
		newClientCalls++
		return line.NewClient(token)
	}

	var getKeyCalls int
	getE2EEPublicKeyWithClient = func(*line.Client, string, int, int) (*line.E2EEPublicKey, error) {
		getKeyCalls++
		if getKeyCalls == 1 {
			return nil, errAuthRequired
		}
		return &line.E2EEPublicKey{
			KeyID:     json.Number("5910969"),
			PublicKey: "specific-peer-public-key",
		}, nil
	}

	lc := newPeerKeyTestClient()
	keyID, publicKey, err := lc.ensurePeerKeyByID(context.Background(), "peer-mid", 5910969)
	if err != nil {
		t.Fatalf("ensurePeerKeyByID returned error: %v", err)
	}
	if keyID != 5910969 || publicKey != "specific-peer-public-key" {
		t.Fatalf("ensurePeerKeyByID returned keyID=%d publicKey=%q", keyID, publicKey)
	}
	if getKeyCalls != 2 {
		t.Fatalf("get key calls = %d, want 2", getKeyCalls)
	}
	if newClientCalls != 2 {
		t.Fatalf("new clients = %d, want 2", newClientCalls)
	}
}

func TestEnsurePeerKeyCachesNoUsablePublicKeyWithoutRecovery(t *testing.T) {
	oldNegotiate := negotiateE2EEPublicKeyWithClient
	t.Cleanup(func() {
		negotiateE2EEPublicKeyWithClient = oldNegotiate
	})

	var negotiateCalls int
	negotiateE2EEPublicKeyWithClient = func(*line.Client, string) (*line.E2EEPublicKey, error) {
		negotiateCalls++
		return nil, line.ErrNoUsableE2EEPublicKey
	}

	lc := newPeerKeyTestClient()
	_, _, err := lc.ensurePeerKey(context.Background(), "peer-mid")
	if !errors.Is(err, line.ErrNoUsableE2EEPublicKey) {
		t.Fatalf("ensurePeerKey error = %v, want ErrNoUsableE2EEPublicKey", err)
	}

	_, _, err = lc.ensurePeerKey(context.Background(), "peer-mid")
	if !errors.Is(err, line.ErrNoUsableE2EEPublicKey) {
		t.Fatalf("cached ensurePeerKey error = %v, want ErrNoUsableE2EEPublicKey", err)
	}
	if negotiateCalls != 1 {
		t.Fatalf("negotiate calls = %d, want cached negative lookup", negotiateCalls)
	}
}
