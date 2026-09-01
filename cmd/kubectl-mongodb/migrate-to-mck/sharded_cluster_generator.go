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

func generateShardedCluster(ac *om.AutomationConfig, opts GenerateOptions) (client.Object, error) {
	acCluster := ac.Deployment.GetShardedClusters()[0]
	acShards := acCluster.Shards()
	if len(acShards) == 0 {
		return nil, fmt.Errorf("sharded cluster %q has no shards", acCluster.Name())
	}

	acClusterName := acCluster.Name()
	k8sResourceName := resolveK8sResourceName(acClusterName, opts)
	if k8sResourceName == "" {
		return nil, fmt.Errorf("sharded cluster name %q cannot be normalized to a valid Kubernetes resource name. Use --resource-name-override to provide one", acClusterName)
	}
	if errs := k8svalidation.IsDNS1123Subdomain(k8sResourceName); len(errs) > 0 {
		return nil, fmt.Errorf("resource name %q is not a valid Kubernetes resource name: %s", k8sResourceName, errs[0])
	}

	rsMap := buildReplicaSetMap(ac.Deployment.GetReplicaSets())

	configRS, ok := rsMap[acCluster.ConfigServerRsName()]
	if !ok {
		return nil, fmt.Errorf("config server replica set %q not found", acCluster.ConfigServerRsName())
	}

	shardRSes := make([]om.ReplicaSet, 0, len(acShards))
	for _, s := range acShards {
		rs, ok := rsMap[s.Rs()]
		if !ok {
			return nil, fmt.Errorf("shard %q replica set %q not found", s.Id(), s.Rs())
		}
		shardRSes = append(shardRSes, rs)
	}

	mongosProcs := activeMongosProcesses(ac.Deployment.GetProcesses())

	processMap := ac.Deployment.ProcessMap()
	_, version, fcv := om.ExtractMemberInfo(shardRSes[0].Members(), processMap)

	externalMembers := buildShardedExternalMembers(configRS, shardRSes, mongosProcs, processMap)

	spec, err := buildShardedClusterSpec(ac, opts, k8sResourceName, version, fcv, acShards, externalMembers)
	if err != nil {
		return nil, fmt.Errorf("failed to build MongoDB spec: %w", err)
	}

	shardedClusterSpec := buildShardedClusterOverrides(k8sResourceName, acClusterName, configRS, acShards)
	shardedClusterSpec.ConfigSrvSpec, err = buildShardedComponentSpec(ac.AgentSSL, processMap, configRS.Members())
	if err != nil {
		return nil, fmt.Errorf("config server replica set %q: %w", configRS.Name(), err)
	}
	// Shard configs, on K8s, derive from one of two places: either the spec.shard field
	// set once for the entire ShardedClusterSpec, or from a shard-specific override. We
	// maintain the invariant that exactly one of these routes is used: either all shards
	// are identical and spec.shard is present, or spec.shard is absent and there is an
	// override for each shard.
	shardConfigs, err := shardAdditionalMongodConfigs(ac.AgentSSL, processMap, shardRSes)
	if err != nil {
		return nil, err
	}
	if common, uniform := commonShardConfig(shardConfigs); uniform {
		if common != nil {
			shardedClusterSpec.ShardSpec = &mdbv1.ShardedClusterComponentSpec{AdditionalMongodConfig: common}
		}
	} else {
		shardedClusterSpec.ShardOverrides = buildShardOverrides(k8sResourceName, shardConfigs)
	}
	shardedClusterSpec.MongosSpec = buildMongosComponentSpec(ac.AgentSSL, mongosProcs)
	spec.ShardedClusterSpec = shardedClusterSpec

	cr := &mdbv1.MongoDB{
		TypeMeta:   metav1.TypeMeta{APIVersion: "mongodb.com/v1", Kind: "MongoDB"},
		ObjectMeta: buildCRObjectMeta(k8sResourceName, opts.Namespace),
		Spec:       spec,
	}
	return cr, nil
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

// shardComponentConfig extracts representative additionalMongodConfig from one component's
// replica set, using pickSourceProcess to choose the member it is read from so a component
// of a sharded cluster and a plain replica set pick the same way.
//
// Returns a nil config, and no error, when nothing survives the filtering: every field was
// operator-managed, so there is nothing for the generated resource to carry. Nil rather than
// an empty config because the field is an omitempty pointer, and omitempty only drops a nil
// one: returning an empty config would write "additionalMongodConfig: {}" into every
// generated resource that configures nothing, and the operator fills the field in itself
// when it is absent.
//
// A member list with no usable source process is an inconsistent automation config and
// returns an error rather than being read as "no config": the settings do exist in Ops
// Manager, and silently dropping them would generate a resource that quietly disagrees with
// the deployment it is meant to reproduce.
func shardComponentConfig(agentSSL *om.AgentSSL, processMap map[string]om.Process, members []om.ReplicaSetMember) (*mdbv1.AdditionalMongodConfig, error) {
	proc, err := pickSourceProcess(members, processMap)
	if err != nil {
		return nil, err
	}
	return applyClientCertificateMode(agentSSL, proc.AdditionalMongodConfig()), nil
}

// buildShardedComponentSpec wraps shardComponentConfig in a component spec, or returns nil
// when there is no config to carry, so the component is left out of the generated resource
// entirely rather than appearing as an empty section.
func buildShardedComponentSpec(agentSSL *om.AgentSSL, processMap map[string]om.Process, members []om.ReplicaSetMember) (*mdbv1.ShardedClusterComponentSpec, error) {
	cfg, err := shardComponentConfig(agentSSL, processMap, members)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, nil
	}
	return &mdbv1.ShardedClusterComponentSpec{AdditionalMongodConfig: cfg}, nil
}

// shardAdditionalMongodConfigs returns each shard's additionalMongodConfig, index-aligned
// with shardRSes. Entries are nil for shards whose config is entirely operator-managed.
func shardAdditionalMongodConfigs(agentSSL *om.AgentSSL, processMap map[string]om.Process, shardRSes []om.ReplicaSet) ([]*mdbv1.AdditionalMongodConfig, error) {
	configs := make([]*mdbv1.AdditionalMongodConfig, 0, len(shardRSes))
	for _, rs := range shardRSes {
		cfg, err := shardComponentConfig(agentSSL, processMap, rs.Members())
		if err != nil {
			return nil, fmt.Errorf("shard replica set %q: %w", rs.Name(), err)
		}
		configs = append(configs, cfg)
	}
	return configs, nil
}

// commonShardConfig reports whether every shard shares one additionalMongodConfig, and if
// so returns it. Equality is value-based on the AdditionalMongodConfig records, and
// insensitive to key orderings.
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

// buildShardOverrides returns one ShardOverride per shard that has a config, in ascending
// shard index. Each entry carries the shard's complete config, not a delta. Shards with a
// nil config are skipped.
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
