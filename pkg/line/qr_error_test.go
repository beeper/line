package line

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	gen "github.com/beeper/line/pkg"
)

func TestQRErrorDetailsAndRedaction(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		status           int
		expired          bool
	}{
		{"nested rejection", `{"code":10051,"message":"PRIVATE","data":{"name":"TalkException","code":35,"reason":"blocked user","accessToken":"PRIVATE","authSessionId":"PRIVATE"}}`, "exception code 35, exception type TalkException, reason blocked user", 400, false},
		{"HTTP success API failure", `{"code":10051,"data":{"name":"SecondaryQrCodeLoginException","code":0,"reason":"PRIVATE"}}`, "exception code 0, exception type SecondaryQrCodeLoginException", 200, false},
		{"unknown exception", `{"code":10051,"data":{"name":"PRIVATE","code":99,"reason":"PRIVATE"}}`, "exception code 99, exception type other", 400, false},
		{"QR exception with private alert", `{"code":10051,"data":{"name":"SecondaryQrCodeException","code":100,"alertMessage":"PRIVATE"}}`, "exception code 100, exception type SecondaryQrCodeException", 400, false},
		{"QR exception with allowlisted alert", `{"code":10051,"data":{"name":"SecondaryQrCodeException","code":5,"alertMessage":"Too many login attempts"}}`, "exception code 5, exception type SecondaryQrCodeException, alert Too many login attempts", 400, false},
		{"poll expired", `{"code":10052,"data":{"statusCode":410}}`, "code 10052, status 410", 400, true},
		{"missing detail", `{"code":10051,"data":{}}`, "QR qrCodeLoginV2 failed: HTTP 400, code 10051", 400, false},
		{"malformed", `PRIVATE`, "QR qrCodeLoginV2 failed: HTTP 400", 400, false},
		{"invalid numeric detail", `{"code":10051,"data":{"code":"PRIVATE"}}`, "QR qrCodeLoginV2 failed: HTTP 400, code 10051", 400, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner, err := gen.NewRunner()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			client := NewClient("")
			client.LoginAttempt = &LoginAttempt{Runner: runner, Context: ctx, cancel: cancel}
			defer client.LoginAttempt.Close()
			client.HTTPClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if !strings.HasSuffix(req.URL.Path, "/qrCodeLoginV2") {
					t.Fatal("unexpected method")
				}
				return &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})
			result, err := client.QRCodeLoginV2(ctx, "PRIVATE-SESSION")
			var qrErr *qrRPCError
			if result != nil || !errors.As(err, &qrErr) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("result=%v error=%v", result, err)
			}
			if strings.Contains(err.Error(), "PRIVATE") || strings.Contains(err.Error(), "status 0") {
				t.Fatalf("unsafe or misleading error: %v", err)
			}
			if IsQRLoginPollExpired(err) != tc.expired {
				t.Fatal("expiry classification changed")
			}
			if client.AccessToken != "" {
				t.Fatal("failed login installed token")
			}
		})
	}
}

