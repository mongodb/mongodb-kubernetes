package migratetomck

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
)

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

func TestGenerateMongoDBCR_ShardedResourceNameOverride(t *testing.T) {
	ac := loadTestAutomationConfig(t, "singlecluster/shardedcluster/split_shard_names/split_shard_names_input.json")

	opts := withDeploymentData(ac, GenerateOptions{
		ResourceNameOverride:  "custom-sc",
		CredentialsSecretName: "my-credentials",
		ConfigMapName:         "my-om-config",
	})

	obj, name, err := GenerateMongoDBCR(ac, opts)
	require.NoError(t, err)
	assert.Equal(t, "custom-sc", name)
	yamlOutput, err := marshalCRToYAML(obj)
	require.NoError(t, err)

	assert.Contains(t, yamlOutput, "name: custom-sc")
	assert.Contains(t, yamlOutput, `shardName: "custom-sc-0"`)
	assert.Contains(t, yamlOutput, `shardName: "custom-sc-1"`)
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

func TestBuildConfigServerNameOverride(t *testing.T) {
	tests := []struct {
		name         string
		resource     string
		configRsName string
		want         string
	}{
		{name: "non-default rs name produces override", resource: "my-sc", configRsName: "csrs", want: "  # configServerNameOverride: \"csrs\"\n"},
		{name: "rs already matches operator default is omitted", resource: "my-sc", configRsName: "my-sc-config", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cr := &mdbv1.MongoDB{ObjectMeta: metav1.ObjectMeta{Name: tt.resource}}
			assert.Equal(t, tt.want, buildConfigServerNameOverride(cr, tt.configRsName))
		})
	}
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
