package migrate

import (
	"flag"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/automationconfig"
)

var updateFixtures = flag.Bool("update-fixtures", false, "overwrite fixture files with current output")

// runMongodb exercises the MongoDB CR generation pipeline (generate mongodb subcommand).
func runMongodb(t *testing.T, ac *om.AutomationConfig, opts GenerateOptions) string {
	t.Helper()
	opts.Namespace = "mongodb"
	optsWithData := withDeploymentData(ac, opts)
	mongodbCR, _, err := GenerateMongoDBCR(ac, optsWithData)
	require.NoError(t, err)
	extra := generateExtraResources(ac, optsWithData)
	objects := make([]client.Object, 0, 1+len(extra))
	objects = append(objects, mongodbCR)
	objects = append(objects, extra...)
	result, err := marshalMultiDoc(objects)
	require.NoError(t, err)
	return result
}

// runUsers exercises the MongoDBUser CR generation pipeline (generate users subcommand).
func runUsers(t *testing.T, ac *om.AutomationConfig, opts GenerateOptions) string {
	t.Helper()
	opts.Namespace = "mongodb"
	_, crName, err := GenerateMongoDBCR(ac, withDeploymentData(ac, opts))
	require.NoError(t, err)
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
	userObjects, err := GenerateUserCRs(ac, crName, opts.Namespace, opts)
	require.NoError(t, err)
	result, err := marshalMultiDoc(userObjects)
	require.NoError(t, err)
	return result
}

