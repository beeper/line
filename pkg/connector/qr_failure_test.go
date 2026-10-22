package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"maunium.net/go/mautrix/bridgev2"

	"github.com/beeper/line/pkg/line"
)

func TestQRRejectionSurfacesFixedReasonWithoutProviderText(t *testing.T) {
	for _, tc := range []struct {
		name, body, wantMessage, wantLabel string
		wantCode                           int
	}{
		{
			name:        "invalid context",
			body:        `{"code":10051,"data":{"name":"SecondaryQrCodeException","code":100,"alertMessage":"PRIVATE-ALERT"}}`,
			wantMessage: "LINE rejected the QR sign-in because the sign-in context was no longer valid. Please start the sign-in again with a fresh QR code.",
			wantLabel:   "INVALID_CONTEXT",
			wantCode:    100,
		},
		{
			name:        "retry later",
			body:        `{"code":10051,"data":{"name":"SecondaryQrCodeException","code":5}}`,
			wantMessage: loginRetryLaterMessage,
			wantLabel:   "RETRY_LATER",
			wantCode:    5,
		},
		{
			name:        "not allowed",
			body:        `{"code":10051,"data":{"name":"SecondaryQrCodeException","code":3}}`,
			wantMessage: "LINE doesn't allow QR sign-in for this account. Please sign in with your email and password instead.",
			wantLabel:   "NOT_ALLOWED_QR_CODE_LOGIN",
			wantCode:    3,
		},
		{
			name:        "unknown class",
			body:        `{"code":10051,"data":{"name":"PRIVATE-CLASS","code":100,"reason":"PRIVATE-REASON"}}`,
			wantMessage: "LINE rejected the QR sign-in. Please try again.",
			wantCode:    100,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			logger := zerolog.New(&output)
			lc := newResolutionClient(t, func(req *http.Request) (*http.Response, error) {
				method := req.URL.Path[strings.LastIndex(req.URL.Path, "/")+1:]
				switch method {
				case "createSession":
					return resolutionResponse(`{"code":0,"data":{"authSessionId":"qr-session"}}`), nil
				case "createQrCode":
					return resolutionResponse(`{"code":0,"data":{"callbackUrl":"line://au/q/test","longPollingMaxCount":1}}`), nil
				case "checkQrCodeVerified":
					return resolutionResponse(`{"code":0,"data":{}}`), nil
				case "verifyCertificate":
					resp := resolutionResponse(`{"code":1,"data":{}}`)
					resp.StatusCode = http.StatusBadRequest
					return resp, nil
				case "createPinCode":
					return resolutionResponse(`{"code":0,"data":{"pinCode":"123456"}}`), nil
				case "checkPinCodeVerified":
					return resolutionResponse(`{"code":0,"data":{}}`), nil
				case "qrCodeLoginV2":
					resp := resolutionResponse(tc.body)
					resp.StatusCode = http.StatusBadRequest
					return resp, nil
				default:
					return nil, fmt.Errorf("unexpected QR RPC: %s", method)
				}
			})
			lc.UserLogin.User.Log = logger
			lq := &LineQRLogin{login: &LineEmailLogin{User: lc.UserLogin.User}}
			defer lq.Cancel()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, err := lq.Start(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := lq.Wait(ctx); err != nil {
				t.Fatal(err)
			}
			step, err := lq.Wait(ctx)
			if step != nil {
				t.Fatalf("rejected QR login returned a step: %+v", step)
			}
			var respErr bridgev2.RespError
			if !errors.As(err, &respErr) {
				t.Fatalf("QR rejection is not a user-facing error: %v", err)
			}
			if respErr.Err != tc.wantMessage {
				t.Fatalf("message = %q, want %q", respErr.Err, tc.wantMessage)
			}
			for _, secret := range []string{"PRIVATE-ALERT", "PRIVATE-CLASS", "PRIVATE-REASON"} {
				if strings.Contains(respErr.Err, secret) || strings.Contains(err.Error(), secret) {
					t.Fatalf("provider text leaked into the user-facing error: %v", err)
				}
			}
			var event map[string]any
			if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &event); err != nil {
				t.Fatalf("failed to parse log event: %v\n%s", err, output.String())
			}
			if event["message"] != "LINE QR login attempt failed" || event["login_flow"] != "qr_token_issue" {
				t.Fatalf("unexpected log event: %v", event)
			}
			if event["qr_method"] != "qrCodeLoginV2" || event["line_error_code"] != float64(tc.wantCode) {
				t.Fatalf("missing sanitized QR details: %v", event)
			}
			if tc.wantLabel == "" {
				if _, ok := event["line_error_label"]; ok {
					t.Fatalf("unknown class received a code label: %v", event)
				}
			} else if event["line_error_label"] != tc.wantLabel {
				t.Fatalf("label = %v, want %q", event["line_error_label"], tc.wantLabel)
			}
		})
	}
}

