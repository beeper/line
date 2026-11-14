package line

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// AuthErrorCategory is a normalized classification of an authentication failure.
// It bridges LINE provider-specific exceptions into stable, bridge-side
// categories that drive user-facing messages, cooldowns, and diagnostics.
type AuthErrorCategory int

const (
	// AuthErrorUnknown is the default category for unrecognized failures.
	AuthErrorUnknown AuthErrorCategory = iota
	// AuthErrorInvalidCredentials means LINE rejected the supplied credentials.
	AuthErrorInvalidCredentials
	// AuthErrorVerificationRequired means LINE requires additional verification.
	AuthErrorVerificationRequired
	// AuthErrorVerificationTemporarilyUnavailable means LINE temporarily cannot
	// complete verification (the confirmed "Verification is temporarily
	// unavailable" provider alert).
	AuthErrorVerificationTemporarilyUnavailable
	// AuthErrorTooManyAttempts means LINE rate-limited login attempts.
	AuthErrorTooManyAttempts
	// AuthErrorRetryLater means LINE asked the client to come back later.
	AuthErrorRetryLater
	// AuthErrorQRLoginNotAllowed means LINE does not allow QR sign-in for this
	// account or session.
	AuthErrorQRLoginNotAllowed
	// AuthErrorQRContextExpired means the QR sign-in context expired before
	// completion (only when explicitly supported by the provider response).
	AuthErrorQRContextExpired
	// AuthErrorNetworkUnavailable means LINE could not be reached.
	AuthErrorNetworkUnavailable
	// AuthErrorServiceUnavailable means LINE returned a service error.
	AuthErrorServiceUnavailable
	// AuthErrorUnsupportedMethod means LINE does not support the requested
	// authentication method.
	AuthErrorUnsupportedMethod
)

// String returns a stable schema label for the category.
func (c AuthErrorCategory) String() string {
	switch c {
	case AuthErrorInvalidCredentials:
		return "INVALID_CREDENTIALS"
	case AuthErrorVerificationRequired:
		return "VERIFICATION_REQUIRED"
	case AuthErrorVerificationTemporarilyUnavailable:
		return "VERIFICATION_TEMPORARILY_UNAVAILABLE"
	case AuthErrorTooManyAttempts:
		return "TOO_MANY_ATTEMPTS"
	case AuthErrorRetryLater:
		return "RETRY_LATER"
	case AuthErrorQRLoginNotAllowed:
		return "QR_LOGIN_NOT_ALLOWED"
	case AuthErrorQRContextExpired:
		return "QR_CONTEXT_EXPIRED"
	case AuthErrorNetworkUnavailable:
		return "NETWORK_UNAVAILABLE"
	case AuthErrorServiceUnavailable:
		return "SERVICE_UNAVAILABLE"
	case AuthErrorUnsupportedMethod:
		return "UNSUPPORTED_METHOD"
	default:
		return "UNKNOWN"
	}
}

// AuthErrorStage identifies the stage of the authentication flow where the
// failure occurred.
type AuthErrorStage int

const (
	// AuthStageUnknown is the default stage.
	AuthStageUnknown AuthErrorStage = iota
	// AuthStageQRCreate is the QR session/code creation stage.
	AuthStageQRCreate
	// AuthStageQRVerify is the QR verification/polling stage.
	AuthStageQRVerify
	// AuthStageQRTokenIssue is the qrCodeLoginV2 token-issue stage.
	AuthStageQRTokenIssue
	// AuthStageEmailLogin is the email/password login stage.
	AuthStageEmailLogin
	// AuthStageEmailVerification is PIN/email challenge completion after credentials were accepted.
	AuthStageEmailVerification
	// AuthStageSessionEstablish is the session establishment stage.
	AuthStageSessionEstablish
)

// String returns a stable label for the stage.
func (s AuthErrorStage) String() string {
	switch s {
	case AuthStageQRCreate:
		return "qr_create"
	case AuthStageQRVerify:
		return "qr_verify"
	case AuthStageQRTokenIssue:
		return "qr_token_issue"
	case AuthStageEmailLogin:
		return "email_login"
	case AuthStageEmailVerification:
		return "email_verification"
	case AuthStageSessionEstablish:
		return "session_establish"
	default:
		return "unknown"
	}
}

