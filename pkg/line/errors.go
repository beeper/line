package line

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

var (
	ErrNoUsableE2EEPublicKey = errors.New("no usable E2EE public key")
	ErrNoUsableE2EEGroupKey  = errors.New("no usable E2EE group key")
	ErrGroupKeyNotFound      = errors.New("group key not found")
)

type tokenRefreshError struct {
	code             int
	loggedOut        bool
	invalidSenderKey bool
	refreshRequired  bool
	requestNeedLogin bool
	diagnostic       string
}

func newTokenRefreshError(code int, response []byte) *tokenRefreshError {
	err := errors.New(string(response))
	result := &tokenRefreshError{
		code: code, loggedOut: IsLoggedOut(err), invalidSenderKey: IsInvalidSenderKey(err),
		refreshRequired: IsRefreshRequired(err), requestNeedLogin: IsRequestNeedLogin(err),
	}
	if !result.loggedOut && !result.invalidSenderKey && !result.refreshRequired && !result.requestNeedLogin {
		result.diagnostic = refreshResponseDiagnostic(response)
	}
	return result
}

func refreshResponseDiagnostic(response []byte) string {
	kind := func(raw []byte) string {
		raw = bytes.TrimSpace(raw)
		if len(raw) == 0 {
			return "absent"
		}
		switch raw[0] {
		case '{':
			return "object"
		case '[':
			return "array"
		case '"':
			return "string"
		case 't', 'f':
			return "boolean"
		case 'n':
			return "null"
		default:
			return "number"
		}
	}
	var root, data map[string]json.RawMessage
	_ = json.Unmarshal(response, &root)
	_ = json.Unmarshal(root["data"], &data)
	parts := []string{"body=" + kind(response)}
	for _, scope := range []struct {
		name   string
		fields map[string]json.RawMessage
	}{{"root", root}, {"data", data}} {
		parts = append(parts, fmt.Sprintf("%s.keys=%d", scope.name, len(scope.fields)))
		for _, key := range []string{"code", "status", "message", "name", "reason", "data", "error", "accessToken", "refreshToken"} {
			value := kind(scope.fields[key])
			if key == "code" || key == "status" {
				var number *int64
				if json.Unmarshal(scope.fields[key], &number) == nil && number != nil {
					value += fmt.Sprintf("(%d)", *number)
				}
			}
			parts = append(parts, scope.name+"."+key+"="+value)
		}
	}
	summary := strings.Join(parts, ",")
	fingerprint := sha256.Sum256([]byte(summary))
	return fmt.Sprintf("response{%s; shape_sha256=%x}", summary, fingerprint[:8])
}

func (e *tokenRefreshError) Error() string {
	message := "refresh response missing access token"
	if e.code != 0 {
		message = fmt.Sprintf("refresh rejected: code %d", e.code)
	}
	switch {
	case e.invalidSenderKey:
		return message + ": invalid sender key"
	case e.requestNeedLogin:
		return message + ": REQUEST_NEED_LOGIN"
	case e.loggedOut:
		return message + ": V3_TOKEN_CLIENT_LOGGED_OUT"
	case e.refreshRequired:
		return message + ": access token refresh required"
	}
	if e.diagnostic != "" {
		return message + ": " + e.diagnostic
	}
	return message
}

// IsRefreshRequired returns true when LINE reports that the access token must
// be refreshed before the request can be retried.
func IsRefreshRequired(err error) bool {
	if err == nil {
		return false
	}
	var refreshErr *tokenRefreshError
	if errors.As(err, &refreshErr) && refreshErr.refreshRequired {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "\"code\":119") ||
		strings.Contains(msg, "access token refresh required")
}

func IsLoggedOut(err error) bool {
	if err == nil {
		return false
	}
	var refreshErr *tokenRefreshError
	if errors.As(err, &refreshErr) && refreshErr.loggedOut {
		return true
	}
	return strings.Contains(err.Error(), "V3_TOKEN_CLIENT_LOGGED_OUT") ||
		IsInvalidSenderKey(err) ||
		IsRequestNeedLogin(err)
}

func IsInvalidSenderKey(err error) bool {
	if err == nil {
		return false
	}
	var refreshErr *tokenRefreshError
	if errors.As(err, &refreshErr) && refreshErr.invalidSenderKey {
		return true
	}
	msg := strings.ToLower(err.Error())
	return hasResponseErrorCode(msg) &&
		strings.Contains(msg, "talkexception") &&
		strings.Contains(msg, "\"code\":83") &&
		strings.Contains(msg, "invalid sender key")
}

func IsRequestNeedLogin(err error) bool {
	if err == nil {
		return false
	}
	var refreshErr *tokenRefreshError
	if errors.As(err, &refreshErr) && refreshErr.requestNeedLogin {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "request_need_login") ||
		hasJSONCode(msg, 10004)
}

