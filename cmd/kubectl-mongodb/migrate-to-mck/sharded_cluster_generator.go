package migratetomck

import (
	"fmt"
	"reflect"

	"sigs.k8s.io/controller-runtime/pkg/client"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
)

func generateShardedCluster(ac *om.AutomationConfig, opts GenerateOptions) (client.Object, string, error) {
	acCluster := ac.Deployment.GetShardedClusters()[0]
	acShards := acCluster.Shards()
	if len(acShards) == 0 {
		return nil, "", fmt.Errorf("sharded cluster %q has no shards", acCluster.Name())
	}

	acClusterName := acCluster.Name()
	k8sResourceName := resolveK8sResourceName(acClusterName, opts)
	if k8sResourceName == "" {
		return nil, "", fmt.Errorf("sharded cluster name %q cannot be normalized to a valid Kubernetes resource name. Use --resource-name-override to provide one", acClusterName)
	}
	if errs := k8svalidation.IsDNS1123Subdomain(k8sResourceName); len(errs) > 0 {
		return nil, "", fmt.Errorf("resource name %q is not a valid Kubernetes resource name: %s", k8sResourceName, errs[0])
	}

	rsMap := buildReplicaSetMap(ac.Deployment.GetReplicaSets())

	configRS, ok := rsMap[acCluster.ConfigServerRsName()]
	if !ok {
		return nil, "", fmt.Errorf("config server replica set %q not found", acCluster.ConfigServerRsName())
	}

	shardRSes := make([]om.ReplicaSet, 0, len(acShards))
	for _, s := range acShards {
		rs, ok := rsMap[s.Rs()]
		if !ok {
			return nil, "", fmt.Errorf("shard %q replica set %q not found", s.Id(), s.Rs())
		}
		shardRSes = append(shardRSes, rs)
	}

	mongosProcs := activeMongosProcesses(ac.Deployment.GetProcesses())

	processMap := ac.Deployment.ProcessMap()
	_, version, fcv := om.ExtractMemberInfo(shardRSes[0].Members(), processMap)

	externalMembers := buildShardedExternalMembers(configRS, shardRSes, mongosProcs, processMap)

	spec, err := buildShardedClusterSpec(ac, opts, k8sResourceName, version, fcv, acShards, externalMembers)
	if err != nil {
		return nil, "", fmt.Errorf("failed to build MongoDB spec: %w", err)
	}

	overrides := buildShardedClusterOverrides(k8sResourceName, acClusterName, configRS, acShards)
	overrides.ConfigSrvSpec = buildShardedComponentSpec(ac.AgentSSL, processMap, configRS.Members())
	// Shards may differ in their mongod settings. When they all agree, that shared config
	// belongs in spec.shard. When they differ, spec.shard is left absent — populating it
	// from one shard would assert that shard's settings about all the others — and each
	// shard states its own config in an override instead.
	shardConfigs := shardAdditionalMongodConfigs(ac.AgentSSL, processMap, shardRSes)
	if common, uniform := commonShardConfig(shardConfigs); uniform {
		if common != nil {
			overrides.ShardSpec = &mdbv1.ShardedClusterComponentSpec{AdditionalMongodConfig: common}
		}
	} else {
		overrides.ShardOverrides = buildShardOverrides(k8sResourceName, shardConfigs)
	}
	overrides.MongosSpec = buildMongosComponentSpec(ac.AgentSSL, mongosProcs)
	spec.ShardedClusterSpec = overrides

	cr := &mdbv1.MongoDB{
		TypeMeta:   metav1.TypeMeta{APIVersion: "mongodb.com/v1", Kind: "MongoDB"},
		ObjectMeta: buildCRObjectMeta(k8sResourceName, opts.Namespace),
		Spec:       spec,
	}
	return cr, k8sResourceName, nil
}

func activeMongosProcesses(procs []om.Process) []om.Process {
	var mongos []om.Process
	for _, p := range procs {
		if p.ProcessType() != om.ProcessTypeMongos || p.IsDisabled() {
			continue
		}
		mongos = append(mongos, p)
	}
	return mongos
}

