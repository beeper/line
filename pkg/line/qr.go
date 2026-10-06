package line

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	gen "github.com/highesttt/matrix-line-messenger/pkg"
)

type QRCodeResponse struct {
	CallbackURL                string `json:"callbackUrl"`
	LongPollingMaxCount        int    `json:"longPollingMaxCount"`
	LongPollingIntervalSeconds int    `json:"longPollingIntervalSec"`
}

func (c *Client) StartQRLogin(ctx context.Context) (session string, qr *QRCodeResponse, err error) {
	if c.LoginAttempt != nil {
		c.LoginAttempt.Close()
	}
	if err = ctx.Err(); err != nil {
		return
	}
	runner, err := gen.NewRunner()
	if err != nil {
		return "", nil, err
	}
	attemptCtx, cancel := context.WithCancel(ctx)
	attempt := &LoginAttempt{Runner: runner, Context: attemptCtx, cancel: cancel}
	c.LoginAttempt = attempt
	context.AfterFunc(attemptCtx, runner.Close)
	defer func() {
		if attemptCtx.Err() != nil {
			err = attemptCtx.Err()
		}
		if err != nil {
			attempt.Close()
			session, qr = "", nil
		}
	}()
	secret, err := runner.GenerateE2EESecret()
	if err != nil {
		return "", nil, err
	}
	var created struct {
		Session string `json:"authSessionId"`
	}
	if err = c.callQRRPC(attemptCtx, "createSession", "", 0, struct{}{}, &created); err != nil {
		return
	}
	if created.Session == "" {
		return "", nil, fmt.Errorf("QR login returned an empty session")
	}
	session = created.Session
	qr = &QRCodeResponse{}
	if err = c.callQRRPC(attemptCtx, "createQrCode", "", 0, map[string]string{"authSessionId": session}, qr); err != nil {
		return
	}
	callback, parseErr := url.Parse(qr.CallbackURL)
	if parseErr != nil || callback.Scheme == "" || (callback.Host == "" && callback.Path == "") {
		return "", nil, fmt.Errorf("QR login returned an invalid callback URL")
	}
	publicKey, err := hex.DecodeString(secret.PublicKeyHex)
	if err != nil || len(publicKey) != 32 {
		return "", nil, fmt.Errorf("QR login generated an invalid public key")
	}
	query := callback.Query()
	query.Set("secret", base64.StdEncoding.EncodeToString(publicKey))
	query.Set("e2eeVersion", "1")
	callback.RawQuery = query.Encode()
	qr.CallbackURL = callback.String()
	return
}

func (c *Client) CheckQRCodeVerifiedContext(ctx context.Context, session string, timeout time.Duration) error {
	return c.callQRRPC(ctx, "checkQrCodeVerified", session, timeout, map[string]string{"authSessionId": session}, nil)
}

func (c *Client) VerifyQRCertificate(ctx context.Context, session, certificate string) (bool, error) {
	err := c.callQRRPC(ctx, "verifyCertificate", "", 0, map[string]string{"authSessionId": session, "certificate": certificate}, nil)
	var response *qrRPCError
	if errors.As(err, &response) && response.httpStatus == http.StatusBadRequest {
		return false, nil
	}
	return err == nil, err
}

type qrRPCError struct {
	method                       string
	httpStatus, code, statusCode int
}

func (e *qrRPCError) Error() string {
	return fmt.Sprintf("QR %s failed: HTTP %d, code %d, status %d", e.method, e.httpStatus, e.code, e.statusCode)
}

func IsQRLoginPollExpired(err error) bool {
	var response *qrRPCError
	return errors.As(err, &response) && response.code == 10052 && response.statusCode == 410
}

func (c *Client) CreatePinCode(ctx context.Context, session string) (string, error) {
	var result struct {
		Pin string `json:"pinCode"`
	}
	if err := c.callQRRPC(ctx, "createPinCode", "", 0, map[string]string{"authSessionId": session}, &result); err != nil {
		return "", err
	}
	if result.Pin == "" {
		return "", fmt.Errorf("QR login returned an empty PIN")
	}
	return result.Pin, nil
}