func TestQRFailureDetailIsNotBuiltFromUnrelatedErrors(t *testing.T) {
	if detail, ok := line.QRLoginFailureDetail(errors.New("unrelated")); ok || detail.Method != "" {
		t.Fatal("unrelated error was reported as a QR failure")
	}
}

func TestQRVerificationTemporarilyUnavailable(t *testing.T) {
	var output bytes.Buffer
	logger := zerolog.New(&output)
	lc := newResolutionClient(t, func(req *http.Request) (*http.Response, error) {
		method := req.URL.Path[strings.LastIndex(req.URL.Path, "/")+1:]
		switch method {
		case "createSession":
			return resolutionResponse(`{"code":0,"data":{"authSessionId":"qr-session"}}`), nil
		case "createQrCode":
			return resolutionResponse(`{"code":0,"data":{"callbackUrl":"line://au/q/test","longPollingMaxCount":1}}`), nil
		case "checkQrCodeVerified":
			return resolutionResponse(`{"code":0,"data":{}}`), nil
		case "verifyCertificate":
			resp := resolutionResponse(`{"code":1,"data":{}}`)
			resp.StatusCode = http.StatusBadRequest
			return resp, nil
		case "createPinCode":
			return resolutionResponse(`{"code":0,"data":{"pinCode":"123456"}}`), nil
		case "checkPinCodeVerified":
			return resolutionResponse(`{"code":0,"data":{}}`), nil
		case "qrCodeLoginV2":
			resp := resolutionResponse(`{"code":10051,"data":{"name":"SecondaryQrCodeException","code":100,"alertMessage":"Verification is temporarily unavailable"}}`)
			resp.StatusCode = http.StatusBadRequest
			return resp, nil
		default:
			return nil, fmt.Errorf("unexpected QR RPC: %s", method)
		}
	})
	lc.UserLogin.User.Log = logger
	lq := &LineQRLogin{login: &LineEmailLogin{User: lc.UserLogin.User}}
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
	if err == nil {
		t.Fatal("verification unavailable QR login was accepted")
	}
	var respErr bridgev2.RespError
	if !errors.As(err, &respErr) {
		t.Fatalf("QR rejection is not a user-facing error: %v", err)
	}

	var event map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &event); err != nil {
		t.Fatalf("failed to parse log event: %v\n%s", err, output.String())
	}
	if event["auth_error_category"] != "VERIFICATION_TEMPORARILY_UNAVAILABLE" {
		t.Fatalf("auth_error_category = %v, want VERIFICATION_TEMPORARILY_UNAVAILABLE", event["auth_error_category"])
	}
	if event["provider_alert_status"] != "recognized" {
		t.Fatalf("provider_alert_status = %v, want recognized", event["provider_alert_status"])
	}
	if event["cooldown_triggered"] != true {
		t.Fatalf("cooldown_triggered = %v, want true", event["cooldown_triggered"])
	}
	if event["retry_recommended"] != true {
		t.Fatalf("retry_recommended = %v, want true", event["retry_recommended"])
	}
	if event["temporary"] != true {
		t.Fatalf("temporary = %v, want true", event["temporary"])
	}
	for _, secret := range []string{"Verification is temporarily unavailable"} {
		if strings.Contains(respErr.Err, secret) || strings.Contains(err.Error(), secret) {
			t.Fatalf("provider text leaked into the user-facing error: %v", err)
		}
	}
}
