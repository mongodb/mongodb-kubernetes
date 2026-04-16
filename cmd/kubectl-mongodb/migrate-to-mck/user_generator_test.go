package migratetomck

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	userv1 "github.com/mongodb/mongodb-kubernetes/api/v1/user"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
)

// TestGenerateUserCRs_EmptyMechanisms verifies users with empty mechanisms generate successfully.
func TestGenerateUserCRs_EmptyMechanisms(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{
		"processes":   []any{},
		"replicaSets": []any{},
	})
	ac.Auth.AutoUser = "mms-automation"
	ac.Auth.Users = []*om.MongoDBUser{
		{Username: "app-user", Database: "admin", Mechanisms: []string{}, Roles: []*om.Role{{Role: "readWrite", Database: "myapp"}}},
	}

	// Option 2: reference an existing secret so the empty Mechanisms slice doesn't gate generation.
	users, err := GenerateUserCRs(ac, "scram-rs", "mongodb", GenerateOptions{
		ExistingUserSecrets: map[string]string{
			"app-user:admin": "app-user-secret",
		},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, users)
}

func TestGenerateUserCRs_DuplicateNormalizedNames(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{
		"processes":   []any{},
		"replicaSets": []any{},
	})
	ac.Auth.AutoUser = "mms-automation"
	ac.Auth.Users = []*om.MongoDBUser{
		{Username: "App_User", Database: "admin", Roles: []*om.Role{{Role: "read", Database: "test"}}},
		{Username: "app-user", Database: "admin", Roles: []*om.Role{{Role: "read", Database: "test"}}},
	}

	// Use Option 2 so both users are processed past the password step; the duplicate check fires on
	// the second user when it tries to register the same normalised CR name "app-user".
	opts := GenerateOptions{
		ExistingUserSecrets: map[string]string{
			"App_User:admin": "app-user-secret",
			"app-user:admin": "app-user2-secret",
		},
	}
	_, err := GenerateUserCRs(ac, "my-rs", "mongodb", opts)
	assert.ErrorContains(t, err, "normalize to the same Kubernetes name")
}

func TestNormalizeName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"app-user", "app-user"},
		{"CN=x509-client,O=MongoDB", "cn-x509-client-o-mongodb"},
		{"user@example.com", "user-example-com"},
		{"UPPER_CASE", "upper-case"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := userv1.NormalizeName(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestNormalizeName_InvalidInput(t *testing.T) {
	result := userv1.NormalizeName("---")
	assert.Empty(t, result)
}
