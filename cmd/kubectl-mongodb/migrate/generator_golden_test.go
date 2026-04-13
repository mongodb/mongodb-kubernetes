package migrate

import (
	"flag"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/automationconfig"
)

var updateGolden = flag.Bool("update-golden", false, "overwrite golden fixture files with current output")

// runAll exercises the full pipeline and returns the complete YAML output.
func runAll(t *testing.T, ac *om.AutomationConfig, opts GenerateOptions) string {
	t.Helper()

	opts.Namespace = "mongodb"

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

	result, err := generateMigrationResources(ac, withDeploymentData(ac, opts))
	require.NoError(t, err)
	return result
}

// TestFixtureMatch compares generated CR output against golden files.
//
// To regenerate: go test -run TestFixtureMatch -update-golden
func TestFixtureMatch(t *testing.T) {
	projectCfg := fullTestConfigs()
	mcProjectCfg := multiClusterTestConfigs()

	tests := []struct {
		name       string
		inputJSON  string
		goldenYAML string
		opts       GenerateOptions
	}{
		// --- complex_replicaset: MongoDB CR + user CRs + LDAP resources + Prometheus secret + user password secrets ---
		{
			name:       "single-cluster replica set — SCRAM, TLS, LDAP, Prometheus, log rotation",
			inputJSON:  "singlecluster/replicaset/complex_replicaset/complex_replicaset_input.json",
			goldenYAML: "singlecluster/replicaset/complex_replicaset/complex_replicaset_all.yaml",
			opts: GenerateOptions{
				CredentialsSecretName: "my-credentials",
				ConfigMapName:         "my-om-config",
				CertsSecretPrefix:     "mdb",
				PrometheusPassword:    "prom-s3cret",
				ProjectConfigs:        projectCfg,
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
				ProjectConfigs:        mcProjectCfg,
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
				ProjectConfigs:        mcProjectCfg,
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

func multiClusterTestConfigs() *ProjectConfigs {
	return &ProjectConfigs{
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

func fullTestConfigs() *ProjectConfigs {
	return &ProjectConfigs{
		MonitoringConfig: &om.MonitoringAgentConfig{
			MonitoringAgentTemplate: &om.MonitoringAgentTemplate{},
			BackingMap: map[string]interface{}{
				"logRotate": map[string]interface{}{
					"sizeThresholdMB":  500.0,
					"timeThresholdHrs": 12,
				},
			},
		},
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
