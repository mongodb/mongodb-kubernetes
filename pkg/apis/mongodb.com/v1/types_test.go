package v1

import (
	"testing"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"

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

func TestGetAgentAuthentication(t *testing.T) {
	auth := newAuthentication()

	assert.Len(t, auth.Modes, 0)
	assert.Empty(t, auth.GetAgentMechanism())

	auth.Modes = append(auth.Modes, util.X509)
	assert.Len(t, auth.Modes, 1)
	assert.Equal(t, util.X509, auth.GetAgentMechanism())

	auth.Modes = append(auth.Modes, util.SCRAM)

	assert.Len(t, auth.Modes, 2)
	assert.Equal(t, util.SCRAM, auth.GetAgentMechanism())
}

func TestMinimumMajorVersion(t *testing.T) {
	mdbSpec := MongoDbSpec{
		Version:                     "3.6.0-ent",
		FeatureCompatibilityVersion: nil,
	}

	assert.Equal(t, mdbSpec.MinimumMajorVersion(), uint64(3))

	mdbSpec = MongoDbSpec{
		Version:                     "4.0.0-ent",
		FeatureCompatibilityVersion: util.StringRef("3.6"),
	}

	assert.Equal(t, mdbSpec.MinimumMajorVersion(), uint64(3))

	mdbSpec = MongoDbSpec{
		Version:                     "4.0.0",
		FeatureCompatibilityVersion: util.StringRef("3.6"),
	}

	assert.Equal(t, mdbSpec.MinimumMajorVersion(), uint64(3))
}
