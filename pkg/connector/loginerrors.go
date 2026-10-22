package connector

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"

	"github.com/beeper/line/pkg/line"
)

// genericLoginFailureReason is shown in the login form when LINE rejects the sign-in but
// gives no reason we can quote. It replaces formatting the raw Go error into the
// instructions, which leaked internal detail into user-facing copy.
const genericLoginFailureReason = "LINE rejected the sign-in. Please check your email and password and try again."

var (
	ErrLoginVerificationFailed = bridgev2.RespError{
		ErrCode:    "DEV.HIGHEST.LINE.VERIFICATION_FAILED",
		Err:        "LINE didn't confirm the verification. Please start the login again.",
		StatusCode: http.StatusBadRequest,
	}
	ErrLoginNoKeychain = bridgev2.RespError{
		ErrCode:    "DEV.HIGHEST.LINE.NO_KEYCHAIN",
		Err:        "LINE finished signing in without sending the encryption keychain. Please reconnect and complete the verification prompt in the LINE app.",
		StatusCode: http.StatusBadRequest,
	}
	ErrLoginTooManyAttempts = bridgev2.RespError{
		ErrCode:    "DEV.HIGHEST.LINE.TOO_MANY_ATTEMPTS",
		Err:        loginTooManyAttemptsReason,
		StatusCode: http.StatusBadRequest,
	}
	ErrLoginRejected = bridgev2.RespError{
		ErrCode:    "DEV.HIGHEST.LINE.LOGIN_REJECTED",
		Err:        genericLoginFailureReason,
		StatusCode: http.StatusUnauthorized,
	}
	ErrLoginUnknown = bridgev2.RespError(mautrix.MUnknown.WithMessage("Internal error logging in to LINE"))
)

// verificationTemporarilyUnavailableCooldown is the bridge-side cooldown for
// LINE's "Verification is temporarily unavailable" provider alert. It is a
// bridge policy, not a LINE-provided retry guarantee.
const verificationTemporarilyUnavailableCooldown = 10 * time.Minute

// intPtr returns a pointer to v when v is non-zero, otherwise nil.
func intPtr(v int) *int {
	if v == 0 {
		return nil
	}
	return &v
}

// safeEmailLoginReason returns a user-facing reason string for known LINE email
// login rejection reasons. It returns the standard rate-limit message for rate
// limits, a fixed message for verification unavailability, and the original
// string only for a small allowlist of safe provider reasons. Unrecognized
// reasons return empty so callers can fall back to generic copy.
func safeEmailLoginReason(reason string) string {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "blocked user", "too many login attempts":
		return loginTooManyAttemptsReason
	case "verification is temporarily unavailable":
		return line.UserMessageForCategory(line.AuthErrorVerificationTemporarilyUnavailable)
	case "authentication failed", "account id or password is invalid":
		return reason
	}
	return ""
}

// wrapLineLoginError translates a LINE error into one the client can act on,
// keeping the original in the chain with %w so logs are unaffected.
func wrapLineLoginError(err error) error {
	if err == nil {
		return nil
	}
	details := parseLoginErrorDetails(err)
	reason := details.ErrorReason
	if reason == "" {
		reason = details.ErrorMessage
	}
	safeReason := safeEmailLoginReason(reason)
	cat := classifyEmailError(details, reason)

	mapped := ErrLoginUnknown
	switch {
	case errors.Is(err, line.ErrLoginVerificationExpired):
		mapped = ErrLoginVerificationFailed
	case cat == line.AuthErrorTooManyAttempts:
		mapped = ErrLoginTooManyAttempts
	case cat == line.AuthErrorRetryLater:
		mapped = ErrLoginRateLimited.WithMessage("%s", loginRetryLaterMessage)
	case cat == line.AuthErrorVerificationTemporarilyUnavailable:
		mapped = ErrLoginRejected.WithMessage("%s", line.UserMessageForCategory(line.AuthErrorVerificationTemporarilyUnavailable))
	case details.HTTPStatus == http.StatusUnauthorized, details.HTTPStatus == http.StatusForbidden:
		mapped = ErrLoginRejected
	case safeReason != "":
		mapped = ErrLoginRejected.WithMessage("%s", safeReason)
	}

	ae := &line.AuthError{
		Flow:                "email",
		Stage:               line.AuthStageEmailLogin,
		HTTPStatus:          details.HTTPStatus,
		ResponseCode:        intPtr(details.ResponseCode),
		ExceptionType:       details.ErrorName,
		ExceptionCode:       intPtr(details.ErrorCode),
		Category:            cat,
		ProviderReason:      line.SanitizeProviderAlert(reason),
		ProviderAlertStatus: line.SafeAlertStatus(details.ErrorReason != ""),
		UserMessage:         mapped.Err,
		RetryRecommended:    isRetryableCategory(cat),
		Temporary:           isTemporaryCategory(cat),
		UserActionRequired:  true,
		BridgeFailure:       false,
		CooldownTriggered:   cat == line.AuthErrorTooManyAttempts || cat == line.AuthErrorRetryLater || cat == line.AuthErrorVerificationTemporarilyUnavailable,
		Err:                 err,
	}
	return fmt.Errorf("%w: %w", ae, mapped)
}

