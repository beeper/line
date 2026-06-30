package line

import (
	"encoding/base64"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestBuildLoginSecretMatchesNodeAlgorithm(t *testing.T) {
	publicKeyB64 := "kOf6ZAf5lQcROq7mwBwIj/NfncQ/d8LDuk6DwpiVQX4="
	publicKey, err := base64.StdEncoding.DecodeString(publicKeyB64)
	if err != nil {
		t.Fatalf("failed to decode public key: %v", err)
	}

	secret, err := buildLoginSecret("123456", publicKey)
	if err != nil {
		t.Fatalf("buildLoginSecret failed: %v", err)
	}

	const want = "tC9UuIuLVhZvv+tKCVg5II41yBYhtOjmhrta1uTecds="
	if secret != want {
		t.Fatalf("unexpected secret: got %q want %q", secret, want)
	}
}

func TestGenerateLoginPINFormat(t *testing.T) {
	pin, err := generateLoginPIN()
	if err != nil {
		t.Fatalf("generateLoginPIN failed: %v", err)
	}

	re := regexp.MustCompile(`^\d{6}$`)
	if !re.MatchString(pin) {
		t.Fatalf("invalid pin format: %q", pin)
	}
}

func TestNormalizeServerPublicKeyB64(t *testing.T) {
	already32 := make([]byte, 32)
	for i := range already32 {
		already32[i] = byte(i)
	}
	already32B64 := base64.StdEncoding.EncodeToString(already32)
	normalized, err := normalizeServerPublicKeyB64(already32B64)
	if err != nil {
		t.Fatalf("normalizeServerPublicKeyB64 failed: %v", err)
	}
	if normalized != already32B64 {
		t.Fatalf("unexpected normalized key for 32-byte input: got %q want %q", normalized, already32B64)
	}

	longer := make([]byte, 40)
	for i := range longer {
		longer[i] = byte(i)
	}
	longerB64 := base64.StdEncoding.EncodeToString(longer)
	normalized, err = normalizeServerPublicKeyB64(longerB64)
	if err != nil {
		t.Fatalf("normalizeServerPublicKeyB64 failed: %v", err)
	}
	wantTrimmed := base64.StdEncoding.EncodeToString(longer[8:])
	if normalized != wantTrimmed {
		t.Fatalf("unexpected normalized key for long input: got %q want %q", normalized, wantTrimmed)
	}
}

func TestNormalizeServerPublicKeyB64RejectsInvalidInput(t *testing.T) {
	if _, err := normalizeServerPublicKeyB64("not-base64"); err == nil {
		t.Fatalf("expected base64 decoding error")
	}

	tooShort := base64.StdEncoding.EncodeToString([]byte{1, 2, 3})
	if _, err := normalizeServerPublicKeyB64(tooShort); err == nil {
		t.Fatalf("expected short-key error")
	}
}

func TestGenerateE2EESecretFromRunner(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping runner integration test in short mode")
	}

	r, err := GetRunner()
	if err != nil {
		t.Fatalf("GetRunner failed: %v", err)
	}
	secretRes, err := r.GenerateE2EESecret()
	if err != nil {
		t.Fatalf("GenerateE2EESecret failed: %v", err)
	}

	if secretRes.Secret == "" {
		t.Fatalf("secret should not be empty")
	}
	if ok := regexp.MustCompile(`^\d{6}$`).MatchString(secretRes.Pin); !ok {
		t.Fatalf("invalid pin format: %q", secretRes.Pin)
	}
	if ok := regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(secretRes.PublicKeyHex); !ok {
		t.Fatalf("invalid public key hex: %q", secretRes.PublicKeyHex)
	}
}

func TestGetSignatureFromRunner(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping runner integration test in short mode")
	}

	r, err := GetRunner()
	if err != nil {
		t.Fatalf("GetRunner failed: %v", err)
	}
	signature, err := r.GetSignature("/api/test", "{}", "token")
	if err != nil {
		t.Fatalf("GetSignature failed: %v", err)
	}

	const want = "HivdBfCU/jXr3qqNDr68RScS2jp4FR2SYGZcL9wbw0k="
	if signature != want {
		t.Fatalf("unexpected signature: got %q want %q", signature, want)
	}
}

func TestDebugExportDerivedSigningKeyDisabledByDefault(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping runner integration test in short mode")
	}
	if os.Getenv("LTSM_ENABLE_DEBUG_EXPORT") == "1" {
		t.Skip("debug export is explicitly enabled in environment")
	}

	r, err := GetRunner()
	if err != nil {
		t.Fatalf("GetRunner failed: %v", err)
	}
	_, err = r.DebugExportDerivedSigningKey("token")
	if err == nil {
		t.Fatalf("expected debug_sign_key_export to be disabled")
	}
	if !strings.Contains(err.Error(), "debug export disabled") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDebugExportDerivedSigningKeyEnabled(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping runner integration test in short mode")
	}

	t.Setenv("LTSM_ENABLE_DEBUG_EXPORT", "1")

	r, err := GetRunner()
	if err != nil {
		t.Fatalf("GetRunner failed: %v", err)
	}

	exported, err := r.DebugExportDerivedSigningKey("token")
	if err != nil {
		t.Fatalf("DebugExportDerivedSigningKey failed: %v", err)
	}

	raw, err := base64.StdEncoding.DecodeString(exported)
	if err != nil {
		t.Fatalf("invalid base64 returned by debug export: %v", err)
	}
	if len(raw) == 0 {
		t.Fatalf("expected non-empty exported signing key")
	}
}
