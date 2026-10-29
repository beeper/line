package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/bridgeconfig"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/provisionutil"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/beeper/line/pkg/line"
)

const resolutionMID = "u0123456789abcdef0123456789abcdef"
const resolutionOwnMID = "ufedcba9876543210fedcba9876543210"

type resolutionTransport func(*http.Request) (*http.Response, error)

func (f resolutionTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type resolutionMatrix struct {
	bridgev2.MatrixConnector
	mu         sync.Mutex
	created    int
	failCreate bool
}

func (*resolutionMatrix) Init(*bridgev2.Bridge) {}
func (m *resolutionMatrix) BotIntent() bridgev2.MatrixAPI {
	return &resolutionIntent{mxid: "@bot:example.com", matrix: m}
}
func (m *resolutionMatrix) GhostIntent(userID networkid.UserID) bridgev2.MatrixAPI {
	return &resolutionIntent{mxid: id.UserID("@line_" + userID + ":example.com"), matrix: m}
}
func (*resolutionMatrix) GetCapabilities() *bridgev2.MatrixCapabilities {
	return &bridgev2.MatrixCapabilities{AutoJoinInvites: true}
}
func (*resolutionMatrix) ParseGhostMXID(id.UserID) (networkid.UserID, bool)         { return "", false }
func (*resolutionMatrix) GenerateDeterministicRoomID(networkid.PortalKey) id.RoomID { return "" }
func (*resolutionMatrix) GetMembers(context.Context, id.RoomID) (map[id.UserID]*event.MemberEventContent, error) {
	return map[id.UserID]*event.MemberEventContent{"@owner:example.com": {Membership: event.MembershipJoin}}, nil
}

type resolutionIntent struct {
	bridgev2.MatrixAPI
	mxid   id.UserID
	matrix *resolutionMatrix
}

func (i *resolutionIntent) GetMXID() id.UserID                                      { return i.mxid }
func (*resolutionIntent) IsDoublePuppet() bool                                      { return false }
func (*resolutionIntent) SetDisplayName(context.Context, string) error              { return nil }
func (*resolutionIntent) SetAvatarURL(context.Context, id.ContentURIString) error   { return nil }
func (*resolutionIntent) SetExtraProfileMeta(context.Context, any) error            { return nil }
func (*resolutionIntent) EnsureInvited(context.Context, id.RoomID, id.UserID) error { return nil }
func (*resolutionIntent) EnsureJoined(context.Context, id.RoomID, ...bridgev2.EnsureJoinedParams) error {
	return nil
}
func (*resolutionIntent) SendState(context.Context, id.RoomID, event.Type, string, *event.Content, time.Time) (*mautrix.RespSendEvent, error) {
	return &mautrix.RespSendEvent{EventID: "$state"}, nil
}
func (i *resolutionIntent) CreateRoom(ctx context.Context, req *mautrix.ReqCreateRoom) (id.RoomID, error) {
	i.matrix.mu.Lock()
	defer i.matrix.mu.Unlock()
	if i.matrix.failCreate {
		return "", errors.New("injected Matrix create failure")
	}
	i.matrix.created++
	return id.RoomID(fmt.Sprintf("!created%d:example.com", i.matrix.created)), nil
}

func newResolutionClient(t *testing.T, transport resolutionTransport) *LineClient {
	t.Helper()
	db, err := dbutil.NewWithDialect("file:"+filepath.ToSlash(filepath.Join(t.TempDir(), "bridge.db")), "sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	connector := &LineConnector{}
	br := bridgev2.NewBridge("test", db, zerolog.New(io.Discard), &bridgeconfig.BridgeConfig{SplitPortals: true}, &resolutionMatrix{}, connector, func(*bridgev2.Bridge) bridgev2.CommandProcessor { return nil })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	br.BackgroundCtx = ctx
	if err = br.DB.Upgrade(ctx); err != nil {
		t.Fatal(err)
	}
	user := &database.User{MXID: "@owner:example.com"}
	if err = br.DB.User.Insert(ctx, user); err != nil {
		t.Fatal(err)
	}
	stored := &database.UserLogin{ID: resolutionOwnMID, UserMXID: user.MXID, Metadata: &UserLoginMetadata{AccessToken: "test-token", Mid: resolutionOwnMID}}
	if err = br.DB.UserLogin.Insert(ctx, stored); err != nil {
		t.Fatal(err)
	}
	login, err := br.GetExistingUserLoginByID(ctx, stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(login.BridgeState.Destroy)
	lc := login.Client.(*LineClient)
	lc.setCachedContact(resolutionOwnMID, line.Contact{Mid: resolutionOwnMID, DisplayName: "Owner"})
	original := newLineAPIClient
	newLineAPIClient = func(token string) *line.Client {
		client := line.NewClient(token)
		client.HTTPClient.Transport = transport
		return client
	}
	t.Cleanup(func() { newLineAPIClient = original })
	return lc
}

func resolutionResponse(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func assertPortalCount(t *testing.T, lc *LineClient, want int) {
	t.Helper()
	portals, err := lc.UserLogin.Bridge.DB.Portal.GetAll(context.Background())
	if err != nil || len(portals) != want {
		t.Fatalf("portal count = %d, err=%v, want %d", len(portals), err, want)
	}
}

func TestResolveIdentifierPublicMIDAndSearch(t *testing.T) {
	var calls []string
	lc := newResolutionClient(t, func(req *http.Request) (*http.Response, error) {
		calls = append(calls, req.URL.Path)
		if !strings.HasSuffix(req.URL.Path, "/findContactByUserid") {
			t.Fatalf("unexpected RPC (possible bulk sync or duplicate lookup): %s", req.URL.Path)
		}
		var args []string
		if err := json.NewDecoder(req.Body).Decode(&args); err != nil || len(args) != 1 || args[0] != "carol.id" {
			t.Fatalf("public ID arguments = %v, %v", args, err)
		}
		return resolutionResponse(`{"code":0,"data":{"mid":"` + resolutionMID + `","displayName":"Carol"}}`), nil
	})
	ctx := context.Background()
	searched, err := lc.SearchUsers(ctx, " CAROL.ID ")
	if err != nil || len(searched) != 1 || searched[0].UserID != resolutionMID {
		t.Fatalf("search = %+v, %v", searched, err)
	}
	assertPortalCount(t, lc, 0)
	resolved, err := lc.ResolveIdentifier(ctx, "carol.id", true)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.UserID != searched[0].UserID || resolved.Chat.PortalKey.ID != resolutionMID || resolved.Chat.PortalKey.Receiver != lc.UserLogin.ID {
		t.Fatalf("incorrect canonical portal: %+v", resolved)
	}
	if resolved.Chat.Portal != nil || resolved.Chat.PortalInfo.Members == nil || *resolved.Chat.PortalInfo.Type != database.RoomTypeDM || *resolved.Chat.PortalInfo.Name != "Carol" {
		t.Fatalf("missing prevalidated DM info: %+v", resolved.Chat)
	}
	assertPortalCount(t, lc, 0)
	midResult, err := lc.ResolveIdentifier(ctx, resolutionMID, true)
	if err != nil || midResult.Chat.PortalKey != resolved.Chat.PortalKey {
		t.Fatalf("MID disagrees with public ID: %+v, %v", midResult, err)
	}
	lookupOnly, err := lc.ResolveIdentifier(ctx, resolutionMID, false)
	if err != nil || lookupOnly.Chat != nil {
		t.Fatalf("lookup-only created chat: %+v, %v", lookupOnly, err)
	}
	if len(calls) != 2 {
		t.Fatalf("got %d RPCs, want one per public lookup and no redundant contact fetch", len(calls))
	}
}

func TestResolveIdentifierMIDLookup(t *testing.T) {
	lc := newResolutionClient(t, func(req *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(req.URL.Path, "/getContactsV2") {
			t.Fatalf("MID sent to wrong endpoint: %s", req.URL.Path)
		}
		return resolutionResponse(`{"code":0,"data":{"contacts":{"` + resolutionMID + `":{"contact":{"mid":"` + resolutionMID + `","displayName":"MID contact"}}}}}`), nil
	})
	result, err := lc.ResolveIdentifier(context.Background(), strings.ToUpper(resolutionMID), true)
	if err != nil || result == nil || result.Chat.PortalKey.ID != resolutionMID {
		t.Fatalf("MID lookup = %+v, %v", result, err)
	}
}

func TestResolveIdentifierPreservesOpaqueMIDCase(t *testing.T) {
	opaque := "U" + "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789_-aDEFg"
	for _, tc := range []struct {
		name, key, responseMID string
		wantErr                bool
	}{
		{"exact opaque", opaque, opaque, false},
		{"different opaque case", "U" + strings.ToLower(opaque[1:]), opaque, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lc := newResolutionClient(t, func(req *http.Request) (*http.Response, error) {
				switch {
				case strings.HasSuffix(req.URL.Path, "/getContactsV2"):
					var args []json.RawMessage
					if err := json.NewDecoder(req.Body).Decode(&args); err != nil || len(args) < 1 {
						t.Fatalf("opaque MID request malformed: %v", err)
					}
					var request struct {
						TargetUserMids []string `json:"targetUserMids"`
					}
					if err := json.Unmarshal(args[0], &request); err != nil || len(request.TargetUserMids) != 1 || request.TargetUserMids[0] != opaque {
						t.Fatalf("opaque MID was changed in request: %q, %v", request, err)
					}
					return resolutionResponse(`{"code":0,"data":{"contacts":{"` + tc.key + `":{"contact":{"mid":"` + tc.responseMID + `","displayName":"Opaque"}}}}}`), nil
				case strings.HasSuffix(req.URL.Path, "/getBuddyProfile"):
					return resolutionResponse(`{"code":0,"data":{}}`), nil
				default:
					t.Fatalf("opaque MID used unexpected RPC: %s", req.URL.Path)
					return nil, nil
				}
			})
			result, err := lc.ResolveIdentifier(context.Background(), opaque, true)
			if tc.wantErr && result != nil || !tc.wantErr && (err != nil || result == nil) {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if tc.wantErr {
				if result != nil {
					t.Fatalf("mismatched opaque identity accepted: %+v", result)
				}
				return
			}
			if result == nil || string(result.UserID) != opaque || string(result.Chat.PortalKey.ID) != opaque {
				t.Fatalf("opaque identity changed: %+v", result)
			}
		})
	}
}

func TestSearchUsersRejectsUnrequestedContactMID(t *testing.T) {
	lc := newResolutionClient(t, func(req *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(req.URL.Path, "/findContactByUserid"):
			return resolutionResponse(`{"code":0,"data":{}}`), nil
		case strings.HasSuffix(req.URL.Path, "/getAllContactIds"):
			return resolutionResponse(`{"code":0,"data":["` + resolutionMID + `"]}`), nil
		case strings.HasSuffix(req.URL.Path, "/getContactsV2"):
			return resolutionResponse(`{"code":0,"data":{"contacts":{"` + resolutionOwnMID + `":{"contact":{"mid":"` + resolutionOwnMID + `","displayName":"spoofed"}}}}}`), nil
		default:
			t.Fatalf("unexpected search RPC: %s", req.URL.Path)
			return nil, nil
		}
	})
	results, err := lc.SearchUsers(context.Background(), "alice")
	if err == nil || len(results) != 0 {
		t.Fatalf("unrequested contact accepted: results=%+v err=%v", results, err)
	}
}

func TestResolveIdentifierFailuresDoNotPersistPortals(t *testing.T) {
	for _, tc := range []struct {
		name, query, response string
		wantErr               bool
	}{
		{"unknown", "unknown.id", `{"code":0,"data":{}}`, false},
		{"restricted", "restricted.id", `{"code":1,"message":"ID search restricted"}`, true},
		{"invalid response", "carol.id", `{"code":0,"data":{"mid":"carol.id"}}`, true},
		{"group response", "carol.id", `{"code":0,"data":{"mid":"c0123456789abcdef0123456789abcdef"}}`, true},
		{"display name", "Carol Smith", "", true},
		{"empty", " ", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lc := newResolutionClient(t, func(req *http.Request) (*http.Response, error) {
				if tc.response == "" {
					t.Fatal("invalid identifier made a network request")
				}
				return resolutionResponse(tc.response), nil
			})
			result, err := lc.ResolveIdentifier(context.Background(), tc.query, true)
			if result != nil || (err != nil) != tc.wantErr {
				t.Fatalf("result=%+v, err=%v", result, err)
			}
			assertPortalCount(t, lc, 0)
		})
	}
}

func TestResolveIdentifierCancellation(t *testing.T) {
	started := make(chan struct{})
	lc := newResolutionClient(t, func(req *http.Request) (*http.Response, error) {
		close(started)
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := lc.ResolveIdentifier(ctx, "carol.id", true); done <- err }()
	select {
	case <-started:
	case <-time.After(30 * time.Second):
		t.Fatal("lookup did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("lookup ignored cancellation")
	}
	assertPortalCount(t, lc, 0)
}

func TestResolveIdentifierConcurrentCanonicalPortalReuse(t *testing.T) {
	lc := newResolutionClient(t, func(req *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(req.URL.Path, "/findContactByUserid") {
			return nil, fmt.Errorf("unexpected RPC: %s", req.URL.Path)
		}
		return resolutionResponse(`{"code":0,"data":{"mid":"` + resolutionMID + `","displayName":"Carol"}}`), nil
	})
	ctx := context.Background()
	const count = 12
	portals := make(chan *bridgev2.Portal, count)
	errors := make(chan error, count)
	var wg sync.WaitGroup
	for range count {
		wg.Go(func() {
			resolved, err := lc.ResolveIdentifier(ctx, "carol.id", true)
			if err != nil {
				errors <- err
				return
			}
			portal, err := lc.UserLogin.Bridge.GetPortalByKey(ctx, resolved.Chat.PortalKey)
			if err != nil {
				errors <- err
				return
			}
			portals <- portal
		})
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	close(portals)
	var first *bridgev2.Portal
	for portal := range portals {
		if first == nil {
			first = portal
		} else if first != portal {
			t.Fatal("duplicate cached portal")
		}
	}
	assertPortalCount(t, lc, 1)
	if first == nil || first.ID != resolutionMID {
		t.Fatal("missing canonical portal")
	}
	// An existing Matrix mapping must survive subsequent resolution.
	first.MXID = "!existing:example.com"
	if err := first.Save(ctx); err != nil {
		t.Fatal(err)
	}
	resolved, err := lc.ResolveIdentifier(ctx, resolutionMID, true)
	if err != nil {
		t.Fatal(err)
	}
	existing, err := lc.UserLogin.Bridge.GetPortalByKey(ctx, resolved.Chat.PortalKey)
	if err != nil || existing.MXID != first.MXID {
		t.Fatalf("existing portal not reused: %v", err)
	}
}

func TestStartChatCreatesReusesAndRetriesRoom(t *testing.T) {
	lc := newResolutionClient(t, func(req *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(req.URL.Path, "/findContactByUserid") {
			return nil, fmt.Errorf("unexpected RPC: %s", req.URL.Path)
		}
		return resolutionResponse(`{"code":0,"data":{"mid":"` + resolutionMID + `","displayName":"Carol"}}`), nil
	})
	matrix := lc.UserLogin.Bridge.Matrix.(*resolutionMatrix)
	ctx := context.Background()
	matrix.failCreate = true
	if _, err := provisionutil.ResolveIdentifier(ctx, lc.UserLogin, "carol.id", true); err == nil {
		t.Fatal("injected room creation failure was ignored")
	}
	// bridgev2 intentionally retains a canonical, retryable portal row after
	// Matrix creation fails; no public-ID alias or wrong-recipient row exists.
	assertPortalCount(t, lc, 1)
	key := networkid.PortalKey{ID: resolutionMID, Receiver: lc.UserLogin.ID}
	row, err := lc.UserLogin.Bridge.DB.Portal.GetByKey(ctx, key)
	if err != nil || row == nil || row.MXID != "" {
		t.Fatalf("failed room state: %+v %v", row, err)
	}
	matrix.failCreate = false
	created, err := provisionutil.ResolveIdentifier(ctx, lc.UserLogin, "carol.id", true)
	if err != nil || created == nil || created.DMRoomID == "" {
		t.Fatalf("create: %+v %v", created, err)
	}
	existing, err := provisionutil.ResolveIdentifier(ctx, lc.UserLogin, resolutionMID, true)
	if err != nil || existing.DMRoomID != created.DMRoomID || existing.JustCreated {
		t.Fatalf("reuse: %+v %v", existing, err)
	}
	if matrix.created != 1 {
		t.Fatalf("created %d rooms", matrix.created)
	}
	assertPortalCount(t, lc, 1)
}
