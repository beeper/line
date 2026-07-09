package connector

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"

	"github.com/highesttt/matrix-line-messenger/pkg/line"
)

func TestPollLoopRebuildsSSEClientAfterReconnect(t *testing.T) {
	oldGetLastOpRevision := getLastOpRevisionWithClient
	oldListenSSE := listenSSEWithClient
	oldReconnectDelay := sseReconnectDelay
	t.Cleanup(func() {
		getLastOpRevisionWithClient = oldGetLastOpRevision
		listenSSEWithClient = oldListenSSE
		sseReconnectDelay = oldReconnectDelay
	})

	getLastOpRevisionWithClient = func(*line.Client) (int64, error) {
		return 1234, nil
	}
	sseReconnectDelay = time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	lc := &LineClient{
		AccessToken: "old",
		UserLogin: &bridgev2.UserLogin{
			Bridge: &bridgev2.Bridge{Log: zerolog.New(io.Discard)},
		},
	}

	var tokens []string
	listenSSEWithClient = func(client *line.Client, ctx context.Context, localRev int64, handler func(eventType, data string)) error {
		if localRev != 1234 {
			t.Fatalf("localRev = %d, want 1234", localRev)
		}
		tokens = append(tokens, client.AccessToken)
		if len(tokens) == 1 {
			lc.setTokens("new", "")
			return io.EOF
		}
		cancel()
		return context.Canceled
	}

	lc.wg.Add(1)
	lc.pollLoop(ctx)

	if len(tokens) != 2 {
		t.Fatalf("SSE attempts = %d, want 2", len(tokens))
	}
	if tokens[0] != "old" || tokens[1] != "new" {
		t.Fatalf("SSE tokens = %v, want [old new]", tokens)
	}
}

func TestPollLoopMarksLoggedOutWhenReceiveAuthFails(t *testing.T) {
	oldGetLastOpRevision := getLastOpRevisionWithClient
	oldListenSSE := listenSSEWithClient
	oldGetProfile := getProfileWithToken
	t.Cleanup(func() {
		getLastOpRevisionWithClient = oldGetLastOpRevision
		listenSSEWithClient = oldListenSSE
		getProfileWithToken = oldGetProfile
	})

	getLastOpRevisionWithClient = func(*line.Client) (int64, error) {
		return 1234, nil
	}

	var profileCalls int
	getProfileWithToken = func(token string) (*line.Profile, error) {
		profileCalls++
		if token != "stale" {
			t.Fatalf("profile token = %q, want stale", token)
		}
		return nil, errLoggedOut
	}

	listenSSEWithClient = func(client *line.Client, ctx context.Context, localRev int64, handler func(eventType, data string)) error {
		if client.AccessToken != "stale" {
			t.Fatalf("SSE client token = %q, want stale", client.AccessToken)
		}
		return errors.New("SSE error: 401")
	}

	lc := &LineClient{
		AccessToken: "stale",
		UserLogin: &bridgev2.UserLogin{
			Bridge: &bridgev2.Bridge{Log: zerolog.New(io.Discard)},
		},
	}

	lc.wg.Add(1)
	lc.pollLoop(context.Background())

	if profileCalls != 1 {
		t.Fatalf("profile calls = %d, want 1", profileCalls)
	}
	if lc.hasAccessToken() {
		t.Fatal("access token was not invalidated after receive auth logout")
	}
	if !lc.isSessionInvalidated() {
		t.Fatal("session was not marked invalidated after receive auth logout")
	}
}

func TestPollLoopMarksLoggedOutWhenReceiveIdleProbeFailsLoggedOut(t *testing.T) {
	oldGetLastOpRevision := getLastOpRevisionWithClient
	oldListenSSE := listenSSEWithClient
	t.Cleanup(func() {
		getLastOpRevisionWithClient = oldGetLastOpRevision
		listenSSEWithClient = oldListenSSE
	})

	var revisionCalls int
	getLastOpRevisionWithClient = func(client *line.Client) (int64, error) {
		if client.AccessToken != "stale" {
			t.Fatalf("revision probe token = %q, want stale", client.AccessToken)
		}
		revisionCalls++
		if revisionCalls == 1 {
			return 1234, nil
		}
		return 0, errLoggedOut
	}

	listenSSEWithClient = func(client *line.Client, ctx context.Context, localRev int64, handler func(eventType, data string)) error {
		if client.AccessToken != "stale" {
			t.Fatalf("SSE client token = %q, want stale", client.AccessToken)
		}
		if localRev != 1234 {
			t.Fatalf("localRev = %d, want 1234", localRev)
		}
		return line.ErrSSEIdleTimeout
	}

	lc := &LineClient{
		AccessToken: "stale",
		UserLogin: &bridgev2.UserLogin{
			Bridge: &bridgev2.Bridge{Log: zerolog.New(io.Discard)},
		},
	}

	lc.wg.Add(1)
	lc.pollLoop(context.Background())

	if revisionCalls != 2 {
		t.Fatalf("revision calls = %d, want 2", revisionCalls)
	}
	if lc.hasAccessToken() {
		t.Fatal("access token was not invalidated after receive idle logout")
	}
	if !lc.isSessionInvalidated() {
		t.Fatal("session was not marked invalidated after receive idle logout")
	}
}

