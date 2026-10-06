package line

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

func TestOwnedShopProductsChromePagination(t *testing.T) {
	c := NewClient("token")
	calls := 0
	c.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var args []json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
			t.Fatal(err)
		}
		if len(args) != 4 || string(args[0]) != `"stickershop"` || string(args[1]) != fmt.Sprint(calls*1000) || string(args[2]) != "1000" || r.URL.Path != "/api/shop/thrift/ShopService/ShopService/getOwnedProductSummaries" {
			t.Fatalf("wrong Chrome request %s %s", r.URL.Path, args)
		}
		var locale map[string]string
		json.Unmarshal(args[3], &locale)
		if locale["country"] != "JP" || locale["language"] != "en" {
			t.Fatal(locale)
		}
		offset := calls * 1000
		calls++
		return obsResponse(200, fmt.Sprintf(`{"code":0,"data":{"offset":%d,"totalSize":1001,"productList":[{"id":"%d","validUntil":"-1","latestVersion":1,"productTypeSummary":{"stickerSummary":{"stickerResourceType":1,"stickerIdRanges":[{"start":"10","size":2}]}}}]}}`, offset, calls)), nil
	})}
	products, err := c.GetOwnedShopProducts(t.Context(), StickerShop, "JP")
	if err != nil {
		t.Fatal(err)
	}
	if len(products) != 2 || calls != 2 || products[0].ValidUntil.String() != "-1" {
		t.Fatalf("%+v %d", products, calls)
	}
}

func TestOwnedShopProductsRejectsStalledPage(t *testing.T) {
	c := NewClient("")
	c.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return obsResponse(200, `{"code":0,"data":{"offset":0,"totalSize":10,"productList":[]}}`), nil
	})}
	if _, err := c.GetOwnedShopProducts(t.Context(), StickerShop, "JP"); err == nil {
		t.Fatal("accepted stalled page")
	}
}

func TestOwnedShopProductsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	c := NewClient("")
	if _, err := c.GetOwnedShopProducts(ctx, StickerShop, "JP"); err == nil {
		t.Fatal("ignored cancellation")
	}
}
