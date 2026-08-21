package line

import (
	"testing"
)

// TestReplayLTSMVectors previously replayed captured JSON IPC vectors through
// the Node.js runner's call() method. Since the runner now uses wazero directly
// (no JSON IPC), this test needs a rewrite to use the typed Runner methods.
// The WASM-level correctness is covered by pkg/wasm/ltsm_test.go (sign vectors,
// AES roundtrip, Curve25519 roundtrip).
func TestReplayLTSMVectors(t *testing.T) {
	t.Skip("vector replay test needs rewrite for wazero-based runner (no JSON IPC)")
}
