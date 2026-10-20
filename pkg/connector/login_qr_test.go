package connector

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"maunium.net/go/mautrix/bridgev2"
)

func TestQRConcurrentWaitIsRejected(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lq := &LineQRLogin{login: &LineEmailLogin{attemptCtx: ctx, attemptCancel: cancel}, poll: make(chan error, 1)}
	results := make(chan error, 2)
	for range 2 {
		go func() { _, err := lq.Wait(context.Background()); results <- err }()
	}
	select {
	case err := <-results:
		if err == nil || !strings.Contains(err.Error(), "already in progress") {
			t.Errorf("unexpected concurrent Wait result: %v", err)
		}
	case <-time.After(time.Second):
		t.Error("concurrent Wait calls both blocked instead of rejecting the duplicate")
		cancel()
		<-results
	}
	cancel()
	select {
	case err := <-results:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("active Wait ignored cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("active Wait did not stop")
	}
}

func TestQRApprovalPINAndTokenValidation(t *testing.T) {
	approved, pinApproved := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	var calls []string
	lc := newResolutionClient(t, func(req *http.Request) (*http.Response, error) {
		method := req.URL.Path[strings.LastIndex(req.URL.Path, "/")+1:]
		mu.Lock()
		calls = append(calls, method)
		mu.Unlock()
		switch method {
		case "createSession":
			return resolutionResponse(`{"code":0,"data":{"authSessionId":"qr-session"}}`), nil
		case "createQrCode":
			return resolutionResponse(`{"code":0,"data":{"callbackUrl":"line://au/q/test","longPollingMaxCount":1}}`), nil
		case "checkQrCodeVerified", "checkPinCodeVerified":
			if req.Header.Get("X-Line-Session-ID") != "qr-session" || req.Header.Get("x-hmac") == "" {
				return nil, errors.New("missing QR authentication headers")
			}
			gate := approved
			if method == "checkPinCodeVerified" {
				gate = pinApproved
			}
			select {
			case <-gate:
			case <-req.Context().Done():
				return nil, req.Context().Err()
			}
			return resolutionResponse(`{"code":0,"data":{}}`), nil
		case "verifyCertificate":
			resp := resolutionResponse(`{"code":1,"data":{}}`)
			resp.StatusCode = http.StatusBadRequest
			return resp, nil
		case "createPinCode":
			return resolutionResponse(`{"code":0,"data":{"pinCode":"123456"}}`), nil
		case "qrCodeLoginV2":
			return resolutionResponse(`{"code":0,"data":{"mid":"` + resolutionOwnMID + `","tokenV3IssueResult":{"accessToken":"qr-token","refreshToken":"qr-refresh"},"metaData":{}}}`), nil
		case "getProfile":
			if req.Header.Get("x-line-access") != "qr-token" {
				return nil, errors.New("QR token was not used for verification")
			}
			return resolutionResponse(`{"code":0,"data":{"mid":"` + resolutionOwnMID + `","displayName":"Owner"}}`), nil
		default:
			return nil, fmt.Errorf("unexpected QR RPC: %s", method)
		}
	})
	lq := &LineQRLogin{login: &LineEmailLogin{User: lc.UserLogin.User}}
	defer lq.Cancel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	step, err := lq.Start(ctx)
	if err != nil || step.DisplayAndWaitParams.Type != bridgev2.LoginDisplayTypeQR {
		t.Fatalf("QR start: %+v %v", step, err)
	}
	callback, err := url.Parse(step.DisplayAndWaitParams.Data)
	if err != nil {
		t.Fatal(err)
	}
	key, err := base64.StdEncoding.DecodeString(callback.Query().Get("secret"))
	if err != nil || len(key) != 32 || callback.Query().Get("e2eeVersion") != "1" {
		t.Fatal("QR callback missing login key material")
	}
	close(approved)
	step, err = lq.Wait(ctx)
	if err != nil || step.DisplayAndWaitParams.Type != bridgev2.LoginDisplayTypeCode || step.DisplayAndWaitParams.Data != "123456" {
		t.Fatalf("PIN step: %+v %v", step, err)
	}
	close(pinApproved)
	step, err = lq.Wait(ctx)
	if step != nil || !errors.Is(err, ErrLoginNoKeychain) {
		t.Fatalf("missing E2EE keychain was accepted: %+v %v", step, err)
	}
	stored, err := lc.UserLogin.Bridge.DB.UserLogin.GetByID(ctx, lc.UserLogin.ID)
	if err != nil || stored.Metadata.(*UserLoginMetadata).AccessToken != "test-token" {
		t.Fatalf("incomplete QR login changed stored session: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := "[createSession createQrCode checkQrCodeVerified verifyCertificate createPinCode checkPinCodeVerified qrCodeLoginV2 getProfile]"
	if fmt.Sprint(calls) != want {
		t.Fatalf("unexpected QR state transitions: %v", calls)
	}
}

func TestQRExpiryAndCancellation(t *testing.T) {
	for _, expire := range []bool{false, true} {
		t.Run(fmt.Sprint(expire), func(t *testing.T) {
			pollStarted, pollStopped := make(chan struct{}), make(chan struct{})
			newResolutionClient(t, func(req *http.Request) (*http.Response, error) {
				switch {
				case strings.HasSuffix(req.URL.Path, "/createSession"):
					return resolutionResponse(`{"code":0,"data":{"authSessionId":"session"}}`), nil
				case strings.HasSuffix(req.URL.Path, "/createQrCode"):
					return resolutionResponse(`{"code":0,"data":{"callbackUrl":"line://au/q/test","longPollingMaxCount":1}}`), nil
				case strings.HasSuffix(req.URL.Path, "/checkQrCodeVerified"):
					close(pollStarted)
					defer close(pollStopped)
					if expire {
						return resolutionResponse(`{"code":10052,"data":{"statusCode":410}}`), nil
					}
					<-req.Context().Done()
					return nil, req.Context().Err()
				default:
					return nil, fmt.Errorf("approval was bypassed: %s", req.URL.Path)
				}
			})
			lq := &LineQRLogin{login: &LineEmailLogin{}}
			defer lq.Cancel()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, err := lq.Start(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case <-pollStarted:
			case <-ctx.Done():
				t.Fatal("poll did not start")
			}
			if !expire {
				lq.Cancel()
			}
			step, err := lq.Wait(ctx)
			want := error(context.Canceled)
			if expire {
				want = ErrLoginVerificationFailed
			}
			if step != nil || !errors.Is(err, want) {
				t.Fatalf("unexpected terminal state: %+v %v", step, err)
			}
			select {
			case <-pollStopped:
			case <-ctx.Done():
				t.Fatal("poll worker leaked")
			}
		})
	}
}

func TestQRStartPreservesDeadline(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	lq := &LineQRLogin{login: &LineEmailLogin{}}
	newResolutionClient(t, func(*http.Request) (*http.Response, error) { return nil, ctx.Err() })
	step, err := lq.Start(ctx)
	if step != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("start after deadline = %+v, %v; want DeadlineExceeded", step, err)
	}
}
