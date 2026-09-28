package connector

import (
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/bridgeconfig"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/status"

	"github.com/rs/zerolog"

	"github.com/highesttt/matrix-line-messenger/pkg/line"
)

func TestInvalidSenderKeyReconnectPersistsFullVerification(t *testing.T) {
	for _, mode := range []string{"ordinary", "invalid-key", "late-invalid-key", "late-stale-key"} {
		t.Run(mode, func(t *testing.T) {
			invalidKey := mode == "invalid-key" || mode == "late-invalid-key"
			ctx := context.Background()
			rawDB, err := dbutil.NewWithDialect("file:"+filepath.ToSlash(filepath.Join(t.TempDir(), "bridge.db")), "sqlite3")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = rawDB.Close() })
			connector := &LineConnector{}
			db := database.New("test", connector.GetDBMetaTypes(), rawDB)
			if err := db.Upgrade(ctx); err != nil {
				t.Fatal(err)
			}
			if err := db.User.Insert(ctx, &database.User{MXID: "@test:example.com"}); err != nil {
				t.Fatal(err)
			}
			stored := &database.UserLogin{ID: "test", UserMXID: "@test:example.com", Metadata: &UserLoginMetadata{
				AccessToken: "token", Email: "test@example.com", Password: "password", Certificate: "certificate",
				ExportedKeyMap: map[string]string{"1": "preserved-key"},
			}}
			if err := db.UserLogin.Insert(ctx, stored); err != nil {
				t.Fatal(err)
			}
			matrix := &bridgeStateTestMatrix{states: make(chan status.BridgeState, 2)}
			bridge := &bridgev2.Bridge{DB: db, Log: zerolog.New(io.Discard), Matrix: matrix,
				Config: &bridgeconfig.BridgeConfig{BridgeStatusNotices: "none"}, BackgroundCtx: ctx, Network: connector}
			login := &bridgev2.UserLogin{UserLogin: stored, Bridge: bridge,
				User: &bridgev2.User{User: &database.User{MXID: stored.UserMXID}, Bridge: bridge}}
			login.BridgeState = bridge.NewBridgeStateQueue(login)
			t.Cleanup(login.BridgeState.Destroy)
			lc := &LineClient{AccessToken: "token", UserLogin: login}
			login.Client = lc
			failure := errLoggedOut
			if mode == "invalid-key" {
				failure = errSenderKey
			}
			recovered := make(chan error, 1)
			if mode == "invalid-key" {
				lc.missingE2EEKeyMu.Lock()
			}
			go func() {
				_, err := lc.recoverClientAfterAuthError(ctx, line.NewClient("token"), failure)
				recovered <- err
			}()
			if mode == "invalid-key" {
				select {
				case <-recovered:
					lc.missingE2EEKeyMu.Unlock()
					t.Fatal("key invalidation did not wait for the E2EE metadata lock")
				case <-time.After(100 * time.Millisecond):
				}
				lc.missingE2EEKeyMu.Unlock()
			}
			if err := <-recovered; err != nil {
				t.Fatal(err)
			}
			if mode == "late-invalid-key" || mode == "late-stale-key" {
				if !lc.isTokenError(errSenderKey) {
					t.Fatal("late sender-key response would skip auth recovery")
				}
				lateCtx, cancel := context.WithCancel(ctx)
				cancel()
				failedToken := "token"
				if mode == "late-stale-key" {
					failedToken = "older-token"
				}
				if _, err := lc.recoverClientAfterAuthError(lateCtx, line.NewClient(failedToken), errSenderKey); err != nil {
					t.Fatal(err)
				}
			}
			wantStateError := status.BridgeStateErrorCode("line-logged-out")
			if mode == "invalid-key" {
				wantStateError = "line-e2ee-key-missing"
			}
			select {
			case state := <-matrix.states:
				if state.StateEvent != status.StateBadCredentials || state.Error != wantStateError || state.UserAction != status.UserActionRelogin {
					t.Fatalf("unexpected reconnect state: %v", state)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("reconnect state was not delivered")
			}
			reloaded, err := db.UserLogin.GetByID(ctx, "test")
			if err != nil {
				t.Fatal(err)
			}
			meta := reloaded.Metadata.(*UserLoginMetadata)
			if !meta.SessionInvalidated || meta.AccessToken != "" || lc.hasAccessToken() {
				t.Fatal("failed session remained usable")
			}
			if meta.ForceFullE2EELogin != invalidKey {
				t.Fatalf("persisted full verification = %v, want %v", meta.ForceFullE2EELogin, invalidKey)
			}
			if meta.ExportedKeyMap["1"] != "preserved-key" {
				t.Fatal("existing key material was erased")
			}
			if shouldPreserveExistingE2EEKeys(false, meta) == invalidKey {
				t.Fatal("reconnect would reuse rejected keys or discard ordinary logout keys")
			}
			restarted := &bridgev2.UserLogin{UserLogin: reloaded, Bridge: bridge, User: login.User}
			restarted.BridgeState = bridge.NewBridgeStateQueue(restarted)
			t.Cleanup(restarted.BridgeState.Destroy)
			if err := connector.LoadUserLogin(ctx, restarted); err != nil {
				t.Fatal(err)
			}
			restarted.Client.(*LineClient).Connect(ctx)
			select {
			case state := <-matrix.states:
				wantRestartError := status.BridgeStateErrorCode("line-logged-out")
				wantRestartMessage := "LINE logged this Chrome Extension session out because another LINE client connected. Click Reconnect in Beeper to reconnect LINE."
				if invalidKey {
					wantRestartError = "line-e2ee-key-missing"
					wantRestartMessage = lineMissingE2EEKeyMessage
				}
				if state.StateEvent != status.StateBadCredentials || state.Error != wantRestartError || state.Message != wantRestartMessage || state.UserAction != status.UserActionRelogin {
					t.Fatalf("unexpected state after bridge restart: %v", state)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("restart reconnect state was not delivered")
			}
			oldLogin := loginWithCredentials
			t.Cleanup(func() { loginWithCredentials = oldLogin })
			var gotCertificate string
			loginWithCredentials = func(_, _, certificate string) (*line.LoginResult, error) {
				gotCertificate = certificate
				return &line.LoginResult{Certificate: "123456"}, nil
			}
			process := &LineEmailLogin{}
			step, err := process.StartWithOverride(ctx, &bridgev2.UserLogin{UserLogin: reloaded, Bridge: bridge})
			if err != nil || step == nil || step.Type != bridgev2.LoginStepTypeDisplayAndWait {
				t.Fatalf("reconnect verification step = %v, error = %v", step, err)
			}
			wantCertificate := "certificate"
			if invalidKey {
				wantCertificate = ""
			}
			if gotCertificate != wantCertificate {
				t.Fatalf("reconnect certificate = %q, want %q", gotCertificate, wantCertificate)
			}
		})
	}
}

