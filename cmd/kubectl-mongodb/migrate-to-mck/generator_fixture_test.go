package migratetomck

import (
	"flag"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/automationconfig"
)

var updateFixtures = flag.Bool("update-fixtures", false, "overwrite fixture files with current output")

// runMongodb exercises the MongoDB CR generation pipeline (generate mongodb subcommand).
func runMongodb(t *testing.T, ac *om.AutomationConfig, opts GenerateOptions) string {
	t.Helper()
	opts.Namespace = "mongodb"
	objects, err := generateMongodbObjects(ac, withDeploymentData(ac, opts))
	require.NoError(t, err)
	result, err := renderObjects(objects)
	require.NoError(t, err)
	return result
}

// runUsers exercises the MongoDBUser CR generation pipeline (generate users subcommand).
func runUsers(t *testing.T, ac *om.AutomationConfig, opts GenerateOptions) string {
	t.Helper()
	opts.Namespace = "mongodb"
	crName, err := resolveMongoDBResourceName(ac, opts.ResourceNameOverride)
	require.NoError(t, err)
	if opts.ExistingUserSecrets == nil {
		opts.ExistingUserSecrets = make(map[string]string)
		for _, user := range scramUsers(ac) {
			opts.ExistingUserSecrets[userKey(user.Username, user.Database)] = suggestedUserSecretName(user)
		}
	}
	objects, err := GenerateUserCRs(ac, crName, opts.Namespace, opts)
	require.NoError(t, err)
	result, err := renderObjects(objects)
	require.NoError(t, err)
	return result
}

// fixtureCase declares one TestFixtureMatch entry.
// fixture is the shared path stem for input + outputs, e.g. "singlecluster/replicaset/foo/foo".
// The runner derives "<fixture>_input.json", "<fixture>_mongodb_cr.yaml", and "<fixture>_users.yaml" (when hasUsers).
// opts is layered on top of defaultGenerateOptions, so cases only need to set fields that differ.
// When wantErr is non-empty, the test expects generateMongodbObjects to return an error containing that string.
type fixtureCase struct {
	name     string
	fixture  string
	hasUsers bool
	opts     GenerateOptions
	wantErr  string
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
			name:     "complex replica set with SCRAM, TLS, LDAP, Prometheus and log rotation",
			fixture:  "singlecluster/replicaset/complex_replicaset/complex_replicaset",
			hasUsers: true,
			opts: GenerateOptions{
				CertsSecretPrefix:    "mdb",
				PrometheusSecretName: PrometheusPasswordSecretName,
				ProjectConfigs:       projectCfg,
			},
		},
		{
			name:    "unknown additionalMongodConfig fields like zstdCompressionLevel are passed through",
			fixture: "singlecluster/replicaset/additional_mongod_config/additional_mongod_config",
		},
		{
			name:    "additionalMongodConfig is taken from the first process when members differ",
			fixture: "singlecluster/replicaset/different_mongod_config/different_mongod_config",
		},
		{
			name:    "TLS requireSSL with clientCertificateMode OPTIONAL does not set allowConnectionsWithoutCertificates",
			fixture: "singlecluster/replicaset/tls/require/require",
			opts:    GenerateOptions{CertsSecretPrefix: "mdb"},
		},
		{
			name:    "TLS allowTLS with clientCertificateMode REQUIRE sets allowConnectionsWithoutCertificates to false",
			fixture: "singlecluster/replicaset/tls/allow/allow",
			opts:    GenerateOptions{CertsSecretPrefix: "mdb"},
		},
		{
			name:    "TLS disabled produces no TLS section in the CR",
			fixture: "singlecluster/replicaset/tls/disabled/disabled",
		},
		{
			name:    "auth disabled produces no security block",
			fixture: "singlecluster/replicaset/authentication/disabled/disabled",
		},
		{
			name:     "SCRAM-SHA-256 auth generates user CRs with password secrets and maps custom roles",
			fixture:  "singlecluster/replicaset/authentication/scram_sha256/scram_sha256",
			hasUsers: true,
		},
		{
			name:     "SCRAM-SHA-1 auth generates user CRs with password secrets",
			fixture:  "singlecluster/replicaset/authentication/scram_sha1/scram_sha1",
			hasUsers: true,
		},
		{
			name:     "SCRAM and X509 auth generates both MongoDB CR and user CRs",
			fixture:  "singlecluster/replicaset/authentication/x509/x509",
			hasUsers: true,
			opts:     GenerateOptions{CertsSecretPrefix: "mdb"},
		},
		{
			name:     "X509-only auth: external agent skipped, app user CR generated, keyFile internal cluster",
			fixture:  "singlecluster/replicaset/authentication/x509_only/x509_only",
			hasUsers: true,
			opts:     GenerateOptions{CertsSecretPrefix: "mdb"},
		},
		{
			name:     "LDAP auth: ldap section + agent password secret generated, external agent skipped, app user CR generated",
			fixture:  "singlecluster/replicaset/authentication/ldap/ldap",
			hasUsers: true,
		},
		{
			name:     "OIDC auth: workforce and workload provider configs mapped, SCRAM agent and app user",
			fixture:  "singlecluster/replicaset/authentication/oidc/oidc",
			hasUsers: true,
		},
		{
			name:    "Prometheus (HTTP) generates spec.prometheus referencing the password secret with no TLS ref",
			fixture: "singlecluster/replicaset/prometheus/prometheus",
			opts:    GenerateOptions{PrometheusSecretName: PrometheusPasswordSecretName, PrometheusPassword: "prom-password"},
		},
		{
			name:    "Prometheus (HTTPS) generates spec.prometheus with a TLS secret ref",
			fixture: "singlecluster/replicaset/prometheus_https/prometheus_https",
			opts:    GenerateOptions{PrometheusSecretName: PrometheusPasswordSecretName, PrometheusPassword: "prom-password"},
		},
		{
			name:    "Prometheus password mismatch is rejected",
			fixture: "singlecluster/replicaset/prometheus/prometheus",
			opts:    GenerateOptions{PrometheusSecretName: PrometheusPasswordSecretName, PrometheusPassword: "wrong-password"},
			wantErr: "does not match the password",
		},
		{
			name:    "member tags are preserved in externalMembers",
			fixture: "singlecluster/replicaset/member_options/member_options",
		},
	}
	runFixtureCases(t, cases)
}

