package migratetomck

import (
	"fmt"
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
		ReplicaSetNameOverride: "custom-name",
		CredentialsSecretName:  "my-credentials",
		ConfigMapName:          "my-om-config",
		CertsSecretPrefix:      "mdb",
	})

	obj, _, err := GenerateMongoDBCR(ac, opts)
	require.NoError(t, err)
	yamlOutput, err := marshalCRToYAML(obj)
	require.NoError(t, err)

	assert.Contains(t, yamlOutput, "name: custom-name")
	assert.Contains(t, yamlOutput, "replicaSetNameOverride: my-rs")
}

func TestGenerateMongoDBCR_MultiCluster_CustomResourceName(t *testing.T) {
	members := make([]any, 5)
	processes := make([]any, 5)
	for i := 0; i < 5; i++ {
		name := fmt.Sprintf("geo-rs-%d", i)
		members[i] = map[string]any{"_id": i, "host": name, "priority": 1, "votes": 1}
		processes[i] = map[string]any{
			"name":                        name,
			"hostname":                    fmt.Sprintf("mongo-%d.example.com", i),
			"version":                     "8.0.4-ent",
			"featureCompatibilityVersion": "8.0",
			"processType":                 "mongod",
			"args2_6": map[string]any{
				"net":         map[string]any{"port": 27017},
				"replication": map[string]any{"replSetName": "geo-rs"},
			},
		}
	}
	ac := om.NewAutomationConfig(om.Deployment{
		"processes":   processes,
		"replicaSets": []any{map[string]any{"_id": "geo-rs", "members": members}},
		"sharding":    []any{},
	})

	opts := withDeploymentData(ac, GenerateOptions{
		ReplicaSetNameOverride: "custom-mc-name",
		CredentialsSecretName:  "mc-credentials",
		ConfigMapName:          "mc-om-config",
		MultiClusterNames:      []string{"east1", "west1"},
	})

	obj, resourceName, err := GenerateMongoDBCR(ac, opts)
	require.NoError(t, err)
	assert.Equal(t, "custom-mc-name", resourceName)
	yamlOutput, err := marshalCRToYAML(obj)
	require.NoError(t, err)
	assert.Contains(t, yamlOutput, "name: custom-mc-name")
	assert.Contains(t, yamlOutput, "replicaSetNameOverride: geo-rs")
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