func TestPollLoopReconnectsWhenReceiveIdleProbeSucceeds(t *testing.T) {
	oldGetLastOpRevision := getLastOpRevisionWithClient
	oldListenSSE := listenSSEWithClient
	oldReconnectDelay := sseReconnectDelay
	t.Cleanup(func() {
		getLastOpRevisionWithClient = oldGetLastOpRevision
		listenSSEWithClient = oldListenSSE
		sseReconnectDelay = oldReconnectDelay
	})

	var revisionCalls int
	getLastOpRevisionWithClient = func(client *line.Client) (int64, error) {
		if client.AccessToken != "valid" {
			t.Fatalf("revision probe token = %q, want valid", client.AccessToken)
		}
		revisionCalls++
		return 1234, nil
	}
	sseReconnectDelay = time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var listenCalls int
	listenSSEWithClient = func(client *line.Client, ctx context.Context, localRev int64, handler func(eventType, data string)) error {
		if client.AccessToken != "valid" {
			t.Fatalf("SSE client token = %q, want valid", client.AccessToken)
		}
		if localRev != 1234 {
			t.Fatalf("localRev = %d, want 1234", localRev)
		}
		listenCalls++
		if listenCalls == 1 {
			return line.ErrSSEIdleTimeout
		}
		cancel()
		return context.Canceled
	}

	lc := &LineClient{
		AccessToken: "valid",
		UserLogin: &bridgev2.UserLogin{
			Bridge: &bridgev2.Bridge{Log: zerolog.New(io.Discard)},
		},
	}

	lc.wg.Add(1)
	lc.pollLoop(ctx)

	if revisionCalls != 2 {
		t.Fatalf("revision calls = %d, want 2", revisionCalls)
	}
	if listenCalls != 2 {
		t.Fatalf("SSE attempts = %d, want 2", listenCalls)
	}
	if !lc.hasAccessToken() {
		t.Fatal("valid access token was invalidated")
	}
	if lc.isSessionInvalidated() {
		t.Fatal("valid session was marked invalidated")
	}
}

func TestReceiveRequestNeedLoginMarksLoggedOutImmediately(t *testing.T) {
	oldGetProfile := getProfileWithToken
	t.Cleanup(func() {
		getProfileWithToken = oldGetProfile
	})

	getProfileWithToken = func(token string) (*line.Profile, error) {
		t.Fatal("REQUEST_NEED_LOGIN should be handled without probing profile")
		return nil, nil
	}

	lc := &LineClient{AccessToken: "stale"}
	stopped := lc.handleReceiveAuthError(context.Background(), errors.New(`SSE error: 401: {"code":10004,"message":"REQUEST_NEED_LOGIN"}`))

	if !stopped {
		t.Fatal("receive auth handler should stop on REQUEST_NEED_LOGIN")
	}
	if lc.hasAccessToken() {
		t.Fatal("access token was not invalidated")
	}
	if !lc.isSessionInvalidated() {
		t.Fatal("session was not marked invalidated")
	}
}

func TestReceiveAuthErrorWithValidProfileDoesNotRecover(t *testing.T) {
	oldGetProfile := getProfileWithToken
	t.Cleanup(func() {
		getProfileWithToken = oldGetProfile
	})

	var profileCalls int
	getProfileWithToken = func(token string) (*line.Profile, error) {
		profileCalls++
		if token != "valid" {
			t.Fatalf("profile token = %q, want valid", token)
		}
		return &line.Profile{}, nil
	}

	lc := &LineClient{AccessToken: "valid"}
	stopped := lc.handleReceiveAuthError(context.Background(), errors.New("SSE error: 401"))

	if stopped {
		t.Fatal("receive auth handler should reconnect without stopping when the profile probe succeeds")
	}
	if profileCalls != 1 {
		t.Fatalf("profile calls = %d, want 1", profileCalls)
	}
	if !lc.hasAccessToken() {
		t.Fatal("valid access token was invalidated")
	}
	if lc.isSessionInvalidated() {
		t.Fatal("valid session was marked invalidated")
	}
}
