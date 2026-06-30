package ltsm

import (
	"crypto/sha256"
	"encoding/base64"
	"testing"
)

// initModule creates and initializes a Module with the embind Imports.
func initModule(t *testing.T) (*Module, *Imports) {
	t.Helper()
	imp := NewImports()
	mod := NewModule(imp)
	imp.SetModule(mod)

	// Phase 1: __wasm_call_ctors
	mod.fP()
	// Phase 2: embind type/class registrations
	mod.fT()

	return mod, imp
}

func TestEmbindInit(t *testing.T) {
	_, imp := initModule(t)

	// Check that fundamental types are registered
	foundVoid := false
	foundBool := false
	foundInt := false
	foundString := false
	foundEmval := false
	for _, ti := range imp.types {
		switch ti.Kind {
		case "void":
			foundVoid = true
		case "bool":
			foundBool = true
		case "int":
			foundInt = true
		case "string":
			foundString = true
		case "emval":
			foundEmval = true
		}
	}

	if !foundVoid {
		t.Error("missing void type registration")
	}
	if !foundBool {
		t.Error("missing bool type registration")
	}
	if !foundInt {
		t.Error("missing int type registration")
	}
	if !foundString {
		t.Error("missing string type registration")
	}
	if !foundEmval {
		t.Error("missing emval type registration")
	}

	t.Logf("registered %d types", len(imp.types))

	// Check expected classes
	expectedClasses := []string{
		"SecureKey",
		"Hmac",
		"Curve25519Key",
		"E2EEKey",
		"E2EEChannel",
		"E2EEKeychain",
		"AesKey",
	}

	for _, name := range expectedClasses {
		ci := imp.classByName[name]
		if ci == nil {
			t.Errorf("missing class: %s", name)
			continue
		}
		t.Logf("class %s: %d ctors, %d methods, %d static methods",
			name, len(ci.Constructors), len(ci.Methods), len(ci.StaticMethods))

		for mName := range ci.Methods {
			t.Logf("  method: %s (args=%d, invoker=%d)", mName, ci.Methods[mName].ArgCount, ci.Methods[mName].InvokerIdx)
		}
		for mName := range ci.StaticMethods {
			t.Logf("  static: %s (args=%d, invoker=%d)", mName, ci.StaticMethods[mName].ArgCount, ci.StaticMethods[mName].InvokerIdx)
		}
		for i, ctor := range ci.Constructors {
			t.Logf("  ctor[%d]: args=%d, invoker=%d", i, ctor.ArgCount, ctor.InvokerIdx)
		}
	}
}

func TestSecureKeyLoadToken(t *testing.T) {
	_, imp := initModule(t)

	token := "wODdrvWqmdP4Zliay-iF3cz3KZcK0ekrial868apg06TXeCo7A1hIQO0ESElHg6D"

	// Write token as std::string in module memory
	strPtr := imp.writeStdString(token)

	// Call SecureKey.loadToken via embind
	skPtr, err := imp.CallStatic("SecureKey", "loadToken", strPtr)
	if err != nil {
		t.Fatalf("SecureKey.loadToken failed: %v", err)
	}
	if skPtr == 0 {
		t.Fatal("SecureKey.loadToken returned null pointer")
	}
	t.Logf("SecureKey.loadToken returned ptr=%d", skPtr)
}

func TestSecureKeyDeriveKey(t *testing.T) {
	_, imp := initModule(t)

	token := "wODdrvWqmdP4Zliay-iF3cz3KZcK0ekrial868apg06TXeCo7A1hIQO0ESElHg6D"
	strPtr := imp.writeStdString(token)

	skPtr, err := imp.CallStatic("SecureKey", "loadToken", strPtr)
	if err != nil {
		t.Fatalf("SecureKey.loadToken failed: %v", err)
	}

	// Create hash inputs
	cvHash := sha256.Sum256([]byte("testClientVersion"))
	atHash := sha256.Sum256([]byte("testAccessToken"))

	cvHandle := imp.WriteEmvalBytes(cvHash[:])
	atHandle := imp.WriteEmvalBytes(atHash[:])

	dkPtr, err := imp.CallMethod("SecureKey", "deriveKey", skPtr, cvHandle, atHandle)
	if err != nil {
		t.Fatalf("SecureKey.deriveKey failed: %v", err)
	}
	if dkPtr == 0 {
		t.Fatal("SecureKey.deriveKey returned null pointer")
	}
	t.Logf("SecureKey.deriveKey returned ptr=%d", dkPtr)
}