// AuthError is a structured authentication error that carries LINE provider
// metadata, normalized classification, and user-facing guidance. It is safe to
// log and to present to operators; user-facing messages are derived separately.
type AuthError struct {
	// Flow is the authentication flow (qr or email).
	Flow string
	// Stage is the authentication stage where the failure occurred.
	Stage AuthErrorStage
	// HTTPStatus is the HTTP status code returned by LINE, if any.
	HTTPStatus int
	// ResponseCode is LINE's response code (e.g. 10051), if any.
	ResponseCode *int
	// ExceptionType is LINE's exception type name, if any.
	ExceptionType string
	// ExceptionCode is LINE's exception code, if any.
	ExceptionCode *int
	// Category is the normalized bridge-side classification.
	Category AuthErrorCategory
	// ProviderReason is a safe, normalized provider reason string, never raw
	// provider text.
	ProviderReason string
	// ProviderAlertStatus describes whether the provider alert was recognized.
	ProviderAlertStatus string
	// ProviderMessage is safe provider wording for logs and user-facing copy.
	ProviderMessage string
	// ProviderMessageStatus distinguishes recognized, sanitized, redacted, and suppressed text.
	ProviderMessageStatus string
	// UserMessage is the concise user-facing explanation.
	UserMessage string
	// RetryRecommended indicates whether retrying is appropriate.
	RetryRecommended bool
	// RetryAfter is the suggested wait duration before retrying, if known.
	RetryAfter *time.Duration
	// Temporary indicates whether the failure is likely temporary.
	Temporary bool
	// UserActionRequired indicates whether the user must take action.
	UserActionRequired bool
	// BridgeFailure indicates whether the failure originated in the bridge.
	BridgeFailure bool
	// CooldownTriggered indicates whether a local cooldown was triggered.
	CooldownTriggered bool
	// Err is the original wrapped error, if any.
	Err error
}

// Error implements the error interface.
func (e *AuthError) Error() string {
	if e.UserMessage != "" {
		return e.UserMessage
	}
	return fmt.Sprintf("LINE authentication failed: %s", e.Category.String())
}

// Unwrap returns the original wrapped error.
func (e *AuthError) Unwrap() error {
	return e.Err
}

