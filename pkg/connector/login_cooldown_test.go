package connector

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"

	"github.com/beeper/line/pkg/line"
)

func runQRLoginToRejection(t *testing.T, body string, status int) (*LineConnector, *bridgev2.User, error) {
	t.Helper()
	lc := newResolutionClient(t, func(req *http.Request) (*http.Response, error) {
		method := req.URL.Path[strings.LastIndex(req.URL.Path, "/")+1:]
		switch method {
		case "createSession":
			return resolutionResponse(`{"code":0,"data":{"authSessionId":"qr-session"}}`), nil
		case "createQrCode":
			return resolutionResponse(`{"code":0,"data":{"callbackUrl":"line://au/q/test","longPollingMaxCount":1}}`), nil
		case "checkQrCodeVerified", "checkPinCodeVerified":
			return resolutionResponse(`{"code":0,"data":{}}`), nil
		case "verifyCertificate":
			resp := resolutionResponse(`{"code":1,"data":{}}`)
			resp.StatusCode = http.StatusBadRequest
			return resp, nil
		case "createPinCode":
			return resolutionResponse(`{"code":0,"data":{"pinCode":"123456"}}`), nil
		case "qrCodeLoginV2":
			resp := resolutionResponse(body)
			resp.StatusCode = status
			return resp, nil
		default:
			return nil, fmt.Errorf("unexpected QR RPC: %s", method)
		}
	})
	lq := &LineQRLogin{login: &LineEmailLogin{User: lc.UserLogin.User, cooldown: &lc.UserLogin.Bridge.Network.(*LineConnector).loginCooldown}}
	defer lq.Cancel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := lq.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := lq.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	_, err := lq.Wait(ctx)
	return lc.UserLogin.Bridge.Network.(*LineConnector), lc.UserLogin.User, err
}

func TestQRStartupRateLimitMapsAndStartsCooldown(t *testing.T) {
	for _, method := range []string{"createSession", "createQrCode"} {
		t.Run(method, func(t *testing.T) {
			calls := 0
			lc := newResolutionClient(t, func(req *http.Request) (*http.Response, error) {
				gotMethod := req.URL.Path[strings.LastIndex(req.URL.Path, "/")+1:]
				if method == "createQrCode" && gotMethod == "createSession" {
					return resolutionResponse(`{"code":0,"data":{"authSessionId":"qr-session"}}`), nil
				}
				calls++
				if gotMethod != method {
					t.Fatalf("unexpected QR RPC %s, wanted %s", gotMethod, method)
				}
				resp := resolutionResponse(`{"code":10051,"data":{"name":"SecondaryQrCodeException","code":5}}`)
				resp.StatusCode = http.StatusTooManyRequests
				return resp, nil
			})
			connector := lc.UserLogin.Bridge.Network.(*LineConnector)
			lq := &LineQRLogin{login: &LineEmailLogin{User: lc.UserLogin.User, cooldown: &connector.loginCooldown}}
			defer lq.Cancel()
			_, err := lq.Start(context.Background())
			var respErr bridgev2.RespError
			if !errors.As(err, &respErr) || respErr.StatusCode != http.StatusTooManyRequests {
				t.Fatalf("startup failure was not mapped to a rate limit: %v", err)
			}
			if connector.loginCooldown.remaining(string(lc.UserLogin.User.MXID)) <= 0 {
				t.Fatal("startup rate limit did not activate account cooldown")
			}
			_, again := lq.Start(context.Background())
			if again == nil || calls != 1 {
				t.Fatalf("startup cooldown did not prevent another provider call: calls=%d err=%v", calls, again)
			}
		})
	}
}

