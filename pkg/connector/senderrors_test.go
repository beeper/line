package connector

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"

	"github.com/beeper/line/pkg/line"
)

func TestWrapSendErrorRemoteRejections(t *testing.T) {
	talkException := func(code int, reason string) error {
		return fmt.Errorf(`API error 400: {"code":10051,"message":"RESPONSE_ERROR","data":{"name":"TalkException","message":"TalkException","code":%d,"reason":%q,"parameterMap":{}}}`, code, reason)
	}
	for _, tc := range []struct {
		name       string
		err        error
		wantStatus event.MessageStatus
		wantSure   bool
	}{
		{name: "excessive access", err: talkException(4, "too many requests"), wantStatus: event.MessageStatusRetriable, wantSure: true},
		{name: "internal error", err: talkException(20, "internal error"), wantStatus: event.MessageStatusRetriable, wantSure: false},
		{name: "illegal argument", err: talkException(0, "invalid message"), wantStatus: event.MessageStatusFail, wantSure: true},
		{name: "abuse block", err: talkException(35, "abuse block"), wantStatus: event.MessageStatusFail, wantSure: true},
		{name: "plain 4xx", err: &line.HTTPError{StatusCode: http.StatusBadRequest}, wantStatus: event.MessageStatusFail, wantSure: true},
		{name: "5xx", err: &line.HTTPError{StatusCode: http.StatusBadGateway}, wantStatus: event.MessageStatusRetriable, wantSure: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var status bridgev2.MessageStatus
			if !errors.As(wrapSendError(tc.err), &status) {
				t.Fatal("error does not wrap bridgev2.MessageStatus")
			}
			if status.Status != tc.wantStatus || status.IsCertain != tc.wantSure {
				t.Fatalf("status/certain = %q/%v, want %q/%v", status.Status, status.IsCertain, tc.wantStatus, tc.wantSure)
			}
			if status.Message == "" || status.ErrorReason != event.MessageStatusNetworkError {
				t.Fatalf("message/reason = %q/%q, want text and %q", status.Message, status.ErrorReason, event.MessageStatusNetworkError)
			}
		})
	}
}