// TestFixtureMatch tests that generating Kubernetes CR manifests from the given
// Automation Config input yields bytes identical to the committed fixture file.
// To regenerate fixture files: go test -run TestFixtureMatch -update-fixtures
func TestFixtureMatch(t *testing.T) {
	projectCfg := fullTestConfigs()
	mcProjectCfg := multiClusterTestConfigs()

	tests := []struct {
		name           string
		inputJSON      string
		fixtureMongoDB string
		fixtureUsers   string // empty if the deployment has no users
		opts           GenerateOptions
	}{
		{
			name:           "single-cluster replica set — SCRAM, TLS, LDAP, Prometheus, log rotation",
			inputJSON:      "singlecluster/replicaset/complex_replicaset/complex_replicaset_input.json",
			fixtureMongoDB: "singlecluster/replicaset/complex_replicaset/complex_replicaset_mongodb_cr.yaml",
			fixtureUsers:   "singlecluster/replicaset/complex_replicaset/complex_replicaset_users.yaml",
			opts: GenerateOptions{
				CredentialsSecretName: "my-credentials",
				ConfigMapName:         "my-om-config",
				CertsSecretPrefix:     "mdb",
				PrometheusPassword:    "prom-s3cret",
				ProjectConfigs:        projectCfg,
			},
		},
		{
			name:           "5-member distributed multi-cluster replica set — split across 2 clusters",
			inputJSON:      "multicluster/replicaset/distributed/distributed_input.json",
			fixtureMongoDB: "multicluster/replicaset/distributed/distributed_2_clusters_mongodb_cr.yaml",
			opts: GenerateOptions{
				CredentialsSecretName: "mc-credentials",
				ConfigMapName:         "mc-om-config",
				MultiClusterNames:     []string{"east1", "west1"},
				ProjectConfigs:        mcProjectCfg,
			},
		},
		{
			name:           "5-member distributed multi-cluster replica set — split across 3 clusters",
			inputJSON:      "multicluster/replicaset/distributed/distributed_input.json",
			fixtureMongoDB: "multicluster/replicaset/distributed/distributed_3_clusters_mongodb_cr.yaml",
			opts: GenerateOptions{
				CredentialsSecretName: "mc-credentials",
				ConfigMapName:         "mc-om-config",
				MultiClusterNames:     []string{"cluster-a", "cluster-b", "cluster-c"},
				ProjectConfigs:        mcProjectCfg,
			},
		},
		{
			name:           "additionalMongodConfig — unknown fields (zstdCompressionLevel) are passed through",
			inputJSON:      "singlecluster/replicaset/additional_mongod_config/additional_mongod_config_input.json",
			fixtureMongoDB: "singlecluster/replicaset/additional_mongod_config/additional_mongod_config_mongodb_cr.yaml",
			opts: GenerateOptions{
				CredentialsSecretName: "my-credentials",
				ConfigMapName:         "my-om-config",
			},
		},
		{
			name:           "different mongod config — additionalMongodConfig taken from first process",
			inputJSON:      "singlecluster/replicaset/different_mongod_config/different_mongod_config_input.json",
			fixtureMongoDB: "singlecluster/replicaset/different_mongod_config/different_mongod_config_mongodb_cr.yaml",
			opts: GenerateOptions{
				CredentialsSecretName: "my-credentials",
				ConfigMapName:         "my-om-config",
			},
		},
		{
			name:           "TLS requireSSL — TLS enabled, mode not in additionalMongodConfig",
			inputJSON:      "singlecluster/replicaset/tls/require/require_input.json",
			fixtureMongoDB: "singlecluster/replicaset/tls/require/require_mongodb_cr.yaml",
			opts: GenerateOptions{
				CredentialsSecretName: "my-credentials",
				ConfigMapName:         "my-om-config",
				CertsSecretPrefix:     "mdb",
			},
		},
		{
			name:           "TLS allowTLS — TLS enabled, mode preserved in additionalMongodConfig",
			inputJSON:      "singlecluster/replicaset/tls/allow/allow_input.json",
			fixtureMongoDB: "singlecluster/replicaset/tls/allow/allow_mongodb_cr.yaml",
			opts: GenerateOptions{
				CredentialsSecretName: "my-credentials",
				ConfigMapName:         "my-om-config",
				CertsSecretPrefix:     "mdb",
			},
		},
		{
			name:           "TLS disabled — no TLS section at all",
			inputJSON:      "singlecluster/replicaset/tls/disabled/disabled_input.json",
			fixtureMongoDB: "singlecluster/replicaset/tls/disabled/disabled_mongodb_cr.yaml",
			opts: GenerateOptions{
				CredentialsSecretName: "my-credentials",
				ConfigMapName:         "my-om-config",
			},
		},
		{
			name:           "auth disabled — no security block",
			inputJSON:      "singlecluster/replicaset/authentication/disabled/disabled_input.json",
			fixtureMongoDB: "singlecluster/replicaset/authentication/disabled/disabled_mongodb_cr.yaml",
			opts: GenerateOptions{
				CredentialsSecretName: "my-credentials",
				ConfigMapName:         "my-om-config",
			},
		},
		{
			name:           "SCRAM-SHA-256 auth — single mechanism with user password secrets",
			inputJSON:      "singlecluster/replicaset/authentication/scram_sha256/scram_sha256_input.json",
			fixtureMongoDB: "singlecluster/replicaset/authentication/scram_sha256/scram_sha256_mongodb_cr.yaml",
			fixtureUsers:   "singlecluster/replicaset/authentication/scram_sha256/scram_sha256_users.yaml",
			opts: GenerateOptions{
				CredentialsSecretName: "my-credentials",
				ConfigMapName:         "my-om-config",
			},
		},
		{
			name:           "SCRAM+X509 auth — MongoDB CR + user CRs",
			inputJSON:      "singlecluster/replicaset/authentication/x509/x509_input.json",
			fixtureMongoDB: "singlecluster/replicaset/authentication/x509/x509_mongodb_cr.yaml",
			fixtureUsers:   "singlecluster/replicaset/authentication/x509/x509_users.yaml",
			opts: GenerateOptions{
				CredentialsSecretName: "my-credentials",
				ConfigMapName:         "my-om-config",
				CertsSecretPrefix:     "mdb",
			},
		},
		{
			name:           "X509-only auth — single mode, keyFile internal cluster",
			inputJSON:      "singlecluster/replicaset/authentication/x509_only/x509_only_input.json",
			fixtureMongoDB: "singlecluster/replicaset/authentication/x509_only/x509_only_mongodb_cr.yaml",
			opts: GenerateOptions{
				CredentialsSecretName: "my-credentials",
				ConfigMapName:         "my-om-config",
				CertsSecretPrefix:     "mdb",
			},
		},
		{
			name:           "member options — tags",
			inputJSON:      "singlecluster/replicaset/member_options/member_options_input.json",
			fixtureMongoDB: "singlecluster/replicaset/member_options/member_options_mongodb_cr.yaml",
			opts: GenerateOptions{
				CredentialsSecretName: "my-credentials",
				ConfigMapName:         "my-om-config",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ac := loadTestAutomationConfig(t, tt.inputJSON)

			mongodbOutput := runMongodb(t, ac, tt.opts)
			fixtureMongoDBPath := "testdata/" + tt.fixtureMongoDB
			checkOrUpdateFixture(t, fixtureMongoDBPath, mongodbOutput)

			if tt.fixtureUsers == "" {
				return
			}
			usersOutput := runUsers(t, ac, tt.opts)
			fixtureUsersPath := "testdata/" + tt.fixtureUsers
			checkOrUpdateFixture(t, fixtureUsersPath, usersOutput)
		})
	}
}

func checkOrUpdateFixture(t *testing.T, path, got string) {
	t.Helper()
	if *updateFixtures {
		require.NoError(t, os.WriteFile(path, []byte(got), 0o644))
		t.Logf("Updated fixture file %s", path)
		return
	}
	expected, err := os.ReadFile(path)
	require.NoError(t, err, "fixture file %s not found; run with -update-fixtures to create it", path)
	assert.Equal(t, string(expected), got,
		"generated output does not match fixture file %s; run with -update-fixtures to accept changes", path)
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
