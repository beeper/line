package line

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestSendMessageRejectsMissingAcknowledgement(t *testing.T) {
	for _, body := range []string{`{"code":0}`, `{"code":0,"data":null}`, `{"code":0,"data":{}}`} {
		t.Run(body, func(t *testing.T) {
			client := NewClient("test-token")
			client.HTTPClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			msg, err := client.SendMessage(1, &Message{To: "u0123456789abcdef0123456789abcdef", Text: "test"})
			if err == nil || msg != nil {
				t.Fatalf("missing acknowledgement accepted: %+v, %v", msg, err)
			}
		})
	}
}

func TestSendMessagePreservesAuthenticationError(t *testing.T) {
	client := NewClient("test-token")
	client.HTTPClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"code":10051,"message":"RESPONSE_ERROR","data":{"name":"TalkException","code":119,"reason":"Access token refresh required"}}`))}, nil
	})
	_, err := client.SendMessage(1, &Message{To: "u0123456789abcdef0123456789abcdef", Text: "test"})
	if !IsAuthError(err) {
		t.Fatalf("send error lost auth classification: %v", err)
	}
}
