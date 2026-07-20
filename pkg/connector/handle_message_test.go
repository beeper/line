package connector

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/highesttt/matrix-line-messenger/pkg/line"
)

type inlineEmojiRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn inlineEmojiRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

type inlineEmojiTestMatrix struct {
	bridgev2.MatrixAPI
	uploadCount int
}

func (m *inlineEmojiTestMatrix) UploadMedia(_ context.Context, _ id.RoomID, _ []byte, _, _ string) (id.ContentURIString, *event.EncryptedFileInfo, error) {
	m.uploadCount++
	return "mxc://example/custom-emoji", nil, nil
}

func TestConvertLineMessageRendersPlaintextCustomEmoji(t *testing.T) {
	const (
		emtver4Token = "\U00100101\U00100211yoo-hoo\U0010ffff"
		productID    = "0123456789abcdef01234567"
	)
	tests := []struct {
		name         string
		text         string
		metadata     map[string]string
		failFirstURL bool
		expectedURLs []string
	}{
		{
			name: "REPLACE metadata",
			text: "hey (yoo-hoo)",
			metadata: map[string]string{
				"REPLACE": `{"sticon":{"resources":[{"S":4,"E":13,"productId":"` + productID + `","sticonId":"456","version":1,"resourceType":"ANIMATION"}]}}`,
			},
			failFirstURL: true,
			expectedURLs: []string{
				"https://stickershop.line-scdn.net/sticonshop/v1/sticon/" + productID + "/android/456_animation.png",
				"https://stickershop.line-scdn.net/sticonshop/v1/sticon/" + productID + "/android/456.png",
			},
		},
		{
			name:     "EMTVER4 text",
			text:     "hey " + emtver4Token,
			metadata: map[string]string{},
			expectedURLs: []string{
				"https://stickershop.line-scdn.net/sticon/v1/7/3/2/732e276ec85f14e6/android/100211_k.png",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var requestedURLs []string
			httpClient := &http.Client{Transport: inlineEmojiRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				requestedURLs = append(requestedURLs, req.URL.String())
				header := make(http.Header)
				header.Set("Content-Type", "image/png")
				statusCode := http.StatusOK
				body := "png"
				if test.failFirstURL && len(requestedURLs) == 1 {
					statusCode = http.StatusNotFound
					body = ""
				}
				return &http.Response{
					StatusCode: statusCode,
					Header:     header,
					Body:       io.NopCloser(strings.NewReader(body)),
					Request:    req,
				}, nil
			})}
			matrix := &inlineEmojiTestMatrix{}
			lc := &LineClient{
				HTTPClient: httpClient,
				UserLogin: &bridgev2.UserLogin{
					Bridge: &bridgev2.Bridge{Log: zerolog.New(io.Discard)},
				},
			}
			data := line.Message{
				ContentType:     int(ContentText),
				ContentMetadata: test.metadata,
			}

			converted, err := lc.convertLineMessage(t.Context(), nil, matrix, data, test.text, test.text, false)
			if err != nil {
				t.Fatalf("convertLineMessage returned error: %v", err)
			}
			if converted == nil || len(converted.Parts) != 1 || converted.Parts[0].Content == nil {
				t.Fatalf("convertLineMessage returned %#v, want one message part", converted)
			}
			content := converted.Parts[0].Content
			if content.Body != "hey (yoo-hoo)" {
				t.Fatalf("Body = %q, want custom emoji fallback text", content.Body)
			}
			if content.Format != event.FormatHTML || !strings.Contains(content.FormattedBody, `<img data-mx-emoticon src="mxc://example/custom-emoji"`) {
				t.Fatalf("formatted message = %q / %q, want inline custom emoji", content.Format, content.FormattedBody)
			}
			if len(requestedURLs) != len(test.expectedURLs) {
				t.Fatalf("requested URLs = %#v, want %#v", requestedURLs, test.expectedURLs)
			}
			for i := range test.expectedURLs {
				if requestedURLs[i] != test.expectedURLs[i] {
					t.Fatalf("requested URLs = %#v, want %#v", requestedURLs, test.expectedURLs)
				}
			}
			if matrix.uploadCount != 1 {
				t.Fatalf("UploadMedia call count = %d, want 1", matrix.uploadCount)
			}
		})
	}
}

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
