package connector

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

func TestLineClientDisconnectCancelsActiveRun(t *testing.T) {
	lc := &LineClient{}
	runCtx, _, started := lc.beginRun(context.Background())
	if !started {
		t.Fatal("beginRun unexpectedly rejected startup")
	}
	workerStarted := make(chan struct{})
	lc.wg.Add(1)
	go func() {
		defer lc.wg.Done()
		close(workerStarted)
		<-runCtx.Done()
	}()
	<-workerStarted

	disconnected := make(chan struct{})
	go func() {
		lc.Disconnect()
		close(disconnected)
	}()
	select {
	case <-runCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("Disconnect did not cancel the active run")
	}
	select {
	case <-disconnected:
		t.Fatal("Disconnect returned before the Connect reservation was released")
	default:
	}
	lc.wg.Done() // release the reservation owned by the simulated Connect call

	select {
	case <-disconnected:
	case <-time.After(time.Second):
		t.Fatal("Disconnect did not cancel and join the active run")
	}
	if !errors.Is(runCtx.Err(), context.Canceled) {
		t.Fatalf("run context error = %v, want context.Canceled", runCtx.Err())
	}
}

func TestLineClientDisconnectBeforeConnectRejectsStartup(t *testing.T) {
	lc := &LineClient{}
	lc.Disconnect()
	lc.recoverMu.Lock()
	recoveryStopped := lc.recoveryStopped
	lc.recoverMu.Unlock()
	if !recoveryStopped {
		t.Fatal("Disconnect did not stop token recovery")
	}
	runCtx, _, started := lc.beginRun(context.Background())
	if started {
		lc.wg.Done()
		t.Fatal("beginRun started after Disconnect completed")
	}
	if !errors.Is(runCtx.Err(), context.Canceled) {
		t.Fatalf("rejected run context error = %v, want context.Canceled", runCtx.Err())
	}
}

func TestCreateLoginSharesFinalizationLock(t *testing.T) {
	connector := &LineConnector{}
	firstProcess, err := connector.CreateLogin(context.Background(), nil, "")
	if err != nil {
		t.Fatalf("first CreateLogin returned error: %v", err)
	}
	secondProcess, err := connector.CreateLogin(context.Background(), nil, "")
	if err != nil {
		t.Fatalf("second CreateLogin returned error: %v", err)
	}
	first := firstProcess.(*LineEmailLogin)
	second := secondProcess.(*LineEmailLogin)
	if first.finalizeMu == nil || first.finalizeMu != second.finalizeMu || first.finalizeMu != &connector.loginFinalizeMu {
		t.Fatal("login processes do not share the connector finalization lock")
	}
}

func TestFindReusedLineLogin(t *testing.T) {
	logins := make(map[string]*bridgev2.UserLogin, 3)
	for _, loginID := range []string{"override", "target", "unrelated"} {
		logins[loginID] = &bridgev2.UserLogin{
			UserLogin: &database.UserLogin{ID: networkid.UserLoginID(loginID)},
		}
	}
	tests := []struct {
		name       string
		overrideID string
		detectedID string
		wantID     string
	}{
		{
			name:       "ordinary same-ID login",
			detectedID: "target",
			wantID:     "target",
		},
		{
			name:       "ordinary new account",
			detectedID: "new-account",
		},
		{
			name:       "same-ID override",
			overrideID: "target",
			detectedID: "target",
			wantID:     "target",
		},
		{
			name:       "override resolves to another existing account",
			overrideID: "override",
			detectedID: "target",
			wantID:     "target",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var override *bridgev2.UserLogin
			if tc.overrideID != "" {
				override = logins[tc.overrideID]
			}
			got := findReusedLineLogin(
				override,
				[]*bridgev2.UserLogin{logins["override"], logins["target"], logins["unrelated"]},
				networkid.UserLoginID(tc.detectedID),
			)
			if tc.wantID == "" {
				if got != nil {
					t.Fatalf("reused login = %s, want nil", got.ID)
				}
			} else if got != logins[tc.wantID] {
				t.Fatalf("reused login = %p, want %p (%s)", got, logins[tc.wantID], tc.wantID)
			}
		})
	}
}

