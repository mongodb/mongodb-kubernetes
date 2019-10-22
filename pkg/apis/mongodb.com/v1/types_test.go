package v1

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEnsureSecurity_WithAllNilValues(t *testing.T) {
	spec := &MongoDbSpec{Security: nil}
	ensureSecurity(spec)
	assert.NotNil(t, spec.Security)
	assert.NotNil(t, spec.Security.TLSConfig)
	assert.NotNil(t, spec.Security.Authentication)
}

func TestEnsureSecurity_WithNilTlsConfig(t *testing.T) {
	spec := &MongoDbSpec{Security: &Security{TLSConfig: nil, Authentication: &Authentication{}}}
	ensureSecurity(spec)
	assert.NotNil(t, spec.Security)
	assert.NotNil(t, spec.Security.TLSConfig)
	assert.NotNil(t, spec.Security.Authentication)
}

func TestEnsureSecurity_WithNilAuthentication(t *testing.T) {
	spec := &MongoDbSpec{Security: &Security{TLSConfig: &TLSConfig{}, Authentication: nil}}
	ensureSecurity(spec)
	assert.NotNil(t, spec.Security)
	assert.NotNil(t, spec.Security.TLSConfig)
	assert.NotNil(t, spec.Security.Authentication)
}

func TestEnsureSecurity_EmptySpec(t *testing.T) {
	spec := &MongoDbSpec{}
	ensureSecurity(spec)
	assert.NotNil(t, spec.Security)
	assert.NotNil(t, spec.Security.TLSConfig)
	assert.NotNil(t, spec.Security.Authentication)
}