func TestHmacDigest(t *testing.T) {
	_, imp := initModule(t)

	// loadToken → deriveKey → Hmac.new → digest
	token := "wODdrvWqmdP4Zliay-iF3cz3KZcK0ekrial868apg06TXeCo7A1hIQO0ESElHg6D"
	strPtr := imp.writeStdString(token)

	skPtr, err := imp.CallStatic("SecureKey", "loadToken", strPtr)
	if err != nil {
		t.Fatalf("SecureKey.loadToken failed: %v", err)
	}

	cvHash := sha256.Sum256([]byte("testClientVersion"))
	atHash := sha256.Sum256([]byte("testAccessToken"))
	cvHandle := imp.WriteEmvalBytes(cvHash[:])
	atHandle := imp.WriteEmvalBytes(atHash[:])

	dkPtr, err := imp.CallMethod("SecureKey", "deriveKey", skPtr, cvHandle, atHandle)
	if err != nil {
		t.Fatalf("SecureKey.deriveKey failed: %v", err)
	}

	// Create Hmac
	hmacPtr, err := imp.Construct("Hmac", dkPtr)
	if err != nil {
		t.Fatalf("Hmac.new failed: %v", err)
	}
	if hmacPtr == 0 {
		t.Fatal("Hmac constructor returned null")
	}
	t.Logf("Hmac constructed at ptr=%d", hmacPtr)

	// Compute digest
	dataToSign := []byte("/api/v4/test" + `{"key":"value"}`)
	dataHandle := imp.WriteEmvalBytes(dataToSign)

	resultHandle, err := imp.CallMethod("Hmac", "digest", hmacPtr, dataHandle)
	if err != nil {
		t.Fatalf("Hmac.digest failed: %v", err)
	}

	sigBytes, err := imp.ReadEmvalBytes(resultHandle)
	if err != nil {
		t.Fatalf("failed to read digest result: %v", err)
	}
	imp.emval.DecRef(resultHandle)

	sig := base64.StdEncoding.EncodeToString(sigBytes)
	t.Logf("HMAC signature: %s (len=%d)", sig, len(sigBytes))

	if len(sigBytes) == 0 {
		t.Fatal("digest returned empty bytes")
	}
}

func TestSignFullFlow(t *testing.T) {
	_, imp := initModule(t)

	// Full sign flow matching Runtime.Sign
	token := "wODdrvWqmdP4Zliay-iF3cz3KZcK0ekrial868apg06TXeCo7A1hIQO0ESElHg6D"
	clientVersion := "13.4.2"
	accessToken := "test_access_token_12345"
	reqPath := "/api/v4/TalkService.do"
	body := ""

	// 1. SecureKey.loadToken
	strPtr := imp.writeStdString(token)
	skPtr, err := imp.CallStatic("SecureKey", "loadToken", strPtr)
	if err != nil {
		t.Fatalf("loadToken failed: %v", err)
	}

	// 2. deriveKey
	cvHash := sha256.Sum256([]byte(clientVersion))
	atHash := sha256.Sum256([]byte(accessToken))
	dkPtr, err := imp.CallMethod("SecureKey", "deriveKey", skPtr, imp.WriteEmvalBytes(cvHash[:]), imp.WriteEmvalBytes(atHash[:]))
	if err != nil {
		t.Fatalf("deriveKey failed: %v", err)
	}

	// 3. Hmac.new
	hmacPtr, err := imp.Construct("Hmac", dkPtr)
	if err != nil {
		t.Fatalf("Hmac.new failed: %v", err)
	}

	// 4. digest
	dataToSign := []byte(reqPath + body)
	resultHandle, err := imp.CallMethod("Hmac", "digest", hmacPtr, imp.WriteEmvalBytes(dataToSign))
	if err != nil {
		t.Fatalf("digest failed: %v", err)
	}

	sigBytes, err := imp.ReadEmvalBytes(resultHandle)
	if err != nil {
		t.Fatalf("read digest result failed: %v", err)
	}
	imp.emval.DecRef(resultHandle)

	sig := base64.StdEncoding.EncodeToString(sigBytes)
	t.Logf("Signature: %s", sig)

	if len(sigBytes) != 32 {
		t.Errorf("expected 32-byte signature, got %d bytes", len(sigBytes))
	}

	// Verify deterministic: same inputs should produce same output
	strPtr2 := imp.writeStdString(token)
	skPtr2, _ := imp.CallStatic("SecureKey", "loadToken", strPtr2)
	dkPtr2, _ := imp.CallMethod("SecureKey", "deriveKey", skPtr2, imp.WriteEmvalBytes(cvHash[:]), imp.WriteEmvalBytes(atHash[:]))
	hmacPtr2, _ := imp.Construct("Hmac", dkPtr2)
	resultHandle2, _ := imp.CallMethod("Hmac", "digest", hmacPtr2, imp.WriteEmvalBytes(dataToSign))
	sigBytes2, _ := imp.ReadEmvalBytes(resultHandle2)

	sig2 := base64.StdEncoding.EncodeToString(sigBytes2)
	if sig != sig2 {
		t.Errorf("non-deterministic: sig1=%s, sig2=%s", sig, sig2)
	}
}

