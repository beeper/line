package connector

import (
	"errors"
	"testing"

	"github.com/highesttt/matrix-line-messenger/pkg/e2ee"
	"github.com/highesttt/matrix-line-messenger/pkg/line"
)

func TestSaveLoginE2EEKeyMetadata(t *testing.T) {
	meta := &UserLoginMetadata{}
	res := &line.LoginResult{
		EncryptedKeyChain: "encrypted-keychain",
		E2EEPublicKey:     "public-key",
		E2EEVersion:       "2",
		E2EEKeyID:         "5625926",
	}

	saveLoginE2EEKeyMetadata(meta, res)

	if meta.EncryptedKeyChain != res.EncryptedKeyChain ||
		meta.E2EEPublicKey != res.E2EEPublicKey ||
		meta.E2EEVersion != res.E2EEVersion ||
		meta.E2EEKeyID != res.E2EEKeyID {
		t.Fatalf("metadata = %#v, want login E2EE fields copied", meta)
	}
}

func TestLoginSecureDataIDPrefersLineMID(t *testing.T) {
	meta := &UserLoginMetadata{Mid: "u-line-mid"}
	if got := loginSecureDataID(meta, "@user:example.com"); got != "u-line-mid" {
		t.Fatalf("loginSecureDataID = %q, want LINE MID", got)
	}

	meta.Mid = ""
	if got := loginSecureDataID(meta, "@user:example.com"); got != "@user:example.com" {
		t.Fatalf("loginSecureDataID fallback = %q, want Matrix fallback", got)
	}
}

func TestRefreshLoginE2EEKeysKeepsMetadataOnExportFailure(t *testing.T) {
	oldManager := newE2EEManager
	exportErr := errors.New("manager unavailable")
	newE2EEManager = func() (*e2ee.Manager, error) {
		return nil, exportErr
	}
	t.Cleanup(func() {
		newE2EEManager = oldManager
	})

	meta := &UserLoginMetadata{
		EncryptedKeyChain: "old-keychain",
		E2EEPublicKey:     "old-public-key",
		E2EEVersion:       "1",
		E2EEKeyID:         "old-key-id",
		ExportedKeyMap:    map[string]string{"old-key-id": "old-export"},
	}
	res := &line.LoginResult{
		EncryptedKeyChain: "new-keychain",
		E2EEPublicKey:     "new-public-key",
		E2EEVersion:       "2",
		E2EEKeyID:         "new-key-id",
	}

	err := (&LineClient{}).refreshLoginE2EEKeys(res, meta, nil)
	if !errors.Is(err, exportErr) {
		t.Fatalf("err = %v, want %v", err, exportErr)
	}
	if meta.EncryptedKeyChain != "old-keychain" ||
		meta.E2EEPublicKey != "old-public-key" ||
		meta.E2EEVersion != "1" ||
		meta.E2EEKeyID != "old-key-id" ||
		meta.ExportedKeyMap["old-key-id"] != "old-export" {
		t.Fatalf("metadata was clobbered on export failure: %#v", meta)
	}
}
