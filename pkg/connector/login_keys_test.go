package connector

import (
	"testing"

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
