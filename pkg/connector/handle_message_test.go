package connector

import (
	"testing"

	"maunium.net/go/mautrix/event"

	"github.com/highesttt/matrix-line-messenger/pkg/line"
)

func TestDecryptMessageBodySkipsGeneratedFallbackWhenDecryptUnavailable(t *testing.T) {
	msg := &line.Message{
		Text:   "",
		Chunks: []string{"encrypted"},
	}

	bodyText, unwrappedText, decryptionFailed := (&LineClient{}).decryptMessageBody(msg, "chat-mid", int(OpReceiveMessage))
	if bodyText != "" || unwrappedText != "" || !decryptionFailed {
		t.Fatalf("decryptMessageBody returned body=%q unwrapped=%q failed=%v, want empty strings and failed=true", bodyText, unwrappedText, decryptionFailed)
	}
}

func TestDecryptMessageBodyTreatsLineFallbackAsEncryptedFailure(t *testing.T) {
	msg := &line.Message{
		Text:   lineDecryptFallbackText,
		Chunks: []string{"encrypted"},
	}

	bodyText, unwrappedText, decryptionFailed := (&LineClient{}).decryptMessageBody(msg, "chat-mid", int(OpReceiveMessage))
	if bodyText != "" || unwrappedText != "" || !decryptionFailed {
		t.Fatalf("decryptMessageBody returned body=%q unwrapped=%q failed=%v, want empty strings and failed=true", bodyText, unwrappedText, decryptionFailed)
	}
}

func TestDecryptMessageBodyKeepsFallbackTextWithoutEncryptedChunks(t *testing.T) {
	msg := &line.Message{
		Text: lineDecryptFallbackText,
	}

	bodyText, unwrappedText, decryptionFailed := (&LineClient{}).decryptMessageBody(msg, "chat-mid", int(OpReceiveMessage))
	if bodyText != lineDecryptFallbackText || unwrappedText != lineDecryptFallbackText || decryptionFailed {
		t.Fatalf("decryptMessageBody returned body=%q unwrapped=%q failed=%v, want fallback text and failed=false", bodyText, unwrappedText, decryptionFailed)
	}
}

func TestConvertLineMessageReturnsNoticeForDecryptFailure(t *testing.T) {
	converted, err := (&LineClient{}).convertLineMessage(
		t.Context(),
		nil,
		nil,
		line.Message{ContentType: int(ContentText)},
		"",
		"",
		true,
	)
	if err != nil {
		t.Fatalf("convertLineMessage returned error: %v", err)
	}
	if converted == nil || len(converted.Parts) != 1 {
		t.Fatalf("convertLineMessage returned %#v, want one notice part", converted)
	}
	content := converted.Parts[0].Content
	if content.MsgType != event.MsgNotice {
		t.Fatalf("MsgType = %s, want %s", content.MsgType, event.MsgNotice)
	}
	if content.Body != lineDecryptFailureNoticeText {
		t.Fatalf("Body = %q, want %q", content.Body, lineDecryptFailureNoticeText)
	}
	if content.Body == lineDecryptFallbackText {
		t.Fatal("notice body must not reuse LINE's historical fallback text")
	}
}
