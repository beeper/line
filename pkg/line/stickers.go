package line

import (
	"context"
	"encoding/json"
	"fmt"
)

const (
	StickerShop = "stickershop"
	SticonShop  = "sticonshop"
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

type SticonSummary struct {
	ResourceType int `json:"sticonResourceType"`
}

type StickerSummary struct {
	ResourceType int    `json:"stickerResourceType"`
	Hash         string `json:"stickerHash"`
	Ranges       []struct {
		Start json.Number `json:"start"`
		Size  int         `json:"size"`
	} `json:"stickerIdRanges"`
}

func (c *Client) GetOwnedProducts(ctx context.Context, shop, country string) ([]ShopProduct, error) {
	const pageSize, maxProducts = 1000, 100000
	var products []ShopProduct
	for len(products) < maxProducts {
		resp, err := c.callRPCWithBaseURLContext(ctx, ShopBaseURL, "ShopService", "getOwnedProductSummaries",
			shop, len(products), pageSize, map[string]string{"language": "en", "country": country})
		if err != nil {
			return nil, err
		}
		var result struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    *struct {
				Products []ShopProduct `json:"productList"`
				Total    int           `json:"totalSize"`
			} `json:"data"`
		}
		if err = json.Unmarshal(resp, &result); err != nil {
			return nil, err
		}
		if result.Code != 0 {
			return nil, fmt.Errorf("getOwnedProductSummaries failed: code %d: %s", result.Code, result.Message)
		} else if result.Data == nil {
			return nil, fmt.Errorf("getOwnedProductSummaries returned no data")
		}
		page := result.Data.Products
		products = append(products, page...)
		if result.Data.Total > 0 {
			if len(products) >= result.Data.Total {
				return products, nil
			} else if len(page) == 0 {
				return nil, fmt.Errorf("getOwnedProductSummaries returned empty page at %d of %d products", len(products), result.Data.Total)
			}
		} else if len(page) < pageSize {
			return products, nil
		}
	}
	return nil, fmt.Errorf("LINE shop catalog exceeded pagination limit")
}
