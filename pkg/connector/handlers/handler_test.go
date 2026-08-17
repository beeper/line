package handlers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"

	"github.com/highesttt/matrix-line-messenger/pkg/line"
)

func TestDecryptDownloadedMediaUsesBodyKey(t *testing.T) {
	ciphertext := []byte("ciphertext")
	plaintext := []byte("plaintext")
	var keys []string
	h := &Handler{DecryptMedia: func(data []byte, key string) ([]byte, error) {
		if !bytes.Equal(data, ciphertext) {
			t.Fatalf("decrypt input = %q, want ciphertext", data)
		}
		keys = append(keys, key)
		return plaintext, nil
	}}

	got, err := h.decryptDownloadedMedia(ciphertext, `{"keyMaterial":"body-key"}`, map[string]string{"ENC_KM": "metadata-key"}, "image")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("decrypted data = %q, want %q", got, plaintext)
	}
	if len(keys) != 1 || keys[0] != "body-key" {
		t.Fatalf("keys = %v, want body key only", keys)
	}
}

func TestDecryptDownloadedMediaFallsBackToENCKM(t *testing.T) {
	ciphertext := []byte("ciphertext")
	var keys []string
	h := &Handler{DecryptMedia: func(data []byte, key string) ([]byte, error) {
		if !bytes.Equal(data, ciphertext) {
			t.Fatalf("decrypt input = %q, want original ciphertext", data)
		}
		keys = append(keys, key)
		if key == "body-key" {
			return nil, errors.New("body key failed")
		}
		return []byte("metadata plaintext"), nil
	}}

	got, err := h.decryptDownloadedMedia(ciphertext, `{"keyMaterial":"body-key"}`, map[string]string{"ENC_KM": "metadata-key"}, "file")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "metadata plaintext" {
		t.Fatalf("decrypted data = %q", got)
	}
	if fmt.Sprint(keys) != "[body-key metadata-key]" {
		t.Fatalf("keys = %v, want body then metadata", keys)
	}
}

func TestDecryptDownloadedMediaPassesThroughPlainMedia(t *testing.T) {
	data := []byte("plain media")
	got, err := new(Handler).decryptDownloadedMedia(data, "", nil, "audio")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("data = %q, want unchanged media", got)
	}
}

func TestDecryptDownloadedMediaRejectsEmptyDeclaredKey(t *testing.T) {
	got, err := new(Handler).decryptDownloadedMedia([]byte("ciphertext"), `{"keyMaterial":""}`, nil, "image")
	if got != nil {
		t.Fatalf("data = %q, want no ciphertext returned", got)
	}
	if !errors.Is(err, bridgev2.ErrIgnoringRemoteEvent) {
		t.Fatalf("err = %v, want ErrIgnoringRemoteEvent", err)
	}
}

func TestDecryptDownloadedMediaFailsClosed(t *testing.T) {
	ciphertext := []byte("ciphertext")
	decryptErr := errors.New("invalid media key")
	var calls int
	h := &Handler{DecryptMedia: func([]byte, string) ([]byte, error) {
		calls++
		return nil, decryptErr
	}}

	got, err := h.decryptDownloadedMedia(ciphertext, `{"keyMaterial":"body-key"}`, map[string]string{"ENC_KM": "metadata-key"}, "video")
	if got != nil {
		t.Fatalf("data = %q, want no ciphertext returned", got)
	}
	if calls != 2 {
		t.Fatalf("decrypt calls = %d, want both declared keys attempted", calls)
	}
	if !errors.Is(err, decryptErr) || !errors.Is(err, bridgev2.ErrIgnoringRemoteEvent) {
		t.Fatalf("err = %v, want decrypt error and ErrIgnoringRemoteEvent", err)
	}
}

func TestTryRecoverClientPassesOriginatingClient(t *testing.T) {
	errAuth := errors.New("SSE error: 401")
	var recoverCalled bool
	failedClient := line.NewClient("failed-token")

	h := &Handler{
		RecoverClient: func(_ context.Context, client *line.Client, err error) (*line.Client, error) {
			recoverCalled = true
			if client != failedClient {
				t.Fatalf("failed client = %#v, want originating client", client)
			}
			if !errors.Is(err, errAuth) {
				t.Fatalf("auth error = %v, want %v", err, errAuth)
			}
			return nil, nil
		},
	}

	client, ok := h.tryRecoverClient(context.Background(), failedClient, errAuth)
	if ok || client != nil {
		t.Fatalf("tryRecoverClient returned client=%v ok=%v, want no recovery", client, ok)
	}
	if !recoverCalled {
		t.Fatal("RecoverClient was not called")
	}
}

func TestTryRecoverClientRecoversOBSObjectInfoUnauthorized(t *testing.T) {
	recoveredClient := line.NewClient("refreshed-token")
	failedClient := line.NewClient("expired-token")
	var recoverCalled bool
	h := &Handler{
		RecoverClient: func(_ context.Context, client *line.Client, err error) (*line.Client, error) {
			recoverCalled = true
			if client != failedClient {
				t.Fatalf("failed client = %#v, want originating client", client)
			}
			if !line.IsUnauthorizedStatus(err) {
				t.Fatalf("error = %v, want unauthorized status", err)
			}
			return recoveredClient, nil
		},
	}

	client, ok := h.tryRecoverClient(context.Background(), failedClient, errors.New("OBS object info failed (401): unauthorized"))
	if !ok || client != recoveredClient {
		t.Fatalf("tryRecoverClient returned client=%v ok=%v, want refreshed client", client, ok)
	}
	if !recoverCalled {
		t.Fatal("RecoverClient was not called for OBS object-info 401")
	}
}

func TestHandleFinalAuthErrorKeepsRetrySourceClient(t *testing.T) {
	errLoggedOut := errors.New("V3_TOKEN_CLIENT_LOGGED_OUT")
	retryClient := line.NewClient("retry-token")
	var calls int
	h := &Handler{
		RecoverClient: func(_ context.Context, client *line.Client, err error) (*line.Client, error) {
			calls++
			if client != retryClient {
				t.Fatalf("failed client = %#v, want retry client", client)
			}
			if !errors.Is(err, errLoggedOut) {
				t.Fatalf("error = %v, want logged-out error", err)
			}
			return nil, nil
		},
	}

	h.handleFinalAuthError(context.Background(), retryClient, errLoggedOut)
	if calls != 1 {
		t.Fatalf("RecoverClient calls = %d, want 1", calls)
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