// TestFixtureMatch_ShardedCluster covers sharded-cluster generator output. To regenerate fixtures: go test -run TestFixtureMatch -update-fixtures
func TestFixtureMatch_ShardedCluster(t *testing.T) {
	cases := []fixtureCase{
		{
			name:     "sharded cluster — SCRAM users, split shard names, config server RS",
			fixture:  "singlecluster/shardedcluster/complex_sharded/complex_sharded",
			hasUsers: true,
			opts:     GenerateOptions{CertsSecretPrefix: "mdb"},
		},
		{
			name:    "sharded cluster — default config RS name",
			fixture: "singlecluster/shardedcluster/default_config_rs/default_config_rs",
		},
		{
			name:     "sharded cluster — LDAP: ldap section + agent password secret generated, external agent skipped, app user CR generated",
			fixture:  "singlecluster/shardedcluster/authentication/ldap/ldap",
			hasUsers: true,
		},
		{
			name:     "sharded cluster — OIDC: provider configs preserved, SCRAM users emitted",
			fixture:  "singlecluster/shardedcluster/authentication/oidc/oidc",
			hasUsers: true,
		},
		{
			name:    "sharded cluster — Prometheus (HTTP): spec.prometheus referencing the password secret",
			fixture: "singlecluster/shardedcluster/authentication/prometheus/prometheus",
			opts:    GenerateOptions{PrometheusSecretName: PrometheusPasswordSecretName, PrometheusPassword: "prom-password"},
		},
		{
			name:    "sharded cluster — Prometheus (HTTP) generates spec.prometheus referencing the password secret with no TLS ref",
			fixture: "singlecluster/shardedcluster/prometheus/prometheus",
			opts:    GenerateOptions{PrometheusSecretName: PrometheusPasswordSecretName, PrometheusPassword: "prom-password"},
		},
		{
			name:    "sharded cluster — Prometheus (HTTPS) generates spec.prometheus with a TLS secret ref",
			fixture: "singlecluster/shardedcluster/prometheus_https/prometheus_https",
			opts:    GenerateOptions{PrometheusSecretName: PrometheusPasswordSecretName, PrometheusPassword: "prom-password"},
		},
		{
			name:    "sharded cluster — Prometheus password mismatch is rejected",
			fixture: "singlecluster/shardedcluster/authentication/prometheus/prometheus",
			opts:    GenerateOptions{PrometheusSecretName: PrometheusPasswordSecretName, PrometheusPassword: "wrong-password"},
			wantErr: "does not match the password",
		},
		{
			name:    "sharded cluster — TLS requireSSL with clientCertificateMode OPTIONAL does not set allowConnectionsWithoutCertificates",
			fixture: "singlecluster/shardedcluster/tls/require/require",
			opts:    GenerateOptions{CertsSecretPrefix: "mdb"},
		},
		{
			name:    "sharded cluster — TLS allowTLS with clientCertificateMode REQUIRE sets allowConnectionsWithoutCertificates to false",
			fixture: "singlecluster/shardedcluster/tls/allow/allow",
			opts:    GenerateOptions{CertsSecretPrefix: "mdb"},
		},
		{
			name:    "sharded cluster — TLS disabled produces no TLS section in the CR",
			fixture: "singlecluster/shardedcluster/tls/disabled/disabled",
		},
		{
			name:    "sharded cluster — auth disabled produces no security block",
			fixture: "singlecluster/shardedcluster/authentication/disabled/disabled",
		},
		{
			name:     "sharded cluster — SCRAM-SHA-256 auth generates user CRs with password secrets",
			fixture:  "singlecluster/shardedcluster/authentication/scram_sha256/scram_sha256",
			hasUsers: true,
		},
		{
			name:     "sharded cluster — SCRAM-SHA-1 auth generates user CRs with password secrets",
			fixture:  "singlecluster/shardedcluster/authentication/scram_sha1/scram_sha1",
			hasUsers: true,
		},
		{
			name:     "sharded cluster — SCRAM and X509 auth generates both MongoDB CR and user CRs",
			fixture:  "singlecluster/shardedcluster/authentication/x509/x509",
			hasUsers: true,
			opts:     GenerateOptions{CertsSecretPrefix: "mdb"},
		},
		{
			name:     "sharded cluster — X509-only auth: external agent skipped, app user CR generated, keyFile internal cluster",
			fixture:  "singlecluster/shardedcluster/authentication/x509_only/x509_only",
			hasUsers: true,
			opts:     GenerateOptions{CertsSecretPrefix: "mdb"},
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

			if tt.wantErr != "" {
				_, err := generateMongodbObjects(ac, opts)
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}

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