func TestSecureKeyExportKey(t *testing.T) {
	_, imp := initModule(t)

	token := "wODdrvWqmdP4Zliay-iF3cz3KZcK0ekrial868apg06TXeCo7A1hIQO0ESElHg6D"
	strPtr := imp.writeStdString(token)
	skPtr, err := imp.CallStatic("SecureKey", "loadToken", strPtr)
	if err != nil {
		t.Fatalf("loadToken failed: %v", err)
	}

	// Mark as exportable
	imp.MarkSecureKeyExportable(skPtr)

	resultHandle, err := imp.CallMethod("SecureKey", "exportKey", skPtr)
	if err != nil {
		t.Fatalf("exportKey failed: %v", err)
	}

	keyBytes, err := imp.ReadEmvalBytes(resultHandle)
	if err != nil {
		t.Fatalf("read exportKey result failed: %v", err)
	}
	imp.emval.DecRef(resultHandle)

	t.Logf("exported key: %d bytes", len(keyBytes))
	if len(keyBytes) == 0 {
		t.Fatal("exportKey returned empty bytes")
	}
}

func TestSecureKeyLoadKeyRoundtrip(t *testing.T) {
	_, imp := initModule(t)

	// loadToken → exportKey → loadKey → verify functional equivalence via HMAC
	token := "wODdrvWqmdP4Zliay-iF3cz3KZcK0ekrial868apg06TXeCo7A1hIQO0ESElHg6D"
	strPtr := imp.writeStdString(token)
	skPtr, err := imp.CallStatic("SecureKey", "loadToken", strPtr)
	if err != nil {
		t.Fatalf("loadToken failed: %v", err)
	}

	imp.MarkSecureKeyExportable(skPtr)
	h1, err := imp.CallMethod("SecureKey", "exportKey", skPtr)
	if err != nil {
		t.Fatalf("exportKey failed: %v", err)
	}
	key1, _ := imp.ReadEmvalBytes(h1)
	imp.emval.DecRef(h1)
	t.Logf("exported key: %d bytes", len(key1))

	// loadKey from exported bytes
	skPtr2, err := imp.CallStatic("SecureKey", "loadKey", imp.WriteEmvalBytes(key1))
	if err != nil {
		t.Fatalf("loadKey failed: %v", err)
	}

	// Verify functional equivalence: both keys produce same HMAC
	cvHash := sha256.Sum256([]byte("testCV"))
	atHash := sha256.Sum256([]byte("testAT"))
	data := []byte("/test/path")

	sign := func(sk uint32) string {
		dk, err := imp.CallMethod("SecureKey", "deriveKey", sk, imp.WriteEmvalBytes(cvHash[:]), imp.WriteEmvalBytes(atHash[:]))
		if err != nil {
			t.Fatalf("deriveKey failed: %v", err)
		}
		hm, err := imp.Construct("Hmac", dk)
		if err != nil {
			t.Fatalf("Hmac.new failed: %v", err)
		}
		rh, err := imp.CallMethod("Hmac", "digest", hm, imp.WriteEmvalBytes(data))
		if err != nil {
			t.Fatalf("digest failed: %v", err)
		}
		sig, _ := imp.ReadEmvalBytes(rh)
		imp.emval.DecRef(rh)
		return base64.StdEncoding.EncodeToString(sig)
	}

	sig1 := sign(skPtr)
	sig2 := sign(skPtr2)

	if sig1 != sig2 {
		t.Errorf("signatures differ: original=%s, reimported=%s", sig1, sig2)
	} else {
		t.Logf("SecureKey roundtrip OK (functionally equivalent): sig=%s", sig1)
	}
}

