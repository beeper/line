package line

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

// These fixtures verify the bridge's request contract, not LINE's acceptance of
// a live login. No live authentication is performed by this test.
func TestQRRequestContractAndMIDRepresentations(t *testing.T) {
	for _, mid := range []string{
		"u0123456789abcdef0123456789abcdef",
		"U0123456789ABCDEF0123456789ABCDEF",
		"UAbCdEfGhIjKlMnOpQrStUvWxYz0123456789_-aDEFg",
	} {
		t.Run(mid, func(t *testing.T) {
			client := NewClient("")
			var calls []string
			client.HTTPClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				method := req.URL.Path[strings.LastIndex(req.URL.Path, "/")+1:]
				calls = append(calls, method)
				service := "SecondaryQrCodeLoginService"
				poll := method == "checkQrCodeVerified" || method == "checkPinCodeVerified"
				if poll {
					service = "SecondaryQrCodeLoginPermitNoticeService"
				}
				if req.Method != "POST" || req.URL.Host != "line-chrome-gw.line-apps.com" || req.URL.Path != "/api/talk/thrift/LoginQrCode/"+service+"/"+method {
					t.Fatal("QR endpoint contract changed")
				}
				if req.Header.Get("x-hmac") == "" || req.Header.Get("x-line-chrome-version") != ExtensionVersion || req.Header.Get("x-line-access") != "" || req.Header.Get("Cookie") != "" {
					t.Fatal("missing signing header or existing session credentials sent to QR login")
				}
				if poll && req.Header.Get("X-Line-Session-ID") != "fixture-session" || !poll && req.Header.Get("X-Line-Session-ID") != "" {
					t.Fatal("wrong QR session header scope")
				}
				var args []map[string]any
				if err := json.NewDecoder(req.Body).Decode(&args); err != nil || len(args) != 1 {
					t.Fatal("invalid QR argument list")
				}
				want := map[string]any{"authSessionId": "fixture-session"}
				body := `{"code":0,"data":{}}`
				status := 200
				switch method {
				case "createSession":
					want = map[string]any{}
					body = `{"code":0,"data":{"authSessionId":"fixture-session"}}`
				case "createQrCode":
					body = `{"code":0,"data":{"callbackUrl":"line://au/q/fixture","longPollingIntervalSec":30,"longPollingMaxCount":1}}`
				case "checkQrCodeVerified":
					if req.Header.Get("X-LST") != "30000" {
						t.Fatal("QR poll duration changed")
					}
				case "verifyCertificate":
					want["certificate"] = ""
					status = 400
					body = `{"code":10051,"data":{"name":"SecondaryQrCodeException","code":2}}`
				case "createPinCode":
					body = `{"code":0,"data":{"pinCode":"123456"}}`
				case "checkPinCodeVerified":
					if req.Header.Get("X-LST") != "110000" {
						t.Fatal("PIN poll duration changed")
					}
				case "qrCodeLoginV2":
					want["systemName"], want["modelName"], want["autoLoginIsRequired"] = "CHROMEOS", "CHROME", false
					body = `{"code":0,"data":{"mid":"` + mid + `","tokenV3IssueResult":{"accessToken":"fixture-token"}}}`
				default:
					t.Fatalf("unexpected QR operation %s", method)
				}
				if !reflect.DeepEqual(args[0], want) {
					t.Fatalf("%s args=%v want=%v", method, args[0], want)
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			ctx := context.Background()
			session, _, err := client.StartQRLogin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer client.LoginAttempt.Close()
			if err = client.CheckQRCodeVerifiedContext(ctx, session, 30*time.Second); err != nil {
				t.Fatal(err)
			}
			if ok, err := client.VerifyQRCertificate(ctx, session, ""); err != nil || ok {
				t.Fatalf("certificate result=%v,%v", ok, err)
			}
			if pin, err := client.CreatePinCode(ctx, session); err != nil || pin != "123456" {
				t.Fatal("PIN issuance failed")
			}
			if err = client.CheckPinCodeVerifiedContext(ctx, session); err != nil {
				t.Fatal(err)
			}
			result, err := client.QRCodeLoginV2(ctx, session)
			if err != nil || result.Mid != mid || result.AuthToken != "fixture-token" || client.AccessToken != "fixture-token" {
				t.Fatalf("identity/token altered: result=%+v err=%v", result, err)
			}
			if strings.Join(calls, ",") != "createSession,createQrCode,checkQrCodeVerified,verifyCertificate,createPinCode,checkPinCodeVerified,qrCodeLoginV2" {
				t.Fatalf("unexpected sequence: %v", calls)
			}
		})
	}
}
