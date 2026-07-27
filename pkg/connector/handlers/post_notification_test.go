package handlers

import (
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"

	"github.com/highesttt/matrix-line-messenger/pkg/line"
)

func TestConvertPostNotification(t *testing.T) {
	relatesTo := &event.RelatesTo{}
	tests := []struct {
		name     string
		metadata map[string]string
		expected string
	}{
		{
			name: "note with multiline preview and link",
			metadata: map[string]string{
				"serviceType": "GB",
				"text":        "First line\nSecond line",
				"postEndUrl":  "https://line.me/R/group/home/posts/post?example=1",
			},
			expected: "You received a LINE note.\n\nPreview:\nFirst line\nSecond line\n\n" +
				"Open in LINE: https://line.me/R/group/home/posts/post?example=1",
		},
		{
			name: "album with name and deep link",
			metadata: map[string]string{
				"serviceType": "AB",
				"albumName":   "Summer photos",
				"postEndUrl":  "line://group/home/albums/album?example=1",
			},
			expected: "LINE album update: Summer photos\n\n" +
				"Open in LINE: line://group/home/albums/album?example=1",
		},
		{
			name:     "missing metadata",
			metadata: nil,
			expected: "You received a LINE post notification.\n\nOpen LINE for full details.",
		},
		{
			name: "unknown service uses available preview",
			metadata: map[string]string{
				"serviceType": "OTHER",
				"text":        "Post preview",
			},
			expected: "You received a LINE post notification.\n\nPreview:\nPost preview\n\n" +
				"Open LINE for full details.",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			converted, err := (&Handler{}).ConvertPostNotification(line.Message{
				ContentMetadata: test.metadata,
			}, relatesTo)
			if err != nil {
				t.Fatalf("ConvertPostNotification returned error: %v", err)
			}
			assertPostNotificationContent(t, converted, test.expected, relatesTo)
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
	if part.Content.RelatesTo != relatesTo {
		t.Fatalf("relates_to = %#v, want original pointer %#v", part.Content.RelatesTo, relatesTo)
	}
}
