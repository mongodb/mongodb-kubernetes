package migrate

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mongodb/mongodb-kubernetes/controllers/om"
)

func loadTestAutomationConfig(t *testing.T, filename string) *om.AutomationConfig {
	t.Helper()
	data, err := os.ReadFile("testdata/" + filename)
	require.NoError(t, err)
	ac, err := om.BuildAutomationConfigFromBytes(data)
	require.NoError(t, err)
	return ac
}

func TestGenerateMongoDBCR_BasicReplicaSet(t *testing.T) {
	ac := loadTestAutomationConfig(t, "replicaset_automation_config.json")

	opts := GenerateOptions{
		CredentialsSecretName: "my-credentials",
		ConfigMapName:         "my-om-config",
	}

	yamlOutput, _, err := GenerateMongoDBCR(ac, opts)
	require.NoError(t, err)

	assert.Contains(t, yamlOutput, "apiVersion: mongodb.com/v1")
	assert.Contains(t, yamlOutput, "kind: MongoDB")
	assert.Contains(t, yamlOutput, "name: my-rs")
	assert.Contains(t, yamlOutput, "type: ReplicaSet")
	assert.Contains(t, yamlOutput, `version: 7.0.12-ent`)
	assert.Contains(t, yamlOutput, "featureCompatibilityVersion: \"7.0\"")
	assert.Contains(t, yamlOutput, "members: 3")
	assert.Contains(t, yamlOutput, "credentials: my-credentials")
	assert.Contains(t, yamlOutput, "votes: 0")
	assert.Contains(t, yamlOutput, `priority: "0"`)
	assert.Contains(t, yamlOutput, "enabled: true")
	assert.Contains(t, yamlOutput, "SCRAM-SHA-256")
	assert.Contains(t, yamlOutput, "# externalMembers will be populated by the operator")
	assert.Contains(t, yamlOutput, "vm-mongo-0.prod.example.com")
	assert.Contains(t, yamlOutput, "vm-mongo-1.prod.example.com")
	assert.Contains(t, yamlOutput, "vm-mongo-2.prod.example.com")
	assert.Contains(t, yamlOutput, "#     votes: 1")
	assert.Contains(t, yamlOutput, "#     priority: 2")
	assert.Contains(t, yamlOutput, "cacheSizeGB")
}

func TestGenerateMongoDBCR_CustomResourceName(t *testing.T) {
	ac := loadTestAutomationConfig(t, "replicaset_automation_config.json")

	opts := GenerateOptions{
		ResourceName:          "custom-name",
		CredentialsSecretName: "my-credentials",
		ConfigMapName:         "my-om-config",
	}

	yamlOutput, _, err := GenerateMongoDBCR(ac, opts)
	require.NoError(t, err)

	assert.Contains(t, yamlOutput, "name: custom-name")
}

func TestGenerateMongoDBCR_NoReplicaSet(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{
		"processes":   []interface{}{},
		"replicaSets": []interface{}{},
	})

	opts := GenerateOptions{
		CredentialsSecretName: "my-credentials",
		ConfigMapName:         "my-om-config",
	}

	_, _, err := GenerateMongoDBCR(ac, opts)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no replica set found")
}

func TestGenerateMongoDBCR_StandaloneFromProcesses(t *testing.T) {
	ac := loadTestAutomationConfig(t, "standalone_automation_config.json")

	opts := GenerateOptions{
		CredentialsSecretName: "my-credentials",
		ConfigMapName:         "my-om-config",
	}

	yamlOutput, _, err := GenerateMongoDBCR(ac, opts)
	require.NoError(t, err)

	assert.Contains(t, yamlOutput, "name: standalone")
	assert.Contains(t, yamlOutput, "members: 1")
	assert.Contains(t, yamlOutput, "type: ReplicaSet")
	assert.Contains(t, yamlOutput, "standalone-0.example.com")
}

