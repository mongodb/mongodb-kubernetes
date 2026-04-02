package operator

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolveReplicaSetAgentCertPaths(t *testing.T) {
	const (
		// Path the operator computes from the agent TLS secret (hash under AgentCertMountPath).
		operatorDerivedAutoPEMPath = "/mongodb-automation/agent-certs/ABC123HASH"
		// Path OM still has (e.g. VM/custom mount) — same field as AC autoPEMKeyFilePath in normal sync.
		omAutoPEMPathCustom = "/var/lib/mongodb-mms-automation/certs/agent.pem"
	)

	t.Run("non-migration steady state OM matches operator-derived path", func(t *testing.T) {
		path, ext := resolveReplicaSetAgentCertPaths(operatorDerivedAutoPEMPath, operatorDerivedAutoPEMPath, 0)
		assert.Equal(t, operatorDerivedAutoPEMPath, path)
		assert.Empty(t, ext)
	})

	t.Run("non-migration empty OM autoPEM path", func(t *testing.T) {
		path, ext := resolveReplicaSetAgentCertPaths("", operatorDerivedAutoPEMPath, 0)
		assert.Equal(t, operatorDerivedAutoPEMPath, path)
		assert.Empty(t, ext)
	})

	t.Run("post-migration first reconcile OM still custom externalMembers empty", func(t *testing.T) {
		// OM/AC still show custom path; externalMembers already empty — items mount, auth uses operator-derived to realign OM.
		path, ext := resolveReplicaSetAgentCertPaths(omAutoPEMPathCustom, operatorDerivedAutoPEMPath, 0)
		assert.Equal(t, operatorDerivedAutoPEMPath, path)
		assert.Equal(t, omAutoPEMPathCustom, ext)
	})

	t.Run("migration externalMembers present OM custom path", func(t *testing.T) {
		path, ext := resolveReplicaSetAgentCertPaths(omAutoPEMPathCustom, operatorDerivedAutoPEMPath, 3)
		assert.Equal(t, omAutoPEMPathCustom, path)
		assert.Equal(t, omAutoPEMPathCustom, ext)
	})

	t.Run("migration externalMembers present OM already operator-derived", func(t *testing.T) {
		path, ext := resolveReplicaSetAgentCertPaths(operatorDerivedAutoPEMPath, operatorDerivedAutoPEMPath, 2)
		assert.Equal(t, operatorDerivedAutoPEMPath, path)
		assert.Empty(t, ext)
	})

	t.Run("migration multi-reconcile sequence", func(t *testing.T) {
		p1, e1 := resolveReplicaSetAgentCertPaths(omAutoPEMPathCustom, operatorDerivedAutoPEMPath, 1)
		assert.Equal(t, omAutoPEMPathCustom, p1)
		assert.Equal(t, omAutoPEMPathCustom, e1)

		p2, e2 := resolveReplicaSetAgentCertPaths(omAutoPEMPathCustom, operatorDerivedAutoPEMPath, 0)
		assert.Equal(t, operatorDerivedAutoPEMPath, p2)
		assert.Equal(t, omAutoPEMPathCustom, e2)

		p3, e3 := resolveReplicaSetAgentCertPaths(operatorDerivedAutoPEMPath, operatorDerivedAutoPEMPath, 0)
		assert.Equal(t, operatorDerivedAutoPEMPath, p3)
		assert.Empty(t, e3)
	})
}