// classifyEmailError classifies an email/login error into an AuthErrorCategory.
func classifyEmailError(details loginErrorDetails, reason string) line.AuthErrorCategory {
	switch {
	case isBlockedUserLoginError(reason):
		return line.AuthErrorTooManyAttempts
	case isLoginRateLimitText(reason):
		return line.AuthErrorTooManyAttempts
	case details.HTTPStatus == http.StatusTooManyRequests:
		return line.AuthErrorTooManyAttempts
	case strings.EqualFold(strings.TrimSpace(reason), "blocked user"):
		return line.AuthErrorTooManyAttempts
	case strings.EqualFold(strings.TrimSpace(reason), "authentication failed"):
		return line.AuthErrorInvalidCredentials
	case strings.EqualFold(strings.TrimSpace(reason), "account id or password is invalid"):
		return line.AuthErrorInvalidCredentials
	case strings.EqualFold(strings.TrimSpace(reason), "verification is temporarily unavailable"):
		return line.AuthErrorVerificationTemporarilyUnavailable
	case details.HTTPStatus == http.StatusUnauthorized, details.HTTPStatus == http.StatusForbidden:
		return line.AuthErrorInvalidCredentials
	default:
		return line.AuthErrorUnknown
	}
}

// isRetryableCategory returns true if the category suggests a retry may succeed.
func isRetryableCategory(cat line.AuthErrorCategory) bool {
	switch cat {
	case line.AuthErrorTooManyAttempts, line.AuthErrorRetryLater,
		line.AuthErrorVerificationTemporarilyUnavailable, line.AuthErrorServiceUnavailable,
		line.AuthErrorNetworkUnavailable:
		return true
	default:
		return false
	}
}

// isTemporaryCategory returns true if the category is likely temporary.
func isTemporaryCategory(cat line.AuthErrorCategory) bool {
	switch cat {
	case line.AuthErrorTooManyAttempts, line.AuthErrorRetryLater,
		line.AuthErrorVerificationTemporarilyUnavailable, line.AuthErrorServiceUnavailable:
		return true
	default:
		return false
	}
}

// qrLoginFailureMessage maps a rejected QR request to fixed user-facing copy.
// The mobile app shows no detail for these rejections, so the bridge is the
// only place the reason can be shown. Provider text is never quoted; only fixed
// schema labels are used.
func qrLoginFailureMessage(detail line.QRFailureDetail) string {
	switch detail.ExceptionLabel {
	case "NOT_ALLOWED_QR_CODE_LOGIN":
		return "LINE doesn't allow QR sign-in for this account. Please sign in with your email and password instead."
	case "INVALID_CONTEXT":
		return "LINE rejected the QR sign-in because the sign-in context was no longer valid. Please start the sign-in again with a fresh QR code."
	case "VERIFICATION_FAILED":
		return "LINE didn't confirm the QR sign-in. Please scan the QR code again and try once more."
	case "VERIFICATION_NOTICE_FAILED":
		return "LINE didn't confirm the QR sign-in request. Please try again."
	case "APP_UPGRADE_REQUIRED":
		return "LINE requires a newer version of the app. Please update the LINE app and try again."
	case "ILLEGAL_ARGUMENT":
		return "LINE rejected the QR sign-in request. Please start the sign-in again."
	}
	return "LINE rejected the QR sign-in. Please try again."
}

