package line

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	gen "github.com/beeper/line/pkg"
)

func TestQRErrorDetailsAndRedaction(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		status           int
		expired          bool
	}{
		{"nested rejection", `{"code":10051,"message":"PRIVATE","data":{"name":"TalkException","code":35,"reason":"blocked user","accessToken":"PRIVATE","authSessionId":"PRIVATE"}}`, "exception code 35, exception type TalkException, reason blocked user", 400, false},
		{"HTTP success API failure", `{"code":10051,"data":{"name":"SecondaryQrCodeLoginException","code":0,"reason":"PRIVATE"}}`, "exception code 0, exception type SecondaryQrCodeLoginException, reason redacted", 200, false},
		{"unknown exception", `{"code":10051,"data":{"name":"PRIVATE","code":99,"reason":"PRIVATE"}}`, "exception code 99, exception type other, reason redacted", 400, false},
		{"QR exception with private alert", `{"code":10051,"data":{"name":"SecondaryQrCodeException","code":100,"alertMessage":"PRIVATE"}}`, "exception code 100, exception type SecondaryQrCodeException, alert redacted", 400, false},
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
