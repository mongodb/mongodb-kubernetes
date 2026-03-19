package migrate

import (
	"flag"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	userv1 "github.com/mongodb/mongodb-kubernetes/api/v1/user"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/automationconfig"
)

var updateGolden = flag.Bool("update-golden", false, "overwrite golden fixture files with current output")

func loadTestAutomationConfig(t *testing.T, filename string) *om.AutomationConfig {
	t.Helper()
	data, err := os.ReadFile("testdata/input/" + filename)
	require.NoError(t, err)
	ac, err := om.BuildAutomationConfigFromBytes(data)
	require.NoError(t, err)
	return ac
}

// TestFixtureMatch compares generated CR output byte-for-byte against golden
// files. Each entry uses a distinct input JSON and produces a different kind of
// output (single-cluster CR, multi-cluster CR, user CRs).
//
// To regenerate all golden files after an intentional change:
//
//	go test -run TestFixtureMatch -update-golden
func TestFixtureMatch(t *testing.T) {
	tests := []struct {
		name       string
		inputJSON  string
		goldenYAML string
		generate   func(t *testing.T, ac *om.AutomationConfig) string
	}{
		{
			name:       "replicaset single-cluster CR",
			inputJSON:  "singlecluster/replicaset/full.json",
			goldenYAML: "singlecluster/replicaset/mongodb_cr.yaml",
			generate: func(t *testing.T, ac *om.AutomationConfig) string {
				agentCfg, processCfg := fullTestConfigs()
				out, _, err := GenerateMongoDBCR(ac, GenerateOptions{
					CredentialsSecretName: "my-credentials",
					ConfigMapName:         "my-om-config",
					AgentConfigs:          agentCfg,
					ProcessConfigs:        processCfg,
					CertsSecretPrefix:     "mdb",
				})
				require.NoError(t, err)
				return out
			},
		},
		{
			name:       "5-member geo-distributed multi-cluster CR (2 clusters)",
			inputJSON:  "multicluster/replicaset/simple.json",
			goldenYAML: "multicluster/replicaset/2_clusters.yaml",
			generate: func(t *testing.T, ac *om.AutomationConfig) string {
				agentCfg, processCfg := multiClusterTestConfigs()
				out, _, err := GenerateMongoDBCR(ac, GenerateOptions{
					CredentialsSecretName: "mc-credentials",
					ConfigMapName:         "mc-om-config",
					MultiClusterNames:     []string{"east1", "west1"},
					AgentConfigs:          agentCfg,
					ProcessConfigs:        processCfg,
				})
				require.NoError(t, err)
				return out
			},
		},
		{
			name:       "5-member geo-distributed multi-cluster CR (3 clusters)",
			inputJSON:  "multicluster/replicaset/simple.json",
			goldenYAML: "multicluster/replicaset/3_clusters.yaml",
			generate: func(t *testing.T, ac *om.AutomationConfig) string {
				agentCfg, processCfg := multiClusterTestConfigs()
				out, _, err := GenerateMongoDBCR(ac, GenerateOptions{
					CredentialsSecretName: "mc-credentials",
					ConfigMapName:         "mc-om-config",
					MultiClusterNames:     []string{"cluster-a", "cluster-b", "cluster-c"},
					AgentConfigs:          agentCfg,
					ProcessConfigs:        processCfg,
				})
				require.NoError(t, err)
				return out
			},
		},
		{
			name:       "replicaset user CRs",
			inputJSON:  "singlecluster/replicaset/full.json",
			goldenYAML: "singlecluster/replicaset/user_crs.yaml",
			generate: func(t *testing.T, ac *om.AutomationConfig) string {
				users, err := GenerateUserCRs(ac, "my-rs")
				require.NoError(t, err)
				var sb strings.Builder
				for i, u := range users {
					if i > 0 {
						sb.WriteString("---\n")
					}
					sb.WriteString(u.YAML)
				}
				return sb.String()
			},
		},
		{
			name:       "SCRAM user password secrets",
			inputJSON:  "singlecluster/replicaset/full.json",
			goldenYAML: "singlecluster/replicaset/password_secrets.yaml",
			generate: func(t *testing.T, ac *om.AutomationConfig) string {
				users, err := GenerateUserCRs(ac, "my-rs")
				require.NoError(t, err)
				var sb strings.Builder
				first := true
				for _, u := range users {
					if !u.NeedsPassword {
						continue
					}
					if !first {
						sb.WriteString("---\n")
					}
					out, err := GeneratePasswordSecretYAML(u.PasswordSecret, "mongodb", "test-password")
					require.NoError(t, err)
					sb.WriteString(out)
					first = false
				}
				return sb.String()
			},
		},
		{
			name:       "Prometheus password secret",
			inputJSON:  "singlecluster/replicaset/full.json",
			goldenYAML: "singlecluster/replicaset/prometheus_password.yaml",
			generate: func(t *testing.T, ac *om.AutomationConfig) string {
				require.True(t, IsPrometheusEnabled(ac.Deployment), "expected prometheus to be enabled")
				out, err := GeneratePasswordSecretYAML("prometheus-password", "mongodb", "prom-s3cret")
				require.NoError(t, err)
				return out
			},
		},
		{
			name:       "LDAP resources (Secret + ConfigMap)",
			inputJSON:  "singlecluster/replicaset/full.json",
			goldenYAML: "singlecluster/replicaset/ldap_resources.yaml",
			generate: func(t *testing.T, ac *om.AutomationConfig) string {
				res, err := GenerateLdapResources(ac, "mongodb")
				require.NoError(t, err)
				require.NotNil(t, res)
				var sb strings.Builder
				sb.WriteString(res.BindQueryPasswordSecret)
				sb.WriteString("---\n")
				sb.WriteString(res.CAConfigMap)
				return sb.String()
			},
		},
		{
			name:       "LDAP resources (Secret only, no CA)",
			inputJSON:  "multicluster/replicaset/simple.json",
			goldenYAML: "multicluster/replicaset/ldap_resources.yaml",
			generate: func(t *testing.T, ac *om.AutomationConfig) string {
				res, err := GenerateLdapResources(ac, "mongodb")
				require.NoError(t, err)
				require.NotNil(t, res)
				require.NotEmpty(t, res.BindQueryPasswordSecret)
				require.Empty(t, res.CAConfigMap, "expected no CA ConfigMap when CAFileContents is absent")
				return res.BindQueryPasswordSecret
			},
		},
		{
			name:       "heterogeneous processes — only common additionalMongodConfig fields",
			inputJSON:  "singlecluster/replicaset/heterogeneous.json",
			goldenYAML: "singlecluster/replicaset/heterogeneous_cr.yaml",
			generate: func(t *testing.T, ac *om.AutomationConfig) string {
				out, _, err := GenerateMongoDBCR(ac, GenerateOptions{
					CredentialsSecretName: "my-credentials",
					ConfigMapName:         "my-om-config",
				})
				require.NoError(t, err)
				return out
			},
		},
		// --- tls/ ---
		{
			name:       "TLS requireSSL — TLS enabled, mode not in additionalMongodConfig",
			inputJSON:  "singlecluster/replicaset/tls/require.json",
			goldenYAML: "singlecluster/replicaset/tls/require_cr.yaml",
			generate: func(t *testing.T, ac *om.AutomationConfig) string {
				out, _, err := GenerateMongoDBCR(ac, GenerateOptions{
					CredentialsSecretName: "my-credentials",
					ConfigMapName:         "my-om-config",
					CertsSecretPrefix:     "mdb",
				})
				require.NoError(t, err)
				return out
			},
		},
		{
			name:       "TLS allowTLS — TLS enabled, mode preserved in additionalMongodConfig",
			inputJSON:  "singlecluster/replicaset/tls/allow.json",
			goldenYAML: "singlecluster/replicaset/tls/allow_cr.yaml",
			generate: func(t *testing.T, ac *om.AutomationConfig) string {
				out, _, err := GenerateMongoDBCR(ac, GenerateOptions{
					CredentialsSecretName: "my-credentials",
					ConfigMapName:         "my-om-config",
					CertsSecretPrefix:     "mdb",
				})
				require.NoError(t, err)
				return out
			},
		},
		{
			name:       "TLS disabled — no TLS section at all",
			inputJSON:  "singlecluster/replicaset/tls/disabled.json",
			goldenYAML: "singlecluster/replicaset/tls/disabled_cr.yaml",
			generate: func(t *testing.T, ac *om.AutomationConfig) string {
				out, _, err := GenerateMongoDBCR(ac, GenerateOptions{
					CredentialsSecretName: "my-credentials",
					ConfigMapName:         "my-om-config",
				})
				require.NoError(t, err)
				return out
			},
		},
		// --- authentication/ ---
		{
			name:       "auth disabled — no security block",
			inputJSON:  "singlecluster/replicaset/authentication/disabled.json",
			goldenYAML: "singlecluster/replicaset/authentication/disabled_cr.yaml",
			generate: func(t *testing.T, ac *om.AutomationConfig) string {
				out, _, err := GenerateMongoDBCR(ac, GenerateOptions{
					CredentialsSecretName: "my-credentials",
					ConfigMapName:         "my-om-config",
				})
				require.NoError(t, err)
				return out
			},
		},
		{
			name:       "SCRAM-only auth — no TLS, no X509, no LDAP",
			inputJSON:  "singlecluster/replicaset/authentication/scram_only.json",
			goldenYAML: "singlecluster/replicaset/authentication/scram_only_cr.yaml",
			generate: func(t *testing.T, ac *om.AutomationConfig) string {
				out, _, err := GenerateMongoDBCR(ac, GenerateOptions{
					CredentialsSecretName: "my-credentials",
					ConfigMapName:         "my-om-config",
				})
				require.NoError(t, err)
				return out
			},
		},
		{
			name:       "SCRAM-only auth — user CRs",
			inputJSON:  "singlecluster/replicaset/authentication/scram_only.json",
			goldenYAML: "singlecluster/replicaset/authentication/scram_only_user_crs.yaml",
			generate: func(t *testing.T, ac *om.AutomationConfig) string {
				users, err := GenerateUserCRs(ac, "scram-rs")
				require.NoError(t, err)
				var sb strings.Builder
				for i, u := range users {
					if i > 0 {
						sb.WriteString("---\n")
					}
					sb.WriteString(u.YAML)
				}
				return sb.String()
			},
		},
		{
			// Empty mechanisms means the operator created the user — no migration flag.
			name:       "SCRAM auth — empty mechanisms (operator-created) — user CRs",
			inputJSON:  "singlecluster/replicaset/authentication/scram_empty_mechanisms.json",
			goldenYAML: "singlecluster/replicaset/authentication/scram_empty_mechanisms_user_crs.yaml",
			generate: func(t *testing.T, ac *om.AutomationConfig) string {
				users, err := GenerateUserCRs(ac, "scram-rs")
				require.NoError(t, err)
				var sb strings.Builder
				for i, u := range users {
					if i > 0 {
						sb.WriteString("---\n")
					}
					sb.WriteString(u.YAML)
					require.False(t, u.MigratedFromVM, "user with empty mechanisms must not be flagged as VM-migrated")
				}
				return sb.String()
			},
		},
		{
			name:       "SCRAM+X509 auth — dual modes, X509 cluster auth",
			inputJSON:  "singlecluster/replicaset/authentication/x509.json",
			goldenYAML: "singlecluster/replicaset/authentication/x509_cr.yaml",
			generate: func(t *testing.T, ac *om.AutomationConfig) string {
				out, _, err := GenerateMongoDBCR(ac, GenerateOptions{
					CredentialsSecretName: "my-credentials",
					ConfigMapName:         "my-om-config",
					CertsSecretPrefix:     "mdb",
				})
				require.NoError(t, err)
				return out
			},
		},
		{
			name:       "SCRAM+X509 auth — user CRs",
			inputJSON:  "singlecluster/replicaset/authentication/x509.json",
			goldenYAML: "singlecluster/replicaset/authentication/x509_user_crs.yaml",
			generate: func(t *testing.T, ac *om.AutomationConfig) string {
				users, err := GenerateUserCRs(ac, "x509-rs")
				require.NoError(t, err)
				var sb strings.Builder
				for i, u := range users {
					if i > 0 {
						sb.WriteString("---\n")
					}
					sb.WriteString(u.YAML)
				}
				return sb.String()
			},
		},
		{
			name:       "X509-only auth — single mode, keyFile internal cluster",
			inputJSON:  "singlecluster/replicaset/authentication/x509_only.json",
			goldenYAML: "singlecluster/replicaset/authentication/x509_only_cr.yaml",
			generate: func(t *testing.T, ac *om.AutomationConfig) string {
				out, _, err := GenerateMongoDBCR(ac, GenerateOptions{
					CredentialsSecretName: "my-credentials",
					ConfigMapName:         "my-om-config",
					CertsSecretPrefix:     "mdb",
				})
				require.NoError(t, err)
				return out
			},
		},
		{
			name:       "member options — hidden, slaveDelay, tags",
			inputJSON:  "singlecluster/replicaset/member_options.json",
			goldenYAML: "singlecluster/replicaset/member_options_cr.yaml",
			generate: func(t *testing.T, ac *om.AutomationConfig) string {
				out, _, err := GenerateMongoDBCR(ac, GenerateOptions{
					CredentialsSecretName: "my-credentials",
					ConfigMapName:         "my-om-config",
				})
				require.NoError(t, err)
				return out
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ac := loadTestAutomationConfig(t, tt.inputJSON)
			yamlOutput := tt.generate(t, ac)

			goldenPath := "testdata/golden/" + tt.goldenYAML

			if *updateGolden {
				err := os.WriteFile(goldenPath, []byte(yamlOutput), 0644)
				require.NoError(t, err)
				t.Logf("Updated golden file %s", goldenPath)
				return
			}

			expected, err := os.ReadFile(goldenPath)
			require.NoError(t, err, "golden file %s not found; run with -update-golden to create it", goldenPath)

			assert.Equal(t, string(expected), yamlOutput,
				"generated output does not match golden file %s; run with -update-golden to accept changes", goldenPath)
		})
	}
}

func TestGenerateMongoDBCR_CustomResourceName(t *testing.T) {
	ac := loadTestAutomationConfig(t, "singlecluster/replicaset/full.json")

	opts := GenerateOptions{
		ResourceName:          "custom-name",
		CredentialsSecretName: "my-credentials",
		ConfigMapName:         "my-om-config",
		CertsSecretPrefix:     "mdb",
	}

	yamlOutput, _, err := GenerateMongoDBCR(ac, opts)
	require.NoError(t, err)

	assert.Contains(t, yamlOutput, "name: custom-name")
	assert.Contains(t, yamlOutput, "replicaSetNameOverride: my-rs")
}

func TestGenerateMongoDBCR_MultiCluster_CustomResourceName(t *testing.T) {
	ac := loadTestAutomationConfig(t, "multicluster/replicaset/simple.json")

	opts := GenerateOptions{
		ResourceName:          "custom-mc-name",
		CredentialsSecretName: "mc-credentials",
		ConfigMapName:         "mc-om-config",
		MultiClusterNames:     []string{"east1", "west1"},
	}

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
	ac := loadTestAutomationConfig(t, "singlecluster/replicaset/full.json")
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
