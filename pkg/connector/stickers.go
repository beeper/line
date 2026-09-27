package connector

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"image"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/provisionutil"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/rs/zerolog"

	"github.com/highesttt/matrix-line-messenger/pkg/line"
)

var _ bridgev2.StickerImportingNetworkAPI = (*LineClient)(nil)

var lineAssetID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)

var stickerShops = []string{line.StickerShop}

const (
	stickerCDN               = "https://stickershop.line-scdn.net"
	maxStickerAssetSize      = 10 * 1024 * 1024
	maxStickersPerPack       = 1000
	stickerCatalogTTL        = 5 * time.Minute
	stickerSyncInterval      = time.Hour
	stickerRoomCreateTimeout = 10 * time.Minute
)

type stickerCatalog struct {
	Products []line.ShopProduct
	Fetched  time.Time
}

type lineSticker struct {
	Shop      string `json:"shop"`
	ProductID string `json:"product_id"`
	ID        string `json:"id"`
	Version   string `json:"version"`
	Option    string `json:"option,omitempty"`
	Hash      string `json:"hash,omitempty"`
	Body      string `json:"body"`
}

func (s lineSticker) packURL() string {
	return "line://" + s.Shop + "/" + s.ProductID
}

func (s lineSticker) metadata() map[string]string {
	metadata := map[string]string{
		"STKPKGID": s.ProductID,
		"STKID":    s.ID,
		"STKVER":   s.Version,
		"STKTXT":   s.Body,
	}
	if s.Option != "" {
		metadata["STKOPT"] = s.Option
	}
	if s.Hash != "" {
		metadata["STKHASH"] = s.Hash
	}
	return metadata
}

func (s lineSticker) assetPath() string {
	prefix := "/stickershop/v1/sticker/" + s.ID
	if s.Hash != "" {
		prefix = "/stickershop/v2/sticker/" + s.ID + "/" + s.Hash
	}
	name := "sticker.png"
	if strings.Contains(s.Option, "A") {
		name = "sticker_animation.png"
	} else if strings.Contains(s.Option, "P") {
		name = "sticker_popup.png"
	}
	return prefix + "/android/" + name
}

func stickerOption(resourceType int) (string, error) {
	switch resourceType {
	case 1:
		return "", nil
	case 2:
		return "A", nil
	case 3:
		return "S", nil
	case 4:
		return "AS", nil
	case 5:
		return "P", nil
	case 6:
		return "PS", nil
	default:
		return "", fmt.Errorf("LINE sticker type %d requires unsupported personalization", resourceType)
	}
}

func activeProduct(product line.ShopProduct, now time.Time) bool {
	expiry, err := product.ValidUntil.Int64()
	return err == nil && (expiry == -1 || expiry > now.UnixMilli())
}

func parseLinePackURL(rawURL string) (shop, productID string, err error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", "", err
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", "", fmt.Errorf("invalid LINE pack URL")
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	switch {
	case parsed.Scheme == "line" && slices.Contains(stickerShops, parsed.Host) && len(parts) == 1:
		shop, productID = parsed.Host, parts[0]
	case parsed.Scheme == "https" && parsed.Host == "store.line.me" && len(parts) >= 3 && len(parts) <= 4 && parts[1] == "product":
		shop, err = shopFromStorePath(parts[0])
		if err != nil {
			return "", "", err
		}
		productID = parts[2]
	default:
		return "", "", fmt.Errorf("use a LINE Store product URL or line://stickershop/PACK_ID")
	}
	if !lineAssetID.MatchString(productID) {
		return "", "", fmt.Errorf("invalid LINE product ID")
	}
	return shop, productID, nil
}

func shopFromStorePath(path string) (string, error) {
	switch path {
	case "stickershop":
		return line.StickerShop, nil
	default:
		return "", fmt.Errorf("unsupported LINE Store section %q", path)
	}
}

