package handlers

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/highesttt/matrix-line-messenger/pkg/line"
)

type postNotificationTestMatrix struct {
	bridgev2.MatrixAPI
	mu      sync.Mutex
	uploads [][]byte
}

func (m *postNotificationTestMatrix) UploadMedia(_ context.Context, _ id.RoomID, data []byte, fileName, _ string) (id.ContentURIString, *event.EncryptedFileInfo, error) {
	m.mu.Lock()
	m.uploads = append(m.uploads, append([]byte(nil), data...))
	m.mu.Unlock()
	return id.ContentURIString("mxc://example/" + fileName), nil, nil
}

func (m *postNotificationTestMatrix) uploadCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.uploads)
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
	var downloadsMu sync.Mutex
	handler := &Handler{
		NewClient: func() *line.Client {
			return line.NewClient("token")
		},
		DownloadAlbumPreview: func(_ context.Context, _ *line.Client, oid, chatID, albumID string) ([]byte, error) {
			downloadsMu.Lock()
			downloads = append(downloads, chatID+"/"+albumID+"/"+oid)
			downloadNumber := len(downloads)
			downloadsMu.Unlock()
			return []byte{0xff, 0xd8, 0xff, byte(downloadNumber)}, nil
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
				"chatId":      "chat-id",
				"postEndUrl":  "line://group/home/albums/album?albumId=legacy-id&albumIdV2=album-id-v2",
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
	downloadsMu.Lock()
	slices.Sort(downloads)
	if got := strings.Join(downloads, ","); got != "chat-id/album-id-v2/oid-one,chat-id/album-id-v2/oid-two" {
		downloadsMu.Unlock()
		t.Fatalf("downloads = %q", got)
	}
	downloadsMu.Unlock()
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
		if part.Content.URL != id.ContentURIString(fmt.Sprintf("mxc://example/album-image-%d.jpg", wantNumber)) {
			t.Fatalf("image %d URL = %q", wantNumber, part.Content.URL)
		}
		if part.Content.Info == nil || part.Content.Info.MimeType != "image/jpeg" || part.Content.Info.Size != 4 {
			t.Fatalf("image %d info = %#v", wantNumber, part.Content.Info)
		}
		if part.Content.RelatesTo != relatesTo {
			t.Fatalf("image %d relates_to = %#v", wantNumber, part.Content.RelatesTo)
		}
	}
	if got := matrix.uploadCount(); got != 2 {
		t.Fatalf("uploads = %d, want 2", got)
	}
}

