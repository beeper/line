package connector

import (
	"testing"

	"maunium.net/go/mautrix/event"
)

func TestGIFPickerCapabilities(t *testing.T) {
	features := (&LineClient{}).GetCapabilities(t.Context(), nil)
	gifFeature := features.File[event.CapMsgGIF]
	if gifFeature == nil {
		t.Fatal("GIF picker capability missing")
	}
	for mimeType, want := range map[string]event.CapabilitySupportLevel{
		"image/gif":  event.CapLevelFullySupported,
		"video/mp4":  event.CapLevelPartialSupport,
		"video/webm": event.CapLevelPartialSupport,
	} {
		if got := gifFeature.GetMimeSupport(mimeType); got != want {
			t.Errorf("%s = %v, want %v", mimeType, got, want)
		}
	}
}
