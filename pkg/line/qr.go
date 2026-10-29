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
	"strings"
	"time"

	gen "github.com/beeper/line/pkg"
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
		if authErr := response.AuthError(); authErr != nil {
			switch authErr.Category {
			case AuthErrorTooManyAttempts, AuthErrorRetryLater, AuthErrorVerificationTemporarilyUnavailable, AuthErrorServiceUnavailable:
				return false, err
			}
		}
		return false, nil
	}
	return err == nil, err
}

type qrRPCError struct {
	method         string
	httpStatus     int
	code           *int
	statusCode     *int
	exceptionCode  *int
	exceptionType  string
	exceptionLabel string
	reason         string
	alert          string
	alertStatus    string
	authError      *AuthError
	retryAfter     time.Duration
}

// QRRPCError is the exported alias for qrRPCError, allowing callers to
// type-assert and access the structured AuthError.
type QRRPCError = qrRPCError

// secondaryQrCodeErrorLabels maps the fixed error codes of
// SecondaryQrCodeException to their schema labels. The codes are only
// interpreted for that exception class, since other classes reuse the numbers.
var secondaryQrCodeErrorLabels = map[int]string{
	0:   "INTERNAL_ERROR",
	1:   "ILLEGAL_ARGUMENT",
	2:   "VERIFICATION_FAILED",
	3:   "NOT_ALLOWED_QR_CODE_LOGIN",
	4:   "VERIFICATION_NOTICE_FAILED",
	5:   "RETRY_LATER",
	100: "INVALID_CONTEXT",
	101: "APP_UPGRADE_REQUIRED",
}

func (e *qrRPCError) Error() string {
	message := fmt.Sprintf("QR %s failed: HTTP %d", e.method, e.httpStatus)
	if e.code != nil {
		message += fmt.Sprintf(", code %d", *e.code)
	}
	if e.statusCode != nil {
		message += fmt.Sprintf(", status %d", *e.statusCode)
	}
	if e.exceptionCode != nil {
		message += fmt.Sprintf(", exception code %d", *e.exceptionCode)
	}
	if e.exceptionType != "" {
		message += ", exception type " + e.exceptionType
	}
	if e.reason != "" {
		message += ", reason " + e.reason
	}
	if e.alert != "" {
		message += ", alert " + e.alert
	}
	return message
}

// buildAuthError constructs a structured AuthError from the raw provider
// fields captured before sanitization. It never exposes raw provider text
// through user-facing fields.
func (e *qrRPCError) buildAuthError(rawName string, rawCode *int, rawReason, rawAlert string) *AuthError {
	stage := AuthStageQRTokenIssue
	switch e.method {
	case "createSession", "createQrCode":
		stage = AuthStageQRCreate
	case "verifyCertificate", "createPinCode", "checkQrCodeVerified", "checkPinCodeVerified":
		stage = AuthStageQRVerify
	}
	providerMessage := rawAlert
	if strings.TrimSpace(providerMessage) == "" {
		providerMessage = rawReason
	}
	safeProviderMessage, providerMessageStatus := SanitizeProviderMessage(providerMessage)
	_, providerAlertStatus := SanitizeProviderMessage(rawAlert)
	if e.alertStatus != "" {
		providerAlertStatus = e.alertStatus
	}
	ae := &AuthError{
		Flow:                  "qr",
		Stage:                 stage,
		HTTPStatus:            e.httpStatus,
		ResponseCode:          e.code,
		ExceptionType:         e.exceptionType,
		ExceptionCode:         e.exceptionCode,
		Category:              AuthErrorUnknown,
		ProviderReason:        SanitizeProviderAlert(rawReason),
		ProviderAlertStatus:   providerAlertStatus,
		ProviderMessage:       safeProviderMessage,
		ProviderMessageStatus: providerMessageStatus,
		UserMessage:           UserMessageForCategory(AuthErrorUnknown),
		RetryRecommended:      false,
		Temporary:             false,
		UserActionRequired:    true,
		BridgeFailure:         false,
		CooldownTriggered:     false,
		RetryAfter:            durationPtr(e.retryAfter),
		Err:                   e,
	}
	if ae.ProviderReason == "absent" && rawAlert != "" {
		ae.ProviderReason = SanitizeProviderAlert(rawAlert)
	}

	switch {
	case e.httpStatus == http.StatusTooManyRequests:
		ae.Category = AuthErrorTooManyAttempts
		ae.RetryRecommended, ae.Temporary, ae.CooldownTriggered = true, true, true
	case rawName == "SecondaryQrCodeException" && e.exceptionCode != nil:
		ae.Category, ae.RetryRecommended, ae.Temporary, ae.CooldownTriggered =
			classifySecondaryQrCode(*e.exceptionCode, rawAlert)
	case rawName == "SecondaryQrCodeLoginException" && e.exceptionCode != nil:
		ae.Category, ae.RetryRecommended, ae.Temporary, ae.CooldownTriggered =
			classifySecondaryQrCode(*e.exceptionCode, rawAlert)
	case e.exceptionType == "TalkException" || e.exceptionType == "LoginException":
		ae.Category = classifyTalkException(e.exceptionCode, rawReason, rawAlert)
	case e.exceptionType == "other":
		ae.Category = AuthErrorUnknown
	}

	// Reason and alert text can override schema-label classification when they
	// unambiguously indicate a rate limit or verification unavailability.
	switch {
	case SanitizeProviderAlert(rawReason) == "too_many_login_attempts" || SanitizeProviderAlert(rawAlert) == "too_many_login_attempts":
		ae.Category = AuthErrorTooManyAttempts
	case SanitizeProviderAlert(rawReason) == "blocked_user" || SanitizeProviderAlert(rawAlert) == "blocked_user":
		ae.Category = AuthErrorTooManyAttempts
	case SanitizeProviderAlert(rawReason) == "verification_temporarily_unavailable" || SanitizeProviderAlert(rawAlert) == "verification_temporarily_unavailable":
		ae.Category = AuthErrorVerificationTemporarilyUnavailable
	}

	// Apply consistent retry/temporary/cooldown semantics for categories not
	// already configured by their classifier.
	applyCategoryDefaults(&ae.RetryRecommended, &ae.Temporary, &ae.CooldownTriggered, ae.Category)

	ae.UserMessage = userMessageForStageAndCategory(ae.Stage, ae.Category, rawAlert)
	if providerMessageStatus == "recognized" || providerMessageStatus == "partially_redacted" || providerMessageStatus == "safe_unrecognized" {
		ae.UserMessage = safeProviderMessage
	}
	return ae
}

