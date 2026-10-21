package connector

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"

	"github.com/beeper/line/pkg/line"
)

var errBlockedContact = bridgev2.WrapErrorInStatus(errors.New("contact is blocked on LINE")).
	WithIsCertain(true).
	WithSendNotice(true).
	WithErrorReason(event.MessageStatusNoPermission).
	WithStatus(event.MessageStatusFail).
	WithMessage("you blocked this contact on LINE, unblock them to send messages")

func notAMemberStatus(err error) bridgev2.MessageStatus {
	return bridgev2.WrapErrorInStatus(err).
		WithIsCertain(true).
		WithSendNotice(true).
		WithErrorReason(event.MessageStatusNoPermission).
		WithStatus(event.MessageStatusFail).
		WithMessage("you are no longer a member of this LINE chat")
}

// wrapSendError turns an error from a LINE call made for a Matrix event into a
// message status. Errors that already carry a status are returned unchanged.
func wrapSendError(err error) error {
	if err == nil {
		return nil
	}
	var ms bridgev2.MessageStatus
	if errors.As(err, &ms) {
		return err
	}
	switch statusCode := line.HTTPStatusCode(err); {
	case errors.Is(err, errLineSessionInvalidated), errors.Is(err, errLineClientSuperseded), line.IsAuthError(err):
		return bridgev2.WrapErrorInStatus(err).
			WithIsCertain(true).
			WithSendNotice(true).
			WithMessage("not logged in to LINE, reconnect and try again")
	case line.IsNotAMemberError(err):
		return notAMemberStatus(err)
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return bridgev2.WrapErrorInStatus(err).
			WithIsCertain(false).
			WithSendNotice(true).
			WithMessage("timed out waiting for LINE")
	case statusCode == http.StatusTooManyRequests:
		return bridgev2.WrapErrorInStatus(err).
			WithIsCertain(true).
			WithSendNotice(true).
			WithErrorReason(event.MessageStatusNetworkError).
			WithMessage("LINE is rate limiting requests, try again later")
	case statusCode >= 500:
		return bridgev2.WrapErrorInStatus(err).
			WithIsCertain(false).
			WithSendNotice(true).
			WithErrorReason(event.MessageStatusNetworkError).
			WithMessage("LINE server error")
	case line.IsTalkException(err), statusCode >= 400:
		return bridgev2.WrapErrorInStatus(err).
			WithIsCertain(true).
			WithSendNotice(true).
			WithErrorReason(event.MessageStatusNetworkError).
			WithMessage("LINE rejected the request")
	default:
		return bridgev2.WrapErrorInStatus(err).
			WithIsCertain(false).
			WithSendNotice(true).
			WithMessage("failed to reach LINE")
	}
}

// wrapConvertError marks a failure to build the LINE message as certain, since
// nothing has been sent yet.
func wrapConvertError(err error) error {
	if err == nil {
		return nil
	}
	var ms bridgev2.MessageStatus
	if errors.As(err, &ms) {
		return err
	}
	return bridgev2.WrapErrorInStatus(err).
		WithIsCertain(true).
		WithSendNotice(true).
		WithMessage("failed to prepare the message for LINE")
}

// wrapMediaUploadError maps a failed OBS upload done before the message is
// sent, so the message is known not to have been delivered.
func wrapMediaUploadError(err error) error {
	if err == nil {
		return nil
	}
	var ms bridgev2.MessageStatus
	if errors.As(err, &ms) {
		return err
	}
	if line.HTTPStatusCode(err) == http.StatusRequestEntityTooLarge {
		return fmt.Errorf("%w: %w", bridgev2.ErrMediaTooLarge, err)
	}
	if line.IsAuthError(err) || errors.Is(err, errLineSessionInvalidated) || errors.Is(err, errLineClientSuperseded) {
		return wrapSendError(err)
	}
	return fmt.Errorf("%w: %w", bridgev2.ErrMediaReuploadFailed, err)
}