func TestSameIDReplacementRetiresOldClientBeforeMetadataChange(t *testing.T) {
	oldMeta := &UserLoginMetadata{AccessToken: "old-token", Mid: "target"}
	login := &bridgev2.UserLogin{
		UserLogin: &database.UserLogin{
			ID:       "target",
			Metadata: oldMeta,
		},
		Bridge: &bridgev2.Bridge{Log: zerolog.New(io.Discard)},
	}
	stale := &LineClient{AccessToken: "old-token", UserLogin: login}
	login.Client = stale

	retireLineLogin(login)
	if !stale.superseded.Load() {
		t.Fatal("same-ID client was not retired before metadata replacement")
	}
	freshMeta := &UserLoginMetadata{AccessToken: "fresh-token", Mid: "target"}
	login.Metadata = freshMeta
	fresh := &LineClient{AccessToken: "fresh-token", UserLogin: login}
	login.Client = fresh
	if login.Client != fresh {
		t.Fatal("replacement client was not published")
	}

	stale.markLoggedOutByOtherClient(context.Background(), errLoggedOut)

	if freshMeta.AccessToken != "fresh-token" || freshMeta.SessionInvalidated {
		t.Fatalf("retired client changed replacement metadata: %#v", freshMeta)
	}
}

func TestForcedLogoutCancelsActiveRun(t *testing.T) {
	lc := &LineClient{AccessToken: "stale"}
	runCtx, _, started := lc.beginRun(context.Background())
	if !started {
		t.Fatal("beginRun unexpectedly rejected startup")
	}
	defer lc.wg.Done()

	lc.markLoggedOutByOtherClient(runCtx, errLoggedOut)

	select {
	case <-runCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("forced logout did not cancel the active run")
	}
	if lc.hasAccessToken() {
		t.Fatal("forced logout did not clear the access token")
	}
	if !lc.isSessionInvalidated() {
		t.Fatal("forced logout did not invalidate the session")
	}
}

func TestStaleForcedLogoutCancelsOnlyStaleClient(t *testing.T) {
	login := &bridgev2.UserLogin{
		Bridge: &bridgev2.Bridge{Log: zerolog.New(io.Discard)},
	}
	stale := &LineClient{AccessToken: "stale-token", UserLogin: login}
	current := &LineClient{AccessToken: "current-token", UserLogin: login}
	login.Client = current
	staleCtx, _, staleStarted := stale.beginRun(context.Background())
	currentCtx, _, currentStarted := current.beginRun(context.Background())
	if !staleStarted || !currentStarted {
		t.Fatal("beginRun unexpectedly rejected startup")
	}
	defer stale.wg.Done()
	defer current.wg.Done()
	defer current.cancelActiveRun()

	stale.markLoggedOutByOtherClient(staleCtx, errLoggedOut)

	select {
	case <-staleCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("stale client run was not canceled")
	}
	select {
	case <-currentCtx.Done():
		t.Fatal("stale client logout canceled the replacement client")
	default:
	}
	if current.getAccessToken() != "current-token" || current.isSessionInvalidated() {
		t.Fatal("stale client logout changed replacement session state")
	}
	if stale.getAccessToken() != "stale-token" || stale.isSessionInvalidated() {
		t.Fatal("stale client logout was not rejected by client ownership")
	}
}

func TestRetiredClientCannotRestart(t *testing.T) {
	lc := &LineClient{}
	lc.retire()
	if !lc.superseded.Load() {
		t.Fatal("retired client was not marked superseded")
	}
	runCtx, _, started := lc.beginRun(context.Background())
	if started {
		lc.wg.Done()
		t.Fatal("retired client accepted a new run")
	}
	if !errors.Is(runCtx.Err(), context.Canceled) {
		t.Fatalf("retired run context error = %v, want context.Canceled", runCtx.Err())
	}
}

func TestRepeatedForcedLogoutStillInvalidatesAndCancels(t *testing.T) {
	lc := &LineClient{
		AccessToken:      "stale-token",
		forcedLogoutSent: true,
		UserLogin: &bridgev2.UserLogin{
			Bridge: &bridgev2.Bridge{Log: zerolog.New(io.Discard)},
		},
	}
	runCtx, _, started := lc.beginRun(context.Background())
	if !started {
		t.Fatal("beginRun unexpectedly rejected startup")
	}
	defer lc.wg.Done()

	lc.markLoggedOutByOtherClient(runCtx, errLoggedOut)

	if lc.hasAccessToken() || !lc.isSessionInvalidated() {
		t.Fatal("repeated forced logout did not keep the session invalidated")
	}
	select {
	case <-runCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("repeated forced logout did not cancel the active run")
	}
}

func TestConnectedStateRejectedAfterSessionInvalidation(t *testing.T) {
	lc := &LineClient{AccessToken: "stale-token", sessionInvalidated: true}
	if lc.sendConnectedStateIfCurrent(context.Background()) {
		t.Fatal("invalidated session was allowed to emit CONNECTED")
	}
}

func TestClaimForcedLogoutStateOnlyOnce(t *testing.T) {
	lc := &LineClient{}
	var claims atomic.Int32
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if lc.claimForcedLogoutState() {
				claims.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := claims.Load(); got != 1 {
		t.Fatalf("forced logout state claims = %d, want 1", got)
	}
}
