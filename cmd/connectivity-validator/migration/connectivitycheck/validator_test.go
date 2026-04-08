package connectivitycheck

import (
	"crypto/x509"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.mongodb.org/mongo-driver/x/mongo/driver/topology"
)

// TestClassifyConnectionError_TLS verifies the x509 error paths without a running server.
func TestClassifyConnectionError_TLS(t *testing.T) {
	wrap := func(inner error) error { return topology.ConnectionError{Wrapped: inner} }

	assert.Equal(t, ExitNetworkFailed, classifyConnectionError(wrap(x509.UnknownAuthorityError{})))
	assert.Equal(t, ExitNetworkFailed, classifyConnectionError(wrap(x509.CertificateInvalidError{Reason: x509.Expired})))
}

func TestIsKeyfileSCRAM(t *testing.T) {
	assert.True(t, isKeyfileSCRAM("SCRAM-SHA-256"))
	assert.True(t, isKeyfileSCRAM("SCRAM-SHA-1"))
	assert.False(t, isKeyfileSCRAM("MONGODB-X509"))
	assert.False(t, isKeyfileSCRAM(""))
}
