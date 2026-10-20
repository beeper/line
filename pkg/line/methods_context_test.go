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
	defer cancel()
	result := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		result <- call(client, ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Errorf("%s worker did not exit after cleanup cancellation", method)
		}
	})

	select {
	case <-requestStarted:
	case err := <-result:
		t.Fatalf("%s failed before reaching transport: %v", method, err)
	// Cold white-box signing happens before RoundTrip and is substantially
	// slower under -race. This is a setup bound, not the cancellation bound.
	case <-time.After(30 * time.Second):
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