func buildShardedClusterSpec(ac *om.AutomationConfig, opts GenerateOptions, k8sResourceName, version, fcv string, acShards []om.Shard, externalMembers []mdbv1.ExternalMember) (mdbv1.MongoDbSpec, error) {
	common, err := buildDbCommonSpec(ac, opts, version, fcv, mdbv1.ShardedCluster, k8sResourceName)
	if err != nil {
		return mdbv1.MongoDbSpec{}, err
	}
	common.AdditionalMongodConfig = nil // ShardedCluster rejects the top-level field; each component carries its own below

	return mdbv1.MongoDbSpec{
		DbCommonSpec:    common,
		ExternalMembers: externalMembers,
		MongodbShardedClusterSizeConfig: status.MongodbShardedClusterSizeConfig{
			// ShardCount is topology and stays as the number of shards in the cluster.
			// The per-node counts start at 0 so that only Kubernetes members are counted here,
			// mirroring the replica set Members field. The existing VM nodes stay in ExternalMembers
			// and Kubernetes members scale up from 0.
			ShardCount:           len(acShards),
			MongodsPerShardCount: 0,
			ConfigServerCount:    0,
			MongosCount:          0,
		},
	}, nil
}

// buildShardedClusterOverrides returns a ShardedClusterSpec with only the fields that
// differ from the K8s defaults. When all AC names match the defaults the spec is empty.
func buildShardedClusterOverrides(k8sResourceName, acClusterName string, configRS om.ReplicaSet, acShards []om.Shard) mdbv1.ShardedClusterSpec {
	var configSrvOverride string
	if configRS.Name() != k8sResourceName+"-config" {
		configSrvOverride = configRS.Name()
	}

	var clusterNameOverride string
	if acClusterName != k8sResourceName {
		clusterNameOverride = acClusterName
	}

	shardNameOverrides := make([]mdbv1.ShardNameOverride, 0, len(acShards))
	for i, s := range acShards {
		k8sName := fmt.Sprintf("%s-%d", k8sResourceName, i)
		sno := mdbv1.ShardNameOverride{ShardName: k8sName}
		if s.Id() != k8sName || s.Rs() != k8sName {
			sno.ShardId = s.Id()
			sno.ReplicaSetName = s.Rs()
		}
		shardNameOverrides = append(shardNameOverrides, sno)
	}

	return mdbv1.ShardedClusterSpec{
		ConfigServerNameOverride:   configSrvOverride,
		ShardedClusterNameOverride: clusterNameOverride,
		ShardNameOverrides:         shardNameOverrides,
	}
}

// shardComponentConfig extracts the additionalMongodConfig for one replica-set component,
// using its first member as representative. Returns nil when the component has no members,
// its first member is absent from the process map, or nothing survives stripping the
// operator-managed infrastructure fields.
func shardComponentConfig(agentSSL *om.AgentSSL, processMap map[string]om.Process, members []om.ReplicaSetMember) *mdbv1.AdditionalMongodConfig {
	if len(members) == 0 {
		return nil
	}
	proc, ok := processMap[members[0].Name()]
	if !ok {
		return nil
	}
	return applyClientCertificateMode(agentSSL, proc.AdditionalMongodConfig())
}

// buildShardedComponentSpec wraps shardComponentConfig in a component spec, or returns nil
// when there is no config to carry.
func buildShardedComponentSpec(agentSSL *om.AgentSSL, processMap map[string]om.Process, members []om.ReplicaSetMember) *mdbv1.ShardedClusterComponentSpec {
	cfg := shardComponentConfig(agentSSL, processMap, members)
	if cfg == nil {
		return nil
	}
	return &mdbv1.ShardedClusterComponentSpec{AdditionalMongodConfig: cfg}
}

// shardAdditionalMongodConfigs returns each shard's additionalMongodConfig, index-aligned
// with shardRSes. Entries are nil for shards whose config could not be determined.
func shardAdditionalMongodConfigs(agentSSL *om.AgentSSL, processMap map[string]om.Process, shardRSes []om.ReplicaSet) []*mdbv1.AdditionalMongodConfig {
	configs := make([]*mdbv1.AdditionalMongodConfig, 0, len(shardRSes))
	for _, rs := range shardRSes {
		configs = append(configs, shardComponentConfig(agentSSL, processMap, rs.Members()))
	}
	return configs
}

