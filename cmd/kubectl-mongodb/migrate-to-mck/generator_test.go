package migratetomck

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"

	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/ldap"
)

func secretsByName(objs []client.Object) map[string]string {
	out := map[string]string{}
	for _, o := range objs {
		if s, ok := o.(*corev1.Secret); ok {
			out[s.Name] = s.StringData[passwordSecretDataKey]
		}
	}
	return out
}

// TestGenerateExtraResources_LDAPAgentPassword verifies that an LDAP agent's external password is
// carried over as a generated Secret alongside the bind-query password.
func TestGenerateExtraResources_LDAPAgentPassword(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{"processes": []any{}, "replicaSets": []any{}})
	ac.Ldap = &ldap.Ldap{Servers: "openldap:389", BindQueryUser: "cn=admin,dc=example,dc=org", BindQueryPassword: "bindpw"}
	ac.Auth.AutoAuthMechanism = "PLAIN"
	ac.Auth.AutoPwd = "agent-ldap-pw"

	got := secretsByName(generateExtraResources(ac, GenerateOptions{Namespace: "mongodb"}))
	assert.Equal(t, "bindpw", got[LdapBindQuerySecretName])
	assert.Equal(t, "agent-ldap-pw", got[LdapAgentPasswordSecretName])
}

// TestGenerateExtraResources_ScramAgentNoLDAPPassword verifies a SCRAM agent does not get an LDAP
// agent-password Secret (only LDAP agents authenticate with an external password).
func TestGenerateExtraResources_ScramAgentNoLDAPPassword(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{"processes": []any{}, "replicaSets": []any{}})
	ac.Auth.AutoAuthMechanism = "SCRAM-SHA-256"
	ac.Auth.AutoPwd = "scram-agent-pw"

	got := secretsByName(generateExtraResources(ac, GenerateOptions{Namespace: "mongodb"}))
	_, exists := got[LdapAgentPasswordSecretName]
	assert.False(t, exists, "SCRAM agent should not produce an LDAP agent-password secret")
}

// withDeploymentData mirrors what runGenerate does before calling generateAll.
func withDeploymentData(ac *om.AutomationConfig, opts GenerateOptions) GenerateOptions {
	if rss := ac.Deployment.GetReplicaSets(); len(rss) > 0 {
		members := rss[0].Members()
		processMap := ac.Deployment.ProcessMap()
		opts.SourceProcess, _ = pickSourceProcess(members, processMap)
	}
	return opts
}

func TestGenerateMongoDBCR_CustomResourceName(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{
		"processes": []any{
			map[string]any{
				"name":                        "my-rs-0",
				"hostname":                    "vm-0.example.com",
				"version":                     "8.0.4-ent",
				"featureCompatibilityVersion": "8.0",
				"processType":                 string(om.ProcessTypeMongod),
				"args2_6": map[string]any{
					"net":         map[string]any{"port": 27017},
					"replication": map[string]any{"replSetName": "my-rs"},
				},
			},
		},
		"replicaSets": []any{
			map[string]any{
				"_id":     "my-rs",
				"members": []any{map[string]any{"_id": 0, "host": "my-rs-0", "priority": 1, "votes": 1}},
			},
		},
		"sharding": []any{},
	})

	opts := withDeploymentData(ac, GenerateOptions{
		ResourceNameOverride:  "custom-name",
		CredentialsSecretName: "my-credentials",
		ConfigMapName:         "my-om-config",
		CertsSecretPrefix:     "mdb",
	})

	obj, _, err := GenerateMongoDBCR(ac, opts)
	require.NoError(t, err)
	yamlOutput, err := marshalCRToYAML(obj)
	require.NoError(t, err)

	assert.Contains(t, yamlOutput, "name: custom-name")
	assert.Contains(t, yamlOutput, "replicaSetNameOverride: my-rs")
}

func TestGenerateMongoDBCR_AutoNormalizesRSName(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{
		"processes": []any{
			map[string]any{
				"name":                        "My_RS-0",
				"hostname":                    "vm-0.example.com",
				"version":                     "8.0.4-ent",
				"featureCompatibilityVersion": "8.0",
				"processType":                 string(om.ProcessTypeMongod),
				"args2_6": map[string]any{
					"net":         map[string]any{"port": 27017},
					"replication": map[string]any{"replSetName": "My_ReplicaSet"},
				},
			},
		},
		"replicaSets": []any{
			map[string]any{
				"_id":     "My_ReplicaSet",
				"members": []any{map[string]any{"_id": 0, "host": "My_RS-0", "priority": 1, "votes": 1}},
			},
		},
		"sharding": []any{},
	})

	opts := withDeploymentData(ac, GenerateOptions{
		CredentialsSecretName: "my-credentials",
		ConfigMapName:         "my-om-config",
	})

	obj, resourceName, err := GenerateMongoDBCR(ac, opts)
	require.NoError(t, err)
	assert.Equal(t, "my-replicaset", resourceName)
	yamlOutput, err := marshalCRToYAML(obj)
	require.NoError(t, err)
	assert.Contains(t, yamlOutput, "name: my-replicaset")
	assert.Contains(t, yamlOutput, "replicaSetNameOverride: My_ReplicaSet")
}

func TestGenerateMongoDBCR_NoReplicaSet(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{
		"processes":   []any{},
		"replicaSets": []any{},
		"sharding":    []any{},
	})

	opts := GenerateOptions{
		CredentialsSecretName: "my-credentials",
		ConfigMapName:         "my-om-config",
	}

	_, _, err := GenerateMongoDBCR(ac, opts)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no replica sets found")
}

func TestGenerateMongoDBCR_ShardedTopologyCounts(t *testing.T) {
	ac := loadTestAutomationConfig(t, "singlecluster/shardedcluster/default_config_rs/default_config_rs_input.json")

	opts := withDeploymentData(ac, GenerateOptions{
		CredentialsSecretName: "my-credentials",
		ConfigMapName:         "my-om-config",
	})

	obj, _, err := GenerateMongoDBCR(ac, opts)
	require.NoError(t, err)
	yamlOutput, err := marshalCRToYAML(obj)
	require.NoError(t, err)

	assert.Contains(t, yamlOutput, "type: ShardedCluster")
	assert.Contains(t, yamlOutput, "shardCount: 2")
	assert.Contains(t, yamlOutput, "mongodsPerShardCount: 2")
	assert.Contains(t, yamlOutput, "configServerCount: 2")
	assert.Contains(t, yamlOutput, "mongosCount: 2")
}

func TestGenerateMongoDBCR_ShardedMissingShardReplicaSet(t *testing.T) {
	ac := loadTestAutomationConfig(t, "singlecluster/shardedcluster/default_config_rs/default_config_rs_input.json")

	rss := ac.Deployment.GetReplicaSets()
	kept := make([]any, 0, len(rss))
	for _, rs := range rss {
		if rs.Name() != "shard0" {
			kept = append(kept, map[string]any(rs))
		}
	}
	ac.Deployment["replicaSets"] = kept

	opts := withDeploymentData(ac, GenerateOptions{
		CredentialsSecretName: "my-credentials",
		ConfigMapName:         "my-om-config",
	})

	_, _, err := GenerateMongoDBCR(ac, opts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "shard0")
}