func durationPtr(duration time.Duration) *time.Duration {
	if duration <= 0 {
		return nil
	}
	return &duration
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds > int64((24*time.Hour)/time.Second) {
			return 24 * time.Hour
		}
		if seconds > 0 {
			return time.Duration(seconds) * time.Second
		}
		return 0
	}
	if at, err := http.ParseTime(value); err == nil && at.After(now) {
		return min(at.Sub(now), 24*time.Hour)
	}
	return 0
}

// applyCategoryDefaults sets RetryRecommended, Temporary, and CooldownTriggered
// from the category when the classifier did not already set them.
func applyCategoryDefaults(retry, temporary, cooldown *bool, cat AuthErrorCategory) {
	switch cat {
	case AuthErrorTooManyAttempts, AuthErrorRetryLater, AuthErrorVerificationTemporarilyUnavailable, AuthErrorServiceUnavailable:
		*retry, *temporary, *cooldown = true, true, true
	case AuthErrorVerificationRequired:
		*retry, *temporary, *cooldown = true, true, true
	case AuthErrorInvalidCredentials, AuthErrorQRLoginNotAllowed, AuthErrorUnsupportedMethod:
		*retry, *temporary, *cooldown = false, false, true
	case AuthErrorQRContextExpired:
		*retry, *temporary, *cooldown = true, false, false
	default:
		*retry, *temporary, *cooldown = false, false, false
	}
}

// AuthError returns the structured authentication error, or nil if the error
// did not carry structured provider metadata.
func (e *qrRPCError) AuthError() *AuthError {
	return e.authError
}

// classifySecondaryQrCode maps SecondaryQrCodeException exception codes to
// bridge-side categories. Code 100 is intentionally NOT treated as ordinary
// QR expiration by default; it maps to VERIFICATION_TEMPORARILY_UNAVAILABLE
// only when the provider alert confirms that meaning.
func classifySecondaryQrCode(code int, alert string) (AuthErrorCategory, bool, bool, bool) {
	switch code {
	case 0:
		return AuthErrorServiceUnavailable, true, true, true
	case 1:
		return AuthErrorInvalidCredentials, false, false, true
	case 2:
		return AuthErrorVerificationRequired, true, true, true
	case 3:
		return AuthErrorQRLoginNotAllowed, false, false, true
	case 4:
		return AuthErrorVerificationRequired, true, true, true
	case 5:
		return AuthErrorRetryLater, true, true, true
	case 100:
		// Code 100 is INVALID_CONTEXT in the schema label map, but the actual
		// provider alert determines the meaning. A confirmed temporary
		// verification alert maps accordingly; otherwise use a neutral
		// fallback rather than claiming QR expiration.
		if SanitizeProviderAlert(alert) == "verification_temporarily_unavailable" {
			return AuthErrorVerificationTemporarilyUnavailable, true, true, true
		}
		return AuthErrorQRContextExpired, true, false, false
	case 101:
		return AuthErrorUnsupportedMethod, false, false, true
	default:
		return AuthErrorUnknown, false, false, false
	}
}

