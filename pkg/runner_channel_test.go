package line

import (
	"bytes"
	"io"
	"os"
	"testing"
)

func TestChannelCreateWithWASMOnlyKeyDoesNotWriteStdout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping runner integration test in short mode")
	}

	r, err := GetRunner()
	if err != nil {
		t.Fatalf("GetRunner failed: %v", err)
	}

	keyID, err := r.KeyGenerate()
	if err != nil {
		t.Fatalf("KeyGenerate failed: %v", err)
	}
	peerKeyID, err := r.KeyGenerate()
	if err != nil {
		t.Fatalf("peer KeyGenerate failed: %v", err)
	}
	peerPub, err := r.KeyGetPublic(peerKeyID)
	if err != nil {
		t.Fatalf("KeyGetPublic failed: %v", err)
	}

	var channelID int
	stdout := captureStdout(t, func() {
		channelID, err = r.ChannelCreate(keyID, peerPub)
	})
	if err != nil {
		t.Fatalf("ChannelCreate failed: %v", err)
	}
	if channelID == 0 {
		t.Fatal("ChannelCreate returned empty channel ID")
	}
	if stdout != "" {
		t.Fatalf("ChannelCreate wrote to stdout: %q", stdout)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	oldStdout := os.Stdout
	readPipe, writePipe, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe failed: %v", err)
	}

	var buf bytes.Buffer
	done := make(chan error, 1)
	go func() {
		_, copyErr := io.Copy(&buf, readPipe)
		done <- copyErr
	}()

	os.Stdout = writePipe
	defer func() {
		os.Stdout = oldStdout
	}()
	fn()
	os.Stdout = oldStdout

	if err := writePipe.Close(); err != nil {
		t.Fatalf("closing stdout pipe writer failed: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("reading captured stdout failed: %v", err)
	}
	if err := readPipe.Close(); err != nil {
		t.Fatalf("closing stdout pipe reader failed: %v", err)
	}

	return buf.String()
}
