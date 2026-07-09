package line

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestListenSSENonOKIncludesResponseBody(t *testing.T) {
	oldClient := sseHTTPClient
	t.Cleanup(func() {
		sseHTTPClient = oldClient
	})

	sseHTTPClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusUnauthorized,
				Header:     make(http.Header),
				Body: io.NopCloser(strings.NewReader(
					`{"code":10051,"message":"RESPONSE_ERROR","data":{"name":"TalkException","code":8,"reason":"V3_TOKEN_CLIENT_LOGGED_OUT"}}`,
				)),
			}, nil
		}),
	}

	err := NewClient("stale-token").ListenSSE(context.Background(), 0, func(event, data string) {})
	if err == nil {
		t.Fatal("expected SSE error")
	}
	if !strings.Contains(err.Error(), "SSE error: 401") {
		t.Fatalf("err = %v, want status code", err)
	}
	if !strings.Contains(err.Error(), "V3_TOKEN_CLIENT_LOGGED_OUT") {
		t.Fatalf("err = %v, want response body detail", err)
	}
}
