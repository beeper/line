package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"maunium.net/go/mautrix/bridgev2"

	"github.com/beeper/line/pkg/line"
)

func TestSubmitUserInputLogsStructuredLoginErrorWithoutCredentials(t *testing.T) {
	oldLogin := loginWithCredentials
	t.Cleanup(func() {
		loginWithCredentials = oldLogin
	})

	loginWithCredentials = func(_, _, _ string) (*line.LoginResult, error) {
		return nil, errors.New(`login failed: API error 400: {"code":10051,"message":"RESPONSE_ERROR","data":{"name":"TalkException","message":"Blocked user","code":35,"reason":"blocked user","token":"response-token"},"password":"response-password"}`)
	}

	var output bytes.Buffer
	logger := zerolog.New(&output)
	login := &LineEmailLogin{
		User: &bridgev2.User{
			Bridge: &bridgev2.Bridge{Log: logger},
			Log:    logger,
		},
	}
	step, err := login.SubmitUserInput(context.Background(), map[string]string{
		"email":    "ana@example.com",
		"password": "input-password",
	})
	if err != nil {
		t.Fatalf("SubmitUserInput returned error: %v", err)
	}
	if step == nil || step.Instructions != loginTooManyAttemptsInstructions {
		t.Fatalf("step instructions = %q, want %q", step.Instructions, loginTooManyAttemptsInstructions)
	}

	var event map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &event); err != nil {
		t.Fatalf("failed to parse log event: %v\n%s", err, output.String())
	}
	wantFields := map[string]any{
		"level":                 "warn",
		"message":               "LINE login attempt failed",
		"login_flow":            "credentials",
		"has_certificate":       false,
		"error_summary":         "API error 400",
		"http_status":           float64(400),
		"line_response_code":    float64(10051),
		"line_response_message": "redacted",
		"line_error_name":       "TalkException",
		"line_error_code":       float64(35),
		"line_error_message":    "redacted",
		"line_error_reason":     "blocked_user",
	}
	for key, want := range wantFields {
		if got := event[key]; got != want {
			t.Errorf("log field %s = %#v, want %#v", key, got, want)
		}
	}

	logged := output.String()
	for _, secret := range []string{"ana@example.com", "input-password", "response-token", "response-password"} {
		if strings.Contains(logged, secret) {
			t.Errorf("log contains secret %q: %s", secret, logged)
		}
	}
}

func TestParseLoginErrorDetailsWithoutJSON(t *testing.T) {
	err := errors.New("login failed: request failed: context deadline exceeded")
	details := parseLoginErrorDetails(err)
	if details.HasHTTPStatus || details.HasResponseFields {
		t.Fatalf("unexpected parsed response details: %#v", details)
	}
	if got := loginErrorSummary(err, details); got != "" {
		t.Fatalf("loginErrorSummary = %q, want empty", got)
	}
}

func TestSessionEstablishmentErrorKeepsStageAndSafeMessage(t *testing.T) {
	err := errors.New(`API error 503: {"code":10051,"data":{"name":"LoginException","message":"LINE could not verify this session. Please try again."}}`)
	wrapped := wrapSessionEstablishError(&LineEmailLogin{authFlow: "qr"}, err)
	var authErr *line.AuthError
	if !errors.As(wrapped, &authErr) {
		t.Fatalf("session failure lacks AuthError: %v", wrapped)
	}
	if authErr.Flow != "qr" || authErr.Stage != line.AuthStageSessionEstablish || authErr.Category != line.AuthErrorServiceUnavailable {
		t.Fatalf("incorrect session classification: %+v", authErr)
	}
	var respErr bridgev2.RespError
	if !errors.As(wrapped, &respErr) || !strings.Contains(respErr.Err, "LINE could not verify this session") {
		t.Fatalf("safe provider message did not reach Matrix: %v", wrapped)
	}
}

func TestLoginTransportErrorsUseNetworkCategory(t *testing.T) {
	wrapped := wrapLineLoginError(context.DeadlineExceeded)
	var authErr *line.AuthError
	if !errors.As(wrapped, &authErr) || authErr.Category != line.AuthErrorNetworkUnavailable || authErr.Stage != line.AuthStageEmailLogin {
		t.Fatalf("transport error classification = %#v (%v)", authErr, wrapped)
	}
	var respErr bridgev2.RespError
	if !errors.As(wrapped, &respErr) || !strings.Contains(respErr.Err, "reach LINE") {
		t.Fatalf("network error is not actionable: %v", wrapped)
	}
}

func TestLoginErrorSummaryDoesNotIncludeNonJSONResponseBody(t *testing.T) {
	err := errors.New("login failed: API error 502: upstream secret response")
	details := parseLoginErrorDetails(err)
	if got := loginErrorSummary(err, details); got != "API error 502" {
		t.Fatalf("loginErrorSummary = %q, want %q", got, "API error 502")
	}
}

func TestLoginErrorReasonDoesNotLeakUnknownProviderText(t *testing.T) {
	for _, tc := range []struct {
		reason, want string
	}{
		{"blocked user", loginTooManyAttemptsReason},
		{"Too many login attempts", loginTooManyAttemptsReason},
		{"Verification is temporarily unavailable", "Verification is temporarily unavailable"},
		{"Account ID or password is invalid", "Account ID or password is invalid"},
		{"Authentication failed", "Authentication failed"},
		{"", ""},
		{"response-token secret", ""},
		{"password: hunter2", "password=[redacted]"},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			if got := safeEmailLoginReason(tc.reason); got != tc.want {
				t.Fatalf("safeEmailLoginReason(%q) = %q, want %q", tc.reason, got, tc.want)
			}
		})
	}
}

func TestLoginErrorSummaryAllowsKnownContextErrors(t *testing.T) {
	err := fmt.Errorf("login failed: %w", context.DeadlineExceeded)
	details := parseLoginErrorDetails(err)
	if got := loginErrorSummary(err, details); got != "request timed out" {
		t.Fatalf("loginErrorSummary = %q, want %q", got, "request timed out")
	}
}
