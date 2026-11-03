package connector

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/beeper/line/pkg/line"
)

var _ bridgev2.StickerImportingNetworkAPI = (*LineClient)(nil)

var lineAssetID = regexp.MustCompile(`^[0-9A-Za-z]{1,32}$`)

type lineSticker struct {
	ProductID string
	ID        string
	Version   string
	Option    string
	Hash      string
	Body      string
	Sticon    bool
}

func linePackURL(productID string, sticon bool) string {
	if sticon {
		return "https://store.line.me/emojishop/product/" + productID
	}
	return "https://store.line.me/stickershop/product/" + productID
}

func productPackURL(product line.ShopProduct) string {
	return linePackURL(product.ID, product.ProductTypeSummary.Sticon != nil)
}

func parseLinePackURL(rawURL string) (string, bool, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", false, err
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	var productID string
	var sticon bool
	if u.User == nil && u.RawQuery == "" && u.Fragment == "" {
		switch {
		case u.Scheme == "line" && (u.Host == line.StickerShop || u.Host == line.SticonShop) && len(parts) == 1:
			productID, sticon = parts[0], u.Host == line.SticonShop
		case u.Scheme == "https" && u.Host == "store.line.me" && (len(parts) == 3 || len(parts) == 4) &&
			(parts[0] == "stickershop" || parts[0] == "emojishop") && parts[1] == "product":
			productID, sticon = parts[2], parts[0] == "emojishop"
		}
	}
	valid := lineAssetID.MatchString(productID)
	if !sticon {
		_, err = strconv.ParseUint(productID, 10, 64)
		valid = err == nil
	}
	if !valid {
		return "", false, fmt.Errorf("use a LINE Store sticker or emoji product URL or line://stickershop/PACK_ID")
	}
	return productID, sticon, nil
}

func (lc *LineClient) ownedStickerProducts(ctx context.Context, refresh bool) (map[string]line.ShopProduct, error) {
	for {
		if err := lc.checkLineCall(ctx); err != nil {
			return nil, err
		}
		lc.stickerPacksMu.Lock()
		if !refresh && lc.stickerPacks != nil {
			packs := lc.stickerPacks
			lc.stickerPacksMu.Unlock()
			return packs, nil
		}
		if pending := lc.stickerPacksFetching; pending != nil {
			lc.stickerPacksMu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-pending:
			}
			refresh = false
			continue
		}
		lc.stickerPacksFetching = make(chan struct{})
		lc.stickerPacksMu.Unlock()
		break
	}
	defer func() {
		lc.stickerPacksMu.Lock()
		close(lc.stickerPacksFetching)
		lc.stickerPacksFetching = nil
		lc.stickerPacksMu.Unlock()
	}()
	_, profile, err := callLineResult(lc, ctx, func(client *line.Client) (*line.Profile, error) {
		return client.GetProfileContext(ctx)
	})
	if err != nil {
		return nil, err
	}
	if profile == nil || profile.RegionCode == "" {
		return nil, fmt.Errorf("LINE profile has no shop region")
	}
	packs := make(map[string]line.ShopProduct)
	for _, shop := range []string{line.StickerShop, line.SticonShop} {
		_, products, err := callLineResult(lc, ctx, func(client *line.Client) ([]line.ShopProduct, error) {
			return client.GetOwnedProducts(ctx, shop, profile.RegionCode)
		})
		if err != nil && shop == line.SticonShop {
			lc.UserLogin.Log.Warn().Err(err).Msg("Failed to fetch owned LINE emoji packs")
		} else if err != nil {
			return nil, err
		}
		for _, product := range products {
			packs[productPackURL(product)] = product
		}
	}
	lc.stickerPacksMu.Lock()
	lc.stickerPacks = packs
	lc.stickerPacksMu.Unlock()
	return packs, nil
}

func productVersion(product line.ShopProduct) (string, error) {
	expiry, err := product.ValidUntil.Int64()
	if err != nil || (expiry != -1 && expiry <= time.Now().UnixMilli()) {
		return "", fmt.Errorf("LINE pack has expired")
	}
	version := product.LatestVersion.String()
	if n, err := strconv.ParseUint(version, 10, 32); err != nil || n == 0 {
		return "", fmt.Errorf("invalid LINE pack version")
	}
	return version, nil
}

