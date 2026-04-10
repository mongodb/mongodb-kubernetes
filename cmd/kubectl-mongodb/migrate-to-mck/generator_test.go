package migratetomck

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	userv1 "github.com/mongodb/mongodb-kubernetes/api/v1/user"
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
		"processes": []interface{}{
			map[string]interface{}{
				"name":                        "my-rs-0",
				"hostname":                    "vm-0.example.com",
				"version":                     "8.0.4-ent",
				"featureCompatibilityVersion": "8.0",
				"processType":                 "mongod",
				"args2_6": map[string]interface{}{
					"net":         map[string]interface{}{"port": 27017},
					"replication": map[string]interface{}{"replSetName": "my-rs"},
				},
			},
		},
		"replicaSets": []interface{}{
			map[string]interface{}{
				"_id":     "my-rs",
				"members": []interface{}{map[string]interface{}{"_id": 0, "host": "my-rs-0", "priority": 1, "votes": 1}},
			},
		},
		"sharding": []interface{}{},
	})

	opts := withDeploymentData(ac, GenerateOptions{
		ReplicaSetNameOverride: "custom-name",
		CredentialsSecretName:  "my-credentials",
		ConfigMapName:          "my-om-config",
		CertsSecretPrefix:      "mdb",
	})

	yamlOutput, _, err := GenerateMongoDBCR(ac, opts)
	require.NoError(t, err)

	assert.Contains(t, yamlOutput, "name: custom-name")
	assert.Contains(t, yamlOutput, "replicaSetNameOverride: my-rs")
}

func TestGenerateMongoDBCR_MultiCluster_CustomResourceName(t *testing.T) {
	members := make([]interface{}, 5)
	processes := make([]interface{}, 5)
	for i := 0; i < 5; i++ {
		name := fmt.Sprintf("geo-rs-%d", i)
		members[i] = map[string]interface{}{"_id": i, "host": name, "priority": 1, "votes": 1}
		processes[i] = map[string]interface{}{
			"name":                        name,
			"hostname":                    fmt.Sprintf("mongo-%d.example.com", i),
			"version":                     "8.0.4-ent",
			"featureCompatibilityVersion": "8.0",
			"processType":                 "mongod",
			"args2_6": map[string]interface{}{
				"net":         map[string]interface{}{"port": 27017},
				"replication": map[string]interface{}{"replSetName": "geo-rs"},
			},
		}
	}
	ac := om.NewAutomationConfig(om.Deployment{
		"processes":   processes,
		"replicaSets": []interface{}{map[string]interface{}{"_id": "geo-rs", "members": members}},
		"sharding":    []interface{}{},
	})

	opts := withDeploymentData(ac, GenerateOptions{
		ReplicaSetNameOverride: "custom-mc-name",
		CredentialsSecretName:  "mc-credentials",
		ConfigMapName:          "mc-om-config",
		MultiClusterNames:      []string{"east1", "west1"},
	})

	yamlOutput, resourceName, err := GenerateMongoDBCR(ac, opts)
	require.NoError(t, err)
	assert.Equal(t, "custom-mc-name", resourceName)
	assert.Contains(t, yamlOutput, "name: custom-mc-name")
	assert.Contains(t, yamlOutput, "replicaSetNameOverride: geo-rs")
}

func TestGenerateMongoDBCR_NoReplicaSet(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{
		"processes":   []interface{}{},
		"replicaSets": []interface{}{},
		"sharding":    []interface{}{},
	})

	opts := GenerateOptions{
		CredentialsSecretName: "my-credentials",
		ConfigMapName:         "my-om-config",
	}

	_, _, err := GenerateMongoDBCR(ac, opts)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no replica sets found")
}

// TestGenerateUserCRs_EmptyMechanisms verifies users with empty mechanisms generate successfully.
func TestGenerateUserCRs_EmptyMechanisms(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{
		"processes":   []interface{}{},
		"replicaSets": []interface{}{},
	})
	ac.Auth.AutoUser = "mms-automation"
	ac.Auth.Users = []*om.MongoDBUser{
		{Username: "app-user", Database: "admin", Mechanisms: []string{}, Roles: []*om.Role{{Role: "readWrite", Database: "myapp"}}},
	}

	users, err := GenerateUserCRs(ac, "scram-rs", "mongodb", nil)
	require.NoError(t, err)
	assert.NotEmpty(t, users)
}

func TestGenerateUserCRs_DuplicateNormalizedNames(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{
		"processes":   []interface{}{},
		"replicaSets": []interface{}{},
	})
	ac.Auth.AutoUser = "mms-automation"
	ac.Auth.Users = []*om.MongoDBUser{
		{Username: "App_User", Database: "admin", Roles: []*om.Role{{Role: "read", Database: "test"}}},
		{Username: "app-user", Database: "admin", Roles: []*om.Role{{Role: "read", Database: "test"}}},
	}

	_, err := GenerateUserCRs(ac, "my-rs", "mongodb", nil)
	assert.ErrorContains(t, err, "normalize to the same Kubernetes name")
}

func TestDistributeMembers(t *testing.T) {
	tests := []struct {
		name        string
		memberCount int
		clusters    []string
		expected    []int
	}{
		{
			name:        "even split",
			memberCount: 4,
			clusters:    []string{"a", "b"},
			expected:    []int{2, 2},
		},
		{
			name:        "uneven split remainder to early clusters",
			memberCount: 5,
			clusters:    []string{"a", "b"},
			expected:    []int{3, 2},
		},
		{
			name:        "three clusters even",
			memberCount: 3,
			clusters:    []string{"a", "b", "c"},
			expected:    []int{1, 1, 1},
		},
		{
			name:        "three clusters remainder",
			memberCount: 5,
			clusters:    []string{"a", "b", "c"},
			expected:    []int{2, 2, 1},
		},
		{
			name:        "single cluster",
			memberCount: 3,
			clusters:    []string{"only"},
			expected:    []int{3},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			externalMembers := make([]mdbv1.ExternalMember, tt.memberCount)
			rsMembers := make([]om.ReplicaSetMember, tt.memberCount)
			result, err := distributeMembers(externalMembers, rsMembers, tt.clusters)
			require.NoError(t, err)
			require.Len(t, result, len(tt.clusters))
			for i, item := range result {
				assert.Equal(t, tt.clusters[i], item.ClusterName)
				assert.Equal(t, tt.expected[i], item.Members, "cluster %s member count", tt.clusters[i])
			}
		})
	}
}

func TestDistributeMembers_EmptyClusterNames(t *testing.T) {
	externalMembers := make([]mdbv1.ExternalMember, 3)
	rsMembers := make([]om.ReplicaSetMember, 3)

	result, err := distributeMembers(externalMembers, rsMembers, nil)
	require.NoError(t, err)
	assert.Nil(t, result)

	result, err = distributeMembers(externalMembers, rsMembers, []string{})
	require.NoError(t, err)
	assert.Nil(t, result)
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
