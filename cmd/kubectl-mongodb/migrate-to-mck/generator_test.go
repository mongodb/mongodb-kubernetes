package migratetomck

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
				"processType":                 "mongod",
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

func TestGenerateMongoDBCR_MultiCluster_NotSupported(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{
		"processes": []any{
			map[string]any{
				"name": "geo-rs-0", "hostname": "mongo-0.example.com",
				"version": "8.0.4-ent", "featureCompatibilityVersion": "8.0",
				"processType": "mongod",
				"args2_6": map[string]any{
					"net":         map[string]any{"port": 27017},
					"replication": map[string]any{"replSetName": "geo-rs"},
				},
			},
		},
		"replicaSets": []any{map[string]any{
			"_id":     "geo-rs",
			"members": []any{map[string]any{"_id": 0, "host": "geo-rs-0", "priority": 1, "votes": 1}},
		}},
		"sharding": []any{},
	})

	opts := withDeploymentData(ac, GenerateOptions{
		ResourceNameOverride:  "custom-mc-name",
		CredentialsSecretName: "mc-credentials",
		ConfigMapName:         "mc-om-config",
		MultiClusterNames:     []string{"east1", "west1"},
	})

	_, _, err := GenerateMongoDBCR(ac, opts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not yet supported")
}

func TestGenerateMongoDBCR_AutoNormalizesRSName(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{
		"processes": []any{
			map[string]any{
				"name":                        "My_RS-0",
				"hostname":                    "vm-0.example.com",
				"version":                     "8.0.4-ent",
				"featureCompatibilityVersion": "8.0",
				"processType":                 "mongod",
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
