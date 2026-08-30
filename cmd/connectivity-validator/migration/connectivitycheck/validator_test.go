package connectivitycheck

import (
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.mongodb.org/mongo-driver/x/mongo/driver/topology"
	"go.uber.org/zap"

	"github.com/mongodb/mongodb-kubernetes/cmd/connectivity-validator/exitcode"
)

// testLog discards output; the validator logs diagnostics but the tests assert on return values.
var testLog = zap.NewNop().Sugar()

// TestClassifyConnectionError_TLS verifies the x509 error paths without a running server.
func TestClassifyConnectionError_TLS(t *testing.T) {
	wrap := func(inner error) error { return topology.ConnectionError{Wrapped: inner} }

	assert.Equal(t, exitcode.ExitNetworkFailed, classifyConnectionError(wrap(x509.UnknownAuthorityError{}), testLog))
	assert.Equal(t, exitcode.ExitNetworkFailed, classifyConnectionError(wrap(x509.CertificateInvalidError{Reason: x509.Expired}), testLog))
}

// TestBuildClientOptions_NoAuthWithMongodTLS ensures TLS is attempted even when no auth
// mechanism is set, and that an invalid CA file surfaces as a parse error.
func TestBuildClientOptions_NoAuthWithMongodTLS_SetsTLS(t *testing.T) {
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	err := os.WriteFile(caFile, []byte("not-a-pem"), 0o600)
	assert.NoError(t, err)

	cfg := Config{
		AuthMechanism:   "",
		MongodTLSCAPath: caFile,
	}
	_, err = buildClientOptions(cfg, "mongodb://localhost:27017/", testLog)
	assert.ErrorContains(t, err, "parsing mongod CA certificate")
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
	_, err = buildClientOptions(cfg, "mongodb://localhost:27017/", testLog)
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
	_, err = buildClientOptions(cfg, "mongodb://localhost:27017/", testLog)
	assert.ErrorContains(t, err, "parsing mongod CA certificate")
}
