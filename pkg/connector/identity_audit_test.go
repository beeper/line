package connector

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/beeper/line/pkg/line"
)

func TestLookupToOutboundIdentityRepresentations(t *testing.T) {
	for _, tc := range []struct{ name, returned, expected string }{
		{"legacy", resolutionMID, resolutionMID},
		{"legacy uppercase", strings.ToUpper(resolutionMID), resolutionMID},
		{"opaque", opaqueMID, opaqueMID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			lc := newResolutionClient(t, func(req *http.Request) (*http.Response, error) {
				calls++
				switch {
				case strings.HasSuffix(req.URL.Path, "/findContactByUserid"):
					return resolutionResponse(`{"code":0,"data":{"mid":"` + tc.returned + `","displayName":"Fixture"}}`), nil
				case strings.HasSuffix(req.URL.Path, "/sendMessage"):
					var args []json.RawMessage
					if err := json.NewDecoder(req.Body).Decode(&args); err != nil || len(args) != 2 {
						t.Fatal("bad send args")
					}
					var sent line.Message
					if err := json.Unmarshal(args[1], &sent); err != nil {
						t.Fatal(err)
					}
					if sent.To != tc.expected || sent.From != resolutionOwnMID {
						t.Fatalf("identity changed: %+v", sent)
					}
					return resolutionResponse(`{"code":0,"data":{"id":"fixture-message"}}`), nil
				default:
					t.Fatalf("unexpected sync/auth/API operation: %s", req.URL.Path)
					return nil, nil
				}
			})
			ctx := context.Background()
			resolved, err := lc.ResolveIdentifier(ctx, "fixture.id", true)
			if err != nil {
				t.Fatal(err)
			}
			msg := outboundTestMessage(t, lc)
			msg.Portal, err = lc.UserLogin.Bridge.GetPortalByKey(ctx, resolved.Chat.PortalKey)
			if err != nil {
				t.Fatal(err)
			}
			response, err := lc.HandleMatrixMessage(ctx, msg)
			if err != nil || response == nil {
				t.Fatalf("send failed: %v", err)
			}
			if calls != 2 || !lc.hasAccessToken() || lc.isSessionInvalidated() {
				t.Fatal("unexpected calls or invalidated session")
			}
		})
	}
}

func TestUnsupportedLookupMIDDoesNotInvalidateLogin(t *testing.T) {
	lc := newResolutionClient(t, func(*http.Request) (*http.Response, error) {
		return resolutionResponse(`{"code":0,"data":{"mid":"U-unsupported-length"}}`), nil
	})
	result, err := lc.ResolveIdentifier(context.Background(), "fixture.id", true)
	if err == nil || result != nil {
		t.Fatal("unverified recipient accepted")
	}
	if !lc.hasAccessToken() || lc.isSessionInvalidated() {
		t.Fatal("lookup validation invalidated login")
	}
	assertPortalCount(t, lc, 0)
}