func productStickers(product line.ShopProduct) ([]lineSticker, error) {
	version, err := productVersion(product)
	if err != nil {
		return nil, err
	}
	summary := product.ProductTypeSummary.Sticker
	if summary == nil || summary.ResourceType < 1 || summary.ResourceType > 6 {
		return nil, fmt.Errorf("unsupported LINE sticker resource type")
	}
	base := lineSticker{
		ProductID: product.ID, Version: version, Hash: summary.Hash, Body: product.Name,
		Option: []string{"", "A", "S", "AS", "P", "PS"}[summary.ResourceType-1],
	}
	var stickers []lineSticker
	for _, idRange := range summary.Ranges {
		start, err := strconv.ParseUint(idRange.Start.String(), 10, 63)
		if err != nil || start == 0 || idRange.Size < 0 || idRange.Size > 1000-len(stickers) {
			return nil, fmt.Errorf("invalid LINE sticker range")
		}
		for i := range idRange.Size {
			sticker := base
			sticker.ID = strconv.FormatUint(start+uint64(i), 10)
			stickers = append(stickers, sticker)
		}
	}
	if len(stickers) == 0 {
		return nil, fmt.Errorf("LINE sticker pack is empty")
	}
	return stickers, nil
}

func productSticons(product line.ShopProduct, metaJSON []byte) ([]lineSticker, error) {
	version, err := productVersion(product)
	if err != nil {
		return nil, err
	}
	summary := product.ProductTypeSummary.Sticon
	if summary == nil || (summary.ResourceType != 1 && summary.ResourceType != 2) {
		return nil, fmt.Errorf("unsupported LINE emoji resource type")
	}
	var meta struct {
		Orders   []string          `json:"orders"`
		AltTexts map[string]string `json:"altTexts"`
	}
	if err = json.Unmarshal(metaJSON, &meta); err != nil {
		return nil, err
	}
	if len(meta.Orders) == 0 || len(meta.Orders) > 1000 {
		return nil, fmt.Errorf("invalid LINE emoji pack size")
	}
	sticons := make([]lineSticker, len(meta.Orders))
	for i, sticonID := range meta.Orders {
		if !lineAssetID.MatchString(sticonID) {
			return nil, fmt.Errorf("invalid LINE emoji ID")
		}
		body := "(emoji)"
		if alt := meta.AltTexts[sticonID]; alt != "" {
			body = "(" + alt + ")"
		}
		sticons[i] = lineSticker{ProductID: product.ID, ID: sticonID, Version: version, Body: body, Sticon: true}
		if summary.ResourceType == 2 {
			sticons[i].Option = "A"
		}
	}
	return sticons, nil
}

func (lc *LineClient) packStickers(ctx context.Context, product line.ShopProduct) ([]lineSticker, error) {
	if product.ProductTypeSummary.Sticon == nil {
		return productStickers(product)
	}
	if !lineAssetID.MatchString(product.ID) {
		return nil, fmt.Errorf("invalid LINE emoji product ID")
	}
	version, err := productVersion(product)
	if err != nil {
		return nil, err
	}
	cacheKey := product.ID + "/" + version
	lc.sticonPacksMu.Lock()
	sticons, ok := lc.sticonPacks[cacheKey]
	lc.sticonPacksMu.Unlock()
	if ok {
		return sticons, nil
	}
	meta, err := lc.fetchStickerCDN(ctx, "https://stickershop.line-scdn.net/sticonshop/v1/product/"+product.ID+"/android/meta.json")
	if err != nil {
		return nil, err
	}
	sticons, err = productSticons(product, meta)
	if err != nil {
		return nil, err
	}
	lc.sticonPacksMu.Lock()
	if lc.sticonPacks == nil || len(lc.sticonPacks) >= 256 {
		lc.sticonPacks = make(map[string][]lineSticker)
	}
	lc.sticonPacks[cacheKey] = sticons
	lc.sticonPacksMu.Unlock()
	return sticons, nil
}

func packMetadata(product line.ShopProduct) *event.ImagePackMetadata {
	usage := event.ImagePackUsageSticker
	if product.ProductTypeSummary.Sticon != nil {
		usage = event.ImagePackUsageEmoji
	}
	return &event.ImagePackMetadata{
		DisplayName: product.Name,
		Usage:       []event.ImagePackUsage{usage},
		BridgedPack: &event.BridgedStickerPack{Network: "line", URL: productPackURL(product)},
	}
}

func (lc *LineClient) ListImagePacks(ctx context.Context) ([]*event.ImagePackMetadata, error) {
	products, err := lc.ownedStickerProducts(ctx, true)
	if err != nil {
		return nil, err
	}
	packs := make([]*event.ImagePackMetadata, 0, len(products))
	for _, product := range products {
		if _, err = lc.packStickers(ctx, product); err == nil {
			packs = append(packs, packMetadata(product))
		}
	}
	return packs, nil
}

func (lc *LineClient) ownedStickerPack(ctx context.Context, packURL string, refreshOnMiss bool) (*line.ShopProduct, []lineSticker, error) {
	for _, refresh := range []bool{false, true} {
		products, err := lc.ownedStickerProducts(ctx, refresh)
		if err != nil {
			return nil, nil, err
		}
		if product, ok := products[packURL]; ok {
			stickers, err := lc.packStickers(ctx, product)
			return &product, stickers, err
		}
		if !refreshOnMiss {
			break
		}
	}
	return nil, nil, fmt.Errorf("LINE sticker pack is not owned")
}

