package migratetomck

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/ldap"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/maputil"
)

func secretsByName(objs []client.Object) map[string]string {
	out := map[string]string{}
	for _, o := range objs {
		if s, ok := o.(*corev1.Secret); ok {
			out[s.Name] = s.StringData[passwordSecretDataKey]
		}
	}
	return out
}

// TestGenerateExtraResources_LDAPAgentPassword verifies that an LDAP agent's external password is
// carried over as a generated Secret alongside the bind-query password.
func TestGenerateExtraResources_LDAPAgentPassword(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{"processes": []any{}, "replicaSets": []any{}})
	ac.Ldap = &ldap.Ldap{Servers: "openldap:389", BindQueryUser: "cn=admin,dc=example,dc=org", BindQueryPassword: "bindpw"}
	ac.Auth.AutoAuthMechanism = "PLAIN"
	ac.Auth.AutoPwd = "agent-ldap-pw"

	got := secretsByName(generateExtraResources(ac, GenerateOptions{Namespace: "mongodb"}))
	assert.Equal(t, "bindpw", got[LdapBindQuerySecretName])
	assert.Equal(t, "agent-ldap-pw", got[LdapAgentPasswordSecretName])
}

// TestGenerateExtraResources_ScramAgentNoLDAPPassword verifies a SCRAM agent does not get an LDAP
// agent-password Secret (only LDAP agents authenticate with an external password).
func TestGenerateExtraResources_ScramAgentNoLDAPPassword(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{"processes": []any{}, "replicaSets": []any{}})
	ac.Auth.AutoAuthMechanism = "SCRAM-SHA-256"
	ac.Auth.AutoPwd = "scram-agent-pw"

	got := secretsByName(generateExtraResources(ac, GenerateOptions{Namespace: "mongodb"}))
	_, exists := got[LdapAgentPasswordSecretName]
	assert.False(t, exists, "SCRAM agent should not produce an LDAP agent-password secret")
}

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

	obj, err := GenerateMongoDBCR(ac, opts)
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

	obj, err := GenerateMongoDBCR(ac, opts)
	require.NoError(t, err)
	assert.Equal(t, "my-replicaset", obj.GetName())
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

	_, err := GenerateMongoDBCR(ac, opts)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no replica sets found")
}

func TestGenerateMongoDBCR_ShardedTopologyCounts(t *testing.T) {
	ac := loadTestAutomationConfig(t, "singlecluster/shardedcluster/default_config_rs/default_config_rs_input.json")

	opts := withDeploymentData(ac, GenerateOptions{
		CredentialsSecretName: "my-credentials",
		ConfigMapName:         "my-om-config",
	})

	obj, err := GenerateMongoDBCR(ac, opts)
	require.NoError(t, err)
	yamlOutput, err := marshalCRToYAML(obj)
	require.NoError(t, err)

	assert.Contains(t, yamlOutput, "type: ShardedCluster")
	// shardCount reflects the full topology, VM shards included, since it drives the shard index.
	assert.Contains(t, yamlOutput, "shardCount: 2")
	// The per-node counts start at 0 so only Kubernetes members are counted, like the replica set
	// Members field. Existing VM nodes stay in externalMembers, so the zero counts are omitted.
	assert.NotContains(t, yamlOutput, "mongodsPerShardCount:")
	assert.NotContains(t, yamlOutput, "configServerCount:")
	assert.NotContains(t, yamlOutput, "mongosCount:")
}

func TestBuildShardedClusterOverrides_SplitShardNames(t *testing.T) {
	// The shard _id differs from its replica set name, so each shard must carry both a
	// shardId and a replicaSetName override. The config server and cluster names also
	// differ from the K8s defaults, so their overrides are set too.
	configRS := om.NewReplicaSet("cfg-rs", "7.0.12-ent")
	acShards := []om.Shard{
		{"_id": "sh0", "rs": "rs-sh0"},
		{"_id": "sh1", "rs": "rs-sh1"},
	}

	overrides := buildShardedClusterOverrides("my-cluster", "my-cluster", configRS, acShards)

	assert.Equal(t, "cfg-rs", overrides.ConfigServerNameOverride)
	assert.Empty(t, overrides.ShardedClusterNameOverride, "cluster name matches the resource name, no override expected")
	require.Len(t, overrides.ShardNameOverrides, 2)
	assert.Equal(t, mdbv1.ShardNameOverride{ShardName: "my-cluster-0", ShardId: "sh0", ReplicaSetName: "rs-sh0"}, overrides.ShardNameOverrides[0])
	assert.Equal(t, mdbv1.ShardNameOverride{ShardName: "my-cluster-1", ShardId: "sh1", ReplicaSetName: "rs-sh1"}, overrides.ShardNameOverrides[1])
}

