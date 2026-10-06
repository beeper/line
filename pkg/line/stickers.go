package line

import (
	"context"
	"encoding/json"
	"fmt"
)

const (
	StickerShop = "stickershop"
	SticonShop  = "sticonshop"

	shopPageSize    = 1000
	maxShopProducts = 100000
)

type ShopProduct struct {
	ID                 string      `json:"id"`
	Name               string      `json:"name"`
	LatestVersion      json.Number `json:"latestVersion"`
	ValidUntil         json.Number `json:"validUntil"`
	ProductTypeSummary struct {
		Sticker *StickerSummary `json:"stickerSummary"`
		Sticon  *SticonSummary  `json:"sticonSummary"`
	} `json:"productTypeSummary"`
}

type StickerSummary struct {
	ResourceType int    `json:"stickerResourceType"`
	Hash         string `json:"stickerHash"`
	Ranges       []struct {
		Start json.Number `json:"start"`
		Size  int         `json:"size"`
	} `json:"stickerIdRanges"`
}

type SticonSummary struct {
	ResourceType int `json:"sticonResourceType"`
}

func (c *Client) GetOwnedShopProducts(ctx context.Context, shop, country string) ([]ShopProduct, error) {
	if shop != StickerShop && shop != SticonShop {
		return nil, fmt.Errorf("unknown LINE shop")
	}
	var products []ShopProduct
	for offset := 0; offset < maxShopProducts; {
		resp, err := c.callRPCWithBaseURLContext(ctx, ShopBaseURL, "ShopService", "getOwnedProductSummaries",
			shop, offset, shopPageSize, map[string]string{"language": "en", "country": country})
		if err != nil {
			return nil, err
		}
		var result struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    struct {
				Products []ShopProduct `json:"productList"`
				Offset   int           `json:"offset"`
				Total    int           `json:"totalSize"`
			} `json:"data"`
		}
		if err = json.Unmarshal(resp, &result); err != nil {
			return nil, err
		}
		if result.Code != 0 {
			return nil, fmt.Errorf("getOwnedProductSummaries: %s", result.Message)
		}
		products = append(products, result.Data.Products...)
		if result.Data.Offset+len(result.Data.Products) >= result.Data.Total {
			return products, nil
		}
		if len(result.Data.Products) == 0 || result.Data.Offset != offset {
			return nil, fmt.Errorf("invalid LINE shop pagination")
		}
		offset += shopPageSize
	}
	return nil, fmt.Errorf("LINE shop catalog exceeded pagination limit")
}
