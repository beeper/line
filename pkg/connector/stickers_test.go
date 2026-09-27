package connector

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/rs/zerolog"
	"go.mau.fi/util/dbutil"
	"go.mau.fi/util/exsync"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/highesttt/matrix-line-messenger/pkg/e2ee"
	"github.com/highesttt/matrix-line-messenger/pkg/line"
)

type stickerTestTransport func(*http.Request) (*http.Response, error)

func (f stickerTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func stickerResponse(data string) *http.Response {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(data))}
}

type stickerTestMatrix struct {
	bridgev2.MatrixAPI
	uploads     int
	states      map[string]event.ImagePackEventContent
	stateWrites int
}

func (m *stickerTestMatrix) UploadMedia(_ context.Context, room id.RoomID, data []byte, name, mime string) (id.ContentURIString, *event.EncryptedFileInfo, error) {
	if room != "" || mime != "image/png" {
		return "", nil, fmt.Errorf("bad upload")
	}
	m.uploads++
	return id.ContentURIString(fmt.Sprintf("mxc://test/sticker%d", m.uploads)), nil, nil
}

func stickerTestProduct(t *testing.T) line.ShopProduct {
	t.Helper()
	var p line.ShopProduct
	if err := json.Unmarshal([]byte(`{"id":"123","name":"Cats","latestVersion":"1","validUntil":"-1","productTypeSummary":{"stickerSummary":{"stickerResourceType":2,"stickerIdRanges":[{"start":"100","size":2}]}}}`), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func testMatrixMessage(portalID networkid.PortalID, content *event.MessageEventContent) *bridgev2.MatrixMessage {
	return &bridgev2.MatrixMessage{
		MatrixEventBase: bridgev2.MatrixEventBase[*event.MessageEventContent]{
			Portal:  &bridgev2.Portal{Portal: &database.Portal{PortalKey: networkid.PortalKey{ID: portalID}}},
			Content: content,
		},
	}
}

func stickerTestClient(t *testing.T) (*LineClient, *stickerTestMatrix) {
	t.Helper()
	raw, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	raw.SetMaxOpenConns(1)
	t.Cleanup(func() { raw.Close() })
	db, err := dbutil.NewWithDB(raw, "sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec(`CREATE TABLE kv_store (bridge_id TEXT,key TEXT,value TEXT,PRIMARY KEY(bridge_id,key))`); err != nil {
		t.Fatal(err)
	}
	bdb := database.New("line", database.MetaTypes{}, db)
	matrix := &stickerTestMatrix{}
	lc := &LineClient{
		Mid:         "ume",
		AccessToken: "test",
		UserLogin: &bridgev2.UserLogin{
			UserLogin: &database.UserLogin{ID: "ume"},
			Bridge:    &bridgev2.Bridge{DB: bdb, Bot: matrix, Log: zerolog.Nop()},
		},
		stickerCatalogs: map[string]stickerCatalog{
			line.StickerShop: {Products: []line.ShopProduct{stickerTestProduct(t)}, Fetched: time.Now()},
		},
	}
	var pngData bytes.Buffer
	png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	lc.HTTPClient = &http.Client{Transport: stickerTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "stickershop.line-scdn.net" {
			t.Fatal(r.URL)
		}
		return stickerResponse(pngData.String()), nil
	})}
	return lc, matrix
}

