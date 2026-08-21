package connector

import (
	"fmt"
	"net/http"

	"maunium.net/go/mautrix/bridgev2"
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
		StatusCode: http.StatusTooManyRequests,
	}
	ErrLoginRejected = bridgev2.RespError{
		ErrCode:    "DEV.HIGHEST.LINE.LOGIN_REJECTED",
		Err:        genericLoginFailureReason,
		StatusCode: http.StatusUnauthorized,
	}
	ErrLoginUnknown = bridgev2.RespError{
		ErrCode:    "M_UNKNOWN",
		Err:        "Internal error logging in to LINE",
		StatusCode: http.StatusInternalServerError,
	}
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