// classifyTalkException maps TalkException codes to categories.
func classifyTalkException(code *int, reason, alert string) AuthErrorCategory {
	if code == nil {
		return AuthErrorUnknown
	}
	switch *code {
	case 1:
		return AuthErrorInvalidCredentials
	case 83:
		return AuthErrorInvalidCredentials
	case 99:
		return AuthErrorServiceUnavailable
	default:
		if SanitizeProviderAlert(reason) == "too_many_login_attempts" ||
			SanitizeProviderAlert(alert) == "too_many_login_attempts" {
			return AuthErrorTooManyAttempts
		}
		return AuthErrorUnknown
	}
}

// userMessageForStageAndCategory returns a stage-aware user-facing message.
func userMessageForStageAndCategory(stage AuthErrorStage, cat AuthErrorCategory, alert string) string {
	switch cat {
	case AuthErrorVerificationTemporarilyUnavailable:
		return "LINE verification is temporarily unavailable for this sign-in. Please wait before trying again."
	case AuthErrorTooManyAttempts:
		return "LINE is temporarily limiting login attempts. Please wait before trying again."
	case AuthErrorRetryLater:
		return "LINE asked us to try the sign-in again later. Please wait before trying again."
	case AuthErrorQRLoginNotAllowed:
		return "LINE is not allowing QR-code login for this account or session. Please sign in with your email and password instead."
	case AuthErrorQRContextExpired:
		return "This QR sign-in has expired. Start a new QR login."
	case AuthErrorInvalidCredentials:
		return "LINE rejected the sign-in credentials. Check your credentials and try again."
	case AuthErrorVerificationRequired:
		return "LINE requires additional verification. Please complete the verification prompt and try again."
	case AuthErrorServiceUnavailable:
		return "LINE is temporarily unavailable. Please try again later."
	case AuthErrorUnsupportedMethod:
		return "LINE does not support this authentication method. Please try a different sign-in method."
	case AuthErrorNetworkUnavailable:
		return "Unable to reach LINE right now. Please try again later."
	default:
		if stage == AuthStageQRTokenIssue {
			return "LINE rejected the QR sign-in request. Please check the bridge logs or contact your administrator."
		}
		return "LINE rejected the login request, but did not provide a recognized reason. Please check the bridge logs or contact your administrator."
	}
}

// QRFailureDetail describes a rejected QR login request. Every field is a fixed
// schema label or an allowlisted category, never raw provider text.
type QRFailureDetail struct {
	Method         string
	HTTPStatus     int
	ResponseCode   int
	ExceptionType  string
	ExceptionCode  int
	ExceptionLabel string
	Reason         string
	Alert          string
}

// QRLoginFailureDetail extracts the sanitized details of a rejected QR login
// request. It reports false for errors that did not come from a QR request.
func QRLoginFailureDetail(err error) (QRFailureDetail, bool) {
	var response *qrRPCError
	if !errors.As(err, &response) {
		return QRFailureDetail{}, false
	}
	detail := QRFailureDetail{
		Method:         response.method,
		HTTPStatus:     response.httpStatus,
		ExceptionType:  response.exceptionType,
		ExceptionLabel: response.exceptionLabel,
		Reason:         response.reason,
		Alert:          response.alert,
	}
	if response.code != nil {
		detail.ResponseCode = *response.code
	}
	if response.exceptionCode != nil {
		detail.ExceptionCode = *response.exceptionCode
	}
	return detail, true
}

func IsQRLoginPollExpired(err error) bool {
	var response *qrRPCError
	return errors.As(err, &response) && response.code != nil && *response.code == 10052 && response.statusCode != nil && *response.statusCode == 410
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
		response := &qrRPCError{method: method, httpStatus: resp.StatusCode, code: wrapper.Code, retryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())}
		var details struct {
			StatusCode *int   `json:"statusCode"`
			Code       *int   `json:"code"`
			Name       string `json:"name"`
			Reason     string `json:"reason"`
			Alert      string `json:"alertMessage"`
		}
		if json.Unmarshal(wrapper.Data, &details) == nil {
			response.statusCode = details.StatusCode
			response.exceptionCode = details.Code
			// Provider strings may contain credentials. Retain fixed schema labels
			// and allowlisted reasons only, never arbitrary message/reason values.
			switch details.Name {
			case "TalkException", "LoginException", "SecondaryQrCodeLoginException", "SecondaryQrCodeException":
				response.exceptionType = details.Name
			case "":
			default:
				response.exceptionType = "other"
			}
			response.reason, _ = SanitizeProviderMessage(details.Reason)
			response.alert, response.alertStatus = SanitizeProviderMessage(details.Alert)
			if details.Alert == "" {
				response.alertStatus = ""
			}
			if details.Name == "SecondaryQrCodeException" && details.Code != nil {
				response.exceptionLabel = secondaryQrCodeErrorLabels[*details.Code]
			}
			response.authError = response.buildAuthError(
				details.Name, details.Code, details.Reason, details.Alert,
			)
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
