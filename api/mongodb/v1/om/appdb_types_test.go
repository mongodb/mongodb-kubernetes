package om

import (
	"testing"

	"github.com/stretchr/testify/assert"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
)

func TestAppDBManagedCAConfigMapName(t *testing.T) {
	tests := []struct {
		name     string
		security *mdbv1.Security
		expected string
	}{
		{
			name:     "no security has no CA ConfigMap",
			security: nil,
			expected: "",
		},
		{
			name:     "legacy CA keeps its ConfigMap name",
			security: &mdbv1.Security{TLSConfig: &mdbv1.TLSConfig{CA: "custom-appdb-ca"}},
			expected: "custom-appdb-ca",
		},
		{
			name:     "managed certificate resolves the managed server CA bundle",
			security: &mdbv1.Security{ManagedCertificate: &mdbv1.ManagedCertificate{}},
			expected: "my-om-db-managed-server-ca-bundle",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			appDB := &AppDBSpec{OpsManagerName: "my-om", Security: tc.security}
			assert.Equal(t, tc.expected, appDB.GetCAConfigMapName())
		})
	}
}

func TestOpsManagerUserPasswordSecretName(t *testing.T) {
	assert.Equal(t, "my-om-om-password", OpsManagerUserPasswordSecretName("my-om"))
}

func TestOpsManagerUserPasswordSecretName_MatchesAppDBSpecMethod(t *testing.T) {
	appDB := &AppDBSpec{OpsManagerName: "my-om"}

	assert.Equal(t, OpsManagerUserPasswordSecretName(appDB.Name()), appDB.GetOpsManagerUserPasswordSecretName())
}