func TestBuildShardedClusterOverrides_DefaultNames(t *testing.T) {
	// When all AC names already match the K8s defaults, no overrides are produced beyond the
	// shard name entries, which carry only the derived shardName.
	configRS := om.NewReplicaSet("my-cluster-config", "7.0.12-ent")
	acShards := []om.Shard{
		{"_id": "my-cluster-0", "rs": "my-cluster-0"},
		{"_id": "my-cluster-1", "rs": "my-cluster-1"},
	}

	overrides := buildShardedClusterOverrides("my-cluster", "my-cluster", configRS, acShards)

	assert.Empty(t, overrides.ConfigServerNameOverride)
	assert.Empty(t, overrides.ShardedClusterNameOverride)
	require.Len(t, overrides.ShardNameOverrides, 2)
	assert.Equal(t, mdbv1.ShardNameOverride{ShardName: "my-cluster-0"}, overrides.ShardNameOverrides[0])
	assert.Equal(t, mdbv1.ShardNameOverride{ShardName: "my-cluster-1"}, overrides.ShardNameOverrides[1])
}

func TestGenerateMongoDBCR_ShardedMissingShardReplicaSet(t *testing.T) {
	ac := loadTestAutomationConfig(t, "singlecluster/shardedcluster/default_config_rs/default_config_rs_input.json")

	rss := ac.Deployment.GetReplicaSets()
	kept := make([]any, 0, len(rss))
	for _, rs := range rss {
		if rs.Name() != "shard0" {
			kept = append(kept, map[string]any(rs))
		}
	}
	ac.Deployment["replicaSets"] = kept

	opts := withDeploymentData(ac, GenerateOptions{
		CredentialsSecretName: "my-credentials",
		ConfigMapName:         "my-om-config",
	})

	_, err := GenerateMongoDBCR(ac, opts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "shard0")
	assert.Contains(t, err.Error(), "not found")
}

func TestBuildDbCommonSpec_DownloadBase(t *testing.T) {
	makeAC := func(downloadBase string) *om.AutomationConfig {
		ac := baseValidReplicaSetAC()
		options := ac.Deployment["options"].(map[string]interface{})
		options["downloadBase"] = downloadBase
		ac.Deployment["options"] = options
		return ac
	}
	opts := GenerateOptions{ConfigMapName: "cm", CredentialsSecretName: "creds", Namespace: "mongodb"}

	t.Run("non-default value is carried over", func(t *testing.T) {
		ac := makeAC("/opt/mongodb/automation")
		spec, err := buildDbCommonSpec(ac, opts, "7.0.12-ent", "", mdbv1.ReplicaSet, "my-rs")
		require.NoError(t, err)
		assert.Equal(t, "/opt/mongodb/automation", spec.DownloadBase)
	})

	t.Run("default value is not set", func(t *testing.T) {
		ac := makeAC(util.DefaultPvcMmsMountPath)
		spec, err := buildDbCommonSpec(ac, opts, "7.0.12-ent", "", mdbv1.ReplicaSet, "my-rs")
		require.NoError(t, err)
		assert.Equal(t, "", spec.DownloadBase)
	})
}

