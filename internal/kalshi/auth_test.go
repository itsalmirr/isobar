package kalshi

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNewSignerRejectsGarbage(t *testing.T) {
	_, err := NewSigner("k", []byte("not a pem"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "PEM")
}

func TestNewSignerRejectsNonRSAPKCS8(t *testing.T) {
	// PKCS8 ed25519 key: parse succeeds but is not *rsa.PrivateKey.
	// Construct an obviously invalid PKCS8 block to exercise the error path.
	bad := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("garbage")})
	_, err := NewSigner("k", bad)
	require.Error(t, err)
}

func TestSignerProducesRFCCompliantSignature(t *testing.T) {
	keyID, pemKey := testKeyPair(t)
	signer, err := NewSigner(keyID, pemKey)
	require.NoError(t, err)

	// Override now() so the timestamp is deterministic for verification.
	fixed := time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC)
	signer.now = func() time.Time { return fixed }

	key, sig, ts, err := signer.AuthHeaders("GET", "/trade-api/v2/markets")
	require.NoError(t, err)
	require.Equal(t, keyID, key)
	require.Equal(t, strconv.FormatInt(fixed.UnixMilli(), 10), ts)

	// Signature decodes as base64 and verifies against the expected message.
	sigBytes, err := base64.StdEncoding.DecodeString(sig)
	require.NoError(t, err)

	expected := ts + "GET" + "/trade-api/v2/markets"
	hash := sha256.Sum256([]byte(expected))

	pub := &signer.privateKey.PublicKey
	require.NoError(t, rsa.VerifyPSS(pub, crypto.SHA256, hash[:], sigBytes, &rsa.PSSOptions{
		SaltLength: rsa.PSSSaltLengthEqualsHash,
	}))
}

func TestSignerAcceptsPKCS8(t *testing.T) {
	// Generate an RSA key, marshal it as PKCS8, and verify the signer
	// accepts that form.
	_, pemKey := testKeyPair(t)
	block, _ := pem.Decode(pemKey)
	require.NotNil(t, block)
	rsaKey, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	require.NoError(t, err)

	der, err := x509.MarshalPKCS8PrivateKey(rsaKey)
	require.NoError(t, err)
	pkcs8PEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})

	signer, err := NewSigner("k", pkcs8PEM)
	require.NoError(t, err)
	_, sig, _, err := signer.AuthHeaders("GET", "/anything")
	require.NoError(t, err)
	require.NotEmpty(t, sig)
}

func TestSignerTimestampsAdvance(t *testing.T) {
	_, pemKey := testKeyPair(t)
	signer, err := NewSigner("k", pemKey)
	require.NoError(t, err)

	_, _, ts1, err := signer.AuthHeaders("GET", "/p")
	require.NoError(t, err)
	time.Sleep(5 * time.Millisecond)
	_, _, ts2, err := signer.AuthHeaders("GET", "/p")
	require.NoError(t, err)

	n1, _ := strconv.ParseInt(ts1, 10, 64)
	n2, _ := strconv.ParseInt(ts2, 10, 64)
	require.Greater(t, n2, n1)
}

func TestLoadPEMPrefersInline(t *testing.T) {
	b, src, err := LoadPEM("inline-pem", "/nonexistent/ignored.pem")
	require.NoError(t, err)
	require.Equal(t, []byte("inline-pem"), b)
	require.Equal(t, "inline", src)
}

func TestLoadPEMReadsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key.pem")
	require.NoError(t, os.WriteFile(path, []byte("file-pem"), 0o600))

	b, src, err := LoadPEM("", path)
	require.NoError(t, err)
	require.Equal(t, []byte("file-pem"), b)
	require.Equal(t, "file:"+path, src)
}

func TestLoadPEMErrors(t *testing.T) {
	_, _, err := LoadPEM("", "")
	require.EqualError(t, err, "kalshi: no PEM source configured")

	_, _, err = LoadPEM("", filepath.Join(t.TempDir(), "missing.pem"))
	require.ErrorContains(t, err, "read private key")
}
