package migratetomck

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	userv1 "github.com/mongodb/mongodb-kubernetes/api/v1/user"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/automationconfig"
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

// TestGenerateUserCRs_EmptyMechanisms verifies users with empty mechanisms are not flagged as VM-migrated.
func TestGenerateUserCRs_EmptyMechanisms(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{
		"processes":   []any{},
		"replicaSets": []any{},
	})
	ac.Auth.AutoUser = "mms-automation"
	ac.Auth.Users = []*om.MongoDBUser{
		{Username: "app-user", Database: "admin", Mechanisms: []string{}, Roles: []*om.Role{{Role: "readWrite", Database: "myapp"}}},
	}

	// Option 2: reference an existing secret so the empty Mechanisms slice doesn't gate generation.
	users, err := GenerateUserCRs(ac, "scram-rs", "mongodb", GenerateOptions{
		ExistingUserSecrets: map[string]string{
			"app-user:admin": "app-user-secret",
		},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, users)
}

func TestGenerateUserCRs_DuplicateNormalizedNames(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{
		"processes":   []any{},
		"replicaSets": []any{},
	})
	ac.Auth.AutoUser = "mms-automation"
	ac.Auth.Users = []*om.MongoDBUser{
		{Username: "App_User", Database: "admin", Roles: []*om.Role{{Role: "read", Database: "test"}}},
		{Username: "app-user", Database: "admin", Roles: []*om.Role{{Role: "read", Database: "test"}}},
	}

	// Use Option 2 so both users are processed past the password step; the duplicate check fires on
	// the second user when it tries to register the same normalised CR name "app-user".
	opts := GenerateOptions{
		ExistingUserSecrets: map[string]string{
			"App_User:admin": "app-user-secret",
			"app-user:admin": "app-user2-secret",
		},
	}
	_, err := GenerateUserCRs(ac, "my-rs", "mongodb", opts)
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
			result := distributeMembers(tt.memberCount, tt.clusters)
			require.Len(t, result, len(tt.clusters))
			for i, item := range result {
				assert.Equal(t, tt.clusters[i], item.ClusterName)
				assert.Equal(t, tt.expected[i], item.Members, "cluster %s member count", tt.clusters[i])
			}
		})
	}
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

func TestDistributeMembers_EmptyClusterNames(t *testing.T) {
	result := distributeMembers(3, nil)
	assert.Nil(t, result)

	result = distributeMembers(3, []string{})
	assert.Nil(t, result)
}

func TestGenerateUserCRs_DuplicateNormalizedNames(t *testing.T) {
	ac := loadTestAutomationConfig(t, "singlecluster/replicaset/scram_tls_ldap_prometheus/scram_tls_ldap_prometheus_input.json")
	ac.Auth.Users = append(ac.Auth.Users,
		&om.MongoDBUser{Username: "App_User", Database: "admin", Roles: []*om.Role{{Role: "read", Database: "test"}}},
		&om.MongoDBUser{Username: "app.user", Database: "admin", Roles: []*om.Role{{Role: "read", Database: "test"}}},
	)

	_, err := GenerateUserCRs(ac, "my-rs")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "normalize to the same Kubernetes name")
}

func multiClusterTestConfigs() (*ProjectAgentConfigs, *ProjectProcessConfigs) {
	return nil, &ProjectProcessConfigs{
		SystemLogRotate: &automationconfig.AcLogRotate{
			LogRotate: automationconfig.LogRotate{
				TimeThresholdHrs: 1,
				NumUncompressed:  2,
				NumTotal:         10,
			},
			SizeThresholdMB:    100,
			PercentOfDiskspace: 0.4,
		},
		AuditLogRotate: &automationconfig.AcLogRotate{
			LogRotate: automationconfig.LogRotate{
				TimeThresholdHrs: 1,
				NumUncompressed:  2,
				NumTotal:         10,
			},
			SizeThresholdMB:    100,
			PercentOfDiskspace: 0.4,
		},
	}
}

func fullTestConfigs() (*ProjectAgentConfigs, *ProjectProcessConfigs) {
	return &ProjectAgentConfigs{
			MonitoringConfig: &om.MonitoringAgentConfig{
				MonitoringAgentTemplate: &om.MonitoringAgentTemplate{},
				BackingMap: map[string]interface{}{
					"logRotate": map[string]interface{}{
						"sizeThresholdMB":  500.0,
						"timeThresholdHrs": 12,
					},
				},
			},
		}, &ProjectProcessConfigs{
			SystemLogRotate: &automationconfig.AcLogRotate{
				LogRotate: automationconfig.LogRotate{
					TimeThresholdHrs:                24,
					NumUncompressed:                 5,
					NumTotal:                        10,
					IncludeAuditLogsWithMongoDBLogs: true,
				},
				SizeThresholdMB:    1000,
				PercentOfDiskspace: 0.4,
			},
			AuditLogRotate: &automationconfig.AcLogRotate{
				LogRotate: automationconfig.LogRotate{
					TimeThresholdHrs:                48,
					NumUncompressed:                 2,
					NumTotal:                        10,
					IncludeAuditLogsWithMongoDBLogs: true,
				},
				SizeThresholdMB:    500,
				PercentOfDiskspace: 0.4,
			},
		}
}