// shardRSWithConfig builds a single-member shard replica set named rsName whose member
// host is "<rsName>-0", and registers that member's process in processMap with the given
// args2_6 storage config. Pass a nil cacheSizeGB to register a process with no args at all,
// which is how Process.AdditionalMongodConfig comes back nil.
func shardRSWithConfig(t *testing.T, processMap map[string]om.Process, rsName string, cacheSizeGB *int) om.ReplicaSet {
	t.Helper()
	host := rsName + "-0"
	rs := om.NewReplicaSet(rsName, "7.0.12-ent")
	rs["members"] = []interface{}{map[string]interface{}{"host": host, "_id": 0, "priority": 1, "votes": 1}}
	if cacheSizeGB == nil {
		processMap[host] = om.Process{"name": host}
		return rs
	}
	processMap[host] = om.Process{
		"name": host,
		"args2_6": map[string]interface{}{
			"storage": map[string]interface{}{
				"wiredTiger": map[string]interface{}{
					"engineConfig": map[string]interface{}{"cacheSizeGB": *cacheSizeGB},
				},
			},
		},
	}
	return rs
}

func intPtr(i int) *int { return &i }

func TestShardAdditionalMongodConfigs_ExtractsPerShard(t *testing.T) {
	// Each shard's config comes from its own first member, not from shard 0's.
	processMap := map[string]om.Process{}
	shardRSes := []om.ReplicaSet{
		shardRSWithConfig(t, processMap, "shard0", intPtr(4)),
		shardRSWithConfig(t, processMap, "shard1", intPtr(8)),
	}

	configs, err := shardAdditionalMongodConfigs(nil, processMap, shardRSes)
	require.NoError(t, err)

	require.Len(t, configs, 2)
	assert.Equal(t, 4, readCacheSizeGB(t, configs[0]))
	assert.Equal(t, 8, readCacheSizeGB(t, configs[1]))
}

func TestShardAdditionalMongodConfigs_ErrorWhenNoSourceProcess(t *testing.T) {
	// A shard with no usable source process is an inconsistent automation config: the
	// settings exist in Ops Manager but cannot be read, so generation stops rather than
	// producing a resource that silently omits them.
	processMap := map[string]om.Process{}
	orphan := om.NewReplicaSet("shard0", "7.0.12-ent")
	orphan["members"] = []interface{}{map[string]interface{}{"host": "missing-host", "_id": 0, "priority": 1, "votes": 1}}

	_, err := shardAdditionalMongodConfigs(nil, processMap, []om.ReplicaSet{orphan})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "shard0")
}

func TestShardAdditionalMongodConfigs_FallsBackToLaterMember(t *testing.T) {
	// The first member missing a process is not fatal on its own: pickSourceProcess moves
	// on to the next voting member, so the config is read from that one instead.
	processMap := map[string]om.Process{
		"shard0-1": {
			"name": "shard0-1",
			"args2_6": map[string]interface{}{
				"storage": map[string]interface{}{
					"wiredTiger": map[string]interface{}{
						"engineConfig": map[string]interface{}{"cacheSizeGB": 7},
					},
				},
			},
		},
	}
	rs := om.NewReplicaSet("shard0", "7.0.12-ent")
	rs["members"] = []interface{}{
		map[string]interface{}{"host": "shard0-0", "_id": 0, "priority": 1, "votes": 1},
		map[string]interface{}{"host": "shard0-1", "_id": 1, "priority": 1, "votes": 1},
	}

	configs, err := shardAdditionalMongodConfigs(nil, processMap, []om.ReplicaSet{rs})

	require.NoError(t, err)
	require.Len(t, configs, 1)
	assert.Equal(t, 7, readCacheSizeGB(t, configs[0]))
}

func TestShardAdditionalMongodConfigs_SkipsNonVotingMember(t *testing.T) {
	// A hidden or zero-vote member can carry deliberately different settings, so it must
	// not be the source even when it comes first and resolves to a process.
	mkProc := func(name string, cacheSizeGB int) om.Process {
		return om.Process{
			"name": name,
			"args2_6": map[string]interface{}{
				"storage": map[string]interface{}{
					"wiredTiger": map[string]interface{}{
						"engineConfig": map[string]interface{}{"cacheSizeGB": cacheSizeGB},
					},
				},
			},
		}
	}
	processMap := map[string]om.Process{
		"shard0-0": mkProc("shard0-0", 1),
		"shard0-1": mkProc("shard0-1", 9),
	}
	rs := om.NewReplicaSet("shard0", "7.0.12-ent")
	rs["members"] = []interface{}{
		map[string]interface{}{"host": "shard0-0", "_id": 0, "priority": 0, "votes": 0},
		map[string]interface{}{"host": "shard0-1", "_id": 1, "priority": 1, "votes": 1},
	}

	configs, err := shardAdditionalMongodConfigs(nil, processMap, []om.ReplicaSet{rs})

	require.NoError(t, err)
	require.Len(t, configs, 1)
	assert.Equal(t, 9, readCacheSizeGB(t, configs[0]), "config must come from the voting member")
}

