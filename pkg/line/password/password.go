package password

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"encoding/hex"
	"fmt"
	"math/big"
)

// EncryptPassword constructs the payload and encrypts it using RSA-PKCS1v15.
func EncryptPassword(email, password, sessionKey, nHex, eHex string) (string, error) {
	// 1. Construct the payload: [len + val]...
	payload, err := createPayload(email, password, sessionKey)
	if err != nil {
		return "", fmt.Errorf("failed to create password payload: %w", err)
	}

	// 2. Parse Public Key from Hex
	pubKey, err := parseRSAPublicKey(nHex, eHex)
	if err != nil {
		return "", fmt.Errorf("failed to parse public key: %w", err)
	}

	// 3. Encrypt using RSA-PKCS1-v1.5
	encryptedBytes, err := rsa.EncryptPKCS1v15(rand.Reader, pubKey, payload)
	if err != nil {
		return "", fmt.Errorf("failed to encrypt payload: %w", err)
	}

	// 4. Return as Hex string
	return hex.EncodeToString(encryptedBytes), nil
}

// Format: [len(sessionKey) + sessionKey + len(email) + email + len(password) + password]
func createPayload(email, password, sessionKey string) ([]byte, error) {
	var payload bytes.Buffer
	for _, field := range []string{sessionKey, email, password} {
		if len(field) > 255 {
			return nil, fmt.Errorf("field is too long: %d bytes", len(field))
		}
		payload.WriteByte(byte(len(field)))
		payload.WriteString(field)
	}
	return payload.Bytes(), nil
}

// parseRSAPublicKey converts hex modulus and exponent into *rsa.PublicKey.
func parseRSAPublicKey(nHex, eHex string) (*rsa.PublicKey, error) {
	n := new(big.Int)
	if _, ok := n.SetString(nHex, 16); !ok {
		return nil, fmt.Errorf("invalid modulus hex: '%s'", nHex)
	}

	e := new(big.Int)
	if _, ok := e.SetString(eHex, 16); !ok {
		return nil, fmt.Errorf("invalid exponent hex")
	}

	if !e.IsInt64() {
		return nil, fmt.Errorf("exponent too large for int")
	}

	return &rsa.PublicKey{
		N: n,
		E: int(e.Int64()),
	}, nil
}
