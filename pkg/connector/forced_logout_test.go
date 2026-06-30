package connector

import (
	"context"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"

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
	getProfileWithToken = func(token string) (*line.Profile, error) {
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
		return &line.LoginResult{Type: 3, Verifier: "verifier", Pin: "123456"}, nil
	}

	override := &bridgev2.UserLogin{
		UserLogin: &database.UserLogin{
			Metadata: &UserLoginMetadata{
				Email:       "stored@example.com",
				Password:    "stored-password",
				Certificate: "stored-cert",
			},
		},
	}

	step, err := (&LineEmailLogin{}).StartWithOverride(context.Background(), override)
	if err != nil {
		t.Fatalf("StartWithOverride returned error: %v", err)
	}
	if gotEmail != "stored@example.com" || gotPassword != "stored-password" || gotCertificate != "stored-cert" {
		t.Fatalf("login called with email=%q password=%q certificate=%q", gotEmail, gotPassword, gotCertificate)
	}
	if step == nil || step.Type != bridgev2.LoginStepTypeDisplayAndWait {
		t.Fatalf("step = %#v, want display-and-wait verification step", step)
	}
	if step.StepID != "dev.highest.matrix.line.wait_verification" {
		t.Fatalf("step ID = %q, want wait verification", step.StepID)
	}
}
