package handlers

import (
	"testing"

	"github.com/highesttt/matrix-line-messenger/pkg/line"
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
