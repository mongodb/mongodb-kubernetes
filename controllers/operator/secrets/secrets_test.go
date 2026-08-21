package secrets

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	"github.com/mongodb/mongodb-kubernetes/pkg/vault"
)

func TestNamespacedNameToVaultPath(t *testing.T) {
	path, err := namespacedNameToVaultPath(
		types.NamespacedName{Namespace: "my-ns", Name: "my-cert"},
		vault.DEFAULT_DATABASE_SECRET_PATH,
	)
	require.NoError(t, err)
	assert.Equal(t, "mongodbenterprise/database/my-ns/my-cert", path)
}

// TestNamespacedNameToVaultPath_RejectsPathTraversal is the regression guard for
// KUBE-309. The name reaching this function originates from free-form CR fields
// (spec.prometheus.tlsSecretKeyRef.name, spec.security.certsSecretPrefix, the
// agent clientCertificateSecretRef), so a traversing value must be rejected
// rather than producing a path that escapes the resource's namespace prefix.
func TestNamespacedNameToVaultPath_RejectsPathTraversal(t *testing.T) {
	tests := []struct {
		name   string
		nsName types.NamespacedName
	}{
		{
			name:   "traversal in secret name",
			nsName: types.NamespacedName{Namespace: "attacker-ns", Name: "../victim-ns/victim-cert"},
		},
		{
			name:   "traversal in namespace",
			nsName: types.NamespacedName{Namespace: "../victim-ns", Name: "victim-cert"},
		},
		{
			name:   "separator in secret name",
			nsName: types.NamespacedName{Namespace: "attacker-ns", Name: "victim-ns/victim-cert"},
		},
		{
			name:   "empty secret name",
			nsName: types.NamespacedName{Namespace: "attacker-ns", Name: ""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path, err := namespacedNameToVaultPath(tt.nsName, vault.DEFAULT_DATABASE_SECRET_PATH)
			require.Error(t, err)
			assert.Empty(t, path)
		})
	}
}
