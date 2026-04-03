package migrate

import (
	"context"
	"flag"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	k8sClient "sigs.k8s.io/controller-runtime/pkg/client"

	userv1 "github.com/mongodb/mongodb-kubernetes/api/v1/user"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/automationconfig"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
)

var updateGolden = flag.Bool("update-golden", false, "overwrite golden fixture files with current output")

func loadTestAutomationConfig(t *testing.T, filename string) *om.AutomationConfig {
	t.Helper()
	data, err := os.ReadFile("testdata/" + filename)
	require.NoError(t, err)
	ac, err := om.BuildAutomationConfigFromBytes(data)
	require.NoError(t, err)
	return ac
}

// withDeploymentData mirrors what runGenerate does before calling generateAll.
func withDeploymentData(ac *om.AutomationConfig, opts GenerateOptions) GenerateOptions {
	opts.ProcessMap = ac.Deployment.ProcessMap()
	if rss := ac.Deployment.GetReplicaSets(); len(rss) > 0 {
		opts.Members = rss[0].Members()
	}
	if len(opts.Members) > 0 {
		opts.SourceProcess, _ = pickSourceProcess(opts.Members, opts.ProcessMap)
	}
	return opts
}

// runAll exercises the full pipeline with a mock kube client and collects all output YAML.
func runAll(t *testing.T, ac *om.AutomationConfig, opts GenerateOptions) string {
	t.Helper()

	origNamespace := namespace
	namespace = "mongodb"
	defer func() { namespace = origNamespace }()

	// Auto-populate test passwords for SCRAM users when not explicitly provided.
	if opts.UserPasswords == nil {
		opts.UserPasswords = make(map[string]string)
		if ac.Auth != nil {
			for _, user := range ac.Auth.Users {
				if user == nil || user.Database == externalDatabase {
					continue
				}
				if user.Username == ac.Auth.AutoUser {
					continue
				}
				opts.UserPasswords[userKey(user.Username, user.Database)] = "test-password"
			}
		}
	}

	ctx := context.Background()
	kube := kubernetesClient.NewClient(kubernetesClient.NewMockedClient())

	mongodbYAML, userCRs, _, err := generateAll(ctx, ac, withDeploymentData(ac, opts), kube)
	require.NoError(t, err)

	var sb strings.Builder
	sb.WriteString(mongodbYAML)
	for _, u := range userCRs {
		sb.WriteString("---\n")
		sb.WriteString(u.YAML)
	}

	// Read back cluster-created resources in deterministic order.

	if ac.Ldap != nil && ac.Ldap.BindQueryPassword != "" {
		sec, err := kube.GetSecret(ctx, k8sClient.ObjectKey{Name: LdapBindQuerySecretName, Namespace: "mongodb"})
		require.NoError(t, err)
		y, err := marshalCRToYAML(sec)
		require.NoError(t, err)
		sb.WriteString("---\n")
		sb.WriteString(y)
	}

	if ac.Ldap != nil && ac.Ldap.CaFileContents != "" {
		cm, err := kube.GetConfigMap(ctx, k8sClient.ObjectKey{Name: LdapCAConfigMapName, Namespace: "mongodb"})
		require.NoError(t, err)
		y, err := marshalCRToYAML(cm)
		require.NoError(t, err)
		sb.WriteString("---\n")
		sb.WriteString(y)
	}

	if opts.PrometheusPassword != "" {
		sec, err := kube.GetSecret(ctx, k8sClient.ObjectKey{Name: PrometheusPasswordSecretName, Namespace: "mongodb"})
		require.NoError(t, err)
		y, err := marshalCRToYAML(sec)
		require.NoError(t, err)
		sb.WriteString("---\n")
		sb.WriteString(y)
	}

	for _, u := range userCRs {
		if !u.NeedsPassword {
			continue
		}
		if _, ok := opts.UserPasswords[userKey(u.Username, u.Database)]; !ok {
			continue
		}
		sec, err := kube.GetSecret(ctx, k8sClient.ObjectKey{Name: u.PasswordSecret, Namespace: "mongodb"})
		require.NoError(t, err)
		y, err := marshalCRToYAML(sec)
		require.NoError(t, err)
		sb.WriteString("---\n")
		sb.WriteString(y)
	}

	return sb.String()
}

