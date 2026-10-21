package connector

import (
	"context"
	"errors"
	"fmt"
	"net/http"

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

// wrapLineLoginError translates a LINE error into one the client can act on, keeping the
// original in the chain with %w so logs are unaffected.
func wrapLineLoginError(err error) error {
	if err == nil {
		return nil
	}
	mapped := ErrLoginUnknown
	details := parseLoginErrorDetails(err)
	reason := details.ErrorReason
	if reason == "" {
		reason = details.ErrorMessage
	}
	switch {
	case errors.Is(err, line.ErrLoginVerificationExpired):
		mapped = ErrLoginVerificationFailed
	case isBlockedUserLoginError(reason):
		mapped = ErrLoginTooManyAttempts
	case details.HTTPStatus == http.StatusTooManyRequests:
		mapped = ErrLoginTooManyAttempts
	case details.HTTPStatus == http.StatusUnauthorized, details.HTTPStatus == http.StatusForbidden:
		mapped = ErrLoginRejected
	case reason != "":
		// LINE's own reason strings are short and user-facing, so quote them.
		mapped = ErrLoginRejected.WithMessage("%s", reason)
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
