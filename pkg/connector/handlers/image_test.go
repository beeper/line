package handlers

import (
	"testing"

	"github.com/beeper/line/pkg/line"
)

func TestLineImageDownloadSourcePrefersPublicResource(t *testing.T) {
	source := lineImageDownloadSource(line.Message{
		ID: "message-id",
		ContentMetadata: map[string]string{
			"DOWNLOAD_URL": "/r/official/business-image",
			"OID":          "ignored-private-oid",
		},
	})

	if source.publicPath != "/r/official/business-image" {
		t.Fatalf("public path = %q", source.publicPath)
	}
	if source.oid != "" || source.isPlainMedia {
		t.Fatalf("source = %#v, want public resource only", source)
	}
}

func TestLineMediaCategoryControlsPlainMediaDownload(t *testing.T) {
	for _, tc := range []struct {
		name, metadata, wantTID string
	}{
		{"original media", `{"category":"original"}`, "original"},
		{"other media", `{"category":"preview"}`, ""},
		{"malformed metadata", "{", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			metadata := map[string]string{"MEDIA_CONTENT_INFO": tc.metadata}
			options := lineOBSDownloadOptions(metadata, true)
			if options.TID != tc.wantTID {
				t.Fatalf("download TID = %q, want %q", options.TID, tc.wantTID)
			}
		})
	}
	if got := lineMediaCategory(nil); got != "" {
		t.Fatalf("nil metadata category = %q", got)
	}
}

func TestLineImageDownloadSourcePrivateAndPlainFallbacks(t *testing.T) {
	privateSource := lineImageDownloadSource(line.Message{
		ID:              "message-id",
		ContentMetadata: map[string]string{"OID": "private-oid"},
	})
	if privateSource.publicPath != "" || privateSource.oid != "private-oid" || privateSource.isPlainMedia {
		t.Fatalf("private source = %#v", privateSource)
	}

	plainSource := lineImageDownloadSource(line.Message{ID: "message-id"})
	if plainSource.publicPath != "" || plainSource.oid != "message-id" || !plainSource.isPlainMedia {
		t.Fatalf("plain source = %#v", plainSource)
	}
}
