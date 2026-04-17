package migratetomck

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mongodb/mongodb-kubernetes/controllers/om"
)

// TestParseUsersSecretsFile_EmptyPath returns nil without error.
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

// TestGenerateUserCRs_ExistingSecrets verifies that Option 2 references the provided secret
// and emits no Secret YAML.
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

// TestGenerateUserCRs_ExistingSecrets_SkipsUnmappedUsers verifies that SCRAM users absent from
// ExistingUserSecrets are silently skipped (not an error).
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

	// Only alice is in the mapping; bob should be skipped.
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

func TestBuildOptions_FlagTranslation(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{
		"processes":   []any{},
		"replicaSets": []any{},
	})

	f := cliFlags{
		configMapName:        "my-cm",
		secretName:           "my-secret",
		namespace:            "mongodb",
		resourceNameOverride: "my-rs",
	}
	opts, err := buildOptions(context.Background(), nil, ac, &ProjectConfigs{}, nil, strings.NewReader(""), f)
	require.NoError(t, err)
	assert.Equal(t, "my-cm", opts.ConfigMapName)
	assert.Equal(t, "my-secret", opts.CredentialsSecretName)
	assert.Equal(t, "mongodb", opts.Namespace)
	assert.Equal(t, "my-rs", opts.ResourceNameOverride)
}

func TestBuildOptions_InvalidMultiClusterNames(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{
		"processes":   []any{},
		"replicaSets": []any{},
	})

	f := cliFlags{multiClusterNames: "  ,  ,  "}
	_, err := buildOptions(context.Background(), nil, ac, &ProjectConfigs{}, nil, strings.NewReader(""), f)
	assert.ErrorContains(t, err, "no valid cluster names")
}

func TestCollectPrometheusCreds_NoPrometheus(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{
		"processes":   []any{},
		"replicaSets": []any{},
	})
	opts := &GenerateOptions{Namespace: "mongodb"}
	err := collectPrometheusCreds(context.Background(), nil, ac, opts, nil, "")
	require.NoError(t, err)
	assert.Empty(t, opts.PrometheusPassword)
	assert.Empty(t, opts.PrometheusSecretName)
}

func TestCollectPrometheusCreds_InteractivePassword(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{
		"processes":   []any{},
		"replicaSets": []any{},
		"prometheus":  map[string]any{"enabled": true, "username": "prom-user"},
	})
	opts := &GenerateOptions{Namespace: "mongodb"}
	scanner := bufio.NewScanner(strings.NewReader("supersecret\n"))
	err := collectPrometheusCreds(context.Background(), nil, ac, opts, scanner, "")
	require.NoError(t, err)
	assert.Equal(t, "supersecret", opts.PrometheusPassword)
}

func TestCollectPrometheusCreds_EmptyPassword(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{
		"processes":   []any{},
		"replicaSets": []any{},
		"prometheus":  map[string]any{"enabled": true, "username": "prom-user"},
	})
	opts := &GenerateOptions{Namespace: "mongodb"}
	scanner := bufio.NewScanner(strings.NewReader("\n"))
	err := collectPrometheusCreds(context.Background(), nil, ac, opts, scanner, "")
	assert.ErrorContains(t, err, "cannot be empty")
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

	// both users press Enter to skip
	opts := &GenerateOptions{}
	scanner := bufio.NewScanner(strings.NewReader("\n\n"))
	err := collectUserPasswords(ac, opts, scanner)
	require.NoError(t, err)
	assert.Empty(t, opts.UserPasswords)
}

func writeTempFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secrets.csv")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func TestIsTLSEnabled_TLSMode(t *testing.T) {
	tests := []struct {
		name    string
		mode    string
		enabled bool
	}{
		{"requireSSL", "requireSSL", true},
		{"requireTLS", "requireTLS", true},
		{"preferSSL", "preferSSL", true},
		{"preferTLS", "preferTLS", true},
		{"allowSSL", "allowSSL", true},
		{"allowTLS", "allowTLS", true},
		{"disabled", "disabled", false},
		{"empty defaults to require", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			processMap := map[string]om.Process{
				"host-0": {
					"args2_6": map[string]interface{}{
						"net": map[string]interface{}{
							"tls": map[string]interface{}{
								"mode": tt.mode,
							},
						},
					},
				},
			}
			assert.Equal(t, tt.enabled, isTLSEnabled(processMap))
		})
	}
}

func TestIsTLSEnabled_SSLMode(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {
			"args2_6": map[string]interface{}{
				"net": map[string]interface{}{
					"ssl": map[string]interface{}{
						"mode": "requireSSL",
					},
				},
			},
		},
	}
	assert.True(t, isTLSEnabled(processMap))
}

func TestIsTLSEnabled_NoArgs(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {},
	}
	assert.False(t, isTLSEnabled(processMap))
}

func TestIsTLSEnabled_NoNet(t *testing.T) {
	processMap := map[string]om.Process{
		"host-0": {
			"args2_6": map[string]interface{}{},
		},
	}
	assert.False(t, isTLSEnabled(processMap))
}

func TestFetchAndValidate_ValidDeployment(t *testing.T) {
	d := om.Deployment(map[string]any{
		"processes": []any{
			map[string]any{
				"name":        "host-0",
				"processType": "mongod",
				"hostname":    "host-0.example.com",
				"args2_6": map[string]any{
					"net":         map[string]any{"port": 27017},
					"replication": map[string]any{"replSetName": "my-rs"},
					"systemLog": map[string]any{
						"destination": "file",
						"path":        "/var/log/mongodb-mms-automation/mongodb.log",
					},
				},
				"authSchemaVersion": om.CalculateAuthSchemaVersion(),
			},
		},
		"replicaSets": []any{
			map[string]any{
				"_id": "my-rs",
				"members": []any{
					map[string]any{"_id": 0, "host": "host-0", "votes": 1, "priority": 1},
				},
			},
		},
		"sharding": []any{},
	})
	conn := om.NewMockedOmConnection(d)
	ac, projectConfigs, sourceProcess, err := fetchAndValidate(conn)
	require.NoError(t, err)
	require.NotNil(t, ac)
	require.NotNil(t, projectConfigs)
	require.NotNil(t, sourceProcess)
	assert.Equal(t, "host-0", sourceProcess.Name())
}

func TestFetchAndValidate_ValidationError(t *testing.T) {
	d := om.Deployment(map[string]any{
		"processes":   []any{},
		"replicaSets": []any{},
		"sharding":    []any{},
	})
	conn := om.NewMockedOmConnection(d)
	_, _, _, err := fetchAndValidate(conn)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "validation failed")
}

func TestPrintValidationResults_CountsErrors(t *testing.T) {
	results := []ValidationResult{
		{Severity: SeverityWarning, Message: "some warning"},
		{Severity: SeverityError, Message: "first error"},
		{Severity: SeverityError, Message: "second error"},
		{Severity: SeverityWarning, Message: "another warning"},
	}
	var buf bytes.Buffer
	count := printValidationResults(&buf, results)
	assert.Equal(t, 2, count)
	assert.Contains(t, buf.String(), "[WARNING] some warning")
	assert.Contains(t, buf.String(), "[ERROR] first error")
	assert.Contains(t, buf.String(), "[ERROR] second error")
}

func TestPrintValidationResults_NoResults(t *testing.T) {
	var buf bytes.Buffer
	count := printValidationResults(&buf, nil)
	assert.Equal(t, 0, count)
	assert.Empty(t, buf.String())
}
