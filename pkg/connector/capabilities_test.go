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
