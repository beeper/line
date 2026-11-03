package connector

import (
	"encoding/json"
	"testing"

	"github.com/beeper/line/pkg/line"
)

func TestParseLinePackURLEmoji(t *testing.T) {
	const productID = "5ac1bfd5040ab15980c9b435"
	for _, rawURL := range []string{
		"https://store.line.me/emojishop/product/" + productID,
		"https://store.line.me/emojishop/product/" + productID + "/en",
		"line://sticonshop/" + productID,
	} {
		got, sticon, err := parseLinePackURL(rawURL)
		if err != nil || got != productID || !sticon {
			t.Fatalf("parseLinePackURL(%q) = %q, %v, %v", rawURL, got, sticon, err)
		}
	}
	if got, sticon, err := parseLinePackURL("https://store.line.me/stickershop/product/123"); err != nil || got != "123" || sticon {
		t.Fatalf("sticker URL = %q, %v, %v", got, sticon, err)
	}
	for _, rawURL := range []string{
		"https://store.line.me/stickershop/product/" + productID,
		"https://store.line.me/emojishop/product/../x",
		"line://sticonshop/a%2Fb",
	} {
		if _, _, err := parseLinePackURL(rawURL); err == nil {
			t.Fatalf("parseLinePackURL(%q) succeeded", rawURL)
		}
	}
}

func testSticonProduct(resourceType int) line.ShopProduct {
	product := line.ShopProduct{ID: "5ac1bfd5040ab15980c9b435", Name: "Moon", LatestVersion: "3", ValidUntil: "-1"}
	product.ProductTypeSummary.Sticon = &line.SticonSummary{ResourceType: resourceType}
	return product
}

func TestProductSticons(t *testing.T) {
	meta := []byte(`{"orders":["001","002"],"altTexts":{"001":"moon heart eyes"}}`)
	sticons, err := productSticons(testSticonProduct(2), meta)
	if err != nil {
		t.Fatal(err)
	}
	if len(sticons) != 2 || sticons[0].ID != "001" || sticons[0].Body != "(moon heart eyes)" ||
		sticons[1].Body != "(emoji)" || sticons[0].Version != "3" || sticons[0].Option != "A" || !sticons[0].Sticon {
		t.Fatalf("unexpected sticons: %+v", sticons)
	}
	assetURL, name := sticons[0].asset()
	if assetURL != "https://stickershop.line-scdn.net/sticonshop/v1/sticon/5ac1bfd5040ab15980c9b435/android/001_animation.png" || name != "001_animation.png" {
		t.Fatalf("asset() = %q, %q", assetURL, name)
	}
	if packMetadata(testSticonProduct(1)).BridgedPack.URL != "https://store.line.me/emojishop/product/5ac1bfd5040ab15980c9b435" {
		t.Fatal("unexpected emoji pack URL")
	}

	for name, tc := range map[string]struct {
		product line.ShopProduct
		meta    string
	}{
		"bad id":       {testSticonProduct(1), `{"orders":["../x"]}`},
		"empty":        {testSticonProduct(1), `{"orders":[]}`},
		"bad type":     {testSticonProduct(3), `{"orders":["001"]}`},
		"invalid json": {testSticonProduct(1), `{`},
		"expired":      {func() line.ShopProduct { p := testSticonProduct(1); p.ValidUntil = json.Number("1"); return p }(), `{"orders":["001"]}`},
		"no summary":   {line.ShopProduct{ID: "a", LatestVersion: "1", ValidUntil: "-1"}, `{"orders":["001"]}`},
	} {
		if _, err := productSticons(tc.product, []byte(tc.meta)); err == nil {
			t.Fatalf("%s: productSticons succeeded", name)
		}
	}
}