// TestFixtureMatch compares generated CR output against golden files.
//
// To regenerate: go test -run TestFixtureMatch -update-golden
func TestFixtureMatch(t *testing.T) {
	agentCfg, processCfg := fullTestConfigs()
	mcAgentCfg, mcProcessCfg := multiClusterTestConfigs()

	tests := []struct {
		name       string
		inputJSON  string
		goldenYAML string
		opts       GenerateOptions
	}{
		// --- scram_tls_ldap_prometheus: MongoDB CR + user CRs + LDAP resources + Prometheus secret + user password secrets ---
		{
			name:       "single-cluster replica set — SCRAM, TLS, LDAP, Prometheus, log rotation",
			inputJSON:  "singlecluster/replicaset/scram_tls_ldap_prometheus/scram_tls_ldap_prometheus_input.json",
			goldenYAML: "singlecluster/replicaset/scram_tls_ldap_prometheus/scram_tls_ldap_prometheus_all.yaml",
			opts: GenerateOptions{
				CredentialsSecretName: "my-credentials",
				ConfigMapName:         "my-om-config",
				CertsSecretPrefix:     "mdb",
				PrometheusPassword:    "prom-s3cret",
				ProjectAgentConfigs:   agentCfg,
				ProjectProcessConfigs: processCfg,
			},
		},
		// --- distributed: MongoDB CR + LDAP bind-query secret (two cluster layouts) ---
		{
			name:       "5-member distributed multi-cluster replica set — split across 2 clusters",
			inputJSON:  "multicluster/replicaset/distributed/distributed_input.json",
			goldenYAML: "multicluster/replicaset/distributed/distributed_2_clusters_all.yaml",
			opts: GenerateOptions{
				CredentialsSecretName: "mc-credentials",
				ConfigMapName:         "mc-om-config",
				MultiClusterNames:     []string{"east1", "west1"},
				ProjectAgentConfigs:   mcAgentCfg,
				ProjectProcessConfigs: mcProcessCfg,
			},
		},
		{
			name:       "5-member distributed multi-cluster replica set — split across 3 clusters",
			inputJSON:  "multicluster/replicaset/distributed/distributed_input.json",
			goldenYAML: "multicluster/replicaset/distributed/distributed_3_clusters_all.yaml",
			opts: GenerateOptions{
				CredentialsSecretName: "mc-credentials",
				ConfigMapName:         "mc-om-config",
				MultiClusterNames:     []string{"cluster-a", "cluster-b", "cluster-c"},
				ProjectAgentConfigs:   mcAgentCfg,
				ProjectProcessConfigs: mcProcessCfg,
			},
		},
		// --- additionalMongodConfig ---
		{
			name:       "additionalMongodConfig — unknown fields (zstdCompressionLevel) are passed through",
			inputJSON:  "singlecluster/replicaset/additional_mongod_config/additional_mongod_config_input.json",
			goldenYAML: "singlecluster/replicaset/additional_mongod_config/additional_mongod_config_mongodb_cr.yaml",
			opts: GenerateOptions{
				CredentialsSecretName: "my-credentials",
				ConfigMapName:         "my-om-config",
			},
		},
		{
			name:       "different mongod config — additionalMongodConfig taken from first process",
			inputJSON:  "singlecluster/replicaset/different_mongod_config/different_mongod_config_input.json",
			goldenYAML: "singlecluster/replicaset/different_mongod_config/different_mongod_config_mongodb_cr.yaml",
			opts: GenerateOptions{
				CredentialsSecretName: "my-credentials",
				ConfigMapName:         "my-om-config",
			},
		},
		// --- tls ---
		{
			name:       "TLS requireSSL — TLS enabled, mode not in additionalMongodConfig",
			inputJSON:  "singlecluster/replicaset/tls/require/require_input.json",
			goldenYAML: "singlecluster/replicaset/tls/require/require_mongodb_cr.yaml",
			opts: GenerateOptions{
				CredentialsSecretName: "my-credentials",
				ConfigMapName:         "my-om-config",
				CertsSecretPrefix:     "mdb",
			},
		},
		{
			name:       "TLS allowTLS — TLS enabled, mode preserved in additionalMongodConfig",
			inputJSON:  "singlecluster/replicaset/tls/allow/allow_input.json",
			goldenYAML: "singlecluster/replicaset/tls/allow/allow_mongodb_cr.yaml",
			opts: GenerateOptions{
				CredentialsSecretName: "my-credentials",
				ConfigMapName:         "my-om-config",
				CertsSecretPrefix:     "mdb",
			},
		},
		{
			name:       "TLS disabled — no TLS section at all",
			inputJSON:  "singlecluster/replicaset/tls/disabled/disabled_input.json",
			goldenYAML: "singlecluster/replicaset/tls/disabled/disabled_mongodb_cr.yaml",
			opts: GenerateOptions{
				CredentialsSecretName: "my-credentials",
				ConfigMapName:         "my-om-config",
			},
		},
		// --- authentication ---
		{
			name:       "auth disabled — no security block",
			inputJSON:  "singlecluster/replicaset/authentication/disabled/disabled_input.json",
			goldenYAML: "singlecluster/replicaset/authentication/disabled/disabled_mongodb_cr.yaml",
			opts: GenerateOptions{
				CredentialsSecretName: "my-credentials",
				ConfigMapName:         "my-om-config",
			},
		},
		{
			name:       "SCRAM-only auth — MongoDB CR + user CRs + password secrets",
			inputJSON:  "singlecluster/replicaset/authentication/scram_only/scram_only_input.json",
			goldenYAML: "singlecluster/replicaset/authentication/scram_only/scram_only_all.yaml",
			opts: GenerateOptions{
				CredentialsSecretName: "my-credentials",
				ConfigMapName:         "my-om-config",
			},
		},
		{
			name:       "SCRAM auth — empty mechanisms (operator-created) — MongoDB CR + user CRs + password secrets",
			inputJSON:  "singlecluster/replicaset/authentication/scram_empty_mechanisms/scram_empty_mechanisms_input.json",
			goldenYAML: "singlecluster/replicaset/authentication/scram_empty_mechanisms/scram_empty_mechanisms_all.yaml",
			opts: GenerateOptions{
				CredentialsSecretName: "my-credentials",
				ConfigMapName:         "my-om-config",
			},
		},
		{
			name:       "SCRAM+X509 auth — MongoDB CR + user CRs + password secrets",
			inputJSON:  "singlecluster/replicaset/authentication/x509/x509_input.json",
			goldenYAML: "singlecluster/replicaset/authentication/x509/x509_all.yaml",
			opts: GenerateOptions{
				CredentialsSecretName: "my-credentials",
				ConfigMapName:         "my-om-config",
				CertsSecretPrefix:     "mdb",
			},
		},
		{
			name:       "X509-only auth — single mode, keyFile internal cluster",
			inputJSON:  "singlecluster/replicaset/authentication/x509_only/x509_only_input.json",
			goldenYAML: "singlecluster/replicaset/authentication/x509_only/x509_only_mongodb_cr.yaml",
			opts: GenerateOptions{
				CredentialsSecretName: "my-credentials",
				ConfigMapName:         "my-om-config",
				CertsSecretPrefix:     "mdb",
			},
		},
		{
			name:       "member options — hidden, slaveDelay, tags",
			inputJSON:  "singlecluster/replicaset/member_options/member_options_input.json",
			goldenYAML: "singlecluster/replicaset/member_options/member_options_mongodb_cr.yaml",
			opts: GenerateOptions{
				CredentialsSecretName: "my-credentials",
				ConfigMapName:         "my-om-config",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ac := loadTestAutomationConfig(t, tt.inputJSON)
			yamlOutput := runAll(t, ac, tt.opts)

			goldenPath := "testdata/" + tt.goldenYAML

			if *updateGolden {
				err := os.WriteFile(goldenPath, []byte(yamlOutput), 0o644)
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
	ac := loadTestAutomationConfig(t, "singlecluster/replicaset/scram_tls_ldap_prometheus/scram_tls_ldap_prometheus_input.json")

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
	ac := loadTestAutomationConfig(t, "multicluster/replicaset/distributed/distributed_input.json")

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

// TestGenerateUserCRs_EmptyMechanisms verifies users with empty mechanisms are not flagged as VM-migrated.
func TestGenerateUserCRs_EmptyMechanisms(t *testing.T) {
	ac := loadTestAutomationConfig(t, "singlecluster/replicaset/authentication/scram_empty_mechanisms/scram_empty_mechanisms_input.json")
	users, err := GenerateUserCRs(ac, "scram-rs")
	require.NoError(t, err)
	for _, u := range users {
		assert.False(t, u.MigratedFromVM, "user %q with empty mechanisms must not be flagged as VM-migrated", u.Username)
	}
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
