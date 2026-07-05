package kalshi

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

// LoadPEM resolves the signer's private key from its two configured
// sources, preferring the inline PEM when both are set (the same
// precedence config.Validate documents for KALSHI_PRIVATE_KEY vs
// KALSHI_PRIVATE_KEY_PATH). The second return is a source label
// ("inline" / "file:<path>") safe for logging — never log the bytes.
func LoadPEM(inline, path string) ([]byte, string, error) {
	if inline != "" {
		return []byte(inline), "inline", nil
	}
	if path == "" {
		return nil, "", errors.New("kalshi: no PEM source configured")
	}
	b, err := os.ReadFile(path) //nolint:gosec // operator-supplied key path from config/env, not user input
	if err != nil {
		return nil, "", fmt.Errorf("kalshi: read private key %s: %w", path, err)
	}
	return b, "file:" + path, nil
}

// Signer produces RSA-PSS authentication headers for Kalshi API requests.
type Signer struct {
	keyID      string
	privateKey *rsa.PrivateKey
	now        func() time.Time
}

// NewSigner creates a Signer from a PEM-encoded RSA private key and its key ID.
func NewSigner(keyID string, pemKey []byte) (*Signer, error) {
	block, _ := pem.Decode(pemKey)
	if block == nil {
		return nil, fmt.Errorf("kalshi: failed to decode PEM block")
	}

	key, err := parseRSAPrivateKey(block)
	if err != nil {
		return nil, fmt.Errorf("kalshi: parse private key: %w", err)
	}

	return &Signer{
		keyID:      keyID,
		privateKey: key,
		now:        time.Now,
	}, nil
}

func parseRSAPrivateKey(block *pem.Block) (*rsa.PrivateKey, error) {
	// Try PKCS8 first (BEGIN PRIVATE KEY), then PKCS1 (BEGIN RSA PRIVATE KEY).
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		rsaKey, ok := key.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("PKCS8 key is not RSA")
		}
		return rsaKey, nil
	}
	return x509.ParsePKCS1PrivateKey(block.Bytes)
}

// AuthHeaders returns the three authentication headers required by the Kalshi
// API: KALSHI-ACCESS-KEY, KALSHI-ACCESS-SIGNATURE, and KALSHI-ACCESS-TIMESTAMP.
//
// The signature covers: timestampMS + httpMethod + requestPath
// using RSA-PSS with SHA-256 and a salt length equal to the hash length.
func (s *Signer) AuthHeaders(method, path string) (key, signature, timestamp string, err error) {
	ts := s.now().UnixMilli()
	timestamp = strconv.FormatInt(ts, 10)

	message := timestamp + method + path
	hash := sha256.Sum256([]byte(message))

	sig, err := rsa.SignPSS(rand.Reader, s.privateKey, crypto.SHA256, hash[:], &rsa.PSSOptions{
		SaltLength: rsa.PSSSaltLengthEqualsHash,
	})
	if err != nil {
		return "", "", "", fmt.Errorf("kalshi: sign request: %w", err)
	}

	return s.keyID, base64.StdEncoding.EncodeToString(sig), timestamp, nil
}
