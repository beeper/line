package connector

import (
	"encoding/json"
	"testing"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"github.com/beeper/line/pkg/line"
)

func newOwnReadReceiptTestClient() *LineClient {
	return &LineClient{
		Mid: "Uown",
		UserLogin: &bridgev2.UserLogin{
			UserLogin: &database.UserLogin{ID: networkid.UserLoginID("Uown")},
		},
	}
}

func TestMakeOwnReadReceiptEvent(t *testing.T) {
	lc := newOwnReadReceiptTestClient()

	receipt, ok := lc.makeOwnReadReceiptEvent(line.Operation{
		Type:        int(OpSendChatChecked),
		Param1:      "Cgroup",
		Param2:      "12345",
		CreatedTime: json.Number("1700000000000"),
	})
	if !ok {
		t.Fatal("receipt was rejected")
	}

	if receipt.GetType() != bridgev2.RemoteEventReadReceipt {
		t.Fatalf("type = %v", receipt.GetType())
	}
	if want := (networkid.PortalKey{ID: "Cgroup", Receiver: "Uown"}); receipt.GetPortalKey() != want {
		t.Fatalf("portal key = %+v, want %+v", receipt.GetPortalKey(), want)
	}
	if sender := receipt.GetSender(); !sender.IsFromMe || sender.Sender != "Uown" {
		t.Fatalf("sender = %+v, want own login", sender)
	}
	if receipt.GetLastReceiptTarget() != "12345" {
		t.Fatalf("last target = %q", receipt.GetLastReceiptTarget())
	}
	if !receipt.GetReadUpTo().IsZero() {
		t.Fatalf("read up to = %v, want zero so an unknown target is not guessed", receipt.GetReadUpTo())
	}
	if want := time.UnixMilli(1700000000000); !receipt.GetTimestamp().Equal(want) {
		t.Fatalf("timestamp = %v, want %v", receipt.GetTimestamp(), want)
	}
}

func TestMakeOwnReadReceiptEventRejectsMalformedOperations(t *testing.T) {
	lc := newOwnReadReceiptTestClient()

	for _, tc := range []struct {
		name string
		op   line.Operation
	}{
		{name: "missing chat", op: line.Operation{Type: int(OpSendChatChecked), Param2: "12345"}},
		{name: "missing message", op: line.Operation{Type: int(OpSendChatChecked), Param1: "Upeer"}},
		{name: "another operation", op: line.Operation{Type: int(OpReadReceipt), Param1: "Upeer", Param2: "12345"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if receipt, ok := lc.makeOwnReadReceiptEvent(tc.op); ok || receipt != nil {
				t.Fatalf("got receipt %+v, ok %v", receipt, ok)
			}
		})
	}
}
