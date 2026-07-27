package handlers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/highesttt/matrix-line-messenger/pkg/line"
)

type postNotificationTestMatrix struct {
	bridgev2.MatrixAPI
	uploads [][]byte
}

func (m *postNotificationTestMatrix) UploadMedia(_ context.Context, _ id.RoomID, data []byte, _, _ string) (id.ContentURIString, *event.EncryptedFileInfo, error) {
	m.uploads = append(m.uploads, append([]byte(nil), data...))
	return id.ContentURIString(fmt.Sprintf("mxc://example/album-%d", len(m.uploads))), nil, nil
}

func TestConvertPostNotification(t *testing.T) {
	relatesTo := &event.RelatesTo{}
	tests := []struct {
		name     string
		metadata map[string]string
		expected string
	}{
		{
			name: "note with multiline preview ignores link",
			metadata: map[string]string{
				"serviceType": "GB",
				"text":        "First line\nSecond line",
				"postEndUrl":  "https://line.me/R/group/home/posts/post?example=1",
			},
			expected: "You received a LINE note.\n\nPreview:\nFirst line\nSecond line",
		},
		{
			name: "album with name ignores deep link",
			metadata: map[string]string{
				"serviceType": "AB",
				"albumName":   "Summer photos",
				"postEndUrl":  "line://group/home/albums/album?example=1&source=chat",
			},
			expected: "LINE album update: Summer photos",
		},
		{
			name:     "missing metadata",
			metadata: nil,
			expected: "You received a LINE post notification.",
		},
		{
			name: "unknown service uses available preview",
			metadata: map[string]string{
				"serviceType": "OTHER",
				"text":        "Post preview",
			},
			expected: "You received a LINE post notification.\n\nPreview:\nPost preview",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			converted, err := (&Handler{}).ConvertPostNotification(
				t.Context(),
				nil,
				nil,
				line.Message{ContentMetadata: test.metadata},
				relatesTo,
			)
			if err != nil {
				t.Fatalf("ConvertPostNotification returned error: %v", err)
			}
			assertPostNotificationContent(t, converted, test.expected, relatesTo)
		})
	}
}

func TestConvertPostNotificationUploadsAlbumPreviewImages(t *testing.T) {
	relatesTo := &event.RelatesTo{}
	matrix := &postNotificationTestMatrix{}
	var downloads []string
	handler := &Handler{
		NewClient: func() *line.Client {
			return line.NewClient("token")
		},
		DownloadOBSResource: func(_ context.Context, _ *line.Client, service, sid, oid string) ([]byte, error) {
			downloads = append(downloads, service+"/"+sid+"/"+oid)
			return []byte{0xff, 0xd8, 0xff, byte(len(downloads))}, nil
		},
	}
	converted, err := handler.ConvertPostNotification(
		t.Context(),
		&bridgev2.Portal{Portal: &database.Portal{MXID: id.RoomID("!room:example.com")}},
		matrix,
		line.Message{
			ID: "message-id",
			ContentMetadata: map[string]string{
				"serviceType": "AB",
				"albumName":   "Summer photos",
				"previewMedias": `[
					{"svc":"album","sid":"a","mediaOid":"oid-one","mediaType":"I"},
					{"svc":"album","sid":"a","mediaOid":"oid-two","mediaType":"I"},
					{"svc":"album","sid":"a","mediaOid":"oid-one","mediaType":"I"},
					{"svc":"album","sid":"a","mediaOid":"video-oid","mediaType":"V"}
				]`,
				"mediaOid":  "oid-one",
				"mediaType": "I",
			},
		},
		relatesTo,
	)
	if err != nil {
		t.Fatalf("ConvertPostNotification returned error: %v", err)
	}
	if got := strings.Join(downloads, ","); got != "album/a/oid-one,album/a/oid-two" {
		t.Fatalf("downloads = %q", got)
	}
	if len(converted.Parts) != 3 {
		t.Fatalf("parts = %d, want notice plus two images", len(converted.Parts))
	}
	assertPostNotificationContent(t, &bridgev2.ConvertedMessage{Parts: converted.Parts[:1]}, "LINE album update: Summer photos", relatesTo)
	for index, part := range converted.Parts[1:] {
		wantNumber := index + 1
		if part.ID != networkid.PartID(fmt.Sprintf("album-image-%d", wantNumber)) {
			t.Fatalf("image %d part ID = %q", wantNumber, part.ID)
		}
		if part.Type != event.EventMessage || part.Content.MsgType != event.MsgImage {
			t.Fatalf("image %d content = %#v", wantNumber, part.Content)
		}
		if part.Content.Body != fmt.Sprintf("album-image-%d.jpg", wantNumber) {
			t.Fatalf("image %d body = %q", wantNumber, part.Content.Body)
		}
		if part.Content.URL != id.ContentURIString(fmt.Sprintf("mxc://example/album-%d", wantNumber)) {
			t.Fatalf("image %d URL = %q", wantNumber, part.Content.URL)
		}
		if part.Content.Info == nil || part.Content.Info.MimeType != "image/jpeg" || part.Content.Info.Size != 4 {
			t.Fatalf("image %d info = %#v", wantNumber, part.Content.Info)
		}
		if part.Content.RelatesTo != relatesTo {
			t.Fatalf("image %d relates_to = %#v", wantNumber, part.Content.RelatesTo)
		}
	}
	if len(matrix.uploads) != 2 {
		t.Fatalf("uploads = %d, want 2", len(matrix.uploads))
	}
}

