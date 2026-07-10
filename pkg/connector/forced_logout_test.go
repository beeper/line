package connector

import (
	"context"
	"io"
	"testing"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"

	"github.com/rs/zerolog"

	"github.com/highesttt/matrix-line-messenger/pkg/line"
)

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
	lc := &LineClient{
		UserLogin: &bridgev2.UserLogin{
			Bridge: &bridgev2.Bridge{Log: zerolog.New(io.Discard)},
		},
	}
	var reloginCalls int
	err := lc.ensureValidTokenWith(
		context.Background(),
		func(context.Context) error { return errAuthRequired },
		func(context.Context) error { return errLoggedOut },
		func(context.Context) error {
			reloginCalls++
			return nil
		},
	)
	if !line.IsLoggedOut(err) {
		t.Fatalf("ensureValidTokenWith error = %v, want logged-out error", err)
	}
	if reloginCalls != 0 {
		t.Fatalf("relogin calls = %d, want 0", reloginCalls)
	}
}

func TestForcedLogoutWinsOverEnsureValidTokenRefresh(t *testing.T) {
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
	go func() {
		ensureDone <- lc.ensureValidTokenWith(
			context.Background(),
			func(context.Context) error { return errAuthRequired },
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
	}()
	<-refreshStarted

	logoutDone := make(chan struct{})
	go func() {
		lc.markLoggedOutByOtherClient(context.Background(), errLoggedOut)
		close(logoutDone)
	}()
	close(allowRefresh)

	if err := <-ensureDone; err != nil {
		t.Fatalf("ensureValidTokenWith returned error: %v", err)
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
