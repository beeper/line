package line

import "testing"

func TestSanitizeProviderMessage(t *testing.T) {
	for _, tc := range []struct {
		name, input, want, status string
	}{
		{"verified verification alert", "Verification is temporarily unavailable.\nPlease try again later.", "Verification is temporarily unavailable.\nPlease try again later.", "recognized"},
		{"token field redacted", "Login failed; access_token=private-value. Please try later.", "Login failed; access_token=[redacted]. Please try later.", "partially_redacted"},
		{"email redacted", "Account for person@example.org is locked.", "Account for [redacted email] is locked.", "partially_redacted"},
		{"bounded safe provider text", "LINE could not complete this sign-in. Try again later.", "LINE could not complete this sign-in. Try again later.", "safe_unrecognized"},
		{"structured response suppressed", `{"token":"private"}`, "", "suppressed"},
		{"control sequence suppressed", "safe text\x1b[31mprivate", "", "suppressed"},
		{"oversized message suppressed", string(make([]byte, 600)), "", "suppressed"},
		{"short unknown suppressed", "no", "", "suppressed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, status := SanitizeProviderMessage(tc.input)
			if got != tc.want || status != tc.status {
				t.Fatalf("SanitizeProviderMessage() = %q, %q; want %q, %q", got, status, tc.want, tc.status)
			}
		})
	}
}