func TestStickerImportPersistenceAndNativeSend(t *testing.T) {
	lc, matrix := stickerTestClient(t)
	pack, err := lc.DownloadImagePack(t.Context(), "https://store.line.me/stickershop/product/123/en")
	if err != nil {
		t.Fatal(err)
	}
	if len(pack.Content.Images) != 2 || matrix.uploads != 2 {
		t.Fatal(pack)
	}
	img := pack.Content.Images["line_100"]
	if img.Info.Width != 2 || img.Info.BridgedSticker.ID != "100" {
		t.Fatal(img)
	}

	if _, err = lc.DownloadImagePack(t.Context(), "line://stickershop/123"); err != nil {
		t.Fatal(err)
	}
	if matrix.uploads != 2 {
		t.Fatal("reuploaded cached pack")
	}
	fresh := &LineClient{Mid: lc.Mid, AccessToken: lc.AccessToken, UserLogin: lc.UserLogin, stickerCatalogs: lc.stickerCatalogs, E2EE: &e2ee.Manager{}}
	previous := newLineAPIClient
	t.Cleanup(func() { newLineAPIClient = previous })
	sent := 0
	newLineAPIClient = func(token string) *line.Client {
		c := line.NewClient(token)
		c.HTTPClient = &http.Client{Transport: stickerTestTransport(func(r *http.Request) (*http.Response, error) {
			if !strings.HasSuffix(r.URL.Path, "/sendMessage") {
				t.Fatalf("native sticker tried E2EE or upload: %s", r.URL)
			}
			var args []json.RawMessage
			json.NewDecoder(r.Body).Decode(&args)
			var msg line.Message
			json.Unmarshal(args[1], &msg)
			if msg.ContentType != 7 || !msg.HasContent || msg.ContentMetadata["STKPKGID"] != "123" || msg.ContentMetadata["STKID"] != "100" || msg.ContentMetadata["STKOPT"] != "A" || len(msg.Chunks) > 0 || msg.ContentMetadata["e2eeVersion"] != "" {
				t.Fatalf("bad native message: %+v", msg)
			}
			sent++
			return stickerResponse(`{"code":0,"data":{"id":"remote-sticker"}}`), nil
		})}
		return c
	}
	for _, kind := range []event.MessageType{event.CapMsgSticker, event.MsgImage} {
		content := &event.MessageEventContent{MsgType: kind, Body: "sticker", URL: img.URL}
		response, err := fresh.HandleMatrixMessage(t.Context(), testMatrixMessage("ufriend", content))
		if err != nil {
			t.Fatal(err)
		}
		if response.DB.ID != "remote-sticker" {
			t.Fatal(response)
		}
	}
	if sent != 2 {
		t.Fatal(sent)
	}
}

func TestStickerOwnershipAndURLValidation(t *testing.T) {
	lc, _ := stickerTestClient(t)
	pack, err := lc.DownloadImagePack(t.Context(), "line://stickershop/123")
	if err != nil {
		t.Fatal(err)
	}
	lc.stickerCatalogs[line.StickerShop] = stickerCatalog{Fetched: time.Now()}
	if _, err = lc.resolveSticker(t.Context(), pack.Content.Images["line_100"].URL); err == nil {
		t.Fatal("allowed unowned sticker")
	}
	if s, err := lc.resolveSticker(t.Context(), "mxc://test/unknown"); s != nil || err != nil {
		t.Fatal("unknown image should remain image")
	}
	for _, u := range []string{"https://evil.test/stickershop/product/123/en", "line://stickershop/../123", "https://store.line.me@evil.test/stickershop/product/123", "line://stickershop/123?x=1"} {
		if _, _, err := parseLinePackURL(u); err == nil {
			t.Fatalf("accepted %s", u)
		}
	}
	p := stickerTestProduct(t)
	p.ValidUntil = json.Number(fmt.Sprint(time.Now().Add(-time.Second).UnixMilli()))
	if activeProduct(p, time.Now()) {
		t.Fatal("accepted expired pack")
	}
}

func (m *stickerTestMatrix) SendState(_ context.Context, room id.RoomID, typ event.Type, key string, content *event.Content, _ time.Time) (*mautrix.RespSendEvent, error) {
	data, err := json.Marshal(content)
	if err != nil {
		return nil, err
	}
	var pack event.ImagePackEventContent
	if err = json.Unmarshal(data, &pack); err != nil {
		return nil, err
	}
	if m.states == nil {
		m.states = make(map[string]event.ImagePackEventContent)
	}
	m.states[string(room)+"/"+typ.Type+"/"+key] = pack
	m.stateWrites++
	return &mautrix.RespSendEvent{EventID: "$pack"}, nil
}

