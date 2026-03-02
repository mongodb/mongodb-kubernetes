package connectivitycheck

import (
	"context"
	"crypto/x509"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.mongodb.org/mongo-driver/x/mongo/driver/topology"
)

// TestClassifyConnectionError_TLS verifies the x509 error paths without a running server.
func TestClassifyConnectionError_TLS(t *testing.T) {
	wrap := func(inner error) error { return topology.ConnectionError{Wrapped: inner} }

	assert.Equal(t, ExitTLSFailed, classifyConnectionError(wrap(x509.UnknownAuthorityError{})))
	assert.Equal(t, ExitTLSFailed, classifyConnectionError(wrap(x509.CertificateInvalidError{Reason: x509.Expired})))
}

// These tests make real network calls but require no Docker or external services.

func TestValidate_DNSFailed_ConnectionString(t *testing.T) {
	cfg := Config{
		ConnectionString: "mongodb://nonexistent.invalid:27017/?serverSelectionTimeoutMS=500",
		AuthMechanism:    "SCRAM-SHA-256",
		KeyfilePath:      "/dev/null",
	}
	assert.Equal(t, ExitDNSFailed, Validate(context.Background(), cfg))
}

func TestValidate_MemberUnreachable(t *testing.T) {
	cfg := Config{
		ConnectionString: "mongodb://localhost:27999/?serverSelectionTimeoutMS=500",
		AuthMechanism:    "SCRAM-SHA-256",
		KeyfilePath:      "/dev/null",
	}
	assert.Equal(t, ExitMemberUnreachable, Validate(context.Background(), cfg))
}
