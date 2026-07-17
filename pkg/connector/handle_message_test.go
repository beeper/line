package connector

import (
	"io"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/highesttt/matrix-line-messenger/pkg/line"
)

func TestConvertLineMessagePreservesMentionsForSticonFallback(t *testing.T) {
	const placeholder = "\U00100084"
	text := "hello " + placeholder
	userMXID := id.UserID("@user:example.com")
	lc := &LineClient{
		Mid: "self-mid",
		UserLogin: &bridgev2.UserLogin{
			UserLogin: &database.UserLogin{UserMXID: userMXID},
			Bridge:    &bridgev2.Bridge{Log: zerolog.New(io.Discard)},
		},
	}
	data := line.Message{
		ContentType: int(ContentText),
		ContentMetadata: map[string]string{
			"MENTION": `{"MENTIONEES":[{"M":"self-mid","S":"0","E":"5"}]}`,
		},
	}

	converted, err := lc.convertLineMessage(t.Context(), nil, nil, data, text, text, false)
	if err != nil {
		t.Fatalf("convertLineMessage returned error: %v", err)
	}
	if converted == nil || len(converted.Parts) != 1 || converted.Parts[0].Content == nil {
		t.Fatalf("convertLineMessage returned %#v, want one message part", converted)
	}
	content := converted.Parts[0].Content
	if content.Body != "hello [Emoji]" {
		t.Fatalf("Body = %q, want cleaned sticon fallback", content.Body)
	}
	if content.Mentions == nil || len(content.Mentions.UserIDs) != 1 || content.Mentions.UserIDs[0] != userMXID {
		t.Fatalf("Mentions = %#v, want user %s", content.Mentions, userMXID)
	}
	if strings.Contains(content.FormattedBody, placeholder) {
		t.Fatalf("FormattedBody still contains LINE placeholder: %q", content.FormattedBody)
	}
}

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