// wrapQRRPCError translates a rejected QR request into a user-facing error.
// The original error stays in the chain with %w so logs are unaffected.
// It now preserves the structured AuthError for caller inspection.
func wrapQRRPCError(err error) error {
	if err == nil {
		return nil
	}
	var respErr bridgev2.RespError
	if errors.As(err, &respErr) {
		return err
	}

	// Extract the structured AuthError from the underlying QRRPCError, if
	// available, to preserve the provider classification and safe alert status.
	var qrErr *line.QRRPCError
	if errors.As(err, &qrErr) && qrErr.AuthError() != nil {
		ae := qrErr.AuthError()
		var mapped bridgev2.RespError
		switch ae.Category {
		case line.AuthErrorTooManyAttempts:
			mapped = ErrLoginTooManyAttempts
		case line.AuthErrorRetryLater:
			mapped = ErrLoginRateLimited.WithMessage("%s", loginRetryLaterMessage)
		case line.AuthErrorVerificationTemporarilyUnavailable:
			mapped = ErrLoginRejected.WithMessage("%s", ae.UserMessage)
		default:
			mapped = ErrLoginRejected.WithMessage("%s", qrLoginFailureMessage(detail))
		}
		return fmt.Errorf("%w: %w", ae, mapped)
	}

	var mapped bridgev2.RespError
	switch statusCode := line.HTTPStatusCode(err); {
	case errors.Is(err, errLineSessionInvalidated), errors.Is(err, errLineClientSuperseded), line.IsAuthError(err):
		mapped = bridgev2.RespError(mautrix.MForbidden.WithMessage("Not logged in to LINE. Reconnect and try again."))
	case line.IsNotAMemberError(err):
		mapped = bridgev2.RespError(mautrix.MForbidden.WithMessage("You are not a member of this LINE chat"))
	case line.IsTalkExceptionNotFound(err):
		mapped = bridgev2.RespError(mautrix.MNotFound.WithMessage("Not found on LINE"))
	case statusCode == http.StatusTooManyRequests:
		mapped = bridgev2.RespError(mautrix.MLimitExceeded.WithMessage("LINE is rate limiting requests. Please wait a moment and try again.").WithStatus(http.StatusBadRequest))
	case errors.Is(err, context.DeadlineExceeded):
		mapped = bridgev2.RespError(mautrix.MUnknown.WithMessage("Timed out waiting for LINE"))
	case line.IsTalkException(err), statusCode >= 400 && statusCode < 500:
		mapped = bridgev2.RespError(mautrix.MForbidden.WithMessage("LINE rejected the request"))
	default:
		mapped = bridgev2.RespError(mautrix.MUnknown.WithMessage("Request to LINE failed"))
	}
	return fmt.Errorf("%w: %w", mapped, err)
}

// wrapLineRespError translates an error from a LINE call made for a provisioning
// request into an HTTP error, keeping the original error in the chain.
func wrapLineRespError(err error) error {
	if err == nil {
		return nil
	}
	var respErr bridgev2.RespError
	if errors.As(err, &respErr) {
		return err
	}
	var mapped bridgev2.RespError
	switch statusCode := line.HTTPStatusCode(err); {
	case errors.Is(err, errLineSessionInvalidated), errors.Is(err, errLineClientSuperseded), line.IsAuthError(err):
		mapped = bridgev2.RespError(mautrix.MForbidden.WithMessage("Not logged in to LINE. Reconnect and try again."))
	case line.IsNotAMemberError(err):
		mapped = bridgev2.RespError(mautrix.MForbidden.WithMessage("You are not a member of this LINE chat"))
	case line.IsTalkExceptionNotFound(err):
		mapped = bridgev2.RespError(mautrix.MNotFound.WithMessage("Not found on LINE"))
	case statusCode == http.StatusTooManyRequests:
		mapped = bridgev2.RespError(mautrix.MLimitExceeded.WithMessage("LINE is rate limiting requests. Please wait a moment and try again.").WithStatus(http.StatusBadRequest))
	case errors.Is(err, context.DeadlineExceeded):
		mapped = bridgev2.RespError(mautrix.MUnknown.WithMessage("Timed out waiting for LINE"))
	case line.IsTalkException(err), statusCode >= 400 && statusCode < 500:
		mapped = bridgev2.RespError(mautrix.MForbidden.WithMessage("LINE rejected the request"))
	default:
		mapped = bridgev2.RespError(mautrix.MUnknown.WithMessage("Request to LINE failed"))
	}
	return fmt.Errorf("%w: %w", mapped, err)
}