func TestConvertPostNotificationProcessesAlbumPreviewsConcurrentlyInOrder(t *testing.T) {
	const previewCount = 6

	matrix := &postNotificationTestMatrix{}
	started := make(chan string, previewCount)
	release := make(map[string]chan struct{}, previewCount)
	var active atomic.Int32
	var peak atomic.Int32
	for index := 1; index <= previewCount; index++ {
		release[fmt.Sprintf("oid-%d", index)] = make(chan struct{})
	}

	handler := &Handler{
		NewClient: func() *line.Client {
			return line.NewClient("token")
		},
		DownloadAlbumPreview: func(ctx context.Context, _ *line.Client, oid, _, _ string) ([]byte, error) {
			current := active.Add(1)
			defer active.Add(-1)
			for {
				previousPeak := peak.Load()
				if current <= previousPeak || peak.CompareAndSwap(previousPeak, current) {
					break
				}
			}
			started <- oid
			select {
			case <-release[oid]:
				return []byte{0xff, 0xd8, 0xff, byte(len(oid))}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	}

	result := make(chan *bridgev2.ConvertedMessage, 1)
	errs := make(chan error, 1)
	go func() {
		converted, err := handler.ConvertPostNotification(
			t.Context(),
			&bridgev2.Portal{Portal: &database.Portal{MXID: id.RoomID("!room:example.com")}},
			matrix,
			line.Message{ContentMetadata: map[string]string{
				"serviceType": "AB",
				"chatId":      "chat-id",
				"previewMedias": `[
					{"svc":"album","sid":"a","mediaOid":"oid-1","mediaType":"I"},
					{"svc":"album","sid":"a","mediaOid":"oid-2","mediaType":"I"},
					{"svc":"album","sid":"a","mediaOid":"oid-3","mediaType":"I"},
					{"svc":"album","sid":"a","mediaOid":"oid-4","mediaType":"I"},
					{"svc":"album","sid":"a","mediaOid":"oid-5","mediaType":"I"},
					{"svc":"album","sid":"a","mediaOid":"oid-6","mediaType":"I"}
				]`,
			}},
			nil,
		)
		result <- converted
		errs <- err
	}()

	initialStarted := make(map[string]struct{}, albumPreviewWorkerLimit)
	for range albumPreviewWorkerLimit {
		select {
		case oid := <-started:
			initialStarted[oid] = struct{}{}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for concurrent album preview workers")
		}
	}
	if len(initialStarted) != albumPreviewWorkerLimit {
		t.Fatalf("initial workers started %d unique previews, want %d", len(initialStarted), albumPreviewWorkerLimit)
	}
	select {
	case oid := <-started:
		t.Fatalf("preview %q started before a worker slot was released", oid)
	case <-time.After(100 * time.Millisecond):
	}

	close(release["oid-4"])
	select {
	case oid := <-started:
		if oid != "oid-5" {
			t.Fatalf("next preview = %q, want oid-5", oid)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for fifth album preview")
	}
	close(release["oid-3"])
	select {
	case oid := <-started:
		if oid != "oid-6" {
			t.Fatalf("next preview = %q, want oid-6", oid)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for sixth album preview")
	}
	close(release["oid-1"])
	close(release["oid-2"])
	close(release["oid-5"])
	close(release["oid-6"])

	var converted *bridgev2.ConvertedMessage
	select {
	case converted = <-result:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for album conversion")
	}
	if err := <-errs; err != nil {
		t.Fatalf("ConvertPostNotification returned error: %v", err)
	}
	if got := peak.Load(); got != albumPreviewWorkerLimit {
		t.Fatalf("peak concurrent preview jobs = %d, want %d", got, albumPreviewWorkerLimit)
	}
	if len(converted.Parts) != previewCount+1 {
		t.Fatalf("parts = %d, want notice plus %d images", len(converted.Parts), previewCount)
	}
	for index, part := range converted.Parts[1:] {
		wantNumber := index + 1
		if part.ID != networkid.PartID(fmt.Sprintf("album-image-%d", wantNumber)) {
			t.Fatalf("part %d ID = %q", wantNumber, part.ID)
		}
		wantURL := id.ContentURIString(fmt.Sprintf("mxc://example/album-image-%d.jpg", wantNumber))
		if part.Content.URL != wantURL {
			t.Fatalf("part %d URL = %q, want %q", wantNumber, part.Content.URL, wantURL)
		}
	}
}

func TestConvertPostNotificationCancelsQueuedAlbumPreviewsAfterFailure(t *testing.T) {
	var calls atomic.Int32
	handler := &Handler{
		NewClient: func() *line.Client {
			return line.NewClient("token")
		},
		DownloadAlbumPreview: func(ctx context.Context, _ *line.Client, oid, _, _ string) ([]byte, error) {
			calls.Add(1)
			if oid == "fatal-oid" {
				return nil, line.ErrOBSEncodingIncomplete
			}
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}

	converted, err := handler.ConvertPostNotification(
		t.Context(),
		&bridgev2.Portal{Portal: &database.Portal{MXID: id.RoomID("!room:example.com")}},
		&postNotificationTestMatrix{},
		line.Message{ContentMetadata: map[string]string{
			"serviceType": "AB",
			"chatId":      "chat-id",
			"previewMedias": `[
				{"svc":"album","sid":"a","mediaOid":"fatal-oid","mediaType":"I"},
				{"svc":"album","sid":"a","mediaOid":"oid-2","mediaType":"I"},
				{"svc":"album","sid":"a","mediaOid":"oid-3","mediaType":"I"},
				{"svc":"album","sid":"a","mediaOid":"oid-4","mediaType":"I"},
				{"svc":"album","sid":"a","mediaOid":"oid-5","mediaType":"I"},
				{"svc":"album","sid":"a","mediaOid":"oid-6","mediaType":"I"}
			]`,
		}},
		nil,
	)
	if converted != nil {
		t.Fatalf("converted = %#v, want nil after fatal preview failure", converted)
	}
	if !errors.Is(err, line.ErrOBSEncodingIncomplete) || !errors.Is(err, bridgev2.ErrIgnoringRemoteEvent) {
		t.Fatalf("err = %v, want encoding and ignoring sentinels", err)
	}
	if got := calls.Load(); got > albumPreviewWorkerLimit {
		t.Fatalf("download calls = %d, want at most %d after cancellation", got, albumPreviewWorkerLimit)
	}
}

func TestConvertPostNotificationUsesTopLevelAlbumMediaFallback(t *testing.T) {
	matrix := &postNotificationTestMatrix{}
	var downloadedOID string
	handler := &Handler{
		NewClient: func() *line.Client {
			return line.NewClient("token")
		},
		DownloadAlbumPreview: func(_ context.Context, _ *line.Client, oid, _, _ string) ([]byte, error) {
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
			"chatId":        "chat-id",
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
		DownloadAlbumPreview: func(_ context.Context, _ *line.Client, oid, _, _ string) ([]byte, error) {
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
			"chatId":      "chat-id",
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
		DownloadAlbumPreview: func(context.Context, *line.Client, string, string, string) ([]byte, error) {
			return nil, line.ErrOBSEncodingIncomplete
		},
	}
	converted, err := handler.ConvertPostNotification(
		t.Context(),
		&bridgev2.Portal{Portal: &database.Portal{MXID: id.RoomID("!room:example.com")}},
		&postNotificationTestMatrix{},
		line.Message{ContentMetadata: map[string]string{
			"serviceType":   "AB",
			"chatId":        "chat-id",
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

func TestConvertPostNotificationKeepsNoticeWhenAlbumChatIDIsMissing(t *testing.T) {
	var downloads atomic.Int32
	handler := &Handler{
		DownloadAlbumPreview: func(context.Context, *line.Client, string, string, string) ([]byte, error) {
			downloads.Add(1)
			return nil, errors.New("unexpected download")
		},
	}

	converted, err := handler.ConvertPostNotification(
		t.Context(),
		nil,
		nil,
		line.Message{ContentMetadata: map[string]string{
			"serviceType":   "AB",
			"albumName":     "Missing metadata",
			"previewMedias": `[{"svc":"album","sid":"a","mediaOid":"preview-oid","mediaType":"I"}]`,
		}},
		nil,
	)
	if err != nil {
		t.Fatalf("ConvertPostNotification returned error: %v", err)
	}
	assertPostNotificationContent(t, converted, "LINE album update: Missing metadata", nil)
	if downloads.Load() != 0 {
		t.Fatalf("downloads = %d, want zero without chatId", downloads.Load())
	}
}

func TestParseAlbumPreviewContext(t *testing.T) {
	tests := []struct {
		name     string
		metadata map[string]string
		want     albumPreviewContext
	}{
		{
			name: "prefers v2 album ID",
			metadata: map[string]string{
				"chatId":     " chat-id ",
				"postEndUrl": "line://group/home/albums/album?albumId=legacy-id&albumIdV2=v2-id",
			},
			want: albumPreviewContext{ChatID: "chat-id", AlbumID: "v2-id"},
		},
		{
			name: "falls back to legacy album ID",
			metadata: map[string]string{
				"chatId":     "chat-id",
				"postEndUrl": "line://group/home/albums/album?albumId=legacy-id",
			},
			want: albumPreviewContext{ChatID: "chat-id", AlbumID: "legacy-id"},
		},
		{
			name:     "keeps chat ID without URL",
			metadata: map[string]string{"chatId": "chat-id"},
			want:     albumPreviewContext{ChatID: "chat-id"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := parseAlbumPreviewContext(test.metadata); got != test.want {
				t.Fatalf("context = %#v, want %#v", got, test.want)
			}
		})
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