func TestQRLoginFailureDetailIgnoresOtherErrors(t *testing.T) {
	detail, ok := QRLoginFailureDetail(errors.New("not a QR error"))
	if ok || detail.Method != "" {
		t.Fatal("unrelated error was reported as a QR failure")
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	if got := parseRetryAfter("17", now); got != 17*time.Second {
		t.Fatalf("seconds Retry-After = %s", got)
	}
	if got := parseRetryAfter(now.Add(2*time.Minute).Format(http.TimeFormat), now); got != 2*time.Minute {
		t.Fatalf("date Retry-After = %s", got)
	}
	if got := parseRetryAfter("invalid", now); got != 0 {
		t.Fatalf("invalid Retry-After = %s", got)
	}
}

func TestQRAuthErrorStage(t *testing.T) {
	for method, want := range map[string]AuthErrorStage{
		"createSession":        AuthStageQRCreate,
		"createQrCode":         AuthStageQRCreate,
		"verifyCertificate":    AuthStageQRVerify,
		"checkQrCodeVerified":  AuthStageQRVerify,
		"createPinCode":        AuthStageQRVerify,
		"checkPinCodeVerified": AuthStageQRVerify,
		"qrCodeLoginV2":        AuthStageQRTokenIssue,
	} {
		err := (&qrRPCError{method: method, httpStatus: http.StatusBadRequest}).buildAuthError("SecondaryQrCodeException", nil, "", "")
		if err.Stage != want {
			t.Errorf("method %s has stage %s, want %s", method, err.Stage, want)
		}
	}
}

func TestQRRPCErrorAuthError(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		body                    string
		status                  int
		wantCategory            AuthErrorCategory
		wantCooldownTriggered   bool
		wantRetryRecommended    bool
		wantProviderAlertStatus string
		wantProviderReason      string
		retryAfter              string
		wantRetryAfter          time.Duration
	}{
		{
			name:                    "HTTP 429",
			body:                    `{"code":10051,"data":{"name":"SecondaryQrCodeException","code":100}}`,
			status:                  http.StatusTooManyRequests,
			wantCategory:            AuthErrorTooManyAttempts,
			wantCooldownTriggered:   true,
			wantRetryRecommended:    true,
			wantProviderAlertStatus: "absent",
			wantProviderReason:      "absent",
			retryAfter:              "17",
			wantRetryAfter:          17 * time.Second,
		},
		{
			name:                    "retry later",
			body:                    `{"code":10051,"data":{"name":"SecondaryQrCodeException","code":5}}`,
			status:                  http.StatusBadRequest,
			wantCategory:            AuthErrorRetryLater,
			wantCooldownTriggered:   true,
			wantRetryRecommended:    true,
			wantProviderAlertStatus: "absent",
			wantProviderReason:      "absent",
		},
		{
			name:                    "verification temporarily unavailable",
			body:                    `{"code":10051,"data":{"name":"SecondaryQrCodeException","code":100,"alertMessage":"Verification is temporarily unavailable"}}`,
			status:                  http.StatusBadRequest,
			wantCategory:            AuthErrorVerificationTemporarilyUnavailable,
			wantCooldownTriggered:   true,
			wantRetryRecommended:    true,
			wantProviderAlertStatus: "recognized",
			wantProviderReason:      "verification_temporarily_unavailable",
		},
		{
			name:                    "QR login not allowed",
			body:                    `{"code":10051,"data":{"name":"SecondaryQrCodeException","code":3}}`,
			status:                  http.StatusBadRequest,
			wantCategory:            AuthErrorQRLoginNotAllowed,
			wantCooldownTriggered:   true,
			wantRetryRecommended:    false,
			wantProviderAlertStatus: "absent",
			wantProviderReason:      "absent",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner, err := gen.NewRunner()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			client := NewClient("")
			client.LoginAttempt = &LoginAttempt{Runner: runner, Context: ctx, cancel: cancel}
			defer client.LoginAttempt.Close()
			client.HTTPClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				headers := make(http.Header)
				if tc.retryAfter != "" {
					headers.Set("Retry-After", tc.retryAfter)
				}
				return &http.Response{StatusCode: tc.status, Header: headers, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})
			_, err = client.QRCodeLoginV2(ctx, "session")
			cancel()

			var qrErr *QRRPCError
			if !errors.As(err, &qrErr) {
				t.Fatalf("error is not a QRRPCError: %v", err)
			}
			ae := qrErr.AuthError()
			if ae == nil {
				t.Fatal("AuthError was nil")
			}
			if ae.Category != tc.wantCategory {
				t.Fatalf("category = %v, want %v", ae.Category, tc.wantCategory)
			}
			if ae.CooldownTriggered != tc.wantCooldownTriggered {
				t.Fatalf("cooldownTriggered = %v, want %v", ae.CooldownTriggered, tc.wantCooldownTriggered)
			}
			if ae.RetryRecommended != tc.wantRetryRecommended {
				t.Fatalf("retryRecommended = %v, want %v", ae.RetryRecommended, tc.wantRetryRecommended)
			}
			if ae.ProviderAlertStatus != tc.wantProviderAlertStatus {
				t.Fatalf("providerAlertStatus = %q, want %q", ae.ProviderAlertStatus, tc.wantProviderAlertStatus)
			}
			if ae.ProviderReason != tc.wantProviderReason {
				t.Fatalf("providerReason = %q, want %q", ae.ProviderReason, tc.wantProviderReason)
			}
			if ae.RetryAfter == nil && tc.wantRetryAfter > 0 || ae.RetryAfter != nil && *ae.RetryAfter != tc.wantRetryAfter {
				t.Fatalf("retryAfter = %v, want %s", ae.RetryAfter, tc.wantRetryAfter)
			}
		})
	}
}
