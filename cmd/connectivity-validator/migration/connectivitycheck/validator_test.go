package connectivitycheck

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

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