func (c *Client) CheckPinCodeVerifiedContext(ctx context.Context, session string) error {
	return c.callQRRPC(ctx, "checkPinCodeVerified", session, 110*time.Second, map[string]string{"authSessionId": session}, nil)
}

func (c *Client) QRCodeLoginV2(ctx context.Context, session string) (*LoginResult, error) {
	var result struct {
		LoginResult
		LastBindTimestamp string      `json:"lastBindTimestamp"`
		MetaData          LoginResult `json:"metaData"`
	}
	args := map[string]any{"authSessionId": session, "systemName": "CHROMEOS", "modelName": "CHROME", "autoLoginIsRequired": false}
	if err := c.callQRRPC(ctx, "qrCodeLoginV2", "", 0, args, &result); err != nil {
		return nil, err
	}
	res := &result.LoginResult
	res.Attempt = c.LoginAttempt
	res.LastPrimaryBindTime = result.LastBindTimestamp
	res.EncryptedKeyChain = result.MetaData.EncryptedKeyChain
	res.E2EEPublicKey = result.MetaData.E2EEPublicKey
	res.E2EEVersion = result.MetaData.E2EEVersion
	res.E2EEKeyID = result.MetaData.E2EEKeyID
	if res.TokenV3IssueResult != nil && res.TokenV3IssueResult.AccessToken != "" {
		res.AuthToken = res.TokenV3IssueResult.AccessToken
	}
	if res.AuthToken == "" {
		return nil, fmt.Errorf("QR login returned no access token")
	}
	c.AccessToken = res.AuthToken
	return res, nil
}

func (c *Client) callQRRPC(ctx context.Context, method, session string, pollTimeout time.Duration, args, out any) error {
	if c.LoginAttempt == nil {
		return fmt.Errorf("QR login has not started")
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(c.LoginAttempt.Context, cancel)
	defer stop()
	defer cancel()
	if err := c.LoginAttempt.Context.Err(); err != nil {
		return err
	}
	body, err := json.Marshal([]any{args})
	if err != nil {
		return err
	}
	service := "SecondaryQrCodeLoginService"
	if pollTimeout > 0 {
		service = "SecondaryQrCodeLoginPermitNoticeService"
	}
	path := "/api/talk/thrift/LoginQrCode/" + service + "/" + method
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://line-chrome-gw.line-apps.com"+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("x-line-chrome-version", ExtensionVersion)
	req.Header.Set("x-lal", "en_US")
	if session != "" {
		req.Header.Set("X-Line-Session-ID", session)
	}
	if pollTimeout > 0 {
		req.Header.Set("X-LST", strconv.FormatInt(pollTimeout.Milliseconds(), 10))
	}
	signature, err := c.LoginAttempt.Runner.GetSignature(path, string(body), "")
	if err != nil {
		return err
	}
	req.Header.Set("x-hmac", signature)
	httpClient := *c.HTTPClient
	if pollTimeout > 0 {
		httpClient.Timeout = pollTimeout + 10*time.Second
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var wrapper struct {
		Code *int            `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&wrapper)
	if resp.StatusCode != http.StatusOK || (wrapper.Code != nil && *wrapper.Code != 0) {
		response := &qrRPCError{method: method, httpStatus: resp.StatusCode}
		if wrapper.Code != nil {
			response.code = *wrapper.Code
		}
		var details struct {
			StatusCode int `json:"statusCode"`
		}
		if json.Unmarshal(wrapper.Data, &details) == nil {
			response.statusCode = details.StatusCode
		}
		return response
	}
	if decodeErr != nil || wrapper.Code == nil {
		return fmt.Errorf("QR %s returned an invalid response", method)
	}
	if out != nil {
		if err = json.Unmarshal(wrapper.Data, out); err != nil {
			return fmt.Errorf("QR %s returned invalid data", method)
		}
	}
	return ctx.Err()
}