var (
	providerCredentialPattern = regexp.MustCompile(`(?i)\b(access[_ -]?token|refresh[_ -]?token|id[_ -]?token|password|passwd|authorization|cookie|authsessionid|session[_ -]?id|qr[_ -]?(token|secret)|credential|secret)\b\s*[:=]\s*("[^"]*"|'[^']*'|[^\s,;.!?]+)([.!?]?)`)
	providerEmailPattern      = regexp.MustCompile(`(?i)\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}\b`)
	providerJWTpattern        = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{8,}\b`)
	providerOpaqueMIDPattern  = regexp.MustCompile(`\bU[A-Za-z0-9_-]{43}\b`)
	providerLongSecretPattern = regexp.MustCompile(`\b[A-Za-z0-9_-]{32,}\b`)
)

// SanitizeProviderMessage keeps useful provider wording while removing common
// credentials and private identifiers. Unknown text is presented only when it
// is short, printable, and not structured or credential-like.
func SanitizeProviderMessage(message string) (safe, status string) {
	if strings.TrimSpace(message) == "" {
		return "", "absent"
	}
	for _, r := range message {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' || unicode.Is(unicode.Cf, r) {
			return "", "suppressed"
		}
	}
	message = strings.ReplaceAll(strings.ReplaceAll(message, "\r\n", "\n"), "\r", "\n")
	message = strings.ReplaceAll(message, "\t", " ")
	message = strings.TrimSpace(message)
	if len([]rune(message)) > 512 {
		return "", "suppressed"
	}
	message = providerCredentialPattern.ReplaceAllString(message, "$1=[redacted]$4")
	message = providerEmailPattern.ReplaceAllString(message, "[redacted email]")
	message = providerJWTpattern.ReplaceAllString(message, "[redacted token]")
	message = providerOpaqueMIDPattern.ReplaceAllString(message, "[redacted LINE ID]")
	message = providerLongSecretPattern.ReplaceAllString(message, "[redacted token]")
	if strings.ContainsAny(message, "{}<>") || strings.Contains(strings.ToLower(message), "bearer ") || strings.Contains(message, "https://") || strings.Contains(message, "http://") {
		return "", "suppressed"
	}
	if (strings.Contains(strings.ToLower(message), "token ") || strings.Contains(strings.ToLower(message), "password ")) && !strings.Contains(message, "[redacted") && !isRecognizedProviderMessage(message) {
		return "", "suppressed"
	}
	if strings.Contains(message, "[redacted") {
		return message, "partially_redacted"
	}
	if isRecognizedProviderMessage(message) {
		return message, "recognized"
	}
	if len([]rune(message)) < 5 {
		return "", "suppressed"
	}
	if !strings.ContainsAny(message, " \n") && !strings.Contains(message, "[redacted") {
		return "", "suppressed"
	}
	return message, "safe_unrecognized"
}

func isRecognizedProviderMessage(message string) bool {
	lower := strings.ToLower(strings.Join(strings.Fields(message), " "))
	for _, phrase := range []string{"verification is temporarily unavailable", "too many login attempts", "retry later", "blocked user", "authentication failed", "account id or password is invalid", "qr code has expired", "qr login is not allowed"} {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

// SanitizeProviderAlert maps a raw LINE provider alert to a safe category
// string. It never returns raw provider text. An empty alert returns "absent".
func SanitizeProviderAlert(alert string) string {
	trimmed := strings.TrimSpace(alert)
	if trimmed == "" {
		return "absent"
	}
	lower := strings.ToLower(trimmed)
	switch {
	case strings.Contains(lower, "verification is temporarily unavailable"):
		return "verification_temporarily_unavailable"
	case strings.Contains(lower, "too many login attempts"):
		return "too_many_login_attempts"
	case strings.EqualFold(lower, "blocked user"):
		return "blocked_user"
	case strings.EqualFold(lower, "authentication failed"):
		return "authentication_failed"
	case strings.EqualFold(lower, "failed to issue v3 token"):
		return "failed_to_issue_v3_token"
	case strings.EqualFold(lower, "account id or password is invalid"):
		return "account_id_or_password_is_invalid"
	default:
		return "unrecognized"
	}
}

// UserMessageForCategory returns the default user-facing message for a
// category. Callers may override with stage-specific wording.
func UserMessageForCategory(c AuthErrorCategory) string {
	switch c {
	case AuthErrorInvalidCredentials:
		return "LINE rejected the email or password. Check your credentials and try again."
	case AuthErrorVerificationRequired:
		return "LINE requires additional verification. Please complete the verification prompt and try again."
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
	case AuthErrorNetworkUnavailable:
		return "Unable to reach LINE right now. Please try again later."
	case AuthErrorServiceUnavailable:
		return "LINE is temporarily unavailable. Please try again later."
	case AuthErrorUnsupportedMethod:
		return "LINE does not support this authentication method. Please try a different sign-in method."
	default:
		return "LINE rejected the login request, but did not provide a recognized reason. Please check the bridge logs or contact your administrator."
	}
}

// IsNetworkError returns true if the error is a network/transport failure.
func IsNetworkError(err error) bool {
	if err == nil {
		return false
	}
	var ae *AuthError
	if errorsAs(err, &ae) {
		switch ae.Category {
		case AuthErrorNetworkUnavailable, AuthErrorServiceUnavailable:
			return true
		}
	}
	return false
}

// IsRateLimited returns true if the error is a rate-limit rejection.
func IsRateLimited(err error) bool {
	if err == nil {
		return false
	}
	var ae *AuthError
	if errorsAs(err, &ae) {
		switch ae.Category {
		case AuthErrorTooManyAttempts, AuthErrorRetryLater:
			return true
		}
	}
	return false
}

// errorsAs wraps errors.As for *AuthError without exposing it in the public
// API surface beyond what callers need.
func errorsAs(err error, target interface{}) bool {
	return errors.As(err, target)
}
