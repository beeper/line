package handlers

import (
	"context"
	"testing"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"

	"github.com/highesttt/matrix-line-messenger/pkg/line"
)

func TestOversizedMediaNotice(t *testing.T) {
	h := &Handler{Log: zerolog.Nop()}
	relatesTo := &event.RelatesTo{}
	converted := h.oversizedMediaNotice(BeeperMaxFileSize+1, "downloaded", relatesTo)
	if converted == nil || len(converted.Parts) != 1 {
		t.Fatalf("converted = %#v, want one notice part", converted)
	}

	part := converted.Parts[0]
	if part.Type != event.EventMessage {
		t.Fatalf("event type = %v, want %v", part.Type, event.EventMessage)
	}
	if part.Content == nil {
		t.Fatal("notice content is nil")
	}
	if part.Content.MsgType != event.MsgNotice {
		t.Fatalf("message type = %v, want %v", part.Content.MsgType, event.MsgNotice)
	}
	const expectedBody = "This file exceeds Beeper's 100MB file size limit. Open LINE to view it."
	if part.Content.Body != expectedBody {
		t.Fatalf("body = %q, want %q", part.Content.Body, expectedBody)
	}
	if part.Content.RelatesTo != relatesTo {
		t.Fatalf("relates_to = %#v, want original pointer %#v", part.Content.RelatesTo, relatesTo)
	}
}

func TestOversizedMediaNoticeAllowsLimitAndBelow(t *testing.T) {
	h := &Handler{Log: zerolog.Nop()}
	for _, size := range []int64{0, BeeperMaxFileSize - 1, BeeperMaxFileSize} {
		if converted := h.oversizedMediaNotice(size, "downloaded", nil); converted != nil {
			t.Fatalf("size %d converted = %#v, want nil", size, converted)
		}
	}
}

func TestOversizedMediaMetadataShortCircuitsAllMediaHandlers(t *testing.T) {
	h := &Handler{
		Log: zerolog.Nop(),
		NewClient: func() *line.Client {
			t.Fatal("NewClient was called for media declared over the size limit")
			return nil
		},
	}
	message := line.Message{
		ID: "message-id",
		ContentMetadata: map[string]string{
			"FILE_SIZE": "104857633",
		},
	}
	relatesTo := &event.RelatesTo{}

	tests := map[string]func() (*bridgev2.ConvertedMessage, error){
		"image": func() (*bridgev2.ConvertedMessage, error) {
			return h.ConvertImage(context.Background(), nil, nil, message, "", relatesTo)
		},
		"video": func() (*bridgev2.ConvertedMessage, error) {
			return h.ConvertVideo(context.Background(), nil, nil, message, "", relatesTo)
		},
		"audio and voice": func() (*bridgev2.ConvertedMessage, error) {
			return h.ConvertAudio(context.Background(), nil, nil, message, "", relatesTo)
		},
		"file": func() (*bridgev2.ConvertedMessage, error) {
			return h.ConvertFile(context.Background(), nil, nil, message, "", relatesTo)
		},
	}
	for name, convert := range tests {
		t.Run(name, func(t *testing.T) {
			converted, err := convert()
			if err != nil {
				t.Fatal(err)
			}
			if converted == nil || len(converted.Parts) != 1 || converted.Parts[0].Content.Body != oversizedFileBody {
				t.Fatalf("converted = %#v, want oversized notice", converted)
			}
		})
	}
}

func TestOversizedMediaMetadataFallsBackForMissingInvalidAndAllowedSizes(t *testing.T) {
	h := &Handler{Log: zerolog.Nop()}
	for name, metadata := range map[string]map[string]string{
		"missing":                     nil,
		"invalid":                     {"FILE_SIZE": "not-a-number"},
		"negative":                    {"FILE_SIZE": "-1"},
		"at limit":                    {"FILE_SIZE": "104857600"},
		"possible encrypted overhead": {"FILE_SIZE": "104857632"},
	} {
		t.Run(name, func(t *testing.T) {
			if converted := h.oversizedMediaNoticeFromMetadata(metadata, nil); converted != nil {
				t.Fatalf("converted = %#v, want normal download fallback", converted)
			}
		})
	}
}
