package migratetomck

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mongodb/mongodb-kubernetes/controllers/om"
)

func writeTempFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secrets.csv")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func TestParseUsersSecretsFile_EmptyPath(t *testing.T) {
	m, err := parseUsersSecretsFile("")
	require.NoError(t, err)
	assert.Nil(t, m)
}

func TestParseUsersSecretsFile_Valid(t *testing.T) {
	content := `# comment
alice:admin,alice-secret
bob:mydb,bob-secret
`
	path := writeTempFile(t, content)
	m, err := parseUsersSecretsFile(path)
	require.NoError(t, err)
	require.Equal(t, "alice-secret", m["alice:admin"])
	require.Equal(t, "bob-secret", m["bob:mydb"])
}

func TestParseUsersSecretsFile_InvalidFormat(t *testing.T) {
	path := writeTempFile(t, "no-comma-here\n")
	_, err := parseUsersSecretsFile(path)
	assert.ErrorContains(t, err, "line 1")
}

func TestParseUsersSecretsFile_MissingDatabase(t *testing.T) {
	path := writeTempFile(t, "username-without-db,some-secret\n")
	_, err := parseUsersSecretsFile(path)
	assert.ErrorContains(t, err, "missing the database part")
}

func TestParseUsersSecretsFile_InvalidSecretName(t *testing.T) {
	path := writeTempFile(t, "alice:admin,Invalid_Name\n")
	_, err := parseUsersSecretsFile(path)
	assert.ErrorContains(t, err, "not a valid Kubernetes name")
}

func TestParseUsersSecretsFile_EmptyFields(t *testing.T) {
	path := writeTempFile(t, ",\n")
	_, err := parseUsersSecretsFile(path)
	assert.ErrorContains(t, err, "expected \"username:database,secret-name\"")
}

func TestGenerateUserCRs_ExistingSecrets(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{
		"processes":   []any{},
		"replicaSets": []any{},
	})
	ac.Auth.AutoUser = "mms-automation"
	ac.Auth.Users = []*om.MongoDBUser{
		{Username: "alice", Database: "admin", Roles: []*om.Role{{Role: "readWrite", Database: "myapp"}}},
	}

	opts := GenerateOptions{
		ExistingUserSecrets: map[string]string{
			"alice:admin": "alice-secret",
		},
	}
	users, err := GenerateUserCRs(ac, "my-rs", "default", opts)
	require.NoError(t, err)
	require.Len(t, users, 1, "Option 2 must not generate a Secret object")
	y, err := marshalCRToYAML(users[0])
	require.NoError(t, err)
	assert.Contains(t, y, "alice-secret")
}

func TestGenerateUserCRs_ExistingSecrets_SkipsUnmappedUsers(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{
		"processes":   []any{},
		"replicaSets": []any{},
	})
	ac.Auth.AutoUser = "mms-automation"
	ac.Auth.Users = []*om.MongoDBUser{
		{Username: "alice", Database: "admin", Roles: []*om.Role{{Role: "read", Database: "test"}}},
		{Username: "bob", Database: "admin", Roles: []*om.Role{{Role: "read", Database: "test"}}},
	}

	opts := GenerateOptions{
		ExistingUserSecrets: map[string]string{
			"alice:admin": "alice-secret",
		},
	}
	users, err := GenerateUserCRs(ac, "my-rs", "default", opts)
	require.NoError(t, err)
	require.Len(t, users, 1)
	y, err := marshalCRToYAML(users[0])
	require.NoError(t, err)
	assert.Contains(t, y, "alice")
}

func TestCollectUserPasswords_SkipOnEnter(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{
		"processes":   []any{},
		"replicaSets": []any{},
	})
	ac.Auth.AutoUser = "mms-automation"
	ac.Auth.Users = []*om.MongoDBUser{
		{Username: "alice", Database: "admin", Roles: []*om.Role{{Role: "read", Database: "test"}}},
		{Username: "bob", Database: "admin", Roles: []*om.Role{{Role: "read", Database: "test"}}},
	}

	opts := &GenerateOptions{}
	scanner := bufio.NewScanner(strings.NewReader("\n\n"))
	err := collectUserPasswords(ac, opts, scanner)
	require.NoError(t, err)
	assert.Empty(t, opts.UserPasswords)
}
