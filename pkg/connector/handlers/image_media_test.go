package handlers

import (
	"testing"

	"github.com/highesttt/matrix-line-messenger/pkg/line"
)

func TestLineOBSDownloadOptionsUsesLongRetryWindowForOriginalMedia(t *testing.T) {
	metadata := map[string]string{
		"MEDIA_CONTENT_INFO": `{"category":"original","extension":"jpeg"}`,
		"OBS_POP":            "pop-token",
	}

	plainOptions := lineOBSDownloadOptions(metadata, true)
	if plainOptions.TID != "original" {
		t.Fatalf("unexpected TID for original plain media: got %q", plainOptions.TID)
	}
	if plainOptions.OBSPop != "pop-token" {
		t.Fatalf("unexpected OBS_POP: got %q", plainOptions.OBSPop)
	}
	if plainOptions.MaxProcessingRetries != line.OBSOriginalMediaMaxRetries {
		t.Fatalf("unexpected retry limit: got %d, want %d", plainOptions.MaxProcessingRetries, line.OBSOriginalMediaMaxRetries)
	}

	encryptedOptions := lineOBSDownloadOptions(metadata, false)
	if encryptedOptions.MaxProcessingRetries != 0 {
		t.Fatalf("unexpected retry override for encrypted media: got %d", encryptedOptions.MaxProcessingRetries)
	}
}