func stickerMXCKey(mxc id.ContentURIString) database.Key {
	return database.Key(fmt.Sprintf("line.sticker.mxc.%x", sha256.Sum256([]byte(mxc))))
}

func (s lineSticker) asset() (string, string) {
	if s.Sticon {
		name := s.ID + ".png"
		if s.Option == "A" {
			name = s.ID + "_animation.png"
		}
		return "https://stickershop.line-scdn.net/sticonshop/v1/sticon/" + s.ProductID + "/android/" + name, name
	}
	prefix := "https://stickershop.line-scdn.net/stickershop/v1/sticker/" + s.ID
	if s.Hash != "" {
		prefix = "https://stickershop.line-scdn.net/stickershop/v2/sticker/" + s.ID + "/" + s.Hash
	}
	name := "sticker.png"
	if strings.Contains(s.Option, "A") {
		name = "sticker_animation.png"
	} else if strings.Contains(s.Option, "P") {
		name = "sticker_popup.png"
	}
	return prefix + "/android/" + name, name
}

func (lc *LineClient) fetchStickerCDN(ctx context.Context, assetURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, assetURL, nil)
	if err != nil {
		return nil, err
	}
	client := http.Client{Timeout: 30 * time.Second}
	if lc.HTTPClient != nil {
		client = *lc.HTTPClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("LINE sticker CDN returned %d", resp.StatusCode)
	}
	const maxSize = 10 * 1024 * 1024
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxSize {
		return nil, fmt.Errorf("LINE sticker asset too large")
	}
	return data, nil
}

func (lc *LineClient) importSticker(ctx context.Context, sticker lineSticker) (*event.ImagePackImage, error) {
	assetURL, name := sticker.asset()
	data, err := lc.fetchStickerCDN(ctx, assetURL)
	if err != nil {
		return nil, err
	}
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("invalid LINE sticker PNG: %w", err)
	}
	mxc, _, err := lc.UserLogin.Bridge.Bot.UploadMedia(ctx, "", data, name, "image/png")
	if err != nil {
		return nil, err
	}
	bridged := &event.BridgedSticker{Network: "line", ID: sticker.ID, PackURL: linePackURL(sticker.ProductID, sticker.Sticon)}
	raw, err := json.Marshal(bridged)
	if err != nil {
		return nil, err
	}
	kv := lc.UserLogin.Bridge.DB.KV
	key := stickerMXCKey(mxc)
	kv.Set(ctx, key, string(raw))
	return &event.ImagePackImage{
		URL: mxc, Body: sticker.Body,
		Info: &event.FileInfo{
			MimeType: "image/png", Size: len(data), Width: config.Width, Height: config.Height,
			IsAnimated: strings.ContainsAny(sticker.Option, "AP"), BridgedSticker: bridged,
		},
	}, nil
}

func (lc *LineClient) DownloadImagePack(ctx context.Context, rawURL string) (*bridgev2.ImportedImagePack, error) {
	productID, sticon, err := parseLinePackURL(rawURL)
	if err != nil {
		return nil, err
	}
	product, stickers, err := lc.ownedStickerPack(ctx, linePackURL(productID, sticon), true)
	if err != nil {
		return nil, err
	}
	pack := &bridgev2.ImportedImagePack{
		Shortcode: "line_" + productID,
		Content: &event.ImagePackEventContent{
			Metadata: *packMetadata(*product),
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

func (lc *LineClient) resolveSticker(ctx context.Context, mxc id.ContentURIString, bridged *event.BridgedSticker, sticon bool) (*lineSticker, error) {
	if bridged == nil && mxc != "" {
		raw := lc.UserLogin.Bridge.DB.KV.Get(ctx, stickerMXCKey(mxc))
		if raw != "" {
			if err := json.Unmarshal([]byte(raw), &bridged); err != nil {
				return nil, err
			}
		}
	}
	if bridged == nil || bridged.Network != "line" || bridged.PackURL == "" {
		return nil, nil
	}
	productID, isSticon, err := parseLinePackURL(bridged.PackURL)
	if err != nil || isSticon != sticon {
		return nil, err
	}
	_, stickers, err := lc.ownedStickerPack(ctx, linePackURL(productID, sticon), false)
	if err != nil {
		return nil, err
	}
	for _, sticker := range stickers {
		if sticker.ID == bridged.ID {
			return &sticker, nil
		}
	}
	return nil, fmt.Errorf("sticker does not belong to the LINE pack")
}