func TestShardComponentConfig_NilIsOmittedFromTheGeneratedResource(t *testing.T) {
	// A component with nothing but operator-managed fields must produce no
	// additionalMongodConfig key at all. The field is an omitempty pointer, so only a nil
	// config is dropped: an empty one would serialise as "additionalMongodConfig: {}".
	processMap := map[string]om.Process{}
	rs := shardRSWithConfig(t, processMap, "shard0", nil)

	cfg, err := shardComponentConfig(nil, processMap, rs.Members())
	require.NoError(t, err)
	require.Nil(t, cfg, "a component with no user config must yield nil, not an empty config")

	spec, err := buildShardedComponentSpec(nil, processMap, rs.Members())
	require.NoError(t, err)
	assert.Nil(t, spec, "a nil config must leave the component out of the resource entirely")
}

func TestShardAdditionalMongodConfigs_ErrorForEmptyMembers(t *testing.T) {
	// A shard replica set with no members has no source process either, so it is rejected
	// for the same reason: nothing to read the shard's mongod settings from.
	empty := om.NewReplicaSet("shard1", "7.0.12-ent")
	empty["members"] = []interface{}{}

	_, err := shardAdditionalMongodConfigs(nil, map[string]om.Process{}, []om.ReplicaSet{empty})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "shard1")
}

func TestCommonShardConfig_AllIdentical(t *testing.T) {
	// Two shards built independently with the same settings must compare equal, since
	// comparison is on the marshalled map rather than pointer identity.
	processMap := map[string]om.Process{}
	shardRSes := []om.ReplicaSet{
		shardRSWithConfig(t, processMap, "shard0", intPtr(4)),
		shardRSWithConfig(t, processMap, "shard1", intPtr(4)),
	}
	configs, err := shardAdditionalMongodConfigs(nil, processMap, shardRSes)
	require.NoError(t, err)

	common, uniform := commonShardConfig(configs)

	assert.True(t, uniform)
	assert.Equal(t, 4, readCacheSizeGB(t, common))
}

func TestCommonShardConfig_AllNil(t *testing.T) {
	// No shard has any config: uniform, and the shared config is nil so spec.shard stays absent.
	common, uniform := commonShardConfig([]*mdbv1.AdditionalMongodConfig{nil, nil})

	assert.True(t, uniform)
	assert.Nil(t, common)
}

func TestCommonShardConfig_Differing(t *testing.T) {
	processMap := map[string]om.Process{}
	shardRSes := []om.ReplicaSet{
		shardRSWithConfig(t, processMap, "shard0", intPtr(4)),
		shardRSWithConfig(t, processMap, "shard1", intPtr(8)),
	}
	configs, err := shardAdditionalMongodConfigs(nil, processMap, shardRSes)
	require.NoError(t, err)

	common, uniform := commonShardConfig(configs)

	assert.False(t, uniform)
	assert.Nil(t, common)
}

func TestCommonShardConfig_NilVersusSetIsNotUniform(t *testing.T) {
	// A nil config and an empty-but-non-nil config both flatten to an empty map, but a nil
	// alongside a populated config is genuine heterogeneity.
	processMap := map[string]om.Process{}
	shardRSes := []om.ReplicaSet{
		shardRSWithConfig(t, processMap, "shard0", nil),
		shardRSWithConfig(t, processMap, "shard1", intPtr(8)),
	}
	configs, err := shardAdditionalMongodConfigs(nil, processMap, shardRSes)
	require.NoError(t, err)

	_, uniform := commonShardConfig(configs)

	assert.False(t, uniform)
}