func TestQRRateLimitIsReportedAndBlocksImmediateRetry(t *testing.T) {
	for _, tc := range []struct {
		name, body, wantMessage string
		status                  int
	}{
		{
			name:        "retry later",
			body:        `{"code":10051,"data":{"name":"SecondaryQrCodeException","code":5}}`,
			wantMessage: loginRetryLaterMessage,
			status:      http.StatusBadRequest,
		},
		{
			name:        "too many attempts",
			body:        `{"code":10051,"data":{"name":"SecondaryQrCodeException","code":100,"reason":"Too many login attempts"}}`,
			wantMessage: loginTooManyAttemptsReason,
			status:      http.StatusBadRequest,
		},
		{
			name:        "http 429",
			body:        `{"code":10051,"data":{"name":"SecondaryQrCodeException","code":100}}`,
			wantMessage: loginTooManyAttemptsReason,
			status:      http.StatusTooManyRequests,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			connector, user, err := runQRLoginToRejection(t, tc.body, tc.status)
			if err == nil {
				t.Fatal("rate limited QR login was accepted")
			}
			var respErr bridgev2.RespError
			if !errors.As(err, &respErr) {
				t.Fatalf("QR rejection is not a user-facing error: %v", err)
			}
			if respErr.Err != tc.wantMessage {
				t.Fatalf("message = %q, want %q", respErr.Err, tc.wantMessage)
			}
			if respErr.StatusCode != http.StatusTooManyRequests {
				t.Fatalf("status = %d, want 429", respErr.StatusCode)
			}
			connector.loginCooldown.block("other-user", time.Now().Add(time.Hour))
			remaining := connector.loginCooldown.remaining(string(user.MXID))
			if remaining <= 0 {
				t.Fatal("rate limit did not start a cooldown")
			}
			lq := &LineQRLogin{login: &LineEmailLogin{User: user, cooldown: &connector.loginCooldown}}
			_, err = lq.Start(context.Background())
			if err == nil {
				t.Fatal("a new QR login was started inside the rate limit window")
			}
			var cooldownErr bridgev2.RespError
			if !errors.As(err, &cooldownErr) {
				t.Fatalf("cooldown error is not user-facing: %v", err)
			}
			if !strings.Contains(cooldownErr.Err, "try again in") {
				t.Fatalf("cooldown message = %q", cooldownErr.Err)
			}
		})
	}
}