func IsUnauthorizedStatus(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "api error 401") ||
		strings.Contains(msg, "api error 403") ||
		strings.Contains(msg, "http 401") ||
		strings.Contains(msg, "http 403") ||
		strings.Contains(msg, "sse error: 401") ||
		strings.Contains(msg, "sse error: 403") ||
		strings.Contains(msg, "obs upload failed (401)") ||
		strings.Contains(msg, "obs upload failed (403)") ||
		strings.Contains(msg, "obs object info failed (401)") ||
		strings.Contains(msg, "obs object info failed (403)") ||
		strings.Contains(msg, "obs download failed (401)") ||
		strings.Contains(msg, "obs download failed (403)")
}

func IsAuthError(err error) bool {
	return IsRefreshRequired(err) || IsLoggedOut(err) || IsUnauthorizedStatus(err)
}

// IsNoUsableE2EEPublicKey returns true when a peer has Letter Sealing disabled
// (negotiateE2EEPublicKey returns empty allowedTypes / specVersion -1, or no key data).
func IsNoUsableE2EEPublicKey(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrNoUsableE2EEPublicKey) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "missing fields (pub=false keyID=-1") ||
		strings.Contains(msg, "missing fields (pub=false keyID=0") ||
		(strings.Contains(msg, "\"allowedTypes\":[]") && strings.Contains(msg, "\"specVersion\":-1"))
}

// IsGroupKeyNotFound returns true when the error is specifically code 5 "not found"
// from getE2EEGroupSharedKey / getLastE2EEGroupSharedKey — meaning no group key has been
// registered yet, but E2EE is supported. Callers should attempt to register a key.
// Matches both the processed error (ErrGroupKeyNotFound) and the raw HTTP 400 error
// from callRPC which contains the TalkException JSON payload.
func IsGroupKeyNotFound(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrGroupKeyNotFound) {
		return true
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "group key not found: not found") {
		return true
	}
	// Match raw API error: HTTP 400 with TalkException code 5 "not found"
	return strings.Contains(msg, "\"code\":10051") &&
		strings.Contains(msg, "talkexception") &&
		(strings.Contains(msg, "\"code\":5,") || strings.Contains(msg, "\"code\":5}"))
}

// IsNoUsableE2EEGroupKey returns true when a group has no shared E2EE key
// (at least one member has Letter Sealing disabled).
func IsNoUsableE2EEGroupKey(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrNoUsableE2EEGroupKey) {
		return true
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "no group key found") ||
		strings.Contains(msg, "no group shared key returned") {
		return true
	}
	// Detect TalkException codes in raw API error strings (HTTP 400 with code 10051).
	// Code 98 = member has LS off; Code 1 = auth failed;
	// Code 100 "exceed max member" = the group is too large for key registration.
	// NOTE: Code 5 "not found" is handled by IsGroupKeyNotFound (auto-register), NOT here.
	if hasResponseErrorCode(msg) && strings.Contains(msg, "talkexception") {
		if strings.Contains(msg, "\"code\":98,") || strings.Contains(msg, "\"code\":98}") ||
			strings.Contains(msg, "\"code\":1,") || strings.Contains(msg, "\"code\":1}") ||
			(hasJSONCode(msg, 100) && (strings.Contains(msg, `"reason":"exceed max member"`) ||
				strings.Contains(msg, `"reason": "exceed max member"`))) {
			return true
		}
	}
	return false
}

type talkExceptionData struct {
	Name    string `json:"name"`
	Message string `json:"message"`
	Code    int    `json:"code"`
	Reason  string `json:"reason"`
}

func loginPollingFailure(httpStatus int, body []byte) error {
	var response struct {
		Code    *int              `json:"code"`
		Message string            `json:"message"`
		Data    talkExceptionData `json:"data"`
	}
	decodeErr := json.Unmarshal(body, &response)
	details := "LINE response without a valid code"
	if decodeErr == nil && response.Code != nil {
		details = fmt.Sprintf("LINE response code %d", *response.Code)
	}
	if decodeErr == nil && response.Code != nil && *response.Code == 10051 && strings.EqualFold(response.Message, "RESPONSE_ERROR") && strings.EqualFold(response.Data.Name, "TalkException") {
		response.Message = "RESPONSE_ERROR"
		response.Data.Name = "TalkException"
		response.Data.Message = safeLoginPollingReason(response.Data.Message)
		response.Data.Reason = safeLoginPollingReason(response.Data.Reason)
		if sanitized, err := json.Marshal(response); err == nil {
			details = string(sanitized)
		}
	}
	if httpStatus != http.StatusOK {
		return fmt.Errorf("LF1 polling failed: API error %d: %s", httpStatus, details)
	}
	return fmt.Errorf("LF1 polling failed: %s", details)
}

