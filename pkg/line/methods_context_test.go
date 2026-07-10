package line

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func testRPCContextCancellation(t *testing.T, method string, call func(*Client, context.Context) error) {
	t.Helper()
	requestStarted := make(chan struct{})
	client := NewClient("valid-token")
	client.HTTPClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			close(requestStarted)
			<-req.Context().Done()
			return nil, req.Context().Err()
		}),
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- call(client, ctx)
	}()

	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatalf("%s request did not start", method)
	}
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("%s did not stop after context cancellation", method)
	}
}

func TestGetLastOpRevisionContextCancellation(t *testing.T) {
	testRPCContextCancellation(t, "getLastOpRevision", func(client *Client, ctx context.Context) error {
		_, err := client.GetLastOpRevisionContext(ctx)
		return err
	})
}

func TestGetProfileContextCancellation(t *testing.T) {
	testRPCContextCancellation(t, "getProfile", func(client *Client, ctx context.Context) error {
		_, err := client.GetProfileContext(ctx)
		return err
	})
}