// commonShardConfig reports whether every shard shares one additionalMongodConfig, and if
// so returns it. Comparison is on the marshalled map rather than pointer identity, so two
// independently built but equivalent configs count as identical and map key ordering cannot
// manufacture a false difference. When shards agree the caller emits spec.shard; when they
// differ, spec.shard is left absent because it would otherwise assert one shard's settings
// about all the others.
func commonShardConfig(configs []*mdbv1.AdditionalMongodConfig) (*mdbv1.AdditionalMongodConfig, bool) {
	if len(configs) == 0 {
		return nil, true
	}
	// ToMap is nil-safe and yields an empty map for a nil config, so nil and non-nil
	// configs compare correctly without special-casing.
	first := configs[0].ToMap()
	for _, cfg := range configs[1:] {
		if !reflect.DeepEqual(first, cfg.ToMap()) {
			return nil, false
		}
	}
	return configs[0], true
}

// buildShardOverrides emits one ShardOverride per shard that has a config, in ascending
// shard index. Shards are never grouped by identical config: an explicit entry per shard
// keeps the reader from having to infer which grouping is the default. Each entry carries
// the shard's complete config rather than a delta, because the operator replaces
// additionalMongodConfig wholesale rather than merging it
// (controllers/operator/mongodbshardedcluster_controller.go, processShardOverride).
// Shards with a nil config are skipped: an override with no additionalMongodConfig is a
// no-op in the operator.
func buildShardOverrides(k8sResourceName string, configs []*mdbv1.AdditionalMongodConfig) []mdbv1.ShardOverride {
	var overrides []mdbv1.ShardOverride
	for i, cfg := range configs {
		if cfg == nil {
			continue
		}
		overrides = append(overrides, mdbv1.ShardOverride{
			// Must be the K8s StatefulSet name; sharded-cluster validation rejects any
			// other form. Same formula as ShardNameOverrides above.
			ShardNames: []string{fmt.Sprintf("%s-%d", k8sResourceName, i)},
			ShardedClusterComponentOverrideSpec: mdbv1.ShardedClusterComponentOverrideSpec{
				AdditionalMongodConfig: cfg,
			},
		})
	}
	return overrides
}

// buildMongosComponentSpec extracts additionalMongodConfig for the mongos component using the first active process.
func buildMongosComponentSpec(agentSSL *om.AgentSSL, mongosProcs []om.Process) *mdbv1.ShardedClusterComponentSpec {
	if len(mongosProcs) == 0 {
		return nil
	}
	cfg := applyClientCertificateMode(agentSSL, mongosProcs[0].AdditionalMongodConfig())
	if cfg == nil {
		return nil
	}
	return &mdbv1.ShardedClusterComponentSpec{AdditionalMongodConfig: cfg}
}

// buildShardedExternalMembers assembles the externalMembers list: config server, then shards, then mongos.
func buildShardedExternalMembers(
	configRS om.ReplicaSet,
	shardRSes []om.ReplicaSet,
	mongosProcs []om.Process,
	processMap map[string]om.Process,
) []mdbv1.ExternalMember {
	var procs []om.Process
	procs = append(procs, rsProcesses(configRS, processMap)...)
	for _, rs := range shardRSes {
		procs = append(procs, rsProcesses(rs, processMap)...)
	}
	procs = append(procs, mongosProcs...)

	return om.ExtractExternalMembers(procs)
}

// rsProcesses returns the processes backing a replica set's members, in member order.
func rsProcesses(rs om.ReplicaSet, processMap map[string]om.Process) []om.Process {
	procs := make([]om.Process, 0, len(rs.Members()))
	for _, m := range rs.Members() {
		if proc, ok := processMap[m.Name()]; ok {
			procs = append(procs, proc)
		}
	}
	return procs
}

// buildReplicaSetMap indexes replica sets by name for O(1) lookup.
func buildReplicaSetMap(rsList []om.ReplicaSet) map[string]om.ReplicaSet {
	m := make(map[string]om.ReplicaSet, len(rsList))
	for _, rs := range rsList {
		m[rs.Name()] = rs
	}
	return m
}
