package connector

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"

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

func TestApplyExportedLoginE2EEKeysClearsForcedReconnect(t *testing.T) {
	meta := &UserLoginMetadata{
		EncryptedKeyChain:  "old-keychain",
		E2EEPublicKey:      "old-public-key",
		E2EEVersion:        "1",
		E2EEKeyID:          "old-key-id",
		ExportedKeyMap:     map[string]string{"old-key-id": "old-export"},
		ForceFullE2EELogin: true,
	}
	res := &line.LoginResult{
		EncryptedKeyChain: "new-keychain",
		E2EEPublicKey:     "new-public-key",
		E2EEVersion:       "2",
		E2EEKeyID:         "new-key-id",
	}
	exported := map[string]string{"new-key-id": "new-export"}

	applyExportedLoginE2EEKeys(meta, res, exported)

	if meta.EncryptedKeyChain != res.EncryptedKeyChain ||
		meta.E2EEPublicKey != res.E2EEPublicKey ||
		meta.E2EEVersion != res.E2EEVersion ||
		meta.E2EEKeyID != res.E2EEKeyID ||
		meta.ExportedKeyMap["new-key-id"] != "new-export" {
		t.Fatalf("metadata = %#v, want refreshed E2EE fields", meta)
	}
	if meta.ForceFullE2EELogin {
		t.Fatal("successful key refresh must clear ForceFullE2EELogin")
	}
}

func TestCopyLoginE2EEKeyMetadataClonesExportedKeyMap(t *testing.T) {
	src := &UserLoginMetadata{
		EncryptedKeyChain: "encrypted-keychain",
		E2EEPublicKey:     "public-key",
		E2EEVersion:       "2",
		E2EEKeyID:         "5625926",
		ExportedKeyMap:    map[string]string{"5625926": "exported-key"},
	}
	dst := &UserLoginMetadata{}

	copyLoginE2EEKeyMetadata(dst, src)

	if dst.EncryptedKeyChain != src.EncryptedKeyChain ||
		dst.E2EEPublicKey != src.E2EEPublicKey ||
		dst.E2EEVersion != src.E2EEVersion ||
		dst.E2EEKeyID != src.E2EEKeyID ||
		dst.ExportedKeyMap["5625926"] != "exported-key" {
		t.Fatalf("metadata = %#v, want copied E2EE fields", dst)
	}
	src.ExportedKeyMap["5625926"] = "mutated"
	if dst.ExportedKeyMap["5625926"] != "exported-key" {
		t.Fatal("ExportedKeyMap must be cloned, not shared")
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

func TestStartWithOverrideForcesFullReconnectWithoutStoredE2EEKeys(t *testing.T) {
	oldLogin := loginWithCredentials
	var gotCertificate string
	loginWithCredentials = func(_, _, certificate string) (*line.LoginResult, error) {
		gotCertificate = certificate
		return nil, errors.New("login failed")
	}
	t.Cleanup(func() {
		loginWithCredentials = oldLogin
	})

	ll := &LineEmailLogin{}
	override := &bridgev2.UserLogin{
		Bridge: &bridgev2.Bridge{Log: zerolog.New(io.Discard)},
		UserLogin: &database.UserLogin{
			Metadata: &UserLoginMetadata{
				Email:       "user@example.com",
				Password:    "password",
				Certificate: "stored-certificate",
			},
		},
	}

	if _, err := ll.StartWithOverride(context.Background(), override); err != nil {
		t.Fatalf("StartWithOverride returned error: %v", err)
	}
	if gotCertificate != "" {
		t.Fatalf("certificate = %q, want empty to force full E2EE reconnect", gotCertificate)
	}
}

func TestStartWithOverrideKeepsCertificateWithStoredE2EEKeys(t *testing.T) {
	oldLogin := loginWithCredentials
	var gotCertificate string
	loginWithCredentials = func(_, _, certificate string) (*line.LoginResult, error) {
		gotCertificate = certificate
		return nil, errors.New("login failed")
	}
	t.Cleanup(func() {
		loginWithCredentials = oldLogin
	})

	ll := &LineEmailLogin{}
	override := &bridgev2.UserLogin{
		Bridge: &bridgev2.Bridge{Log: zerolog.New(io.Discard)},
		UserLogin: &database.UserLogin{
			Metadata: &UserLoginMetadata{
				Email:          "user@example.com",
				Password:       "password",
				Certificate:    "stored-certificate",
				ExportedKeyMap: map[string]string{"5625926": "exported-key"},
			},
		},
	}

	if _, err := ll.StartWithOverride(context.Background(), override); err != nil {
		t.Fatalf("StartWithOverride returned error: %v", err)
	}
	if gotCertificate != "stored-certificate" {
		t.Fatalf("certificate = %q, want stored certificate", gotCertificate)
	}
}

func TestStartWithOverrideForcesFullReconnectWhenE2EEKeyMissing(t *testing.T) {
	oldLogin := loginWithCredentials
	var gotCertificate string
	loginWithCredentials = func(_, _, certificate string) (*line.LoginResult, error) {
		gotCertificate = certificate
		return nil, errors.New("login failed")
	}
	t.Cleanup(func() {
		loginWithCredentials = oldLogin
	})

	ll := &LineEmailLogin{}
	override := &bridgev2.UserLogin{
		Bridge: &bridgev2.Bridge{Log: zerolog.New(io.Discard)},
		UserLogin: &database.UserLogin{
			Metadata: &UserLoginMetadata{
				Email:              "user@example.com",
				Password:           "password",
				Certificate:        "stored-certificate",
				ExportedKeyMap:     map[string]string{"old-key-id": "old-export"},
				ForceFullE2EELogin: true,
			},
		},
	}

	if _, err := ll.StartWithOverride(context.Background(), override); err != nil {
		t.Fatalf("StartWithOverride returned error: %v", err)
	}
	if gotCertificate != "" {
		t.Fatalf("certificate = %q, want empty to force full E2EE reconnect", gotCertificate)
	}
}

func TestShouldPreserveExistingE2EEKeys(t *testing.T) {
	existing := &UserLoginMetadata{ExportedKeyMap: map[string]string{"old-key-id": "old-export"}}
	if !shouldPreserveExistingE2EEKeys(false, existing) {
		t.Fatal("expected existing keys to be preserved when no new keys were exported")
	}
	if shouldPreserveExistingE2EEKeys(true, existing) {
		t.Fatal("must not preserve existing keys when new keys were exported")
	}
	existing.ForceFullE2EELogin = true
	if shouldPreserveExistingE2EEKeys(false, existing) {
		t.Fatal("must not preserve stale keys after missing-key forced full reconnect")
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