func TestConvertPostNotificationUsesTopLevelAlbumMediaFallback(t *testing.T) {
	matrix := &postNotificationTestMatrix{}
	var downloadedOID string
	handler := &Handler{
		NewClient: func() *line.Client {
			return line.NewClient("token")
		},
		DownloadOBSResource: func(_ context.Context, _ *line.Client, _, _, oid string) ([]byte, error) {
			downloadedOID = oid
			return []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, nil
		},
	}
	converted, err := handler.ConvertPostNotification(
		t.Context(),
		&bridgev2.Portal{Portal: &database.Portal{MXID: id.RoomID("!room:example.com")}},
		matrix,
		line.Message{ContentMetadata: map[string]string{
			"serviceType":   "AB",
			"previewMedias": `{broken`,
			"mediaOid":      "fallback-oid",
			"mediaType":     "I",
		}},
		nil,
	)
	if err != nil {
		t.Fatalf("ConvertPostNotification returned error: %v", err)
	}
	if downloadedOID != "fallback-oid" {
		t.Fatalf("downloaded OID = %q, want fallback-oid", downloadedOID)
	}
	if len(converted.Parts) != 2 || converted.Parts[1].Content.Info.MimeType != "image/png" {
		t.Fatalf("converted = %#v, want notice and PNG fallback", converted)
	}
}

func TestConvertPostNotificationKeepsSuccessfulAlbumImagesWhenOneExpired(t *testing.T) {
	matrix := &postNotificationTestMatrix{}
	handler := &Handler{
		NewClient: func() *line.Client {
			return line.NewClient("token")
		},
		IsLoggedOut: func(error) bool {
			return false
		},
		ShouldRecover: func(context.Context, error) bool {
			return false
		},
		DownloadOBSResource: func(_ context.Context, _ *line.Client, _, _, oid string) ([]byte, error) {
			if oid == "expired-oid" {
				return nil, line.ErrOBSObjectNotFound
			}
			return []byte{'G', 'I', 'F', '8', '9', 'a'}, nil
		},
	}
	converted, err := handler.ConvertPostNotification(
		t.Context(),
		&bridgev2.Portal{Portal: &database.Portal{MXID: id.RoomID("!room:example.com")}},
		matrix,
		line.Message{ContentMetadata: map[string]string{
			"serviceType": "AB",
			"previewMedias": `[
				{"svc":"album","sid":"a","mediaOid":"expired-oid","mediaType":"I"},
				{"svc":"album","sid":"a","mediaOid":"available-oid","mediaType":"I"}
			]`,
		}},
		nil,
	)
	if err != nil {
		t.Fatalf("ConvertPostNotification returned error: %v", err)
	}
	if len(converted.Parts) != 2 {
		t.Fatalf("parts = %d, want notice plus available image", len(converted.Parts))
	}
	if converted.Parts[1].ID != "album-image-2" || converted.Parts[1].Content.Info.MimeType != "image/gif" {
		t.Fatalf("available image part = %#v", converted.Parts[1])
	}
}

func TestConvertPostNotificationLeavesTransientAlbumFailureRetryable(t *testing.T) {
	handler := &Handler{
		NewClient: func() *line.Client {
			return line.NewClient("token")
		},
		IsLoggedOut: func(error) bool {
			return false
		},
		ShouldRecover: func(context.Context, error) bool {
			return false
		},
		DownloadOBSResource: func(context.Context, *line.Client, string, string, string) ([]byte, error) {
			return nil, line.ErrOBSEncodingIncomplete
		},
	}
	converted, err := handler.ConvertPostNotification(
		t.Context(),
		&bridgev2.Portal{Portal: &database.Portal{MXID: id.RoomID("!room:example.com")}},
		&postNotificationTestMatrix{},
		line.Message{ContentMetadata: map[string]string{
			"serviceType":   "AB",
			"previewMedias": `[{"svc":"album","sid":"a","mediaOid":"pending-oid","mediaType":"I"}]`,
		}},
		nil,
	)
	if converted != nil {
		t.Fatalf("converted = %#v, want nil for retryable failure", converted)
	}
	if !errors.Is(err, line.ErrOBSEncodingIncomplete) || !errors.Is(err, bridgev2.ErrIgnoringRemoteEvent) {
		t.Fatalf("err = %v, want encoding and ignoring sentinels", err)
	}
}

func assertPostNotificationContent(t *testing.T, converted *bridgev2.ConvertedMessage, expectedBody string, relatesTo *event.RelatesTo) {
	t.Helper()
	if converted == nil || len(converted.Parts) != 1 || converted.Parts[0].Content == nil {
		t.Fatalf("converted = %#v, want one message part", converted)
	}
	part := converted.Parts[0]
	if part.Type != event.EventMessage {
		t.Fatalf("event type = %v, want %v", part.Type, event.EventMessage)
	}
	if part.Content.MsgType != event.MsgNotice {
		t.Fatalf("message type = %v, want %v", part.Content.MsgType, event.MsgNotice)
	}
	if part.Content.Body != expectedBody {
		t.Fatalf("body = %q, want %q", part.Content.Body, expectedBody)
	}
	if part.Content.Format != "" || part.Content.FormattedBody != "" {
		t.Fatalf("formatted message = %q / %q, want plain text only", part.Content.Format, part.Content.FormattedBody)
	}
	if part.Content.RelatesTo != relatesTo {
		t.Fatalf("relates_to = %#v, want original pointer %#v", part.Content.RelatesTo, relatesTo)
	}
}
