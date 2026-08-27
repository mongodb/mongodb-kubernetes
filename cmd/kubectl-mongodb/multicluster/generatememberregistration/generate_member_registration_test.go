package generatememberregistration

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	cryptorand "crypto/rand"
)

func TestLoadMemberClusterApiServerCA(t *testing.T) {
	caPEM := generateTestCAPEM(t, "member-cluster-ca")

	tests := []struct {
		name      string
		flagValue string
		setup     func(t *testing.T) (path string, want []byte)
		wantError string
	}{
		{
			name:      "blank value means no override",
			flagValue: "",
		},
		{
			name: "valid single CA cert file",
			setup: func(t *testing.T) (string, []byte) {
				return writeTestFile(t, t.TempDir(), "ca.pem", caPEM), caPEM
			},
		},
		{
			name: "valid multi-cert bundle",
			setup: func(t *testing.T) (string, []byte) {
				bundle := concatPEMs(caPEM, generateTestCAPEM(t, "member-cluster-ca-2"))
				return writeTestFile(t, t.TempDir(), "ca-bundle.pem", bundle), bundle
			},
		},
		{
			name: "bundle containing a private key is rejected",
			setup: func(t *testing.T) (string, []byte) {
				bundle := concatPEMs(caPEM, generateTestPrivateKeyPEM(t))
				return writeTestFile(t, t.TempDir(), "tls-bundle.pem", bundle), nil
			},
			wantError: "found a private key",
		},
		{
			name: "non-PEM garbage is rejected",
			setup: func(t *testing.T) (string, []byte) {
				return writeTestFile(t, t.TempDir(), "garbage.pem", []byte("not a pem bundle")), nil
			},
			wantError: "no PEM encoded certificate found",
		},
		{
			name:      "missing file is rejected",
			flagValue: filepath.Join(t.TempDir(), "does-not-exist.pem"),
			wantError: "failed reading CA file",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := tc.flagValue
			var want []byte
			if tc.setup != nil {
				path, want = tc.setup(t)
			}

			got, err := loadMemberClusterApiServerCA(path)
			if tc.wantError != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantError)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, want, got)
		})
	}
}

// generateTestCAPEM returns a self signed, PEM encoded CA certificate. It is generated rather than
// hardcoded so the fixture is always accepted by x509.CertPool.AppendCertsFromPEM, which is what
// validateCAPEM relies on.
func generateTestCAPEM(t *testing.T, commonName string) []byte {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), cryptorand.Reader)
	require.NoError(t, err)

	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour * 24),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}

	der, err := x509.CreateCertificate(cryptorand.Reader, &template, &template, &key.PublicKey, key)
	require.NoError(t, err)

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// generateTestPrivateKeyPEM returns a PEM encoded EC private key, standing in for the server key a
// TLS terminator keeps alongside its certificate in a single bundle file.
func generateTestPrivateKeyPEM(t *testing.T) []byte {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), cryptorand.Reader)
	require.NoError(t, err)

	der, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)

	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
}

// concatPEMs joins PEM blocks into the single bundle file a user would pass on the command line.
func concatPEMs(pems ...[]byte) []byte {
	var bundle []byte
	for _, p := range pems {
		bundle = append(bundle, p...)
	}
	return bundle
}

func writeTestFile(t *testing.T, dir, name string, contents []byte) string {
	t.Helper()

	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, contents, 0o600))
	return path
}
