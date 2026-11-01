package e2ee

import (
	"os"
	"testing"
)

func TestRemoveSecureDataFile(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.Mkdir("data", 0o700); err != nil {
		t.Fatal(err)
	}
	if HasSecureDataFile("u-mid") {
		t.Fatal("HasSecureDataFile = true before the file exists")
	}
	if err := RemoveSecureDataFile("u-mid"); err != nil {
		t.Fatalf("RemoveSecureDataFile(missing) = %v, want nil", err)
	}
	if err := os.WriteFile(secureDataPath("u-mid"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !HasSecureDataFile("u-mid") {
		t.Fatal("HasSecureDataFile = false after writing the file")
	}
	if err := RemoveSecureDataFile("u-mid"); err != nil {
		t.Fatalf("RemoveSecureDataFile = %v", err)
	}
	if HasSecureDataFile("u-mid") {
		t.Fatal("file still exists after RemoveSecureDataFile")
	}
}