func (lc *LineClient) ownedStickerProducts(ctx context.Context, shop string) ([]line.ShopProduct, error) {
	lc.stickerMu.Lock()
	defer lc.stickerMu.Unlock()
	if catalog, ok := lc.stickerCatalogs[shop]; ok && time.Since(catalog.Fetched) < stickerCatalogTTL {
		return catalog.Products, nil
	}
	_, profile, err := callLineResult(lc, ctx, func(client *line.Client) (*line.Profile, error) {
		return client.GetProfileContext(ctx)
	})
	if err != nil {
		return nil, err
	}
	if profile.RegionCode == "" {
		return nil, fmt.Errorf("LINE profile has no shop region")
	}
	_, products, err := callLineResult(lc, ctx, func(client *line.Client) ([]line.ShopProduct, error) {
		return client.GetOwnedShopProducts(ctx, shop, profile.RegionCode)
	})
	if err != nil {
		return nil, err
	}
	if lc.stickerCatalogs == nil {
		lc.stickerCatalogs = make(map[string]stickerCatalog)
	}
	lc.stickerCatalogs[shop] = stickerCatalog{Products: products, Fetched: time.Now()}
	return products, nil
}

func (lc *LineClient) resetStickerCatalogs() {
	lc.stickerMu.Lock()
	lc.stickerCatalogs = nil
	lc.stickerMu.Unlock()
}

func packMetadata(shop string, product line.ShopProduct) *event.ImagePackMetadata {
	return &event.ImagePackMetadata{
		DisplayName: product.Name,
		Usage:       []event.ImagePackUsage{event.ImagePackUsageSticker},
		BridgedPack: &event.BridgedStickerPack{
			Network: "line",
			URL:     "line://" + shop + "/" + product.ID,
		},
	}
}

func supportedProduct(shop string, product line.ShopProduct) bool {
	if !lineAssetID.MatchString(product.ID) || !activeProduct(product, time.Now()) {
		return false
	}
	summary := product.ProductTypeSummary.Sticker
	if shop != line.StickerShop || summary == nil {
		return false
	}
	_, err := stickerOption(summary.ResourceType)
	return err == nil
}

func (lc *LineClient) ListImagePacks(ctx context.Context) ([]*event.ImagePackMetadata, error) {
	var packs []*event.ImagePackMetadata
	for _, shop := range stickerShops {
		products, err := lc.ownedStickerProducts(ctx, shop)
		if err != nil {
			return nil, err
		}
		for _, product := range products {
			if supportedProduct(shop, product) {
				packs = append(packs, packMetadata(shop, product))
			}
		}
	}
	return packs, nil
}

func (lc *LineClient) ownedStickerProduct(ctx context.Context, shop, productID string) (*line.ShopProduct, error) {
	products, err := lc.ownedStickerProducts(ctx, shop)
	if err != nil {
		return nil, err
	}
	for _, product := range products {
		if product.ID == productID && supportedProduct(shop, product) {
			return &product, nil
		}
	}
	return nil, fmt.Errorf("LINE pack %s is not owned, has expired, or requires unsupported personalization", productID)
}

func (lc *LineClient) fetchStickerAsset(ctx context.Context, path string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, stickerCDN+path, nil)
	if err != nil {
		return nil, err
	}
	client := lc.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	noRedirectClient := *client
	noRedirectClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	resp, err := noRedirectClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("LINE sticker CDN returned %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("LINE sticker asset too large")
	}
	return data, nil
}

func (lc *LineClient) productStickers(_ context.Context, shop string, product line.ShopProduct) ([]lineSticker, error) {
	version := product.LatestVersion.String()
	if n, err := strconv.ParseUint(version, 10, 32); err != nil || n == 0 {
		return nil, fmt.Errorf("invalid LINE pack version")
	}
	summary := product.ProductTypeSummary.Sticker
	if summary == nil {
		return nil, fmt.Errorf("missing sticker summary")
	}
	option, err := stickerOption(summary.ResourceType)
	if err != nil {
		return nil, err
	}
	if summary.Hash != "" && !lineAssetID.MatchString(summary.Hash) {
		return nil, fmt.Errorf("invalid sticker hash")
	}
	base := lineSticker{
		Shop:      shop,
		ProductID: product.ID,
		Version:   version,
		Option:    option,
		Hash:      summary.Hash,
		Body:      product.Name,
	}
	var stickers []lineSticker
	for _, idRange := range summary.Ranges {
		start, err := strconv.ParseUint(idRange.Start.String(), 10, 63)
		if err != nil || idRange.Size < 0 || len(stickers)+idRange.Size > maxStickersPerPack ||
			start > uint64(1<<63-1)-uint64(idRange.Size) {
			return nil, fmt.Errorf("invalid sticker range")
		}
		for i := range idRange.Size {
			sticker := base
			sticker.ID = strconv.FormatUint(start+uint64(i), 10)
			stickers = append(stickers, sticker)
		}
	}
	if len(stickers) == 0 {
		return nil, fmt.Errorf("LINE pack is empty")
	}
	return stickers, nil
}

