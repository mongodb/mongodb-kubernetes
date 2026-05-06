package migratetomck

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
	if opts.ExistingUserSecrets == nil {
		opts.ExistingUserSecrets = make(map[string]string)
		if ac.Auth != nil {
			for _, user := range ac.Auth.Users {
				if user == nil || user.Database == externalDatabase {
					continue
				}
				if user.Username == ac.Auth.AutoUser {
					continue
				}
				opts.ExistingUserSecrets[userKey(user.Username, user.Database)] = user.Username + "-password"
			}
		}
	}
	userObjects, err := GenerateUserCRs(ac, crName, opts.Namespace, opts)
	require.NoError(t, err)
	result, err := marshalMultiDoc(userObjects)
	require.NoError(t, err)
	return result
}

// fixtureCase declares one TestFixtureMatch entry.
// fixture is the shared path stem for input + outputs, e.g. "singlecluster/replicaset/foo/foo".
// The runner derives "<fixture>_input.json", "<fixture>_mongodb_cr.yaml", and "<fixture>_users.yaml" (when hasUsers).
// opts is layered on top of defaultGenerateOptions, so cases only need to set fields that differ.
type fixtureCase struct {
	name     string
	fixture  string
	hasUsers bool
	opts     GenerateOptions
}

// withDefaultBoilerplate fills in the credentials secret name and OM config-map name unless the case set them.
func withDefaultBoilerplate(opts GenerateOptions) GenerateOptions {
	if opts.CredentialsSecretName == "" {
		opts.CredentialsSecretName = "my-credentials"
	}
	if opts.ConfigMapName == "" {
		opts.ConfigMapName = "my-om-config"
	}
	return opts
}

// TestFixtureMatch_ReplicaSet covers replica-set generator output. To regenerate fixtures: go test -run TestFixtureMatch -update-fixtures
func TestFixtureMatch_ReplicaSet(t *testing.T) {
	projectCfg := fullTestConfigs()
	cases := []fixtureCase{
		{
			name:     "single-cluster replica set — SCRAM, TLS, LDAP, Prometheus, log rotation",
			fixture:  "singlecluster/replicaset/complex_replicaset/complex_replicaset",
			hasUsers: true,
			opts: GenerateOptions{
				CertsSecretPrefix:  "mdb",
				PrometheusPassword: "prom-s3cret",
				ProjectConfigs:     projectCfg,
			},
		},
		{
			name:    "additionalMongodConfig — unknown fields (zstdCompressionLevel) are passed through",
			fixture: "singlecluster/replicaset/additional_mongod_config/additional_mongod_config",
		},
		{
			name:    "different mongod config — additionalMongodConfig taken from first process",
			fixture: "singlecluster/replicaset/different_mongod_config/different_mongod_config",
		},
		{
			name:    "TLS requireSSL — TLS enabled, mode not in additionalMongodConfig",
			fixture: "singlecluster/replicaset/tls/require/require",
			opts:    GenerateOptions{CertsSecretPrefix: "mdb"},
		},
		{
			name:    "TLS allowTLS — TLS enabled, mode preserved in additionalMongodConfig",
			fixture: "singlecluster/replicaset/tls/allow/allow",
			opts:    GenerateOptions{CertsSecretPrefix: "mdb"},
		},
		{
			name:    "TLS disabled — no TLS section at all",
			fixture: "singlecluster/replicaset/tls/disabled/disabled",
		},
		{
			name:    "auth disabled — no security block",
			fixture: "singlecluster/replicaset/authentication/disabled/disabled",
		},
		{
			name:     "SCRAM-SHA-256 auth — single mechanism with user password secrets",
			fixture:  "singlecluster/replicaset/authentication/scram_sha256/scram_sha256",
			hasUsers: true,
		},
		{
			name:     "SCRAM+X509 auth — MongoDB CR + user CRs",
			fixture:  "singlecluster/replicaset/authentication/x509/x509",
			hasUsers: true,
			opts:     GenerateOptions{CertsSecretPrefix: "mdb"},
		},
		{
			name:    "X509-only auth — single mode, keyFile internal cluster",
			fixture: "singlecluster/replicaset/authentication/x509_only/x509_only",
			opts:    GenerateOptions{CertsSecretPrefix: "mdb"},
		},
		{
			name:    "member options — tags",
			fixture: "singlecluster/replicaset/member_options/member_options",
		},
	}
	runFixtureCases(t, cases)
}

func runFixtureCases(t *testing.T, cases []fixtureCase) {
	t.Helper()
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			opts := withDefaultBoilerplate(tt.opts)
			ac := loadTestAutomationConfig(t, tt.fixture+"_input.json")

			mongodbOutput := runMongodb(t, ac, opts)
			checkOrUpdateFixture(t, "testdata/"+tt.fixture+"_mongodb_cr.yaml", mongodbOutput)

			if !tt.hasUsers {
				return
			}
			usersOutput := runUsers(t, ac, opts)
			checkOrUpdateFixture(t, "testdata/"+tt.fixture+"_users.yaml", usersOutput)
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
