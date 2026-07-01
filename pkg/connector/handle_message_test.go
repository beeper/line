package connector

import (
	"testing"

	"github.com/highesttt/matrix-line-messenger/pkg/line"
)

func TestDecryptMessageBodySkipsGeneratedFallbackWhenDecryptUnavailable(t *testing.T) {
	msg := &line.Message{
		Text:   "",
		Chunks: []string{"encrypted"},
	}

	bodyText, unwrappedText := (&LineClient{}).decryptMessageBody(msg, "chat-mid", int(OpReceiveMessage))
	if bodyText != "" || unwrappedText != "" {
		t.Fatalf("decryptMessageBody returned body=%q unwrapped=%q, want empty strings", bodyText, unwrappedText)
	}
}

func TestDecryptMessageBodyTreatsLineFallbackAsEncryptedFailure(t *testing.T) {
	msg := &line.Message{
		Text:   lineDecryptFallbackText,
		Chunks: []string{"encrypted"},
	}

	bodyText, unwrappedText := (&LineClient{}).decryptMessageBody(msg, "chat-mid", int(OpReceiveMessage))
	if bodyText != "" || unwrappedText != "" {
		t.Fatalf("decryptMessageBody returned body=%q unwrapped=%q, want empty strings", bodyText, unwrappedText)
	}
}

func TestDecryptMessageBodyKeepsFallbackTextWithoutEncryptedChunks(t *testing.T) {
	msg := &line.Message{
		Text: lineDecryptFallbackText,
	}

	bodyText, unwrappedText := (&LineClient{}).decryptMessageBody(msg, "chat-mid", int(OpReceiveMessage))
	if bodyText != lineDecryptFallbackText || unwrappedText != lineDecryptFallbackText {
		t.Fatalf("decryptMessageBody returned body=%q unwrapped=%q, want fallback text", bodyText, unwrappedText)
	}
}
