package line

import (
	"strings"
	"testing"
)

func TestNormalizeUserMID(t *testing.T) {
	opaque := "U" + "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789_-aDEFg"
	legacy := "u0123456789abcdef0123456789abcdef"
	for _, tc := range []struct {
		name, input, want string
		valid             bool
	}{
		{"opaque", opaque, opaque, true},
		{"case-sensitive opaque", "U" + strings.ToLower(opaque[1:]), "U" + strings.ToLower(opaque[1:]), true},
		{"legacy", legacy, legacy, true},
		{"legacy uppercase", strings.ToUpper(legacy), legacy, true},
		{"lowercase opaque prefix", strings.ToLower(opaque), "", false},
		{"wrong kind", "C" + opaque[1:], "", false},
		{"short", opaque[:43], "", false},
		{"padding", opaque[:43] + "=", "", false},
		{"nonhex legacy", legacy[:32] + "g", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, valid := NormalizeUserMID(tc.input)
			if valid != tc.valid || valid && got != tc.want || !valid && got != tc.input {
				t.Fatalf("NormalizeUserMID(%q) = %q, %v", tc.input, got, valid)
			}
		})
	}
}