func stickerKey(kind, value string) database.Key {
	return database.Key(fmt.Sprintf("line.sticker.%s.%x", kind, sha256.Sum256([]byte(value))))
}

func (lc *LineClient) saveStickerKV(ctx context.Context, key database.Key, value string) error {
	kv := lc.UserLogin.Bridge.DB.KV
	kv.Set(ctx, key, value)
	if kv.Get(ctx, key) != value {
		return fmt.Errorf("failed to persist LINE sticker mapping")
	}
	return nil
}

func (lc *LineClient) importSticker(ctx context.Context, sticker lineSticker) (*event.ImagePackImage, error) {
	kv := lc.UserLogin.Bridge.DB.KV
	raw, err := json.Marshal(sticker)
	if err != nil {
		return nil, err
	}
	assetKey := stickerKey("asset", string(raw))
	var img event.ImagePackImage
	if cached := kv.Get(ctx, assetKey); cached != "" && json.Unmarshal([]byte(cached), &img) == nil && img.URL != "" {
		return &img, nil
	}
	data, err := lc.fetchStickerAsset(ctx, sticker.assetPath(), maxStickerAssetSize)
	if err != nil {
		return nil, err
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || format != "png" {
		return nil, fmt.Errorf("invalid LINE sticker PNG")
	}
	mxc, file, err := lc.UserLogin.Bridge.Bot.UploadMedia(ctx, "", data, "sticker.png", "image/png")
	if err != nil {
		return nil, err
	}
	if file != nil {
		return nil, fmt.Errorf("image packs require unencrypted media")
	}
	img = event.ImagePackImage{
		URL:  mxc,
		Body: sticker.Body,
		Info: &event.FileInfo{
			MimeType:   "image/png",
			IsAnimated: strings.ContainsAny(sticker.Option, "AP"),
			Size:       len(data),
			Width:      config.Width,
			Height:     config.Height,
			BridgedSticker: &event.BridgedSticker{
				Network: "line",
				ID:      sticker.ID,
				PackURL: sticker.packURL(),
			},
		},
	}
	if err = lc.saveStickerKV(ctx, stickerKey("mxc", string(mxc)), string(raw)); err != nil {
		return nil, err
	}
	imgJSON, err := json.Marshal(img)
	if err != nil {
		return nil, err
	}
	if err = lc.saveStickerKV(ctx, assetKey, string(imgJSON)); err != nil {
		return nil, err
	}
	return &img, nil
}

func (lc *LineClient) DownloadImagePack(ctx context.Context, rawURL string) (*bridgev2.ImportedImagePack, error) {
	shop, productID, err := parseLinePackURL(rawURL)
	if err != nil {
		return nil, err
	}
	product, err := lc.ownedStickerProduct(ctx, shop, productID)
	if err != nil {
		return nil, err
	}
	stickers, err := lc.productStickers(ctx, shop, *product)
	if err != nil {
		return nil, err
	}
	pack := &bridgev2.ImportedImagePack{
		Shortcode: "line_" + shop + "_" + productID,
		Content: &event.ImagePackEventContent{
			Metadata: *packMetadata(shop, *product),
			Images:   make(map[string]*event.ImagePackImage, len(stickers)),
		},
	}
	for _, sticker := range stickers {
		img, err := lc.importSticker(ctx, sticker)
		if err != nil {
			return nil, err
		}
		pack.Content.Images["line_"+sticker.ID] = img
	}
	return pack, nil
}

func (lc *LineClient) resolveSticker(ctx context.Context, mxc id.ContentURIString) (*lineSticker, error) {
	if mxc == "" || lc.UserLogin == nil || lc.UserLogin.Bridge == nil || lc.UserLogin.Bridge.DB == nil {
		return nil, nil
	}
	raw := lc.UserLogin.Bridge.DB.KV.Get(ctx, stickerKey("mxc", string(mxc)))
	if raw == "" {
		return nil, nil
	}
	var stored lineSticker
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return nil, err
	}
	product, err := lc.ownedStickerProduct(ctx, stored.Shop, stored.ProductID)
	if err != nil {
		return nil, err
	}
	stickers, err := lc.productStickers(ctx, stored.Shop, *product)
	if err != nil {
		return nil, err
	}
	for _, sticker := range stickers {
		if sticker.ID == stored.ID {
			return &sticker, nil
		}
	}
	return nil, fmt.Errorf("sticker no longer belongs to the LINE pack")
}

