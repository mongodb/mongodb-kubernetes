package certs

import (
	"testing"

	"github.com/stretchr/testify/assert"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

// TestUnusedClientCertificateWarnings covers when the operator points out that a
// managedCertificate.client block has no effect: only when the resource asks for the feature, configures
// the block, and the deployment issues no client certificates because neither internal cluster
// authentication nor the agent uses x509.
func TestUnusedClientCertificateWarnings(t *testing.T) {
	withSecurity := func(security func(*mdbv1.Security)) *mdbv1.MongoDB {
		return newMongoDB(resName, func(m *mdbv1.MongoDB) {
			security(m.Spec.Security)
		})
	}
	managed := func(s *mdbv1.Security) { s.ManagedCertificate = &mdbv1.ManagedCertificate{} }
	withClientBlock := func(s *mdbv1.Security) { s.ManagedCertificate.Client = &mdbv1.CertificateConfig{} }
	scramInternalAuth := func(s *mdbv1.Security) {
		s.Authentication = &mdbv1.Authentication{Enabled: true, InternalCluster: util.SCRAM}
	}
	x509InternalAuth := func(s *mdbv1.Security) {
		s.Authentication = &mdbv1.Authentication{Enabled: true, InternalCluster: util.X509}
	}
	x509Agent := func(s *mdbv1.Security) {
		s.Authentication = &mdbv1.Authentication{
			Enabled:         true,
			InternalCluster: util.SCRAM,
			Agents:          mdbv1.AgentAuthentication{Mode: util.X509},
		}
	}

	t.Run("no warning when the feature is off", func(t *testing.T) {
		// managedCertificate is what turns the feature on, and client is inside it, so a resource
		// without the block cannot have configured the client settings either.
		res := withSecurity(scramInternalAuth)
		assert.Empty(t, UnusedClientConfigured(res))
	})

	t.Run("no warning when the client block is not configured", func(t *testing.T) {
		res := withSecurity(func(s *mdbv1.Security) { managed(s); scramInternalAuth(s) })
		assert.Empty(t, UnusedClientConfigured(res))
	})

	t.Run("no warning when internal cluster authentication uses x509", func(t *testing.T) {
		res := withSecurity(func(s *mdbv1.Security) { managed(s); withClientBlock(s); x509InternalAuth(s) })
		assert.Empty(t, UnusedClientConfigured(res))
	})

	t.Run("no warning when the agent uses x509", func(t *testing.T) {
		res := withSecurity(func(s *mdbv1.Security) { managed(s); withClientBlock(s); x509Agent(s) })
		assert.Empty(t, UnusedClientConfigured(res))
	})

	t.Run("warns when neither uses x509", func(t *testing.T) {
		res := withSecurity(func(s *mdbv1.Security) { managed(s); withClientBlock(s); scramInternalAuth(s) })
		warnings := UnusedClientConfigured(res)
		assert.Len(t, warnings, 1)
		assert.Contains(t, string(warnings[0]), "spec.managedCertificate.client is set even though the deployment doesn't use x509 for internal")
	})
}
