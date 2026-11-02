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

type lineSticker struct {
	ProductID string
	ID        string
	Version   string
	Option    string
	Hash      string
	Body      string
}

func linePackURL(productID string) string {
	return "https://store.line.me/stickershop/product/" + productID
}

func parseLinePackURL(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	var productID string
	if u.User == nil && u.RawQuery == "" && u.Fragment == "" {
		switch {
		case u.Scheme == "line" && u.Host == "stickershop" && len(parts) == 1:
			productID = parts[0]
		case u.Scheme == "https" && u.Host == "store.line.me" && (len(parts) == 3 || len(parts) == 4) && parts[0] == "stickershop" && parts[1] == "product":
			productID = parts[2]
		}
	}
	if _, err = strconv.ParseUint(productID, 10, 64); err != nil {
		return "", fmt.Errorf("use a LINE Store sticker product URL or line://stickershop/PACK_ID")
	}
	return productID, nil
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
	_, products, err := callLineResult(lc, ctx, func(client *line.Client) ([]line.ShopProduct, error) {
		return client.GetOwnedStickerProducts(ctx, profile.RegionCode)
	})
	if err != nil {
		return nil, err
	}
	packs := make(map[string]line.ShopProduct, len(products))
	for _, product := range products {
		packs[product.ID] = product
	}
	lc.stickerPacksMu.Lock()
	lc.stickerPacks = packs
	lc.stickerPacksMu.Unlock()
	return packs, nil
}

func productStickers(product line.ShopProduct) ([]lineSticker, error) {
	expiry, err := product.ValidUntil.Int64()
	if err != nil || (expiry != -1 && expiry <= time.Now().UnixMilli()) {
		return nil, fmt.Errorf("LINE sticker pack has expired")
	}
	version := product.LatestVersion.String()
	if n, err := strconv.ParseUint(version, 10, 32); err != nil || n == 0 {
		return nil, fmt.Errorf("invalid LINE pack version")
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

func packMetadata(product line.ShopProduct) *event.ImagePackMetadata {
	return &event.ImagePackMetadata{
		DisplayName: product.Name,
		Usage:       []event.ImagePackUsage{event.ImagePackUsageSticker},
		BridgedPack: &event.BridgedStickerPack{Network: "line", URL: linePackURL(product.ID)},
	}
}

func (lc *LineClient) ListImagePacks(ctx context.Context) ([]*event.ImagePackMetadata, error) {
	products, err := lc.ownedStickerProducts(ctx, true)
	if err != nil {
		return nil, err
	}
	packs := make([]*event.ImagePackMetadata, 0, len(products))
	for _, product := range products {
		if _, err = productStickers(product); err == nil {
			packs = append(packs, packMetadata(product))
		}
	}
	return packs, nil
}

func (lc *LineClient) ownedStickerPack(ctx context.Context, productID string) (*line.ShopProduct, []lineSticker, error) {
	for _, refresh := range []bool{false, true} {
		products, err := lc.ownedStickerProducts(ctx, refresh)
		if err != nil {
			return nil, nil, err
		}
		if product, ok := products[productID]; ok {
			stickers, err := productStickers(product)
			return &product, stickers, err
		}
	}
	return nil, nil, fmt.Errorf("LINE sticker pack is not owned")
}

func stickerMXCKey(mxc id.ContentURIString) database.Key {
	return database.Key(fmt.Sprintf("line.sticker.mxc.%x", sha256.Sum256([]byte(mxc))))
}

func (lc *LineClient) importSticker(ctx context.Context, sticker lineSticker) (*event.ImagePackImage, error) {
	prefix := "https://stickershop.line-scdn.net/stickershop/v1/sticker/" + sticker.ID
	if sticker.Hash != "" {
		prefix = "https://stickershop.line-scdn.net/stickershop/v2/sticker/" + sticker.ID + "/" + sticker.Hash
	}
	name := "sticker.png"
	if strings.Contains(sticker.Option, "A") {
		name = "sticker_animation.png"
	} else if strings.Contains(sticker.Option, "P") {
		name = "sticker_popup.png"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, prefix+"/android/"+name, nil)
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
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("invalid LINE sticker PNG: %w", err)
	}
	mxc, _, err := lc.UserLogin.Bridge.Bot.UploadMedia(ctx, "", data, name, "image/png")
	if err != nil {
		return nil, err
	}
	bridged := &event.BridgedSticker{Network: "line", ID: sticker.ID, PackURL: linePackURL(sticker.ProductID)}
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
	productID, err := parseLinePackURL(rawURL)
	if err != nil {
		return nil, err
	}
	product, stickers, err := lc.ownedStickerPack(ctx, productID)
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

func (lc *LineClient) resolveSticker(ctx context.Context, content *event.MessageEventContent) (*lineSticker, error) {
	var bridged *event.BridgedSticker
	if content.Info != nil {
		bridged = content.Info.BridgedSticker
	}
	if bridged == nil && content.URL != "" {
		raw := lc.UserLogin.Bridge.DB.KV.Get(ctx, stickerMXCKey(content.URL))
		if raw != "" {
			if err := json.Unmarshal([]byte(raw), &bridged); err != nil {
				return nil, err
			}
		}
	}
	if bridged == nil || bridged.Network != "line" || bridged.PackURL == "" {
		return nil, nil
	}
	productID, err := parseLinePackURL(bridged.PackURL)
	if err != nil {
		return nil, err
	}
	_, stickers, err := lc.ownedStickerPack(ctx, productID)
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
