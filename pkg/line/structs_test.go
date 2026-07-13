package line

import (
	"encoding/json"
	"testing"
)

func TestFlexibleMidMapUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name string
		data string
		want []string
	}{
		{
			name: "string valued object",
			data: `{"U-one":"first","U-two":"second"}`,
			want: []string{"U-one", "U-two"},
		},
		{
			name: "boolean valued object",
			data: `{"U-one":true,"U-two":false}`,
			want: []string{"U-one", "U-two"},
		},
		{
			name: "array",
			data: `["U-one","U-two"]`,
			want: []string{"U-one", "U-two"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var got FlexibleMidMap
			if err := json.Unmarshal([]byte(test.data), &got); err != nil {
				t.Fatalf("Unmarshal returned error: %v", err)
			}
			if len(got) != len(test.want) {
				t.Fatalf("decoded MIDs = %v, want %v", got, test.want)
			}
			for _, mid := range test.want {
				if !got[mid] {
					t.Errorf("decoded MIDs = %v, missing %s", got, mid)
				}
			}
		})
	}
}