func safeLoginPollingReason(reason string) string {
	// Arbitrary provider text can echo secrets, so retain only fixed rejection messages.
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "authentication failed":
		return "authentication failed"
	case "failed to issue v3 token":
		return "Failed to issue V3 token"
	case "blocked user":
		return "blocked user"
	case "account id or password is invalid":
		return "Account ID or password is invalid"
	case "too many login attempts":
		return "Too many login attempts"
	default:
		return ""
	}
}

// IsE2EEGroupMemberMismatch identifies a rejected registration that needs a
// fresh server member/key snapshot, not a plaintext fallback.
func IsE2EEGroupMemberMismatch(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	start := strings.IndexByte(msg, '{')
	if start < 0 {
		return false
	}
	var response struct {
		Code    int               `json:"code"`
		Message string            `json:"message"`
		Data    talkExceptionData `json:"data"`
	}
	if json.NewDecoder(strings.NewReader(msg[start:])).Decode(&response) != nil {
		return false
	}
	return response.Code == 10051 && strings.EqualFold(response.Message, "RESPONSE_ERROR") &&
		strings.EqualFold(response.Data.Name, "TalkException") && response.Data.Code == 99 &&
		strings.EqualFold(strings.TrimSpace(response.Data.Reason), "member count mismatch")
}

// IsGroupKeyNotRegisteredError returns true when SendMessage returns code 99
// "group key is not registered". This means a group key must be registered before
// sending any message (even plain text) to this group.
func IsGroupKeyNotRegisteredError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "\"code\":99,") &&
		strings.Contains(msg, "group key is not registered")
}

// IsTalkExceptionNotFound returns true when LINE wraps a TalkException code 5
// "not found" response. Callers must interpret the method context themselves:
// the same code can mean different missing resources for different Talk APIs.
func IsTalkExceptionNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return hasResponseErrorCode(msg) &&
		strings.Contains(msg, "talkexception") &&
		(strings.Contains(msg, "\"code\":5,") ||
			strings.Contains(msg, "\"code\":5}") ||
			strings.Contains(msg, "\"code\": 5,")) &&
		(strings.Contains(msg, "\"reason\":\"not found\"") ||
			strings.Contains(msg, "\"reason\": \"not found\""))
}

// IsNotAMemberError returns true when the API reports the user is not a member of a chat.
func IsNotAMemberError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return hasResponseErrorCode(msg) &&
		strings.Contains(msg, "talkexception") &&
		strings.Contains(msg, "\"code\":10,") &&
		strings.Contains(msg, "not a member")
}

func IsInvalidPaidReactionType(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return hasResponseErrorCode(msg) &&
		strings.Contains(msg, "invalid paidreactiontype in reactiontype")
}

func hasResponseErrorCode(msg string) bool {
	return strings.Contains(msg, "\"code\":10051") ||
		strings.Contains(msg, "\"code\": 10051") ||
		strings.Contains(msg, "code 10051")
}

func hasJSONCode(msg string, code int) bool {
	value := strconv.Itoa(code)
	for _, prefix := range []string{`"code":`, `"code": `} {
		codeStart := prefix + value
		for _, suffix := range []string{",", "}", " ", "\t", "\r", "\n"} {
			if strings.Contains(msg, codeStart+suffix) {
				return true
			}
		}
	}
	return false
}

func isNoUsableE2EEGroupKeyTalkException(message string, data talkExceptionData) bool {
	if !strings.EqualFold(message, "RESPONSE_ERROR") || !strings.EqualFold(data.Name, "TalkException") {
		return false
	}
	// Error 5 "not found" = no group shared key exists
	// Error 98 "member settings off" = at least one member has LS disabled
	// Error 100 "exceed max member" = the group is too large for key registration
	return (data.Code == 5 && strings.EqualFold(data.Reason, "not found")) ||
		(data.Code == 98 && strings.Contains(strings.ToLower(data.Reason), "member settings off")) ||
		(data.Code == 100 && strings.EqualFold(strings.TrimSpace(data.Reason), "exceed max member"))
}

func parseTalkExceptionData(raw json.RawMessage) talkExceptionData {
	var data talkExceptionData
	_ = json.Unmarshal(raw, &data)
	return data
}

func parseE2EEGroupKeyError(method, message string, rawData json.RawMessage) error {
	talk := parseTalkExceptionData(rawData)
	if isNoUsableE2EEGroupKeyTalkException(message, talk) {
		if talk.Code == 5 {
			return fmt.Errorf("%w: %s", ErrGroupKeyNotFound, talk.Reason)
		}
		return fmt.Errorf("%w: %s", ErrNoUsableE2EEGroupKey, talk.Reason)
	}
	return fmt.Errorf("%s failed: %s", method, message)
}
