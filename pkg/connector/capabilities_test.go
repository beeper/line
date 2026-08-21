package connector

import (
	"context"
	"testing"

	"maunium.net/go/mautrix/event"
)

func TestCapabilitiesAdvertiseFileSizeLimit(t *testing.T) {
	const wantMaxFileSize int64 = 100 * 1024 * 1024
	caps := (&LineClient{}).GetCapabilities(context.Background(), nil)
	for _, messageType := range []event.CapabilityMsgType{
		event.MsgImage,
		event.MsgFile,
		event.MsgVideo,
		event.MsgAudio,
		event.CapMsgVoice,
		event.CapMsgGIF,
	} {
		features := caps.File[messageType]
		if features == nil {
			t.Fatalf("%s file features are missing", messageType)
		}
		if features.MaxSize != wantMaxFileSize {
			t.Errorf("%s MaxSize = %d, want %d", messageType, features.MaxSize, wantMaxFileSize)
		}
	}
}

func TestCapabilitiesAdvertiseRawGIFSupport(t *testing.T) {
	caps := (&LineClient{}).GetCapabilities(context.Background(), nil)
	features := caps.File[event.CapMsgGIF]
	if features == nil {
		t.Fatal("GIF file features are missing")
	}
	if got := features.MimeTypes["image/gif"]; got != event.CapLevelFullySupported {
		t.Fatalf("image/gif support = %v, want %v", got, event.CapLevelFullySupported)
	}
	if len(features.MimeTypes) != 1 {
		t.Fatalf("GIF MIME type count = %d, want 1", len(features.MimeTypes))
	}
}