func TestStickerRoomSyncUpdatesAndRemovesOnlyManagedPacks(t *testing.T) {
	lc, matrix := stickerTestClient(t)
	room := id.RoomID("!room:test")
	packs, err := lc.ListImagePacks(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = lc.syncStickerRoom(t.Context(), room, packs); err != nil {
		t.Fatal(err)
	}
	if matrix.stateWrites != 2 {
		t.Fatalf("state writes %d", matrix.stateWrites)
	}
	if err = lc.syncStickerRoom(t.Context(), room, packs); err != nil {
		t.Fatal(err)
	}
	if matrix.stateWrites != 2 {
		t.Fatal("unchanged pack republished")
	}
	matrix.states["unrelated"] = event.ImagePackEventContent{Images: map[string]*event.ImagePackImage{"keep": {Body: "keep"}}}
	if err = lc.syncStickerRoom(t.Context(), room, nil); err != nil {
		t.Fatal(err)
	}
	if matrix.stateWrites != 4 || len(matrix.states["unrelated"].Images) != 1 {
		t.Fatal("removed unrelated state")
	}
	for key, pack := range matrix.states {
		if key != "unrelated" && len(pack.Images) != 0 {
			t.Fatal("expired pack remained")
		}
	}
}

func TestStickerDownloadRejectsOversizeAndRedirect(t *testing.T) {
	lc, _ := stickerTestClient(t)
	lc.HTTPClient.Transport = stickerTestTransport(func(*http.Request) (*http.Response, error) { return stickerResponse("12345"), nil })
	if _, err := lc.fetchStickerAsset(t.Context(), "/test", 4); err == nil {
		t.Fatal("accepted oversized asset")
	}
	lc.HTTPClient.Transport = stickerTestTransport(func(*http.Request) (*http.Response, error) {
		r := stickerResponse("")
		r.StatusCode = 302
		r.Header.Set("Location", "http://localhost/private")
		return r, nil
	})
	if _, err := lc.fetchStickerAsset(t.Context(), "/test", 4); err == nil {
		t.Fatal("followed redirect")
	}
}

func TestStickerPacksAppliedToNewRooms(t *testing.T) {
	lc, matrix := stickerTestClient(t)
	portal := &bridgev2.Portal{
		Portal:      &database.Portal{PortalKey: networkid.PortalKey{ID: "ufriend"}},
		RoomCreated: exsync.NewEvent(),
	}
	runCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	lc.activeRun = &lineClientRun{ctx: runCtx, cancel: cancel}
	info := lc.withStickerPacks(&bridgev2.ChatInfo{})
	info.ExtraUpdates(t.Context(), portal)
	info.ExtraUpdates(t.Context(), portal)
	portal.MXID = "!new:test"
	portal.RoomCreated.Set()
	lc.wg.Wait()
	rooms, err := lc.stickerRooms(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(rooms) != 1 || rooms[0] != portal.MXID || matrix.stateWrites != 2 {
		t.Fatalf("rooms=%v state writes=%d", rooms, matrix.stateWrites)
	}
	for key, pack := range matrix.states {
		if !strings.HasPrefix(key, "!new:test/") || len(pack.Images) != 2 {
			t.Fatalf("unexpected pack state %s: %+v", key, pack)
		}
	}

	existing := &bridgev2.Portal{
		Portal:      &database.Portal{PortalKey: networkid.PortalKey{ID: "uother"}, MXID: "!existing:test"},
		RoomCreated: exsync.NewEvent(),
	}
	info.ExtraUpdates(t.Context(), existing)
	if _, ok := lc.pendingStickerRooms.Load(existing.PortalKey); ok {
		t.Fatal("existing room was scheduled for packs")
	}
}

func TestStickerPacksNotAppliedAfterDisconnect(t *testing.T) {
	lc, matrix := stickerTestClient(t)
	runCtx, cancel := context.WithCancel(t.Context())
	lc.activeRun = &lineClientRun{ctx: runCtx, cancel: cancel}
	pending := &bridgev2.Portal{
		Portal:      &database.Portal{PortalKey: networkid.PortalKey{ID: "upending"}},
		RoomCreated: exsync.NewEvent(),
	}
	info := lc.withStickerPacks(&bridgev2.ChatInfo{})
	info.ExtraUpdates(t.Context(), pending)
	lc.Disconnect()
	if _, ok := lc.pendingStickerRooms.Load(pending.PortalKey); ok {
		t.Fatal("pending room task outlived the client")
	}
	late := &bridgev2.Portal{
		Portal:      &database.Portal{PortalKey: networkid.PortalKey{ID: "ulate"}},
		RoomCreated: exsync.NewEvent(),
	}
	info.ExtraUpdates(t.Context(), late)
	if _, ok := lc.pendingStickerRooms.Load(late.PortalKey); ok {
		t.Fatal("room task started after disconnect")
	}
	if matrix.stateWrites != 0 {
		t.Fatalf("state writes after disconnect: %d", matrix.stateWrites)
	}
}