func TestGenerateMongoDBCR_ShardedTopology(t *testing.T) {
	ac := loadTestAutomationConfig(t, "sharded_with_rs_automation_config.json")

	opts := GenerateOptions{
		CredentialsSecretName: "my-credentials",
		ConfigMapName:         "my-om-config",
	}

	yamlOutput, _, err := GenerateMongoDBCR(ac, opts)
	require.NoError(t, err)

	assert.Contains(t, yamlOutput, "name: shard-rs")
	assert.Contains(t, yamlOutput, "members: 2")
	assert.Contains(t, yamlOutput, "shard-a.example.com")
	assert.Contains(t, yamlOutput, "shard-b.example.com")
}

func TestGenerateMongoDBCR_ConfigMapRefName(t *testing.T) {
	ac := loadTestAutomationConfig(t, "replicaset_automation_config.json")

	opts := GenerateOptions{
		CredentialsSecretName: "my-credentials",
		ConfigMapName:         "my-om-project",
	}

	yamlOutput, _, err := GenerateMongoDBCR(ac, opts)
	require.NoError(t, err)

	assert.Contains(t, yamlOutput, "name: my-om-project")
}

func TestGenerateMongoDBCR_MemberConfigZeroVotes(t *testing.T) {
	ac := loadTestAutomationConfig(t, "replicaset_automation_config.json")

	opts := GenerateOptions{
		CredentialsSecretName: "my-credentials",
		ConfigMapName:         "my-om-config",
	}

	yamlOutput, _, err := GenerateMongoDBCR(ac, opts)
	require.NoError(t, err)

	memberConfigCount := strings.Count(yamlOutput, `votes: 0`)
	assert.Equal(t, 3, memberConfigCount, "Expected 3 memberConfig entries with votes: 0")
}

func TestGenerateUserCRs_ScramAndExternal(t *testing.T) {
	ac := loadTestAutomationConfig(t, "replicaset_automation_config.json")

	users, err := GenerateUserCRs(ac, "my-rs")
	require.NoError(t, err)
	require.Len(t, users, 2, "expected 2 users (automation agent should be skipped)")

	scramUser := users[0]
	assert.Equal(t, "app-user", scramUser.Username)
	assert.Equal(t, "admin", scramUser.Database)
	assert.True(t, scramUser.NeedsPassword)
	assert.Contains(t, scramUser.YAML, "kind: MongoDBUser")
	assert.Contains(t, scramUser.YAML, "username: app-user")
	assert.Contains(t, scramUser.YAML, "db: admin")
	assert.Contains(t, scramUser.YAML, "name: my-rs")
	assert.Contains(t, scramUser.YAML, "passwordSecretKeyRef")
	assert.Contains(t, scramUser.YAML, "app-user-password")
	assert.Contains(t, scramUser.YAML, "name: readWrite")
	assert.Contains(t, scramUser.YAML, "name: read")

	x509User := users[1]
	assert.Equal(t, "CN=x509-client,O=MongoDB", x509User.Username)
	assert.Equal(t, "$external", x509User.Database)
	assert.False(t, x509User.NeedsPassword)
	assert.Contains(t, x509User.YAML, "kind: MongoDBUser")
	assert.Contains(t, x509User.YAML, "db: $external")
	assert.NotContains(t, x509User.YAML, "app-user-password")
}

func TestGenerateUserCRs_SkipsAutomationAgent(t *testing.T) {
	ac := loadTestAutomationConfig(t, "replicaset_automation_config.json")

	users, err := GenerateUserCRs(ac, "my-rs")
	require.NoError(t, err)

	for _, u := range users {
		assert.NotEqual(t, "mms-automation", u.Username)
	}
}

func TestGenerateUserCRs_NoUsers(t *testing.T) {
	ac := loadTestAutomationConfig(t, "standalone_automation_config.json")

	users, err := GenerateUserCRs(ac, "standalone")
	require.NoError(t, err)
	assert.Empty(t, users)
}

func TestNormalizeK8sName(t *testing.T) {
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
			assert.Equal(t, tt.expected, normalizeK8sName(tt.input))
		})
	}
}