func TestCommonShardConfig_SingleShard(t *testing.T) {
	// A one-shard cluster is trivially uniform, so it keeps using spec.shard.
	processMap := map[string]om.Process{}
	shardRSes := []om.ReplicaSet{shardRSWithConfig(t, processMap, "shard0", intPtr(4))}
	configs, err := shardAdditionalMongodConfigs(nil, processMap, shardRSes)
	require.NoError(t, err)

	common, uniform := commonShardConfig(configs)

	assert.True(t, uniform)
	assert.Equal(t, 4, readCacheSizeGB(t, common))
}

func TestBuildShardOverrides_OneEntryPerShardInIndexOrder(t *testing.T) {
	// Every shard gets its own entry, named with the K8s StatefulSet name, in ascending
	// shard index. Identical shards are not grouped: each line answers exactly one question.
	processMap := map[string]om.Process{}
	shardRSes := []om.ReplicaSet{
		shardRSWithConfig(t, processMap, "shard0", intPtr(4)),
		shardRSWithConfig(t, processMap, "shard1", intPtr(4)),
		shardRSWithConfig(t, processMap, "shard2", intPtr(8)),
	}
	configs, err := shardAdditionalMongodConfigs(nil, processMap, shardRSes)
	require.NoError(t, err)

	overrides := buildShardOverrides("my-cluster", configs)

	require.Len(t, overrides, 3)
	assert.Equal(t, []string{"my-cluster-0"}, overrides[0].ShardNames)
	assert.Equal(t, []string{"my-cluster-1"}, overrides[1].ShardNames)
	assert.Equal(t, []string{"my-cluster-2"}, overrides[2].ShardNames)
	assert.Equal(t, 4, readCacheSizeGB(t, overrides[0].AdditionalMongodConfig))
	assert.Equal(t, 4, readCacheSizeGB(t, overrides[1].AdditionalMongodConfig))
	assert.Equal(t, 8, readCacheSizeGB(t, overrides[2].AdditionalMongodConfig))
}

func TestBuildShardOverrides_SkipsShardsWithNilConfig(t *testing.T) {
	// An override carrying no additionalMongodConfig is a no-op in the operator, so shards
	// with nothing to say are omitted rather than emitted as empty entries.
	processMap := map[string]om.Process{}
	shardRSes := []om.ReplicaSet{
		shardRSWithConfig(t, processMap, "shard0", nil),
		shardRSWithConfig(t, processMap, "shard1", intPtr(8)),
	}
	configs, err := shardAdditionalMongodConfigs(nil, processMap, shardRSes)
	require.NoError(t, err)

	overrides := buildShardOverrides("my-cluster", configs)

	require.Len(t, overrides, 1)
	assert.Equal(t, []string{"my-cluster-1"}, overrides[0].ShardNames, "shard index must survive the skip")
	assert.Equal(t, 8, readCacheSizeGB(t, overrides[0].AdditionalMongodConfig))
}

func TestBuildShardOverrides_EmptyWhenNoShardHasConfig(t *testing.T) {
	overrides := buildShardOverrides("my-cluster", []*mdbv1.AdditionalMongodConfig{nil, nil})

	assert.Empty(t, overrides)
}

// readCacheSizeGB pulls storage.wiredTiger.engineConfig.cacheSizeGB out of an
// AdditionalMongodConfig. The wrapped map is private, so ToMap is the only way in, and the
// value survives a JSON round-trip inside Process.AdditionalMongodConfig as a float64.
func readCacheSizeGB(t *testing.T, cfg *mdbv1.AdditionalMongodConfig) int {
	t.Helper()
	require.NotNil(t, cfg)
	return maputil.ReadMapValueAsInt(cfg.ToMap(), "storage", "wiredTiger", "engineConfig", "cacheSizeGB")
}

func TestGenerateMongoDBCR_HomogeneousShardsUseSpecShard(t *testing.T) {
	// The existing two-shard fixture has identical shard configs, so output must keep using
	// spec.shard with no shardOverrides. This is the guard against churning the common case.
	ac := loadTestAutomationConfig(t, "singlecluster/shardedcluster/default_config_rs/default_config_rs_input.json")
	opts := withDeploymentData(ac, GenerateOptions{
		CredentialsSecretName: "my-credentials",
		ConfigMapName:         "my-om-config",
	})

	obj, err := GenerateMongoDBCR(ac, opts)
	require.NoError(t, err)
	mdb, ok := obj.(*mdbv1.MongoDB)
	require.True(t, ok)

	assert.Empty(t, mdb.Spec.ShardOverrides, "identical shards must not produce overrides")
}

