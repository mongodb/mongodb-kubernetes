package migratetomck

import (
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/api/v1/status"
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
	overrides.ShardSpec = buildShardedComponentSpec(ac.AgentSSL, processMap, shardRSes[0].Members())
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

// buildShardedComponentSpec extracts additionalMongodConfig for a replica set component using its first member.
func buildShardedComponentSpec(agentSSL *om.AgentSSL, processMap map[string]om.Process, members []om.ReplicaSetMember) *mdbv1.ShardedClusterComponentSpec {
	if len(members) == 0 {
		return nil
	}
	proc, ok := processMap[members[0].Name()]
	if !ok {
		return nil
	}
	cfg := applyClientCertificateMode(agentSSL, proc.AdditionalMongodConfig())
	if cfg == nil {
		return nil
	}
	return &mdbv1.ShardedClusterComponentSpec{AdditionalMongodConfig: cfg}
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
	var members []mdbv1.ExternalMember

	configMembers, _, _ := om.ExtractMemberInfo(configRS.Members(), processMap)
	members = append(members, configMembers...)

	for _, rs := range shardRSes {
		shardMembers, _, _ := om.ExtractMemberInfo(rs.Members(), processMap)
		members = append(members, shardMembers...)
	}

	members = append(members, om.ExtractExternalMembers(mongosProcs)...)

	return members
}

// buildReplicaSetMap indexes replica sets by name for O(1) lookup.
func buildReplicaSetMap(rsList []om.ReplicaSet) map[string]om.ReplicaSet {
	m := make(map[string]om.ReplicaSet, len(rsList))
	for _, rs := range rsList {
		m[rs.Name()] = rs
	}
	return m
}
