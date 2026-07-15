package handlers

import (
	"context"
	"errors"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"

	"github.com/highesttt/matrix-line-messenger/pkg/line"
)

func TestTryRecoverClientUsesShouldRecover(t *testing.T) {
	errAuth := errors.New("SSE error: 401")
	var recoverCalled bool

	h := &Handler{
		ShouldRecover: func(context.Context, error) bool {
			return false
		},
		IsLoggedOut: func(error) bool {
			return false
		},
		IsRefreshRequired: func(error) bool {
			return true
		},
		RecoverToken: func(context.Context) error {
			recoverCalled = true
			return nil
		},
	}

	client, ok := h.tryRecoverClient(context.Background(), errAuth)
	if ok || client != nil {
		t.Fatalf("tryRecoverClient returned client=%v ok=%v, want no recovery", client, ok)
	}
	if recoverCalled {
		t.Fatal("RecoverToken was called despite ShouldRecover returning false")
	}
}

func TestTryRecoverClientRecoversOBSObjectInfoUnauthorized(t *testing.T) {
	recoveredClient := line.NewClient("refreshed-token")
	var recoverCalled bool
	h := &Handler{
		ShouldRecover: func(_ context.Context, err error) bool {
			return line.IsUnauthorizedStatus(err)
		},
		IsLoggedOut: func(error) bool {
			return false
		},
		RecoverToken: func(context.Context) error {
			recoverCalled = true
			return nil
		},
		NewClient: func() *line.Client {
			return recoveredClient
		},
	}

	client, ok := h.tryRecoverClient(context.Background(), errors.New("OBS object info failed (401): unauthorized"))
	if !ok || client != recoveredClient {
		t.Fatalf("tryRecoverClient returned client=%v ok=%v, want refreshed client", client, ok)
	}
	if !recoverCalled {
		t.Fatal("RecoverToken was not called for OBS object-info 401")
	}
}

func TestMediaDownloadFailureOnlyMaterializesKnownExpiry(t *testing.T) {
	converted, err := mediaDownloadFailure("Image", line.ErrOBSObjectNotFound, nil)
	if err != nil {
		t.Fatal(err)
	}
	if converted == nil || len(converted.Parts) != 1 {
		t.Fatalf("converted = %#v, want one placeholder part", converted)
	}
	if converted.Parts[0].Content.MsgType != event.MsgNotice || converted.Parts[0].Content.Body != "[Image unavailable — LINE media expired before it could be bridged]" {
		t.Fatalf("placeholder content = %#v", converted.Parts[0].Content)
	}

	converted, err = mediaDownloadFailure("Image", line.ErrOBSEncodingIncomplete, nil)
	if converted != nil {
		t.Fatalf("converted transient failure = %#v, want nil", converted)
	}
	if !errors.Is(err, line.ErrOBSEncodingIncomplete) {
		t.Fatalf("err = %v, want ErrOBSEncodingIncomplete", err)
	}
	if !errors.Is(err, bridgev2.ErrIgnoringRemoteEvent) {
		t.Fatalf("err = %v, want ErrIgnoringRemoteEvent", err)
	}
}

func TestOBSTalkMetaMessageID(t *testing.T) {
	if got := obsTalkMetaMessageID("message-id", true); got != "" {
		t.Fatalf("plain media talk-meta ID = %q, want empty", got)
	}
	if got := obsTalkMetaMessageID("message-id", false); got != "message-id" {
		t.Fatalf("encrypted media talk-meta ID = %q", got)
	}
}
