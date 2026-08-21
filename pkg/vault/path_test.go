package vault

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSecretPath(t *testing.T) {
	tests := []struct {
		name       string
		basePath   string
		components []string
		expected   string
	}{
		{
			name:       "plain namespace and name",
			basePath:   DEFAULT_DATABASE_SECRET_PATH,
			components: []string{"my-ns", "my-cert"},
			expected:   "mongodbenterprise/database/my-ns/my-cert",
		},
		{
			name:       "no components returns base path",
			basePath:   DEFAULT_DATABASE_SECRET_PATH,
			components: nil,
			expected:   "mongodbenterprise/database",
		},
		{
			name:       "dots inside a component are allowed",
			basePath:   DEFAULT_DATABASE_SECRET_PATH,
			components: []string{"my-ns", "my.cert..pem"},
			expected:   "mongodbenterprise/database/my-ns/my.cert..pem",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path, err := SecretPath(tt.basePath, tt.components...)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, path)
		})
	}
}

// TestSecretPath_RejectsPathTraversal is the regression guard for KUBE-309: a
// CR-supplied secret name such as "../victim-ns/victim-cert" must never be
// concatenated into a Vault path, since it would walk out of the resource's own
// namespace prefix and into another tenant's secrets.
func TestSecretPath_RejectsPathTraversal(t *testing.T) {
	tests := []struct {
		name      string
		component string
	}{
		{name: "parent directory traversal", component: "../victim-ns/victim-cert"},
		{name: "bare parent directory", component: ".."},
		{name: "current directory", component: "."},
		{name: "leading slash", component: "/victim-ns/victim-cert"},
		{name: "trailing slash", component: "victim-cert/"},
		{name: "embedded separator", component: "victim-ns/victim-cert"},
		{name: "backslash separator", component: `..\victim-ns\victim-cert`},
		{name: "nested traversal", component: "a/../../victim-ns/victim-cert"},
		{name: "empty component", component: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// as the secret name
			path, err := SecretPath(DEFAULT_DATABASE_SECRET_PATH, "attacker-ns", tt.component)
			require.Error(t, err, "traversing secret name must be rejected")
			assert.Empty(t, path)

			// and as the namespace
			path, err = SecretPath(DEFAULT_DATABASE_SECRET_PATH, tt.component, "some-cert")
			require.Error(t, err, "traversing namespace must be rejected")
			assert.Empty(t, path)
		})
	}
}

// TestVaultClientRejectsTraversalBeforeLogin asserts the chokepoint guard: the
// two methods that talk to Vault reject a non-canonical path before attempting
// to log in, so no traversing path can reach Vault even from a sink that still
// builds its path by concatenation.
func TestVaultClientRejectsTraversalBeforeLogin(t *testing.T) {
	traversalPath := "mongodbenterprise/database/attacker-ns/../victim-ns/victim-cert"

	// A zero-value client has no transport; reaching Login would fail
	// differently, so an "invalid vault secret path" error proves the guard ran
	// first.
	client := &VaultClient{}

	_, err := client.ReadSecret(traversalPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid vault secret path")

	err = client.PutSecret(traversalPath, map[string]any{"data": map[string]any{}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid vault secret path")
}

func TestValidateSecretPath(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		expectErr bool
	}{
		{name: "canonical path", path: "mongodbenterprise/database/my-ns/my-cert", expectErr: false},
		{name: "dots inside a segment", path: "mongodbenterprise/database/my-ns/my.cert..pem", expectErr: false},
		{name: "empty", path: "", expectErr: true},
		{name: "absolute", path: "/mongodbenterprise/database/my-ns/my-cert", expectErr: true},
		{name: "parent traversal", path: "mongodbenterprise/database/attacker-ns/../victim-ns/victim-cert", expectErr: true},
		{name: "current directory segment", path: "mongodbenterprise/database/./my-ns/my-cert", expectErr: true},
		{name: "double separator", path: "mongodbenterprise/database//my-ns/my-cert", expectErr: true},
		{name: "trailing separator", path: "mongodbenterprise/database/my-ns/my-cert/", expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSecretPath(tt.path)
			if tt.expectErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
