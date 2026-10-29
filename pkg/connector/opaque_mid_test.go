package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2/provisionutil"

	"github.com/beeper/line/pkg/line"
)

// Invented values matching the observed 44-byte U + URL-safe alphabet shape.
// Live evidence verified exact public lookup -> getContactsV2 identity roundtrip;
// these fixtures contain no production identifiers or profile data.
const opaqueMID = "U" + "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789_-aDEFg"

func TestOpaqueMIDPublicSearchAndPortalReuse(t *testing.T) {
	calls := 0
	lc := newResolutionClient(t, func(req *http.Request) (*http.Response, error) {
		calls++
		if !strings.HasSuffix(req.URL.Path, "/findContactByUserid") {
			t.Fatalf("unexpected fallback or sync: %s", req.URL.Path)
		}
		return resolutionResponse(`{"code":0,"data":{"mid":"` + opaqueMID + `","displayName":"Fixture"}}`), nil
	})
	ctx := context.Background()
	var logs bytes.Buffer
	lc.UserLogin.Bridge.Log = zerolog.New(&logs)
	ctx = lc.UserLogin.Bridge.Log.WithContext(ctx)
	search, err := lc.SearchUsers(ctx, "fixture.id")
	if err != nil || len(search) != 1 || string(search[0].UserID) != opaqueMID {
		t.Fatalf("search=%+v err=%v", search, err)
	}
	assertPortalCount(t, lc, 0)
	first, err := provisionutil.ResolveIdentifier(ctx, lc.UserLogin, opaqueMID, true)
	if err != nil {
		t.Fatalf("%v: %s", err, logs.String())
	}
	second, err := provisionutil.ResolveIdentifier(ctx, lc.UserLogin, "fixture.id", true)
	if err != nil || second.DMRoomID != first.DMRoomID || second.JustCreated {
		t.Fatalf("portal identity changed: %+v %v", second, err)
	}
	assertPortalCount(t, lc, 1)
	if calls != 2 || lc.UserLogin.Bridge.Matrix.(*resolutionMatrix).created != 1 {
		t.Fatalf("unexpected calls=%d or duplicate room", calls)
	}
}

func TestOpaqueMIDExactIdentity(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		t.Run(map[bool]string{false: "unchanged", true: "case mismatch"}[mismatch], func(t *testing.T) {
			lc := newResolutionClient(t, func(req *http.Request) (*http.Response, error) {
				if !strings.HasSuffix(req.URL.Path, "/getContactsV2") {
					t.Fatalf("wrong endpoint: %s", req.URL.Path)
				}
				var args []json.RawMessage
				if err := json.NewDecoder(req.Body).Decode(&args); err != nil || !strings.Contains(string(args[0]), opaqueMID) {
					t.Fatalf("request changed opaque identity: %s", args)
				}
				mid := opaqueMID
				if mismatch {
					mid = "U" + strings.ToLower(mid[1:])
				}
				return resolutionResponse(`{"code":0,"data":{"contacts":{"` + opaqueMID + `":{"contact":{"mid":"` + mid + `"}}}}}`), nil
			})
			result, err := lc.ResolveIdentifier(context.Background(), " "+opaqueMID+" ", true)
			if mismatch {
				if err == nil || result != nil {
					t.Fatal("case-altered identity accepted")
				}
			} else if err != nil || result == nil || string(result.UserID) != opaqueMID {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			assertPortalCount(t, lc, 0)
		})
	}
}

func TestOpaqueMIDFallbackIdentity(t *testing.T) {
	other := "U" + strings.ToLower(opaqueMID[1:])
	for _, tc := range []struct {
		name, key, mid string
		wantErr        bool
	}{
		{"valid", opaqueMID, opaqueMID, false},
		{"case mismatched body", opaqueMID, other, true},
		{"case mismatched key", other, opaqueMID, true},
		{"unsolicited case variant", other, other, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lc := newResolutionClient(t, func(req *http.Request) (*http.Response, error) {
				switch {
				case strings.HasSuffix(req.URL.Path, "/findContactByUserid"):
					return resolutionResponse(`{"code":0,"data":{}}`), nil
				case strings.HasSuffix(req.URL.Path, "/getAllContactIds"):
					return resolutionResponse(`{"code":0,"data":["` + opaqueMID + `"]}`), nil
				case strings.HasSuffix(req.URL.Path, "/getContactsV2"):
					body, _ := json.Marshal(map[string]any{"code": 0, "data": line.ContactsResponse{Contacts: map[string]line.ContactWrapper{tc.key: {Contact: line.Contact{Mid: tc.mid, DisplayName: "Fixture"}}}}})
					return resolutionResponse(string(body)), nil
				default:
					t.Fatalf("unexpected RPC: %s", req.URL.Path)
					return nil, nil
				}
			})
			results, err := lc.SearchUsers(context.Background(), "Fixture")
			if (err != nil) != tc.wantErr {
				t.Fatalf("results=%+v err=%v", results, err)
			}
			if !tc.wantErr && (len(results) != 1 || string(results[0].UserID) != opaqueMID) {
				t.Fatalf("opaque fallback identity changed: %+v", results)
			}
			if tc.wantErr && len(results) != 0 {
				t.Fatal("invalid fallback accepted")
			}
			assertPortalCount(t, lc, 0)
		})
	}
}
