package line

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
)

func TestGetOwnedStickerProductsPaging(t *testing.T) {
	page := func(total int, ids ...string) string {
		products := make([]string, len(ids))
		for i, id := range ids {
			products[i] = fmt.Sprintf(`{"id":%q}`, id)
		}
		totalField := ""
		if total >= 0 {
			totalField = fmt.Sprintf(`,"totalSize":%d`, total)
		}
		return fmt.Sprintf(`{"code":0,"data":{"offset":0%s,"productList":[%s]}}`, totalField, strings.Join(products, ","))
	}
	fullPage := make([]string, 1000)
	for i := range fullPage {
		fullPage[i] = fmt.Sprint(i)
	}
	tests := []struct {
		name        string
		pages       []string
		wantIDs     []string
		wantOffsets []int
		wantErr     bool
	}{
		{"ignores echoed offset and short pages", []string{page(3, "1", "2"), page(3, "3")}, []string{"1", "2", "3"}, []int{0, 2}, false},
		{"errors on empty page before total", []string{page(5, "1", "2"), page(5)}, nil, []int{0, 2}, true},
		{"errors on null data", []string{`{"code":0,"data":null}`}, nil, []int{0}, true},
		{"missing total", []string{page(-1, "1")}, []string{"1"}, []int{0}, false},
		{"zero total stops on empty page", []string{page(0)}, nil, []int{0}, false},
		{"missing total pages until short page", []string{page(-1, fullPage...), page(-1, "1000")}, append(slices.Clone(fullPage), "1000"), []int{0, 1000}, false},
		{"zero total pages until empty page", []string{page(0, fullPage...), page(0)}, fullPage, []int{0, 1000}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var offsets []int
			client := NewClient("token")
			client.HTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				var args []json.RawMessage
				body, _ := io.ReadAll(req.Body)
				if err := json.Unmarshal(body, &args); err != nil || len(args) != 4 || string(args[0]) != `"stickershop"` {
					t.Fatalf("unexpected request body %s", body)
				}
				var offset int
				_ = json.Unmarshal(args[1], &offset)
				offsets = append(offsets, offset)
				if len(offsets) > len(tt.pages) {
					t.Fatalf("too many requests")
				}
				return obsResponse(http.StatusOK, tt.pages[len(offsets)-1]), nil
			})}
			products, err := client.GetOwnedStickerProducts(context.Background(), "JP")
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			var ids []string
			for _, product := range products {
				ids = append(ids, product.ID)
			}
			if !slices.Equal(ids, tt.wantIDs) || !slices.Equal(offsets, tt.wantOffsets) {
				t.Fatalf("ids = %v offsets = %v, want %v %v", ids, offsets, tt.wantIDs, tt.wantOffsets)
			}
		})
	}
}
