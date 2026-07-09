package line

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
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

func TestListenSSEIdleTimeout(t *testing.T) {
	oldClient := sseHTTPClient
	oldTimeout := sseIdleTimeout
	t.Cleanup(func() {
		sseHTTPClient = oldClient
		sseIdleTimeout = oldTimeout
	})

	sseIdleTimeout = 10 * time.Millisecond
	pr, pw := io.Pipe()
	t.Cleanup(func() {
		_ = pw.Close()
	})

	sseHTTPClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       pr,
			}, nil
		}),
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err := NewClient("stale-token").ListenSSE(ctx, 0, func(event, data string) {})
	if !errors.Is(err, ErrSSEIdleTimeout) {
		t.Fatalf("err = %v, want ErrSSEIdleTimeout", err)
	}
}
