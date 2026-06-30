package handlers

import (
	"context"
	"errors"
	"testing"
)

func TestTryRecoverClientUsesShouldRecover(t *testing.T) {
	errAuth := errors.New("SSE error: 401")
	var recoverCalled bool

	h := &Handler{
		ShouldRecover: func(context.Context, error) bool {
			return false
		},
		IsLoggedOut: func(error) bool {
			return false
		},
		IsRefreshRequired: func(error) bool {
			return true
		},
		RecoverToken: func(context.Context) error {
			recoverCalled = true
			return nil
		},
	}

	client, ok := h.tryRecoverClient(context.Background(), errAuth)
	if ok || client != nil {
		t.Fatalf("tryRecoverClient returned client=%v ok=%v, want no recovery", client, ok)
	}
	if recoverCalled {
		t.Fatal("RecoverToken was called despite ShouldRecover returning false")
	}
}