func TestGenerateMongoDBCR_HeterogeneousShardsUseOverridesAndOmitSpecShard(t *testing.T) {
	// Shard 1 carries a mongod setting shard 0 does not. spec.shard must be left absent
	// rather than assert shard 0's settings about shard 1, and each shard gets an explicit
	// override.
	ac := loadTestAutomationConfig(t, "singlecluster/shardedcluster/default_config_rs/default_config_rs_input.json")
	setProcessCacheSizeGB(t, ac, "shard0-0", 4)
	setProcessCacheSizeGB(t, ac, "shard0-1", 4)
	setProcessCacheSizeGB(t, ac, "shard1-0", 8)
	setProcessCacheSizeGB(t, ac, "shard1-1", 8)
	// Give the config server a setting of its own so the assertion below is not vacuous.
	setProcessCacheSizeGB(t, ac, "my-sharded-cluster-config-0", 2)
	setProcessCacheSizeGB(t, ac, "my-sharded-cluster-config-1", 2)

	opts := withDeploymentData(ac, GenerateOptions{
		CredentialsSecretName: "my-credentials",
		ConfigMapName:         "my-om-config",
	})

	obj, err := GenerateMongoDBCR(ac, opts)
	require.NoError(t, err)
	mdb, ok := obj.(*mdbv1.MongoDB)
	require.True(t, ok)

	assert.Nil(t, mdb.Spec.ShardSpec, "spec.shard must be absent when shards differ")
	require.Len(t, mdb.Spec.ShardOverrides, 2)
	assert.Equal(t, []string{"my-sharded-cluster-0"}, mdb.Spec.ShardOverrides[0].ShardNames)
	assert.Equal(t, []string{"my-sharded-cluster-1"}, mdb.Spec.ShardOverrides[1].ShardNames)
	assert.Equal(t, 4, readCacheSizeGB(t, mdb.Spec.ShardOverrides[0].AdditionalMongodConfig))
	assert.Equal(t, 8, readCacheSizeGB(t, mdb.Spec.ShardOverrides[1].AdditionalMongodConfig))

	// The config server and mongos components are unaffected by shard heterogeneity.
	assert.NotNil(t, mdb.Spec.ConfigSrvSpec, "config server spec must still be populated")
}

// setProcessCacheSizeGB injects storage.wiredTiger.engineConfig.cacheSizeGB into the named
// process's args2_6, creating the intermediate maps as needed.
func setProcessCacheSizeGB(t *testing.T, ac *om.AutomationConfig, processName string, cacheSizeGB int) {
	t.Helper()
	for _, p := range ac.Deployment.GetProcesses() {
		if p.Name() != processName {
			continue
		}
		args, ok := p["args2_6"].(map[string]interface{})
		if !ok {
			args = map[string]interface{}{}
			p["args2_6"] = args
		}
		maputil.SetMapValue(args, cacheSizeGB, "storage", "wiredTiger", "engineConfig", "cacheSizeGB")
		return
	}
	t.Fatalf("process %q not found in the automation config", processName)
}

