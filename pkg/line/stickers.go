package line

import (
	"context"
	"encoding/json"
	"fmt"
)

type ShopProduct struct {
	ID                 string      `json:"id"`
	Name               string      `json:"name"`
	LatestVersion      json.Number `json:"latestVersion"`
	ValidUntil         json.Number `json:"validUntil"`
	ProductTypeSummary struct {
		Sticker *StickerSummary `json:"stickerSummary"`
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

func (c *Client) GetOwnedStickerProducts(ctx context.Context, country string) ([]ShopProduct, error) {
	const pageSize, maxProducts = 1000, 100000
	var products []ShopProduct
	for offset := 0; offset < maxProducts; {
		resp, err := c.callRPCWithBaseURLContext(ctx, ShopBaseURL, "ShopService", "getOwnedProductSummaries",
			"stickershop", offset, pageSize, map[string]string{"language": "en", "country": country})
		if err != nil {
			return nil, err
		}
		var result struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    *struct {
				Products []ShopProduct `json:"productList"`
				Offset   int           `json:"offset"`
				Total    int           `json:"totalSize"`
			} `json:"data"`
		}
		if err = json.Unmarshal(resp, &result); err != nil {
			return nil, err
		}
		if result.Code != 0 {
			return nil, fmt.Errorf("getOwnedProductSummaries failed: code %d: %s", result.Code, result.Message)
		}
		data := result.Data
		if data == nil || data.Offset != offset || data.Total < offset || data.Total > maxProducts {
			return nil, fmt.Errorf("invalid LINE shop pagination")
		}
		products = append(products, data.Products...)
		offset += len(data.Products)
		if offset >= data.Total {
			return products, nil
		}
		if len(data.Products) == 0 {
			return nil, fmt.Errorf("empty LINE shop page before end of catalog")
		}
	}
	return nil, fmt.Errorf("LINE shop catalog exceeded pagination limit")
}