func TestEnsureValidTokenReturnsLoggedOutWithoutRelogin(t *testing.T) {
	oldGetProfile := getProfileWithToken
	oldLogin := loginWithCredentials
	t.Cleanup(func() {
		getProfileWithToken = oldGetProfile
		loginWithCredentials = oldLogin
	})

	var profileCalls int
	var loginCalls int
	getProfileWithToken = func(_ context.Context, token string) (*line.Profile, error) {
		profileCalls++
		if token != "expired" {
			t.Fatalf("profile token = %q, want expired", token)
		}
		return nil, errLoggedOut
	}
	loginWithCredentials = func(email, password, certificate string) (*line.LoginResult, error) {
		loginCalls++
		return &line.LoginResult{AuthToken: "new-token"}, nil
	}

	lc := &LineClient{AccessToken: "expired"}
	err := lc.ensureValidToken(context.Background())
	if !line.IsLoggedOut(err) {
		t.Fatalf("ensureValidToken error = %v, want logged-out error", err)
	}
	if profileCalls != 1 {
		t.Fatalf("profile calls = %d, want 1", profileCalls)
	}
	if loginCalls != 0 {
		t.Fatalf("login calls = %d, want 0", loginCalls)
	}
}

func TestEnsureValidTokenDoesNotReloginAfterLoggedOutRefresh(t *testing.T) {
	oldGetProfile := getProfileWithToken
	oldRecover := recoverLineToken
	t.Cleanup(func() {
		getProfileWithToken = oldGetProfile
		recoverLineToken = oldRecover
	})

	lc := &LineClient{
		AccessToken: "expired",
		UserLogin: &bridgev2.UserLogin{
			Bridge: &bridgev2.Bridge{Log: zerolog.New(io.Discard)},
		},
	}
	var reloginCalls int
	getProfileWithToken = func(_ context.Context, token string) (*line.Profile, error) {
		if token != "expired" {
			t.Fatalf("profile token = %q, want expired", token)
		}
		return nil, errAuthRequired
	}
	recoverLineToken = func(lc *LineClient, ctx context.Context) error {
		return lc.recoverTokenWith(
			ctx,
			func(context.Context) error { return errLoggedOut },
			func(context.Context) error {
				reloginCalls++
				return nil
			},
		)
	}

	err := lc.ensureValidToken(context.Background())
	if !line.IsAuthError(err) {
		t.Fatalf("ensureValidToken error = %v, want auth error", err)
	}
	if reloginCalls != 0 {
		t.Fatalf("relogin calls = %d, want 0", reloginCalls)
	}
	if lc.hasAccessToken() || !lc.isSessionInvalidated() {
		t.Fatal("logged-out refresh did not invalidate the session")
	}
}

