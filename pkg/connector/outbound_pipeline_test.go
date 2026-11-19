package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

	"github.com/beeper/line/pkg/line"
)

func outboundTestMessage(t *testing.T, lc *LineClient) *bridgev2.MatrixMessage {
	t.Helper()
	portal, err := lc.UserLogin.Bridge.GetPortalByKey(context.Background(), networkid.PortalKey{ID: resolutionMID, Receiver: lc.UserLogin.ID})
	if err != nil {
		t.Fatal(err)
	}
	return &bridgev2.MatrixMessage{MatrixEventBase: bridgev2.MatrixEventBase[*event.MessageEventContent]{
		Portal:  portal,
		Event:   &event.Event{ID: "$test-outbound", Sender: lc.UserLogin.UserMXID, RoomID: "!test:example.com"},
		Content: &event.MessageEventContent{MsgType: event.MsgText, Body: "test message"},
	}}
}

func TestOutboundOwnerAndCanonicalRecipient(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			requests := 0
			lc := newResolutionClient(t, func(req *http.Request) (*http.Response, error) {
				requests++
				if !strings.HasSuffix(req.URL.Path, "/sendMessage") {
					return nil, fmt.Errorf("unexpected endpoint: %s", req.URL.Path)
				}
				var args []json.RawMessage
				if err := json.NewDecoder(req.Body).Decode(&args); err != nil || len(args) != 2 {
					t.Fatalf("invalid send args: %v %v", args, err)
				}
				var sent line.Message
				if err := json.Unmarshal(args[1], &sent); err != nil {
					t.Fatal(err)
				}
				if sent.To != resolutionMID || sent.From != resolutionOwnMID || sent.Text != "test message" {
					t.Fatalf("misrouted/incorrect message: %+v", sent)
				}
				if fail {
					return resolutionResponse(`{"code":42,"message":"send rejected"}`), nil
				}
				return resolutionResponse(`{"code":0,"data":{"id":"line-message-id"}}`), nil
			})
			msg := outboundTestMessage(t, lc)
			login, _, err := msg.Portal.FindPreferredLogin(context.Background(), lc.UserLogin.User, true)
			if err != nil || login != lc.UserLogin {
				t.Fatalf("owner login association: %v", err)
			}
			other := &bridgev2.User{User: &database.User{MXID: "@other:example.com"}, Bridge: lc.UserLogin.Bridge}
			if _, _, err = msg.Portal.FindPreferredLogin(context.Background(), other, true); !errors.Is(err, bridgev2.ErrNotLoggedIn) {
				t.Fatalf("unrelated sender was allowed to use owner's login: %v", err)
			}
			resp, err := lc.HandleMatrixMessage(context.Background(), msg)
			if fail {
				if err == nil || resp != nil {
					t.Fatalf("rejected send reported success: %+v %v", resp, err)
				}
			} else if err != nil || resp == nil || resp.DB.ID != "line-message-id" {
				t.Fatalf("successful send lost LINE acknowledgment: %+v %v", resp, err)
			}
			if requests != 1 {
				t.Fatalf("send RPCs=%d", requests)
			}
		})
	}
}

func TestOutboundRequestCancellation(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	lc := newResolutionClient(t, func(req *http.Request) (*http.Response, error) {
		close(started)
		select {
		case <-req.Context().Done():
			return nil, req.Context().Err()
		case <-release:
			return nil, context.Canceled
		}
	})
	msg := outboundTestMessage(t, lc)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := lc.HandleMatrixMessage(ctx, msg); done <- err }()
	select {
	case <-started:
	case <-time.After(30 * time.Second):
		cancel()
		close(release)
		<-done
		t.Fatal("send did not start")
	}
	cancel()
	select {
	case err := <-done:
		close(release)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("send returned %v", err)
		}
	case <-time.After(time.Second):
		close(release)
		<-done
		t.Fatal("LINE send did not inherit Matrix request cancellation")
	}
}
