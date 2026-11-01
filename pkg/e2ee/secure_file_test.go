package e2ee

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRemoveSecureDataFile(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.Mkdir("data", 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("data", "lcs_secure_u-mid.json")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RemoveSecureDataFile("u-mid"); err != nil {
		t.Fatalf("RemoveSecureDataFile = %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file still exists after RemoveSecureDataFile: %v", err)
	}
	if err := RemoveSecureDataFile("u-mid"); err != nil {
		t.Fatalf("RemoveSecureDataFile(missing) = %v, want nil", err)
	}
	if err := RemoveSecureDataFile(""); err != nil {
		t.Fatalf("RemoveSecureDataFile(empty) = %v, want nil", err)
	}
}