func TestForcedLogoutWinsOverEnsureValidTokenRefresh(t *testing.T) {
	oldGetProfile := getProfileWithToken
	oldRecover := recoverLineToken
	t.Cleanup(func() {
		getProfileWithToken = oldGetProfile
		recoverLineToken = oldRecover
	})

	lc := &LineClient{
		AccessToken: "old-token",
		UserLogin: &bridgev2.UserLogin{
			Bridge: &bridgev2.Bridge{Log: zerolog.New(io.Discard)},
		},
	}
	refreshStarted := make(chan struct{})
	allowRefresh := make(chan struct{})
	ensureDone := make(chan error, 1)
	var reloginCalls int
	getProfileWithToken = func(_ context.Context, token string) (*line.Profile, error) {
		if token == "recovered-token" {
			return &line.Profile{}, nil
		}
		if token != "old-token" {
			t.Fatalf("profile token = %q, want old-token or recovered-token", token)
		}
		return nil, errAuthRequired
	}
	recoverLineToken = func(lc *LineClient, ctx context.Context) error {
		return lc.recoverTokenWith(
			ctx,
			func(context.Context) error {
				close(refreshStarted)
				<-allowRefresh
				lc.setTokens("recovered-token", "")
				return nil
			},
			func(context.Context) error {
				reloginCalls++
				return nil
			},
		)
	}
	go func() {
		ensureDone <- lc.ensureValidToken(context.Background())
	}()
	<-refreshStarted

	logoutDone := make(chan struct{})
	go func() {
		lc.markLoggedOutByOtherClient(context.Background(), errLoggedOut)
		close(logoutDone)
	}()
	close(allowRefresh)

	if err := <-ensureDone; err != nil {
		t.Fatalf("ensureValidToken returned error: %v", err)
	}
	if reloginCalls != 0 {
		t.Fatalf("relogin calls = %d, want 0", reloginCalls)
	}
	select {
	case <-logoutDone:
	case <-time.After(time.Second):
		t.Fatal("forced logout did not complete after startup refresh")
	}
	if lc.hasAccessToken() || !lc.isSessionInvalidated() {
		t.Fatal("startup refresh resurrected the forcefully logged-out session")
	}
}

func TestStartWithOverrideUsesStoredCredentials(t *testing.T) {
	oldLogin := loginWithCredentials
	t.Cleanup(func() {
		loginWithCredentials = oldLogin
	})

	var gotEmail, gotPassword, gotCertificate string
	loginWithCredentials = func(email, password, certificate string) (*line.LoginResult, error) {
		gotEmail = email
		gotPassword = password
		gotCertificate = certificate
		return &line.LoginResult{Certificate: "123456"}, nil
	}

	override := &bridgev2.UserLogin{
		UserLogin: &database.UserLogin{
			Metadata: &UserLoginMetadata{
				Email:       "stored@example.com",
				Password:    "stored-password",
				Certificate: "stored-cert",
				ExportedKeyMap: map[string]string{
					"5625926": "exported-key",
				},
			},
		},
	}

	login := &LineEmailLogin{}
	step, err := login.StartWithOverride(context.Background(), override)
	if err != nil {
		t.Fatalf("StartWithOverride returned error: %v", err)
	}
	if gotEmail != "stored@example.com" || gotPassword != "stored-password" || gotCertificate != "stored-cert" {
		t.Fatalf("login called with email=%q password=%q certificate=%q", gotEmail, gotPassword, gotCertificate)
	}
	if step == nil || step.Type != bridgev2.LoginStepTypeDisplayAndWait {
		t.Fatalf("step = %#v, want display-and-wait verification step", step)
	}
	if step.StepID != "dev.highest.matrix.line.enter_pin" {
		t.Fatalf("step ID = %q, want enter PIN", step.StepID)
	}
	if login.ExistingLogin != override {
		t.Fatal("override login was not retained for retirement before replacement")
	}
}
