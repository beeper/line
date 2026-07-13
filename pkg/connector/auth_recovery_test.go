package connector

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/highesttt/matrix-line-messenger/pkg/line"
)

var (
	errAuthRequired = errors.New(`API error 400: {"code":10051,"message":"RESPONSE_ERROR","data":{"name":"TalkException","code":119,"reason":"Access token refresh required"}}`)
	errLoggedOut    = errors.New(`API error 400: {"code":10051,"message":"RESPONSE_ERROR","data":{"name":"TalkException","code":8,"reason":"V3_TOKEN_CLIENT_LOGGED_OUT"}}`)
	errSenderKey    = errors.New(`API error 400: {"code":10051,"message":"RESPONSE_ERROR","data":{"name":"TalkException","code":83,"reason":"invalid sender key"}}`)
	errNotMember    = errors.New(`API error 400: {"code":10051,"data":{"name":"TalkException","code":10,"reason":"not a member"}}`)
	errNetwork      = errors.New("request failed: dial tcp: i/o timeout")
)

func TestCallLineWithRecovery(t *testing.T) {
	tests := []struct {
		name          string
		callErrors    []error
		recoverErr    error
		wantCalls     int
		wantRecover   int
		wantErr       error
		wantErrPrefix string
		wantAuthError bool
	}{
		{
			name:       "success without recovery",
			callErrors: []error{nil},
			wantCalls:  1,
		},
		{
			name:        "non auth error is returned without recovery",
			callErrors:  []error{errNotMember},
			wantCalls:   1,
			wantRecover: 0,
			wantErr:     errNotMember,
		},
		{
			name:        "network error is returned without recovery",
			callErrors:  []error{errNetwork},
			wantCalls:   1,
			wantRecover: 0,
			wantErr:     errNetwork,
		},
		{
			name:        "auth error recovers and retries once",
			callErrors:  []error{errAuthRequired, nil},
			wantCalls:   2,
			wantRecover: 1,
		},
		{
			name:          "recovery failure is returned without retry",
			callErrors:    []error{errAuthRequired},
			recoverErr:    errors.New("refresh failed"),
			wantCalls:     1,
			wantRecover:   1,
			wantErrPrefix: "failed to recover token after LINE auth error",
			wantAuthError: true,
		},
		{
			name:        "retry auth error is not retried again",
			callErrors:  []error{errAuthRequired, errAuthRequired},
			wantCalls:   2,
			wantRecover: 1,
			wantErr:     errAuthRequired,
		},
		{
			name:          "retry non auth error is returned to caller",
			callErrors:    []error{errAuthRequired, errors.New("Extension does not support file upload")},
			wantCalls:     2,
			wantRecover:   1,
			wantErrPrefix: "Extension does not support file upload",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls int
			var recoveries int

			_, _, err := callLineWithRecovery(context.Background(), nil, lineCallDeps[struct{}]{
				newClient: func() *line.Client {
					return line.NewClient("token")
				},
				recover: func(context.Context) error {
					recoveries++
					return tt.recoverErr
				},
				isAuthError: line.IsAuthError,
				call: func(*line.Client) (struct{}, error) {
					err := tt.callErrors[calls]
					calls++
					return struct{}{}, err
				},
			})

			if calls != tt.wantCalls {
				t.Fatalf("calls = %d, want %d", calls, tt.wantCalls)
			}
			if recoveries != tt.wantRecover {
				t.Fatalf("recoveries = %d, want %d", recoveries, tt.wantRecover)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErrPrefix != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErrPrefix) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErrPrefix)
				}
			}
			if tt.wantAuthError && (!errors.Is(err, errAuthRequired) || !line.IsAuthError(err)) {
				t.Fatalf("err = %v, want original auth error to remain detectable", err)
			}
			if tt.wantErr == nil && tt.wantErrPrefix == "" && err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
		})
	}
}