func TestAesKeyEncryptDecrypt(t *testing.T) {
	_, imp := initModule(t)

	// Generate an AesKey
	genHandle := imp.WriteEmvalBytes([]byte{}) // empty seed for generate
	aesPtr, err := imp.CallStatic("AesKey", "generate", 32, genHandle)
	if err != nil {
		t.Fatalf("AesKey.generate failed: %v", err)
	}
	if aesPtr == 0 {
		t.Fatal("AesKey.generate returned null")
	}

	// Encrypt
	plaintext := []byte("Hello, Letter Sealing!")
	ptHandle := imp.WriteEmvalBytes(plaintext)
	ctHandle, err := imp.CallMethod("AesKey", "encrypt", aesPtr, ptHandle)
	if err != nil {
		t.Fatalf("AesKey.encrypt failed: %v", err)
	}
	ciphertext, err := imp.ReadEmvalBytes(ctHandle)
	if err != nil {
		t.Fatalf("read encrypt result failed: %v", err)
	}
	imp.emval.DecRef(ctHandle)
	t.Logf("encrypted: %d bytes → %d bytes", len(plaintext), len(ciphertext))

	if len(ciphertext) <= len(plaintext) {
		t.Error("ciphertext should be longer than plaintext (includes IV/tag)")
	}

	// Decrypt
	ctHandle2 := imp.WriteEmvalBytes(ciphertext)
	ptHandle2, err := imp.CallMethod("AesKey", "decrypt", aesPtr, ctHandle2)
	if err != nil {
		t.Fatalf("AesKey.decrypt failed: %v", err)
	}
	decrypted, err := imp.ReadEmvalBytes(ptHandle2)
	if err != nil {
		t.Fatalf("read decrypt result failed: %v", err)
	}
	imp.emval.DecRef(ptHandle2)

	if string(decrypted) != string(plaintext) {
		t.Errorf("decrypted mismatch: got %q, want %q", decrypted, plaintext)
	}
	t.Logf("AesKey encrypt/decrypt roundtrip OK")
}

func TestCurve25519KeyGenerate(t *testing.T) {
	_, imp := initModule(t)

	// Load a SecureKey first
	token := "wODdrvWqmdP4Zliay-iF3cz3KZcK0ekrial868apg06TXeCo7A1hIQO0ESElHg6D"
	strPtr := imp.writeStdString(token)
	skPtr, err := imp.CallStatic("SecureKey", "loadToken", strPtr)
	if err != nil {
		t.Fatalf("loadToken failed: %v", err)
	}

	// Generate Curve25519 key
	c25Ptr, err := imp.CallStatic("Curve25519Key", "generate", skPtr)
	if err != nil {
		t.Fatalf("Curve25519Key.generate failed: %v", err)
	}
	if c25Ptr == 0 {
		t.Fatal("Curve25519Key.generate returned null")
	}

	// Get public key
	pubHandle, err := imp.CallMethod("Curve25519Key", "getPublicKey", c25Ptr)
	if err != nil {
		t.Fatalf("getPublicKey failed: %v", err)
	}
	pubKey, err := imp.ReadEmvalBytes(pubHandle)
	if err != nil {
		t.Fatalf("read public key failed: %v", err)
	}
	imp.emval.DecRef(pubHandle)

	t.Logf("Curve25519 public key: %d bytes", len(pubKey))
	if len(pubKey) != 32 {
		t.Errorf("expected 32-byte public key, got %d bytes", len(pubKey))
	}
}