func TestVerificationTemporarilyUnavailableBlocksRetry(t *testing.T) {
	oldLogin := loginWithCredentials
	t.Cleanup(func() { loginWithCredentials = oldLogin })
	loginWithCredentials = func(_, _, _ string) (*line.LoginResult, error) {
		return nil, errors.New(`login failed: API error 400: {"code":10051,"data":{"name":"TalkException","reason":"Verification is temporarily unavailable"}}`)
	}
	logger := zerolog.New(io.Discard)
	var cooldown loginCooldown
	ll := &LineEmailLogin{
		User: &bridgev2.User{
			User:   &database.User{MXID: "@user:example.com"},
			Bridge: &bridgev2.Bridge{Log: logger},
			Log:    logger,
		},
		cooldown: &cooldown,
	}
	step, err := ll.SubmitUserInput(context.Background(), map[string]string{
		"email":    "user@example.com",
		"password": "secret",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if step == nil {
		t.Fatal("expected error step, got nil")
	}
	remaining := cooldown.remaining("@user:example.com")
	if remaining <= verificationTemporarilyUnavailableCooldown-time.Minute || remaining > verificationTemporarilyUnavailableCooldown {
		t.Fatalf("unexpected cooldown remaining: %s", remaining)
	}
}

func TestQRRejectionWithoutRateLimitDoesNotBlockRetry(t *testing.T) {
	connector, user, err := runQRLoginToRejection(t, `{"code":10051,"data":{"name":"SecondaryQrCodeException","code":2}}`, http.StatusBadRequest)
	if err == nil {
		t.Fatal("QR login was accepted")
	}
	if remaining := connector.loginCooldown.remaining(string(user.MXID)); remaining != 0 {
		t.Fatalf("a verification failure started a rate limit cooldown: %s", remaining)
	}
	lq := &LineQRLogin{login: &LineEmailLogin{User: user, cooldown: &connector.loginCooldown}}
	_, err = lq.Start(context.Background())
	if err != nil && strings.Contains(err.Error(), "limiting how often") {
		t.Fatalf("unexpected rate limit error: %v", err)
	}
}

func TestEmailLoginRateLimitBlocksNextAttempt(t *testing.T) {
	oldLogin := loginWithCredentials
	t.Cleanup(func() { loginWithCredentials = oldLogin })
	loginWithCredentials = func(_, _, _ string) (*line.LoginResult, error) {
		return nil, errors.New(`login failed: API error 429: {"code":10051,"data":{"name":"TalkException","reason":"Too many login attempts"}}`)
	}
	logger := zerolog.New(io.Discard)
	var cooldown loginCooldown
	ll := &LineEmailLogin{
		User: &bridgev2.User{
			User:   &database.User{MXID: "@user:example.com"},
			Bridge: &bridgev2.Bridge{Log: logger},
			Log:    logger,
		},
		cooldown: &cooldown,
	}
	step, err := ll.SubmitUserInput(context.Background(), map[string]string{
		"email":    "user@example.com",
		"password": "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if step == nil || step.Instructions != loginTooManyAttemptsInstructions {
		t.Fatalf("step instructions = %v, want %q", step, loginTooManyAttemptsInstructions)
	}
	if remaining := cooldown.remaining("@user:example.com"); remaining <= 0 {
		t.Fatal("rate limited login did not start a cooldown")
	}
	if _, err := ll.Start(context.Background()); err == nil {
		t.Fatal("a new login was started inside the rate limit window")
	} else if !strings.Contains(err.Error(), "try again in") {
		t.Fatalf("unexpected cooldown error: %v", err)
	}
}

func TestLoginCooldownExpires(t *testing.T) {
	var cooldown loginCooldown
	cooldown.block("@user:example.com", time.Now().Add(-time.Minute))
	if remaining := cooldown.remaining("@user:example.com"); remaining != 0 {
		t.Fatalf("expired cooldown still blocks login: %s", remaining)
	}
	cooldown.block("@user:example.com", time.Now().Add(time.Minute))
	if remaining := cooldown.remaining("@user:example.com"); remaining <= 0 {
		t.Fatal("active cooldown does not block login")
	}
	if remaining := cooldown.remaining("@other:example.com"); remaining != 0 {
		t.Fatalf("cooldown leaked to another account: %s", remaining)
	}
}

func TestLoginClaimSerializesSameAccount(t *testing.T) {
	var cooldown loginCooldown
	user := &bridgev2.User{User: &database.User{MXID: "@same:example.com"}}
	first := &LineEmailLogin{User: user, cooldown: &cooldown}
	second := &LineEmailLogin{User: user, cooldown: &cooldown}
	if err := first.claimLogin(); err != nil {
		t.Fatal(err)
	}
	if err := second.claimLogin(); !errors.Is(err, ErrLoginInProgress) {
		t.Fatalf("concurrent login claim = %v, want ErrLoginInProgress", err)
	}
	first.releaseLogin()
	if err := second.claimLogin(); err != nil {
		t.Fatalf("claim after release = %v", err)
	}
	second.releaseLogin()
}

func TestLoginCooldownUsesRetryAfter(t *testing.T) {
	var cooldown loginCooldown
	user := &bridgev2.User{User: &database.User{MXID: "@user:example.com"}}
	ll := &LineEmailLogin{User: user, cooldown: &cooldown}
	delay := 20 * time.Minute
	ll.noteLoginRateLimit(&line.AuthError{Category: line.AuthErrorTooManyAttempts, RetryAfter: &delay})
	if remaining := cooldown.remaining(string(user.MXID)); remaining < delay-time.Second || remaining > delay {
		t.Fatalf("cooldown=%s, want provider Retry-After %s", remaining, delay)
	}
}

func TestLoginRateLimitClassification(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want loginRateLimitKind
	}{
		{"http 429", errors.New("login failed: API error 429: {\"code\":10051}"), loginRateLimitLocked},
		{"too many attempts", errors.New("login failed: API error 400: {\"code\":10051,\"data\":{\"name\":\"TalkException\",\"reason\":\"Too many login attempts\"}}"), loginRateLimitLocked},
		{"blocked user", errors.New("login failed: API error 400: {\"code\":10051,\"data\":{\"name\":\"TalkException\",\"reason\":\"blocked user\"}}"), loginRateLimitLocked},
		{"invalid credentials", errors.New("login failed: API error 400: {\"code\":10051,\"data\":{\"name\":\"TalkException\",\"reason\":\"Account ID or password is invalid\"}}"), loginRateLimitNone},
		{"unrelated", errors.New("network unreachable"), loginRateLimitNone},
		{"nil", nil, loginRateLimitNone},
		{"verification temporarily unavailable via AuthError", wrapLineLoginError(errors.New(`login failed: API error 400: {"code":10051,"data":{"name":"TalkException","reason":"Verification is temporarily unavailable"}}`)), loginRateLimitVerificationUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyLoginRateLimit(tc.err); got != tc.want {
				t.Fatalf("classification = %v, want %v", got, tc.want)
			}
		})
	}
}
