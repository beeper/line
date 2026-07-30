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
				recover: func(context.Context, *line.Client, error) (*line.Client, error) {
					recoveries++
					if tt.recoverErr != nil {
						return nil, tt.recoverErr
					}
					return line.NewClient("recovered"), nil
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
		recover: func(context.Context, *line.Client, error) (*line.Client, error) {
			return refreshedClient, nil
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
	if newClients != 0 {
		t.Fatalf("new clients = %d, want 0 because recovery returned the retry client", newClients)
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
		recover: func(context.Context, *line.Client, error) (*line.Client, error) {
			return line.NewClient("unexpected"), nil
		},
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

func TestLineClientIsTokenErrorClassifiesRecoverableErrors(t *testing.T) {
	lc := &LineClient{}
	if !lc.isTokenError(errAuthRequired) {
		t.Fatal("expected auth-required error to be classified as token error")
	}
	if !lc.isTokenError(errLoggedOut) {
		t.Fatal("logged-out sessions must reach source-aware auth handling")
	}
	if !lc.isTokenError(errSenderKey) {
		t.Fatal("invalid sender key sessions must reach source-aware auth handling")
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

func TestCallLineUsingRetriesStaleLogoutWithCurrentToken(t *testing.T) {
	lc := &LineClient{AccessToken: "current-token"}
	var calls []string
	client, err := lc.callLineUsing(context.Background(), line.NewClient("old-token"), func(client *line.Client) error {
		calls = append(calls, client.AccessToken)
		if client.AccessToken == "old-token" {
			return errLoggedOut
		}
		return nil
	})
	if err != nil {
		t.Fatalf("callLineUsing returned error: %v", err)
	}
	if client == nil || client.AccessToken != "current-token" {
		t.Fatalf("client = %#v, want current-token", client)
	}
	if len(calls) != 2 || calls[0] != "old-token" || calls[1] != "current-token" {
		t.Fatalf("call tokens = %v, want [old-token current-token]", calls)
	}
	if lc.isSessionInvalidated() {
		t.Fatal("stale logout invalidated the current session")
	}
}

func TestRecoveryLoggedOutErrorInvalidatesCurrentSession(t *testing.T) {
	oldRecover := recoverLineToken
	t.Cleanup(func() {
		recoverLineToken = oldRecover
	})
	recoverLineToken = func(*LineClient, context.Context) error {
		return errLoggedOut
	}

	lc := &LineClient{AccessToken: "current-token"}
	retryClient, err := lc.recoverClientAfterAuthError(context.Background(), line.NewClient("current-token"), errAuthRequired)
	if err != nil {
		t.Fatalf("recoverClientAfterAuthError returned error: %v", err)
	}
	if retryClient != nil {
		t.Fatalf("retry client = %#v, want nil", retryClient)
	}
	if lc.hasAccessToken() || !lc.isSessionInvalidated() {
		t.Fatal("logged-out recovery error did not invalidate the current session")
	}
}

func TestConcurrentOldTokenFailuresWaitForSingleRecovery(t *testing.T) {
	oldRecover := recoverLineToken
	t.Cleanup(func() {
		recoverLineToken = oldRecover
	})

	lc := &LineClient{AccessToken: "old-token"}
	recoveryStarted := make(chan struct{})
	allowRecovery := make(chan struct{})
	var recoveryCalls atomic.Int32
	recoverLineToken = func(lc *LineClient, ctx context.Context) error {
		return lc.runTokenRecovery(ctx, func(context.Context) error {
			if recoveryCalls.Add(1) == 1 {
				close(recoveryStarted)
			}
			<-allowRecovery
			lc.setTokens("new-token", "")
			return nil
		})
	}

	primaryDone := make(chan error, 1)
	go func() {
		_, err := lc.callLineUsing(context.Background(), line.NewClient("old-token"), func(client *line.Client) error {
			if client.AccessToken == "old-token" {
				return errAuthRequired
			}
			return nil
		})
		primaryDone <- err
	}()

	select {
	case <-recoveryStarted:
	case <-time.After(time.Second):
		t.Fatal("primary recovery did not start")
	}

	const staleCalls = 8
	var started sync.WaitGroup
	started.Add(staleCalls)
	staleDone := make(chan error, staleCalls)
	for range staleCalls {
		go func() {
			_, err := lc.callLineUsing(context.Background(), line.NewClient("old-token"), func(client *line.Client) error {
				if client.AccessToken == "old-token" {
					started.Done()
					return errLoggedOut
				}
				return nil
			})
			staleDone <- err
		}()
	}
	started.Wait()
	close(allowRecovery)

	if err := <-primaryDone; err != nil {
		t.Fatalf("primary call returned error: %v", err)
	}
	for range staleCalls {
		if err := <-staleDone; err != nil {
			t.Fatalf("stale call returned error: %v", err)
		}
	}
	if got := recoveryCalls.Load(); got != 1 {
		t.Fatalf("recovery calls = %d, want 1", got)
	}
	if lc.getAccessToken() != "new-token" || lc.isSessionInvalidated() {
		t.Fatal("concurrent stale failures clobbered the recovered session")
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

func TestStaleForcedLogoutDoesNotClobberInFlightRecovery(t *testing.T) {
	lc := &LineClient{AccessToken: "old-token"}
	runCtx, _, started := lc.beginRun(context.Background())
	if !started {
		t.Fatal("beginRun unexpectedly rejected startup")
	}
	defer lc.wg.Done()
	defer lc.cancelActiveRun()

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

	type recoveryResult struct {
		client *line.Client
		err    error
	}
	logoutDone := make(chan recoveryResult, 1)
	go func() {
		client, err := lc.recoverClientAfterAuthError(context.Background(), line.NewClient("old-token"), errLoggedOut)
		logoutDone <- recoveryResult{client: client, err: err}
	}()
	close(allowRecovery)

	if err := <-recoveryDone; err != nil {
		t.Fatalf("recovery returned error: %v", err)
	}
	select {
	case result := <-logoutDone:
		if result.err != nil {
			t.Fatalf("stale logout handling returned error: %v", result.err)
		}
		if result.client == nil || result.client.AccessToken != "recovered-token" {
			t.Fatalf("retry client = %#v, want recovered-token", result.client)
		}
	case <-time.After(time.Second):
		t.Fatal("stale logout handling did not complete after recovery")
	}
	if lc.getAccessToken() != "recovered-token" {
		t.Fatalf("access token = %q, want recovered-token", lc.getAccessToken())
	}
	if lc.isSessionInvalidated() {
		t.Fatal("stale logout invalidated the recovered session")
	}
	select {
	case <-runCtx.Done():
		t.Fatal("stale logout canceled the active run")
	default:
	}
}

func TestCurrentTokenLoggedOutErrorsInvalidateSession(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "client logged out", err: errLoggedOut},
		{name: "invalid sender key", err: errSenderKey},
		{name: "request need login", err: errors.New(`SSE error: 401: {"code":10004,"message":"REQUEST_NEED_LOGIN"}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lc := &LineClient{AccessToken: "current-token"}
			retryClient, err := lc.recoverClientAfterAuthError(context.Background(), line.NewClient("current-token"), tt.err)
			if err != nil {
				t.Fatalf("recoverClientAfterAuthError returned error: %v", err)
			}
			if retryClient != nil {
				t.Fatalf("retry client = %#v, want nil", retryClient)
			}
			if lc.hasAccessToken() || !lc.isSessionInvalidated() {
				t.Fatal("current-token logout did not invalidate the session")
			}
		})
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