func TestGenerateMongoDBCR_PerShardMongotHostBecomesShardOverrides(t *testing.T) {
	// Each shard in a search-enabled sharded source points at its own mongot endpoint, so the
	// shards are heterogeneous and each states its own endpoint in an override. This is the
	// real-world case the synthetic cacheSizeGB fixture stands in for.
	ac := loadTestAutomationConfig(t, "singlecluster/shardedcluster/default_config_rs/default_config_rs_input.json")
	shard0Host := "mdb-search-0-svc.mongodb.svc.cluster.local:27027"
	shard1Host := "mdb-search-1-svc.mongodb.svc.cluster.local:27027"
	setSearchParameters(t, ac, "shard0-0", shard0Host)
	setSearchParameters(t, ac, "shard0-1", shard0Host)
	setSearchParameters(t, ac, "shard1-0", shard1Host)
	setSearchParameters(t, ac, "shard1-1", shard1Host)

	opts := withDeploymentData(ac, GenerateOptions{
		CredentialsSecretName: "my-credentials",
		ConfigMapName:         "my-om-config",
	})

	obj, err := GenerateMongoDBCR(ac, opts)
	require.NoError(t, err)
	mdb, ok := obj.(*mdbv1.MongoDB)
	require.True(t, ok)

	assert.Nil(t, mdb.Spec.ShardSpec, "differing mongot endpoints must not collapse into spec.shard")
	require.Len(t, mdb.Spec.ShardOverrides, 2)
	assert.Equal(t, []string{"my-sharded-cluster-0"}, mdb.Spec.ShardOverrides[0].ShardNames)
	assert.Equal(t, []string{"my-sharded-cluster-1"}, mdb.Spec.ShardOverrides[1].ShardNames)

	sp0 := readSetParameter(t, mdb.Spec.ShardOverrides[0].AdditionalMongodConfig)
	sp1 := readSetParameter(t, mdb.Spec.ShardOverrides[1].AdditionalMongodConfig)
	assert.Equal(t, shard0Host, sp0["mongotHost"])
	assert.Equal(t, shard1Host, sp1["mongotHost"])
	assert.Equal(t, true, sp0["useGrpcForSearch"], "non-endpoint search keys are carried too")
}

func TestGenerateMongoDBCR_UniformMongotHostStaysInSpecShard(t *testing.T) {
	// A single mongot endpoint shared by every shard is homogeneous, so it belongs in
	// spec.shard. This is the shape the existing sharded search e2e exercises.
	ac := loadTestAutomationConfig(t, "singlecluster/shardedcluster/default_config_rs/default_config_rs_input.json")
	host := "mdb-search-svc.mongodb.svc.cluster.local:27027"
	for _, name := range []string{"shard0-0", "shard0-1", "shard1-0", "shard1-1"} {
		setSearchParameters(t, ac, name, host)
	}

	opts := withDeploymentData(ac, GenerateOptions{
		CredentialsSecretName: "my-credentials",
		ConfigMapName:         "my-om-config",
	})

	obj, err := GenerateMongoDBCR(ac, opts)
	require.NoError(t, err)
	mdb, ok := obj.(*mdbv1.MongoDB)
	require.True(t, ok)

	assert.Empty(t, mdb.Spec.ShardOverrides)
	require.NotNil(t, mdb.Spec.ShardSpec)
	assert.Equal(t, host, readSetParameter(t, mdb.Spec.ShardSpec.AdditionalMongodConfig)["mongotHost"])
}

// setSearchParameters writes the six mongot setParameters onto the named process, using
// mongotHost for both endpoint keys the way the search controller does.
func setSearchParameters(t *testing.T, ac *om.AutomationConfig, processName, mongotHost string) {
	t.Helper()
	for _, p := range ac.Deployment.GetProcesses() {
		if p.Name() != processName {
			continue
		}
		args, ok := p["args2_6"].(map[string]interface{})
		if !ok {
			args = map[string]interface{}{}
			p["args2_6"] = args
		}
		args["setParameter"] = map[string]interface{}{
			"mongotHost":                                      mongotHost,
			"searchIndexManagementHostAndPort":                mongotHost,
			"skipAuthenticationToSearchIndexManagementServer": false,
			"skipAuthenticationToMongot":                      false,
			"searchTLSMode":                                   "disabled",
			"useGrpcForSearch":                                true,
		}
		return
	}
	t.Fatalf("process %q not found in the automation config", processName)
}

// readSetParameter pulls the setParameter submap out of an AdditionalMongodConfig.
func readSetParameter(t *testing.T, cfg *mdbv1.AdditionalMongodConfig) map[string]interface{} {
	t.Helper()
	require.NotNil(t, cfg)
	sp, ok := cfg.ToMap()["setParameter"].(map[string]interface{})
	require.True(t, ok, "expected a setParameter section, got %v", cfg.ToMap())
	return sp
}
