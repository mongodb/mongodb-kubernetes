package construct

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
)

// monarchTestMDB returns a minimal MongoDB resource exercising the fields the
// monarch deployment builder reads. role is "active" or "standby".
func monarchTestMDB(role mdbv1.MonarchRole) *mdbv1.MongoDB {
	mdb := &mdbv1.MongoDB{
		ObjectMeta: metav1.ObjectMeta{Name: "rs-test", Namespace: "ns"},
		Spec: mdbv1.MongoDbSpec{
			DbCommonSpec: mdbv1.DbCommonSpec{Version: "8.0.16"},
			Members:      3,
			Monarch: &mdbv1.MonarchSpec{
				Role: role,
				S3: mdbv1.MonarchS3Config{
					Bucket:               "monarch-bucket",
					Region:               "us-east-1",
					CredentialsSecretRef: corev1.LocalObjectReference{Name: "monarch-creds"},
					Prefix:               "failoverdemo",
				},
				Image: "monarch:test",
			},
		},
	}
	return mdb
}

// parseConfigYAML reads the rendered Secret's config.yaml and unmarshals
// into a generic map for assertion. Returns the map and the raw text for
// inclusion in failure messages.
func parseConfigYAML(t *testing.T, secret *corev1.Secret) (map[string]interface{}, string) {
	t.Helper()
	require.NotNil(t, secret.Data, "Secret.Data is nil")
	rawBytes, ok := secret.Data["config.yaml"]
	require.True(t, ok, "Secret missing config.yaml key; data keys=%v", keysOf(secret.Data))
	raw := string(rawBytes)
	var parsed map[string]interface{}
	require.NoError(t, yaml.Unmarshal(rawBytes, &parsed), "config.yaml is not valid YAML:\n%s", raw)
	return parsed, raw
}

func keysOf(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestBuildMonarchConfigSecret_Shipper exercises the full active-role contract.
// Each assertion corresponds to a field the agent / monarch binary requires.
// Updating this test is the canonical place to discover that the upstream
// schema has changed (a new required field, a renamed key, etc.) — the
// sentinel comments next to each assertion say where in the agent code the
// expectation lives.
func TestBuildMonarchConfigSecret_Shipper(t *testing.T) {
	mdb := monarchTestMDB(mdbv1.MonarchRoleActive)
	srcURI := "mongodb://rs-test-0.svc.ns.svc.cluster.local:27017,rs-test-1.svc.ns.svc.cluster.local:27017/?replicaSet=rs-test"

	cm := BuildMonarchConfigSecret(mdb, "ns", srcURI, "mms-shipper", "secret123", "rs-test-monarch-secrets")
	parsed, raw := parseConfigYAML(t, cm)

	// Top-level keys the binary parses; missing keys = lazy-fail at runtime.
	for _, key := range []string{
		"clusterPrefix",
		"shardId",
		"replSetName",
		"replSetHosts",
		"srcURI",
		"backupMongoNodeURI",
		"aws",
		"mode",         // shipperOnly default → blocks FCBIS; must be set
		"clusterStore", // without this the standby's injector 404s on cluster_manifest
		"securityKeyFile",
	} {
		assert.Contains(t, parsed, key, "shipper config missing required key %q. Full config:\n%s", key, raw)
	}

	// mode must be shipperAndSnapshotter — without snapshotter the standby
	// agent loops on DownloadFCBIS forever.
	assert.Equal(t, "shipperAndSnapshotter", parsed["mode"])

	// clusterStore.{initialize,clusterId,allShardIds,shardDriftLimit} —
	// shipper InitializeClusterStore validates these. shardDriftLimit must
	// be > 0 (binary literally rejects 0).
	cs, ok := parsed["clusterStore"].(map[string]interface{})
	require.True(t, ok, "clusterStore is not a map: %T", parsed["clusterStore"])
	assert.Equal(t, true, cs["initialize"], "clusterStore.initialize must be true so the manifest gets written")
	assert.NotEmpty(t, cs["clusterId"], "clusterStore.clusterId must be set")
	assert.NotEmpty(t, cs["allShardIds"], "clusterStore.allShardIds must list at least one shard")
	drift, ok := cs["shardDriftLimit"].(int)
	require.True(t, ok, "shardDriftLimit not an int: %T", cs["shardDriftLimit"])
	assert.Greater(t, drift, 0, "shardDriftLimit must be > 0 (binary rejects 0)")

	// Credentials embedded in URIs (mms-shipper@admin auth for $_backupFile).
	assert.Contains(t, parsed["srcURI"], "mms-shipper:secret123@", "srcURI must embed mms-shipper credentials")
	assert.Contains(t, parsed["backupMongoNodeURI"], "mms-shipper:secret123@", "backupMongoNodeURI must embed creds")
	assert.Contains(t, parsed["srcURI"], "authSource=admin")
	assert.Contains(t, parsed["backupMongoNodeURI"], "directConnection=true",
		"snapshotter requires directConnection URI; rejects RS-style")

	// Keyfile path matches the constant the volume mount uses.
	assert.Equal(t, MonarchKeyfilePath, parsed["securityKeyFile"],
		"securityKeyFile YAML key (NOT securityKeyFilePath) must match the mounted path")
}

// TestBuildMonarchConfigSecret_Injector exercises the standby-role contract.
// Note the injector binary has no mode flag and no clusterStore block.
func TestBuildMonarchConfigSecret_Injector(t *testing.T) {
	mdb := monarchTestMDB(mdbv1.MonarchRoleStandby)
	srcURI := "mongodb://rs-test-0.svc.ns.svc.cluster.local:27017/?replicaSet=rs-test"

	cm := BuildMonarchConfigSecret(mdb, "ns", srcURI, "", "", "rs-test-monarch-secrets")
	parsed, raw := parseConfigYAML(t, cm)

	for _, key := range []string{
		"clusterPrefix", "shardId", "replSetName", "replSetHosts",
		"srcURI", "backupMongoNodeURI", "aws",
		"securityKeyFile",
	} {
		assert.Contains(t, parsed, key, "injector config missing required key %q. Full config:\n%s", key, raw)
	}

	assert.NotContains(t, parsed, "mode", "injector binary has no mode flag")
	assert.NotContains(t, parsed, "clusterStore", "clusterStore is shipper-only")
	assert.Equal(t, MonarchKeyfilePath, parsed["securityKeyFile"])
}

// TestBuildMonarchConfigSecret_NoSecretSkipsKeyfile asserts the SCRAM-disabled
// path: when the operator passes an empty Secret name, the YAML omits
// securityKeyFile so the binary doesn't try to open a non-existent file.
func TestBuildMonarchConfigSecret_NoSecretSkipsKeyfile(t *testing.T) {
	mdb := monarchTestMDB(mdbv1.MonarchRoleActive)
	cm := BuildMonarchConfigSecret(mdb, "ns", "mongodb://rs-test-0:27017", "", "", "")
	_, raw := parseConfigYAML(t, cm)
	assert.False(t, strings.Contains(raw, "securityKeyFile"),
		"securityKeyFile must be absent when no Secret is passed (SCRAM disabled path)")
}