type publishedStickerPack struct {
	Digest    string   `json:"digest"`
	StateKeys []string `json:"state_keys"`
}

func (lc *LineClient) syncStickerRoom(ctx context.Context, room id.RoomID, packs []*event.ImagePackMetadata) error {
	manifestKey := stickerKey("published", string(lc.UserLogin.ID)+"/"+string(room))
	manifest := map[string]publishedStickerPack{}
	if raw := lc.UserLogin.Bridge.DB.KV.Get(ctx, manifestKey); raw != "" {
		if err := json.Unmarshal([]byte(raw), &manifest); err != nil {
			return err
		}
	}
	saveManifest := func() error {
		raw, err := json.Marshal(manifest)
		if err != nil {
			return err
		}
		return lc.saveStickerKV(ctx, manifestKey, string(raw))
	}
	active := make(map[string]bool, len(packs))
	var firstErr error
	for _, meta := range packs {
		packURL := meta.BridgedPack.URL
		active[packURL] = true
		pack, err := lc.DownloadImagePack(ctx, packURL)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		content, err := json.Marshal(pack.Content)
		if err != nil {
			return err
		}
		digest := fmt.Sprintf("%x", sha256.Sum256(content))
		previous := manifest[packURL]
		if previous.Digest == digest {
			continue
		}
		parts, err := provisionutil.SplitPack(pack)
		if err != nil {
			return err
		}
		prefix := fmt.Sprintf("line_%x_%s", sha256.Sum256([]byte(lc.UserLogin.ID)), pack.Shortcode)
		next := publishedStickerPack{Digest: digest}
		for i, part := range parts {
			stateKey := fmt.Sprintf("%s_%d", prefix, i)
			for _, stateType := range []event.Type{event.StateImagePack, event.StateUnstableImagePack} {
				if _, err = lc.UserLogin.Bridge.Bot.SendState(ctx, room, stateType, stateKey, &event.Content{VeryRaw: part}, time.Now()); err != nil {
					return err
				}
			}
			next.StateKeys = append(next.StateKeys, stateKey)
			if !slices.Contains(previous.StateKeys, stateKey) {
				previous.StateKeys = append(previous.StateKeys, stateKey)
			}
			previous.Digest = ""
			manifest[packURL] = previous
			if err = saveManifest(); err != nil {
				return err
			}
		}
		for _, stateKey := range previous.StateKeys {
			if slices.Contains(next.StateKeys, stateKey) {
				continue
			}
			if err = lc.clearStickerPack(ctx, room, stateKey); err != nil {
				return err
			}
		}
		manifest[packURL] = next
		if err = saveManifest(); err != nil {
			return err
		}
	}
	for packURL, previous := range manifest {
		if active[packURL] {
			continue
		}
		for _, stateKey := range previous.StateKeys {
			if err := lc.clearStickerPack(ctx, room, stateKey); err != nil {
				return err
			}
		}
		delete(manifest, packURL)
		if err := saveManifest(); err != nil {
			return err
		}
	}
	return firstErr
}

