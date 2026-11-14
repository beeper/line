package line

import "strings"

// NormalizeUserMID preserves case-sensitive Chrome gateway IDs and canonicalizes
// legacy hexadecimal MIDs, which are case-insensitive.
func NormalizeUserMID(mid string) (string, bool) {
	if len(mid) == 44 && mid[0] == 'U' {
		for _, ch := range mid[1:] {
			if !(ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-') {
				return mid, false
			}
		}
		return mid, true
	}
	if len(mid) != 33 || mid[0] != 'u' && mid[0] != 'U' {
		return mid, false
	}
	for _, ch := range mid[1:] {
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f' || ch >= 'A' && ch <= 'F') {
			return mid, false
		}
	}
	return strings.ToLower(mid), true
}
