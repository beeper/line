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
	"maps"
	"net/http"
	"slices"
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
			line.SticonShop:  {Fetched: time.Now()},
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

func emojiTestPack(t *testing.T, lc *LineClient) (*bridgev2.ImportedImagePack, *int) {
	t.Helper()
	var product line.ShopProduct
	productJSON := `{"id":"emojipack","name":"Brown Bear","latestVersion":"2","validUntil":"-1",` +
		`"productTypeSummary":{"sticonSummary":{"sticonResourceType":2}}}`
	if err := json.Unmarshal([]byte(productJSON), &product); err != nil {
		t.Fatal(err)
	}
	lc.stickerCatalogs[line.SticonShop] = stickerCatalog{Products: []line.ShopProduct{product}, Fetched: time.Now()}
	assetTransport := lc.HTTPClient.Transport
	metadataFetches := 0
	lc.HTTPClient.Transport = stickerTestTransport(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "meta.json") {
			metadataFetches++
			return stickerResponse(`{"orders":["001","002"],"altTexts":{"001":"cat","002":"dog"}}`), nil
		}
		return assetTransport.RoundTrip(r)
	})
	pack, err := lc.DownloadImagePack(t.Context(), "line://sticonshop/emojipack")
	if err != nil {
		t.Fatal(err)
	}
	return pack, &metadataFetches
}

func TestEmojiPackSendsNativeInlineEmoji(t *testing.T) {
	lc, _ := stickerTestClient(t)
	pack, metadataFetches := emojiTestPack(t, lc)
	if len(pack.Content.Metadata.Usage) != 2 || pack.Content.Metadata.Usage[0] != event.ImagePackUsageEmoji {
		t.Fatalf("emoji pack usage = %v", pack.Content.Metadata.Usage)
	}
	cat := pack.Content.Images["brown_bear_cat"]
	if cat == nil || pack.Content.Images["brown_bear_dog"] == nil {
		t.Fatalf("unexpected emote keys: %v", slices.Collect(maps.Keys(pack.Content.Images)))
	}
	for range 20 {
		for _, img := range pack.Content.Images {
			if _, err := lc.resolveSticker(t.Context(), img.URL); err != nil {
				t.Fatal(err)
			}
		}
	}
	if *metadataFetches != 1 {
		t.Fatalf("downloaded meta.json %d times", *metadataFetches)
	}

	previous := newLineAPIClient
	t.Cleanup(func() { newLineAPIClient = previous })
	var sent []line.Message
	newLineAPIClient = func(token string) *line.Client {
		c := line.NewClient(token)
		c.HTTPClient = &http.Client{Transport: stickerTestTransport(func(r *http.Request) (*http.Response, error) {
			var args []json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
				t.Fatal(err)
			}
			var msg line.Message
			if err := json.Unmarshal(args[1], &msg); err != nil {
				t.Fatal(err)
			}
			sent = append(sent, msg)
			return stickerResponse(`{"code":0,"data":{"id":"text"}}`), nil
		})}
		return c
	}
	inline := &event.MessageEventContent{
		MsgType:       event.MsgText,
		Body:          "fallback",
		Format:        event.FormatHTML,
		FormattedBody: fmt.Sprintf(`🙂 <img data-mx-emoticon src="%s" alt="(cat)">`, cat.URL),
	}
	standalone := &event.MessageEventContent{MsgType: event.CapMsgSticker, Body: "cat", URL: cat.URL}
	for _, content := range []*event.MessageEventContent{inline, standalone} {
		if _, err := lc.HandleMatrixMessage(t.Context(), testMatrixMessage("ufriend", content)); err != nil {
			t.Fatal(err)
		}
	}
	if inline.Body != "fallback" || standalone.MsgType != event.CapMsgSticker {
		t.Fatal("original event content was mutated")
	}
	if len(sent) != 2 || sent[0].Text != "🙂 (cat)" || sent[1].Text != "(cat)" {
		t.Fatalf("unexpected messages: %+v", sent)
	}
	for _, msg := range sent {
		if msg.ContentType != 0 || msg.ContentMetadata["REPLACE"] == "" ||
			msg.ContentMetadata["STICON_OWNERSHIP"] != `["emojipack"]` {
			t.Fatalf("invalid inline emoji message: %+v", msg)
		}
	}

	lc.stickerCatalogs[line.SticonShop] = stickerCatalog{Fetched: time.Now()}
	if _, err := lc.HandleMatrixMessage(t.Context(), testMatrixMessage("ufriend", inline)); err != nil {
		t.Fatalf("unowned emoji failed the send: %v", err)
	}
	fallback := sent[len(sent)-1]
	if fallback.Text != "fallback" || fallback.ContentMetadata["REPLACE"] != "" {
		t.Fatalf("unowned emoji was not sent as plain text: %+v", fallback)
	}
}

func TestEmoteShortcodes(t *testing.T) {
	emotes := []lineSticker{
		{Body: "(Happy!)"},
		{Body: "(happy)"},
		{Body: "(猫)"},
		{Body: "(" + strings.Repeat("very long description ", 10) + ")"},
	}
	shortcodes := emoteShortcodes("Brown Bear", emotes)
	expected := []string{"brown_bear_happy", "brown_bear_happy_2", "brown_bear_3"}
	for i, want := range expected {
		if shortcodes[i] != want {
			t.Fatalf("shortcode %d = %q, want %q", i, shortcodes[i], want)
		}
	}
	long := shortcodes[3]
	if len(long) > maxShortcodeLength || !lineAssetID.MatchString(long) || !strings.HasPrefix(long, "brown_bear_very_long") {
		t.Fatalf("invalid long shortcode %q", long)
	}
	if got := emoteShortcodes("", []lineSticker{{Body: "(cat)"}}); got[0] != "line_cat" {
		t.Fatalf("unnamed pack shortcode = %q", got[0])
	}
}
