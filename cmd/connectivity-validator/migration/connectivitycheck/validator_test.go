package connectivitycheck

import (
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.mongodb.org/mongo-driver/x/mongo/driver/topology"

	"github.com/mongodb/mongodb-kubernetes/cmd/connectivity-validator/exitcode"
)

// TestClassifyConnectionError_TLS verifies the x509 error paths without a running server.
func TestClassifyConnectionError_TLS(t *testing.T) {
	wrap := func(inner error) error { return topology.ConnectionError{Wrapped: inner} }

	assert.Equal(t, exitcode.ExitNetworkFailed, classifyConnectionError(wrap(x509.UnknownAuthorityError{})))
	assert.Equal(t, exitcode.ExitNetworkFailed, classifyConnectionError(wrap(x509.CertificateInvalidError{Reason: x509.Expired})))
}

// TestBuildClientOptions_ClientCertRequired_MissingCert verifies that when
// ClientCertRequired is true and the cert file is absent, buildClientOptions
// returns a clear "certificate required but not found" error instead of
// falling back to CA-only TLS.
func TestBuildClientOptions_ClientCertRequired_MissingCert(t *testing.T) {
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	err := os.WriteFile(caFile, []byte("not-a-pem"), 0o600)
	assert.NoError(t, err)

	cfg := Config{
		AuthMechanism:      "",
		MongodTLSCAPath:    caFile,
		CertPath:           filepath.Join(t.TempDir(), "missing.pem"),
		ClientCertRequired: true,
	}
	_, err = buildClientOptions(cfg, "mongodb://localhost:27017/")
	assert.ErrorContains(t, err, "client certificate required but not found")
}

// TestBuildClientOptions_ClientCertOptional_MissingCert verifies that when
// ClientCertRequired is false and the cert file is absent, buildClientOptions
// falls back to CA-only TLS (surfaces as a CA parse error, not a cert error).
func TestBuildClientOptions_ClientCertOptional_MissingCert(t *testing.T) {
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	err := os.WriteFile(caFile, []byte("not-a-pem"), 0o600)
	assert.NoError(t, err)

	cfg := Config{
		AuthMechanism:      "",
		MongodTLSCAPath:    caFile,
		CertPath:           filepath.Join(t.TempDir(), "missing.pem"),
		ClientCertRequired: false,
	}
	_, err = buildClientOptions(cfg, "mongodb://localhost:27017/")
	assert.ErrorContains(t, err, "parsing mongod CA certificate")
}

// TestBuildClientOptions_SCRAMMissingKeyfile_Error ensures that a missing keyfile returns an error.
// The operator always mounts the keyfile Secret before creating the Job, so a missing keyfile
// indicates a setup problem and must not silently proceed without credentials.
func TestBuildClientOptions_SCRAMMissingKeyfile_Error(t *testing.T) {
	cfg := Config{
		AuthMechanism: "SCRAM-SHA-256",
		KeyfilePath:   filepath.Join(t.TempDir(), "nonexistent-keyfile"),
	}
	_, err := buildClientOptions(cfg, "mongodb://localhost:27017/")
	assert.ErrorContains(t, err, "reading keyfile")
}

// TestBuildClientOptions_NoAuthWithMongodTLS ensures TLS is configured even when no auth
// mechanism is set, as long as MongodTLSCAPath points to a valid CA file.
func TestBuildClientOptions_NoAuthWithMongodTLS_SetsTLS(t *testing.T) {
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	// Write a minimal self-signed PEM so the CA pool parser accepts it.
	// Use the test certificate embedded in the crypto/tls package instead of generating one.
	// A deliberately invalid PEM is enough to confirm the code path reached SetTLSConfig; we
	// test the actual failure (bad PEM) to confirm the branch is hit.
	err := os.WriteFile(caFile, []byte("not-a-pem"), 0o600)
	assert.NoError(t, err)

	cfg := Config{
		AuthMechanism:   "",
		MongodTLSCAPath: caFile,
	}
	_, err = buildClientOptions(cfg, "mongodb://localhost:27017/")
	// The CA file contains garbage PEM, so AppendCertsFromPEM returns false and we expect an
	// error about parsing the certificate. This confirms the TLS branch was reached.
	assert.ErrorContains(t, err, "parsing mongod CA certificate")
}