func TestCallLineWithRecoveryReusesClientUntilRecovery(t *testing.T) {
	ctx := context.Background()
	initialClient := line.NewClient("initial")
	refreshedClient := line.NewClient("refreshed")
	var newClients int
	var calls []string

	client, _, err := callLineWithRecovery(ctx, initialClient, lineCallDeps[struct{}]{
		newClient: func() *line.Client {
			newClients++
			return refreshedClient
		},
		recover: func(context.Context) error {
			return nil
		},
		isAuthError: line.IsAuthError,
		call: func(client *line.Client) (struct{}, error) {
			calls = append(calls, client.AccessToken)
			if len(calls) == 1 {
				return struct{}{}, errAuthRequired
			}
			return struct{}{}, nil
		},
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if client != refreshedClient {
		t.Fatal("expected recovered client to be returned")
	}
	if newClients != 1 {
		t.Fatalf("new clients = %d, want 1", newClients)
	}
	if len(calls) != 2 || calls[0] != "initial" || calls[1] != "refreshed" {
		t.Fatalf("calls used clients %v, want [initial refreshed]", calls)
	}
}

func TestCallLineWithRecoveryUsesProvidedClientWithoutRecreating(t *testing.T) {
	ctx := context.Background()
	initialClient := line.NewClient("initial")
	var newClients int

	client, _, err := callLineWithRecovery(ctx, initialClient, lineCallDeps[struct{}]{
		newClient: func() *line.Client {
			newClients++
			return line.NewClient("unexpected")
		},
		recover:     func(context.Context) error { return nil },
		isAuthError: line.IsAuthError,
		call: func(client *line.Client) (struct{}, error) {
			if client.AccessToken != "initial" {
				t.Fatalf("client token = %q, want initial", client.AccessToken)
			}
			return struct{}{}, nil
		},
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if client != initialClient {
		t.Fatal("expected provided client to be returned")
	}
	if newClients != 0 {
		t.Fatalf("new clients = %d, want 0", newClients)
	}
}

func TestLineClientIsTokenErrorExcludesNonRecoverableErrors(t *testing.T) {
	lc := &LineClient{}
	if !lc.isTokenError(errAuthRequired) {
		t.Fatal("expected auth-required error to be classified as token error")
	}
	if lc.isTokenError(errLoggedOut) {
		t.Fatal("logged-out sessions must not trigger token recovery")
	}
	if lc.isTokenError(errSenderKey) {
		t.Fatal("invalid sender key sessions must not trigger token recovery")
	}
	lc.sessionInvalidated = true
	if lc.isTokenError(errAuthRequired) {
		t.Fatal("invalidated sessions must not trigger token recovery")
	}
	lc.sessionInvalidated = false
	if lc.isTokenError(line.ErrNoUsableE2EEGroupKey) {
		t.Fatal("E2EE group key errors must not trigger token recovery")
	}
	if lc.isTokenError(line.ErrNoUsableE2EEPublicKey) {
		t.Fatal("E2EE public key errors must not trigger token recovery")
	}
}

func TestRunTokenRecoverySkipsRecentRecovery(t *testing.T) {
	lc := &LineClient{recoverTime: time.Now()}
	var calls int

	err := lc.runTokenRecovery(context.Background(), func(context.Context) error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if calls != 0 {
		t.Fatalf("recovery calls = %d, want 0", calls)
	}
}

func TestRunTokenRecoveryRejectsInvalidatedSessionBeforeRecentRecovery(t *testing.T) {
	lc := &LineClient{recoverTime: time.Now(), sessionInvalidated: true}
	var calls int

	err := lc.runTokenRecovery(context.Background(), func(context.Context) error {
		calls++
		return nil
	})
	if !errors.Is(err, errLineSessionInvalidated) {
		t.Fatalf("err = %v, want errLineSessionInvalidated", err)
	}
	if calls != 0 {
		t.Fatalf("recovery calls = %d, want 0", calls)
	}
}

func TestRunTokenRecoveryRejectsSupersededClient(t *testing.T) {
	lc := &LineClient{}
	lc.retire()
	var calls int
	err := lc.runTokenRecovery(context.Background(), func(context.Context) error {
		calls++
		return nil
	})
	if !errors.Is(err, errLineClientSuperseded) {
		t.Fatalf("err = %v, want errLineClientSuperseded", err)
	}
	if calls != 0 {
		t.Fatalf("recovery calls = %d, want 0", calls)
	}
}

func TestRecoverTokenDoesNotReloginAfterForcedLogoutRefresh(t *testing.T) {
	lc := &LineClient{}
	var reloginCalls int
	err := lc.recoverTokenWith(
		context.Background(),
		func(context.Context) error { return errLoggedOut },
		func(context.Context) error {
			reloginCalls++
			return nil
		},
	)
	if !line.IsLoggedOut(err) {
		t.Fatalf("recoverTokenWith error = %v, want logged-out error", err)
	}
	if reloginCalls != 0 {
		t.Fatalf("relogin calls = %d, want 0", reloginCalls)
	}
}

func TestRecoverTokenDoesNotReloginAfterCancellation(t *testing.T) {
	lc := &LineClient{}
	ctx, cancel := context.WithCancel(context.Background())
	var reloginCalls int
	err := lc.recoverTokenWith(
		ctx,
		func(context.Context) error {
			cancel()
			return context.Canceled
		},
		func(context.Context) error {
			reloginCalls++
			return nil
		},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("recoverTokenWith error = %v, want context.Canceled", err)
	}
	if reloginCalls != 0 {
		t.Fatalf("relogin calls = %d, want 0", reloginCalls)
	}
}

func TestForcedLogoutWinsOverInFlightRecovery(t *testing.T) {
	lc := &LineClient{AccessToken: "old-token"}
	recoveryStarted := make(chan struct{})
	allowRecovery := make(chan struct{})
	recoveryDone := make(chan error, 1)
	go func() {
		recoveryDone <- lc.runTokenRecovery(context.Background(), func(context.Context) error {
			close(recoveryStarted)
			<-allowRecovery
			lc.setTokens("recovered-token", "")
			return nil
		})
	}()
	<-recoveryStarted

	logoutDone := make(chan struct{})
	go func() {
		lc.markLoggedOutByOtherClient(context.Background(), errLoggedOut)
		close(logoutDone)
	}()
	close(allowRecovery)

	if err := <-recoveryDone; err != nil {
		t.Fatalf("recovery returned error: %v", err)
	}
	select {
	case <-logoutDone:
	case <-time.After(time.Second):
		t.Fatal("forced logout did not complete after recovery")
	}
	if lc.hasAccessToken() {
		t.Fatal("in-flight recovery resurrected the invalidated session")
	}
	if !lc.isSessionInvalidated() {
		t.Fatal("session was not invalidated after in-flight recovery")
	}
}

func TestRunTokenRecoverySerializesConcurrentRecovery(t *testing.T) {
	var lc LineClient
	var calls int32
	started := make(chan struct{})
	release := make(chan struct{})

	recover := func(context.Context) error {
		if atomic.AddInt32(&calls, 1) == 1 {
			close(started)
			<-release
		}
		return nil
	}

	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- lc.runTokenRecovery(context.Background(), recover)
		}()
	}

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first recovery did not start")
	}

	close(release)
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("recovery calls = %d, want 1", got)
	}
}
