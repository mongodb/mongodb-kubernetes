package connectivitycheck

import (
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"

	"github.com/mongodb/mongodb-kubernetes/cmd/connectivity-validator/exitcode"
	"github.com/stretchr/testify/assert"
	"go.mongodb.org/mongo-driver/x/mongo/driver/topology"
)

// TestClassifyConnectionError_TLS verifies the x509 error paths without a running server.
func TestClassifyConnectionError_TLS(t *testing.T) {
	wrap := func(inner error) error { return topology.ConnectionError{Wrapped: inner} }

	assert.Equal(t, exitcode.ExitNetworkFailed, classifyConnectionError(wrap(x509.UnknownAuthorityError{})))
	assert.Equal(t, exitcode.ExitNetworkFailed, classifyConnectionError(wrap(x509.CertificateInvalidError{Reason: x509.Expired})))
}

func TestIsKeyfileSCRAM(t *testing.T) {
	assert.True(t, isKeyfileSCRAM("SCRAM-SHA-256"))
	assert.True(t, isKeyfileSCRAM("SCRAM-SHA-1"))
	assert.False(t, isKeyfileSCRAM("MONGODB-X509"))
	assert.False(t, isKeyfileSCRAM(""))
}

// TestBuildClientOptions_SCRAMMissingKeyfile_Error ensures a missing keyfile Secret mount
// returns an error rather than proceeding without credentials.
func TestBuildClientOptions_SCRAMMissingKeyfile_Error(t *testing.T) {
	cfg := Config{
		AuthMechanism: "SCRAM-SHA-256",
		KeyfilePath:   filepath.Join(t.TempDir(), "nonexistent-keyfile"),
	}
	_, err := buildClientOptions(cfg, "mongodb://localhost:27017/")
	assert.ErrorContains(t, err, "reading keyfile")
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
	_, err = buildClientOptions(cfg, "mongodb://localhost:27017/")
	assert.ErrorContains(t, err, "parsing mongod CA certificate")
}
