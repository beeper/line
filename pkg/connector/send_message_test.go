package connector

import (
	"errors"
	"fmt"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"

	"github.com/highesttt/matrix-line-messenger/pkg/e2ee"
	"github.com/highesttt/matrix-line-messenger/pkg/line"
)

func TestLineGroupE2EEReconnectRequiredError(t *testing.T) {
	err := lineGroupE2EEReconnectRequiredError(fmt.Errorf("failed to unwrap group key: %w for 5625926", e2ee.ErrMissingOwnPrivateKey))
	if err == nil {
		t.Fatal("expected missing private key to produce a message status error")
	}

	var status bridgev2.MessageStatus
	if !errors.As(err, &status) {
		t.Fatalf("error %T does not wrap bridgev2.MessageStatus", err)
	}
	if status.Status != event.MessageStatusFail {
		t.Fatalf("Status = %q, want %q", status.Status, event.MessageStatusFail)
	}
	if !status.IsCertain || !status.SendNotice {
		t.Fatalf("status certainty/notice flags = %v/%v, want true/true", status.IsCertain, status.SendNotice)
	}
	if status.Message != lineGroupE2EEReconnectNotice {
		t.Fatalf("Message = %q, want %q", status.Message, lineGroupE2EEReconnectNotice)
	}
	if !errors.Is(err, e2ee.ErrMissingOwnPrivateKey) {
		t.Fatalf("err = %v, want to wrap ErrMissingOwnPrivateKey", err)
	}
}

func TestLineGroupE2EEReconnectRequiredErrorIgnoresNoUsableGroupKey(t *testing.T) {
	err := lineGroupE2EEReconnectRequiredError(line.ErrNoUsableE2EEGroupKey)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

func TestLineGroupE2EEFetchFailureErrorReturnsAuthErrors(t *testing.T) {
	err := lineGroupE2EEFetchFailureError(errLoggedOut)
	if !errors.Is(err, errLoggedOut) {
		t.Fatalf("err = %v, want logged-out error", err)
	}
}

func TestLineGroupE2EEFetchFailureErrorAllowsNoUsableGroupKeyFallback(t *testing.T) {
	err := lineGroupE2EEFetchFailureError(fmt.Errorf("auto-register group key: %w", line.ErrNoUsableE2EEGroupKey))
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

func TestLineGroupE2EEFetchFailureErrorWrapsMissingPrivateKeyStatus(t *testing.T) {
	err := lineGroupE2EEFetchFailureError(fmt.Errorf("failed to unwrap group key: %w", e2ee.ErrMissingOwnPrivateKey))
	var status bridgev2.MessageStatus
	if !errors.As(err, &status) {
		t.Fatalf("error %T does not wrap bridgev2.MessageStatus", err)
	}
	if status.Message != lineGroupE2EEReconnectNotice || status.Status != event.MessageStatusFail {
		t.Fatalf("status = %#v, want reconnect failure notice", status)
	}
}

func TestShouldRetrySendWithoutReplyRelation(t *testing.T) {
	err := errors.New(`API error 400: {"code":10051,"message":"RESPONSE_ERROR","data":{"name":"TalkException","message":"TalkException","code":5,"reason":"not found","parameterMap":null}}`)
	msg := &line.Message{RelatedMessageID: "1234567890"}

	if !shouldRetrySendWithoutReplyRelation(msg, err) {
		t.Fatal("expected related-message not found error to trigger retry")
	}

	msg.RelatedMessageID = ""
	if shouldRetrySendWithoutReplyRelation(msg, err) {
		t.Fatal("message without reply relation should not retry")
	}

	msg.RelatedMessageID = "1234567890"
	if shouldRetrySendWithoutReplyRelation(msg, errors.New("other error")) {
		t.Fatal("non-LINE not found error should not retry")
	}
}

func TestClearReplyRelation(t *testing.T) {
	msg := &line.Message{
		RelatedMessageID:          "1234567890",
		MessageRelationType:       3,
		RelatedMessageServiceCode: 1,
		ContentMetadata: map[string]string{
			"message_relation_server_message_id": "1234567890",
			"message_relation_type":              "3",
			"message_relation_service_code":      "1",
			"keep":                               "value",
		},
	}

	clearReplyRelation(msg)

	if msg.RelatedMessageID != "" || msg.MessageRelationType != 0 || msg.RelatedMessageServiceCode != 0 {
		t.Fatalf("reply relation was not cleared: %#v", msg)
	}
	if _, ok := msg.ContentMetadata["message_relation_server_message_id"]; ok {
		t.Fatal("content metadata relation ID was not cleared")
	}
	if got := msg.ContentMetadata["keep"]; got != "value" {
		t.Fatalf("unrelated content metadata = %q, want value", got)
	}
}