func (lc *LineClient) clearStickerPack(ctx context.Context, room id.RoomID, stateKey string) error {
	empty := &event.ImagePackEventContent{Images: map[string]*event.ImagePackImage{}}
	for _, stateType := range []event.Type{event.StateImagePack, event.StateUnstableImagePack} {
		if _, err := lc.UserLogin.Bridge.Bot.SendState(ctx, room, stateType, stateKey, &event.Content{Parsed: empty}, time.Now()); err != nil {
			return err
		}
	}
	return nil
}

func (lc *LineClient) stickerRooms(ctx context.Context) ([]id.RoomID, error) {
	var rooms []id.RoomID
	if raw := lc.UserLogin.Bridge.DB.KV.Get(ctx, stickerKey("rooms", string(lc.UserLogin.ID))); raw != "" {
		if err := json.Unmarshal([]byte(raw), &rooms); err != nil {
			return nil, err
		}
	}
	return rooms, nil
}

func (lc *LineClient) enableRoomStickers(ctx context.Context, room id.RoomID) error {
	lc.stickerSyncMu.Lock()
	defer lc.stickerSyncMu.Unlock()
	packs, err := lc.ListImagePacks(ctx)
	if err != nil {
		return err
	}
	rooms, err := lc.stickerRooms(ctx)
	if err != nil {
		return err
	}
	if !slices.Contains(rooms, room) {
		rooms = append(rooms, room)
		raw, err := json.Marshal(rooms)
		if err != nil {
			return err
		}
		if err = lc.saveStickerKV(ctx, stickerKey("rooms", string(lc.UserLogin.ID)), string(raw)); err != nil {
			return err
		}
	}
	return lc.syncStickerRoom(ctx, room, packs)
}

func (lc *LineClient) syncStickerPacksOnce(ctx context.Context) error {
	lc.stickerSyncMu.Lock()
	defer lc.stickerSyncMu.Unlock()
	rooms, err := lc.stickerRooms(ctx)
	if err != nil || len(rooms) == 0 {
		return err
	}
	packs, err := lc.ListImagePacks(ctx)
	if err != nil {
		return err
	}
	var firstErr error
	for _, room := range rooms {
		if err = lc.syncStickerRoom(ctx, room, packs); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (lc *LineClient) syncStickerPacks(ctx context.Context) {
	defer lc.wg.Done()
	ticker := time.NewTicker(stickerSyncInterval)
	defer ticker.Stop()
	for {
		if err := lc.syncStickerPacksOnce(ctx); err != nil && ctx.Err() == nil {
			lc.UserLogin.Log.Warn().Err(err).Msg("Failed to synchronize LINE sticker packs")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (lc *LineClient) withStickerPacks(info *bridgev2.ChatInfo) *bridgev2.ChatInfo {
	if info != nil {
		info.ExtraUpdates = bridgev2.MergeExtraUpdaters(info.ExtraUpdates, lc.enableStickersOnRoomCreate)
	}
	return info
}

func (lc *LineClient) enableStickersOnRoomCreate(ctx context.Context, portal *bridgev2.Portal) bool {
	if portal.MXID != "" || portal.RoomType == database.RoomTypeSpace {
		return false
	}
	if _, pending := lc.pendingStickerRooms.LoadOrStore(portal.PortalKey, struct{}{}); pending {
		return false
	}
	runCtx, ok := lc.startRunTask()
	if !ok {
		lc.pendingStickerRooms.Delete(portal.PortalKey)
		return false
	}
	go func() {
		defer lc.wg.Done()
		defer lc.pendingStickerRooms.Delete(portal.PortalKey)
		ctx := zerolog.Ctx(ctx).WithContext(runCtx)
		if err := portal.RoomCreated.WaitTimeoutCtx(ctx, stickerRoomCreateTimeout); err != nil {
			return
		}
		if err := lc.enableRoomStickers(ctx, portal.MXID); err != nil {
			lc.UserLogin.Log.Warn().Err(err).Stringer("room_id", portal.MXID).Msg("Failed to add LINE sticker packs to new room")
		}
	}()
	return false
}
