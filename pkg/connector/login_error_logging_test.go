package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"maunium.net/go/mautrix/bridgev2"

	"github.com/highesttt/matrix-line-messenger/pkg/line"
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
		"http_status":           float64(400),
		"line_response_code":    float64(10051),
		"line_response_message": "RESPONSE_ERROR",
		"line_error_name":       "TalkException",
		"line_error_code":       float64(35),
		"line_error_message":    "Blocked user",
		"line_error_reason":     "blocked user",
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
	details := parseLoginErrorDetails(errors.New("login failed: request failed: context deadline exceeded"))
	if details.HasHTTPStatus || details.HasResponseFields {
		t.Fatalf("unexpected parsed response details: %#v", details)
	}
	if got := loginErrorSummary(errors.New("login failed: request failed: context deadline exceeded")); got != "login failed: request failed: context deadline exceeded" {
		t.Fatalf("loginErrorSummary = %q", got)
	}
}
