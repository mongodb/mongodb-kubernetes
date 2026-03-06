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
	assert.Contains(t, yamlOutput, "cacheSizeGB")

	// externalMembers should be a real spec field with process IDs
	assert.Contains(t, yamlOutput, "externalMembers:")
	assert.Contains(t, yamlOutput, "- my-rs-0")
	assert.Contains(t, yamlOutput, "- my-rs-1")
	assert.Contains(t, yamlOutput, "- my-rs-2")

	// internal cluster auth from args2_6.security.clusterAuthMode
	assert.Contains(t, yamlOutput, "internalCluster: X509")

	// additionalMongodConfig should include setParameter, oplogSizeMB (but not dbPath or systemLog)
	assert.Contains(t, yamlOutput, "setParameter")
	assert.Contains(t, yamlOutput, "authenticationMechanisms")
	assert.Contains(t, yamlOutput, "oplogSizeMB")
	assert.NotContains(t, yamlOutput, "dbPath", "dbPath should not be in additionalMongodConfig; operator always overwrites it")

	// systemLog should be in agent.mongod.systemLog, not additionalMongodConfig
	assert.Contains(t, yamlOutput, "path: /var/log/mongodb/mongod.log")

	// member tags should be carried over
	assert.Contains(t, yamlOutput, "region: us-east-1")
	assert.Contains(t, yamlOutput, "use: analytics")
	assert.Contains(t, yamlOutput, "region: us-west-2")

	// custom MongoDB roles
	assert.Contains(t, yamlOutput, "role: appReadOnly")
	assert.Contains(t, yamlOutput, "db: myapp")
	assert.Contains(t, yamlOutput, "find")

	// logRotate should be extracted into agent config
	assert.Contains(t, yamlOutput, "sizeThresholdMB")
	assert.Contains(t, yamlOutput, "timeThresholdHrs")

	// resource name matches RS name, so no override needed
	assert.NotContains(t, yamlOutput, "replicaSetNameOverride")

	// status should not appear in generated YAML
	assert.NotContains(t, yamlOutput, "status")

	// horizons are NOT extracted (operator overwrites them on K8s members)
	assert.NotContains(t, yamlOutput, "replicaSetHorizons")

	// LDAP from fixture
	assert.Contains(t, yamlOutput, "bindQueryUser: cn=admin,dc=example,dc=com")
	assert.Contains(t, yamlOutput, "ldap-bind-query-password")
	assert.Contains(t, yamlOutput, "ldap-ca")
	assert.Contains(t, yamlOutput, "ldap1.example.com:636")
	assert.Contains(t, yamlOutput, "ldap2.example.com:636")
	assert.Contains(t, yamlOutput, "transportSecurity: tls")

	// OIDC from fixture
	assert.Contains(t, yamlOutput, "configurationName: okta")
	assert.Contains(t, yamlOutput, "issuerURI: https://dev-123456.okta.com/oauth2/default")
	assert.Contains(t, yamlOutput, "audience: api://mongodb-cluster")

	// auth modes should include LDAP and X509 from deploymentAuthMechanisms
	assert.Contains(t, yamlOutput, "- LDAP")
	assert.Contains(t, yamlOutput, "- X509")
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
	assert.Contains(t, yamlOutput, "replicaSetNameOverride: my-rs")
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
	assert.Contains(t, err.Error(), "no replica sets found")
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
	assert.Contains(t, yamlOutput, "- shard-rs-0")
	assert.Contains(t, yamlOutput, "- shard-rs-1")
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
	require.Len(t, users, 3, "expected 3 users (automation agent should be skipped)")

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

	ldapUser := users[2]
	assert.Equal(t, "ldap-reader", ldapUser.Username)
	assert.Equal(t, "$external", ldapUser.Database)
	assert.False(t, ldapUser.NeedsPassword)
	assert.Contains(t, ldapUser.YAML, "kind: MongoDBUser")
	assert.Contains(t, ldapUser.YAML, "db: $external")
}

func TestGenerateUserCRs_SkipsAutomationAgent(t *testing.T) {
	ac := loadTestAutomationConfig(t, "replicaset_automation_config.json")

	users, err := GenerateUserCRs(ac, "my-rs")
	require.NoError(t, err)

	for _, u := range users {
		assert.NotEqual(t, "mms-automation-agent", u.Username)
	}
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
