package operator

import (
	"context"
	"fmt"
	"slices"

	"github.com/hashicorp/go-multierror"
	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/automationconfig"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/annotations"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/configmap"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/service"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/util/merge"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/util/scale"

	mdbcv1 "github.com/mongodb/mongodb-kubernetes-operator/api/v1"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/client"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	omv1 "github.com/10gen/ops-manager-kubernetes/api/v1/om"
	mdbstatus "github.com/10gen/ops-manager-kubernetes/api/v1/status"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/om/backup"
	"github.com/10gen/ops-manager-kubernetes/controllers/om/deployment"
	"github.com/10gen/ops-manager-kubernetes/controllers/om/host"
	"github.com/10gen/ops-manager-kubernetes/controllers/om/replicaset"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/agents"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/certs"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/connection"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct/scalers"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct/scalers/interfaces"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/controlledfeature"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/create"
	enterprisepem "github.com/10gen/ops-manager-kubernetes/controllers/operator/pem"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/project"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/recovery"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/watch"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/workflow"
	"github.com/10gen/ops-manager-kubernetes/pkg/dns"
	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	mekoService "github.com/10gen/ops-manager-kubernetes/pkg/kube/service"
	"github.com/10gen/ops-manager-kubernetes/pkg/multicluster"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/architectures"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/versionutil"
	"github.com/10gen/ops-manager-kubernetes/pkg/vault"
	"github.com/10gen/ops-manager-kubernetes/pkg/vault/vaultwatcher"
)

// ReconcileMongoDbShardedCluster is the reconciler for the sharded cluster
type ReconcileMongoDbShardedCluster struct {
	*ReconcileCommonController
	omConnectionFactory om.ConnectionFactory
	memberClustersMap   map[string]client.Client
}

func newShardedClusterReconciler(ctx context.Context, kubeClient client.Client, memberClusterMap map[string]client.Client, omFunc om.ConnectionFactory) *ReconcileMongoDbShardedCluster {
	return &ReconcileMongoDbShardedCluster{
		ReconcileCommonController: NewReconcileCommonController(ctx, kubeClient),
		omConnectionFactory:       omFunc,
		memberClustersMap:         memberClusterMap,
	}
}

type ShardedClusterDeploymentState struct {
	CommonDeploymentState `json:",inline"`
	LastAchievedSpec      *mdbv1.MongoDbSpec   `json:"lastAchievedSpec"`
	Status                *mdbv1.MongoDbStatus `json:"status"`
}

func NewShardedClusterDeploymentState() *ShardedClusterDeploymentState {
	return &ShardedClusterDeploymentState{
		CommonDeploymentState: CommonDeploymentState{ClusterMapping: map[string]int{}},
		LastAchievedSpec:      &mdbv1.MongoDbSpec{},
		Status:                &mdbv1.MongoDbStatus{},
	}
}

func (r *ShardedClusterReconcileHelper) initializeMemberClusters(globalMemberClustersMap map[string]client.Client, log *zap.SugaredLogger) error {
	mongoDB := r.sc
	shardsMap := r.desiredShardsConfiguration
	if mongoDB.Spec.IsMultiCluster() {
		if !multicluster.IsMemberClusterMapInitializedForMultiCluster(globalMemberClustersMap) {
			return xerrors.Errorf("member clusters have to be initialized for MultiCluster Sharded Cluster topology")
		}

		allReferencedClusterNamesMap := map[string]struct{}{}
		for _, clusterSpecItem := range r.getConfigSrvClusterSpecList() {
			allReferencedClusterNamesMap[clusterSpecItem.ClusterName] = struct{}{}
		}
		for _, clusterSpecItem := range r.getMongosClusterSpecList() {
			allReferencedClusterNamesMap[clusterSpecItem.ClusterName] = struct{}{}
		}
		for _, shardComponentSpec := range shardsMap {
			for _, clusterSpecItem := range shardComponentSpec.ClusterSpecList {
				allReferencedClusterNamesMap[clusterSpecItem.ClusterName] = struct{}{}
			}
		}
		var allReferencedClusterNames []string
		for clusterName := range allReferencedClusterNamesMap {
			allReferencedClusterNames = append(allReferencedClusterNames, clusterName)
		}
		slices.Sort(allReferencedClusterNames)

		r.deploymentState.ClusterMapping = multicluster.AssignIndexesForMemberClusterNames(r.deploymentState.ClusterMapping, allReferencedClusterNames)

		configSrvGetLastAppliedMembersFunc := func(memberClusterName string) int {
			if count, ok := r.deploymentState.Status.SizeStatusInClusters.ConfigServerMongodsInClusters[memberClusterName]; ok {
				return count
			} else {
				return 0
			}
		}
		r.configSrvMemberClusters = createMemberClusterListFromClusterSpecList(r.getConfigSrvClusterSpecList(), globalMemberClustersMap, log, r.deploymentState.ClusterMapping, configSrvGetLastAppliedMembersFunc, false)

		mongosGetLastAppliedMembersFunc := func(memberClusterName string) int {
			if count, ok := r.deploymentState.Status.SizeStatusInClusters.MongosCountInClusters[memberClusterName]; ok {
				return count
			} else {
				return 0
			}
		}
		r.mongosMemberClusters = createMemberClusterListFromClusterSpecList(r.getMongosClusterSpecList(), globalMemberClustersMap, log, r.deploymentState.ClusterMapping, mongosGetLastAppliedMembersFunc, false)
		r.shardsMemberClustersMap, r.allShardsMemberClusters = r.createShardsMemberClusterLists(shardsMap, globalMemberClustersMap, log, r.deploymentState, false)
	} else {
		r.shardsMemberClustersMap, r.allShardsMemberClusters = r.createShardsMemberClusterLists(shardsMap, globalMemberClustersMap, log, r.deploymentState, true)
		r.configSrvMemberClusters = []multicluster.MemberCluster{multicluster.GetLegacyCentralMemberCluster(r.deploymentState.Status.MongodbShardedClusterSizeConfig.ConfigServerCount, 0, r.commonController.client, r.commonController.SecretClient)}
		r.mongosMemberClusters = []multicluster.MemberCluster{multicluster.GetLegacyCentralMemberCluster(r.deploymentState.Status.MongodbShardedClusterSizeConfig.MongosCount, 0, r.commonController.client, r.commonController.SecretClient)}
	}

	r.allMemberClusters = r.createAllMemberClustersList()

	log.Debugf("Initialized shards member cluster list: %+v", util.Transform(r.allShardsMemberClusters, func(m multicluster.MemberCluster) string {
		// TODO Replicas is not relevant when iterating over allShardsMemberClusters; construct full list by iterating over shardsMemberClustersMap
		return fmt.Sprintf("{Name: %s, Index: %d, Replicas: %d, Active: %t, Healthy: %t}", m.Name, m.Index, m.Replicas, m.Active, m.Healthy)
	}))
	log.Debugf("Initialized mongos member cluster list: %+v", util.Transform(r.mongosMemberClusters, func(m multicluster.MemberCluster) string {
		return fmt.Sprintf("{Name: %s, Index: %d, Replicas: %d, Active: %t, Healthy: %t}", m.Name, m.Index, m.Replicas, m.Active, m.Healthy)
	}))
	log.Debugf("Initialized config servers member cluster list: %+v", util.Transform(r.configSrvMemberClusters, func(m multicluster.MemberCluster) string {
		return fmt.Sprintf("{Name: %s, Index: %d, Replicas: %d, Active: %t, Healthy: %t}", m.Name, m.Index, m.Replicas, m.Active, m.Healthy)
	}))
	return nil
}

// createAllMemberClustersList is returning a list of all unique member clusters used across all clusterSpecLists.
func (r *ShardedClusterReconcileHelper) createAllMemberClustersList() []multicluster.MemberCluster {
	var allClusters []multicluster.MemberCluster
	allClusters = append(allClusters, r.allShardsMemberClusters...)
	allClusters = append(allClusters, r.mongosMemberClusters...)
	allClusters = append(allClusters, r.configSrvMemberClusters...)
	allClustersMap := map[string]multicluster.MemberCluster{}
	for _, memberCluster := range allClusters {
		// we deliberately reset replicas to not use it accidentally
		// allClustersMap contains unique cluster names across all clusterSpecLists, but replicas part will be invalid
		memberCluster.Replicas = 0
		allClustersMap[memberCluster.Name] = memberCluster
	}

	allClusters = nil
	for _, memberCluster := range allClustersMap {
		allClusters = append(allClusters, memberCluster)
	}
	return allClusters
}

// createShardsMemberClusterLists creates a list of member clusters from the current desired shards configuration.
// legacyMemberCluster parameter is used to indicate the member cluster should be marked as Legacy for reusing this function also in single-cluster mode.
func (r *ShardedClusterReconcileHelper) createShardsMemberClusterLists(shardsMap map[int]*mdbv1.ShardedClusterComponentSpec, globalMemberClustersMap map[string]client.Client, log *zap.SugaredLogger, deploymentState *ShardedClusterDeploymentState, legacyMemberCluster bool) (map[int][]multicluster.MemberCluster, []multicluster.MemberCluster) {
	shardMemberClustersMap := map[int][]multicluster.MemberCluster{}
	var allShardsMemberClusters []multicluster.MemberCluster
	alreadyAdded := map[string]struct{}{}
	// Shards can have different member clusters specified in spec.ShardSpec.ClusterSpecList and in shard overrides.
	// Here we construct a unique list of member clusters on which shards are deployed
	for shardIdx, shardSpec := range shardsMap {
		shardGetLastAppliedMembersFunc := func(memberClusterName string) int {
			shardOverridesInClusters := deploymentState.Status.SizeStatusInClusters.ShardOverridesInClusters
			if _, ok := shardOverridesInClusters[r.sc.ShardRsName(shardIdx)]; ok {
				if count, ok := shardOverridesInClusters[r.sc.ShardRsName(shardIdx)][memberClusterName]; ok {
					// If we stored an override for this shard in the status, get the member count from it
					return count
				}
			}
			if count, ok := deploymentState.Status.SizeStatusInClusters.ShardMongodsInClusters[memberClusterName]; ok {
				// Otherwise get the default one ShardMongodsInClusters
				// ShardMongodsInClusters is not correct in the edge case where all shards are overridden
				// but we won't enter this branch as we check for override in the branch above
				// This edge case is tested in e2e_multi_cluster_sharded_scaling_all_shard_overrides
				return count
			}

			return 0
		}
		// we use here shardSpec.ClusterSpecList directly as it's already a "processed" one from shardMap
		shardMemberClustersMap[shardIdx] = createMemberClusterListFromClusterSpecList(shardSpec.ClusterSpecList, globalMemberClustersMap, log, deploymentState.ClusterMapping, shardGetLastAppliedMembersFunc, legacyMemberCluster)

		for _, shardMemberCluster := range shardMemberClustersMap[shardIdx] {
			if _, ok := alreadyAdded[shardMemberCluster.Name]; !ok {
				// We don't care from which shard we use memberCluster for this list;
				// we deliberately reset Replicas to not accidentally use it
				shardMemberCluster.Replicas = 0
				allShardsMemberClusters = append(allShardsMemberClusters, shardMemberCluster)
				alreadyAdded[shardMemberCluster.Name] = struct{}{}
			}
		}
	}

	return shardMemberClustersMap, allShardsMemberClusters
}

func (r *ShardedClusterReconcileHelper) getShardNameToShardIdxMap() map[string]int {
	mapping := map[string]int{}
	for shardIdx := 0; shardIdx < max(r.sc.Spec.ShardCount, r.deploymentState.Status.MongodbShardedClusterSizeConfig.ShardCount); shardIdx++ {
		mapping[r.sc.ShardRsName(shardIdx)] = shardIdx
	}

	return mapping
}

func (r *ShardedClusterReconcileHelper) getShardClusterSpecList() mdbv1.ClusterSpecList {
	spec := r.sc.Spec
	if spec.IsMultiCluster() {
		return spec.ShardSpec.ClusterSpecList
	} else {
		return mdbv1.ClusterSpecList{
			{
				ClusterName:  multicluster.LegacyCentralClusterName,
				Members:      spec.MongodsPerShardCount,
				MemberConfig: spec.MemberConfig,
			},
		}
	}
}

func (r *ShardedClusterReconcileHelper) getMongosClusterSpecList() mdbv1.ClusterSpecList {
	spec := r.sc.Spec
	if spec.IsMultiCluster() {
		// TODO return merged, desired mongos configuration
		return spec.MongosSpec.ClusterSpecList
	} else {
		return mdbv1.ClusterSpecList{
			{
				ClusterName: multicluster.LegacyCentralClusterName,
				Members:     spec.MongosCount,
			},
		}
	}
}

func (r *ShardedClusterReconcileHelper) getConfigSrvClusterSpecList() mdbv1.ClusterSpecList {
	spec := r.sc.Spec
	if spec.IsMultiCluster() {
		return spec.ConfigSrvSpec.ClusterSpecList
	} else {
		return mdbv1.ClusterSpecList{
			{
				ClusterName:  multicluster.LegacyCentralClusterName,
				Members:      spec.ConfigServerCount,
				MemberConfig: spec.MemberConfig,
			},
		}
	}
}

// prepareDesiredShardsConfiguration calculates full expected configuration of sharded cluster spec resource.
// It returns map of each shard (by index) with its configuration over all clusters and applying all pods spec overrides.
// In other words, this function is rendering final configuration of each shard over all member clusters applying all override logic.
// The reconciler implementation should refer to this structure only without taking into consideration complexities of MongoDbSpec wrt sharded clusters.
func (r *ShardedClusterReconcileHelper) prepareDesiredShardsConfiguration() map[int]*mdbv1.ShardedClusterComponentSpec {
	spec := r.sc.Spec.DeepCopy()
	// We initialize ClusterSpecList to contain a single legacy cluster in case of SingleCluster mode.
	if spec.ShardSpec == nil {
		spec.ShardSpec = &mdbv1.ShardedClusterComponentSpec{}
	}
	spec.ShardSpec.ClusterSpecList = r.getShardClusterSpecList()
	// We don't need to do the same for shardOverrides for single-cluster as shardOverrides[].ClusterSpecList can be set only for Multi-Cluster mode.
	// And we don't need that artificial legacy cluster as for single-cluster all necessary configuration is defined top-level.

	// We create here a collapsed structure of each shard configuration with all overrides applied to make the final configuration.
	// For single cluster deployment it will be a single-element ClusterSpecList for each shard.
	// For multiple clusters, each shard will have configuration specified for each member cluster.
	shardComponentSpecs := map[int]*mdbv1.ShardedClusterComponentSpec{}

	for shardIdx := 0; shardIdx < max(spec.ShardCount, r.deploymentState.Status.MongodbShardedClusterSizeConfig.ShardCount); shardIdx++ {
		topLevelPersistenceOverride, topLevelPodSpecOverride := getShardTopLevelOverrides(spec, shardIdx)
		shardComponentSpec := *spec.ShardSpec.DeepCopy()
		shardComponentSpec.ClusterSpecList = processClusterSpecList(shardComponentSpec.ClusterSpecList, topLevelPodSpecOverride, topLevelPersistenceOverride)
		shardComponentSpecs[shardIdx] = &shardComponentSpec
	}

	for _, shardOverride := range expandShardOverrides(spec.ShardOverrides) {
		// guaranteed to have one shard name in expandedShardOverrides
		shardName := shardOverride.ShardNames[0]
		shardIndex := r.getShardNameToShardIdxMap()[shardName]
		// here we copy the whole element and overwrite at the end of every iteration
		defaultShardConfiguration := shardComponentSpecs[shardIndex].DeepCopy()
		topLevelPersistenceOverride, topLevelPodSpecOverride := getShardTopLevelOverrides(spec, shardIndex)
		shardComponentSpecs[shardIndex] = processShardOverride(spec, shardOverride, defaultShardConfiguration, topLevelPodSpecOverride, topLevelPersistenceOverride)
	}
	return shardComponentSpecs
}

func getShardTopLevelOverrides(spec *mdbv1.MongoDbSpec, shardIdx int) (*mdbv1.Persistence, *corev1.PodTemplateSpec) {
	topLevelPodSpecOverride, topLevelPersistenceOverride := extractOverridesFromPodSpec(spec.ShardPodSpec)

	// specific shard level sts and persistence override
	// TODO: as of 1.30 we deprecated ShardSpecificPodSpec, we should completely get rid of it in a few releases
	if shardIdx < len(spec.ShardSpecificPodSpec) {
		shardSpecificPodSpec := spec.ShardSpecificPodSpec[shardIdx]
		if shardSpecificPodSpec.PodTemplateWrapper.PodTemplate != nil {
			// We replace the override instead of merging it, because in single-cluster the override wasn't merging
			// those specs; we keep the same behavior for backwards compatibility
			topLevelPodSpecOverride = shardSpecificPodSpec.PodTemplateWrapper.PodTemplate.DeepCopy()
		}
		// ShardSpecificPodSpec applies to both template and persistence
		if shardSpecificPodSpec.Persistence != nil {
			topLevelPersistenceOverride = shardSpecificPodSpec.Persistence.DeepCopy()
		}
	}
	return topLevelPersistenceOverride, topLevelPodSpecOverride
}

func mergeOverrideClusterSpecList(shardOverride mdbv1.ShardOverride, defaultShardConfiguration *mdbv1.ShardedClusterComponentSpec, topLevelPodSpecOverride *corev1.PodTemplateSpec, topLevelPersistenceOverride *mdbv1.Persistence) *mdbv1.ShardedClusterComponentSpec {
	finalShardConfiguration := defaultShardConfiguration.DeepCopy()
	// We override here all elements of ClusterSpecList, but statefulset overrides if provided here
	// will be merged on top of previous sts overrides.
	for shardOverrideClusterSpecIdx := range shardOverride.ClusterSpecList {
		shardOverrideClusterSpecItem := &shardOverride.ClusterSpecList[shardOverrideClusterSpecIdx]
		foundIdx := slices.IndexFunc(defaultShardConfiguration.ClusterSpecList, func(item mdbv1.ClusterSpecItem) bool {
			return item.ClusterName == shardOverrideClusterSpecItem.ClusterName
		})
		// If the cluster is not found, it means this ShardOverride adds a new cluster that was not in ClusterSpecList
		// We need to propagate top level specs, from e.g ShardPodSpec or ShardSpecificPodSpec, and apply a merge
		if foundIdx == -1 {
			if shardOverrideClusterSpecItem.StatefulSetConfiguration == nil {
				shardOverrideClusterSpecItem.StatefulSetConfiguration = &mdbcv1.StatefulSetConfiguration{}
			}
			// We only need to perform a merge if there is a top level override, otherwise we keep an empty sts configuration
			if topLevelPodSpecOverride != nil {
				shardOverrideClusterSpecItem.StatefulSetConfiguration.SpecWrapper.Spec.Template = merge.PodTemplateSpecs(*topLevelPodSpecOverride, shardOverrideClusterSpecItem.StatefulSetConfiguration.SpecWrapper.Spec.Template)
			}
			if (shardOverrideClusterSpecItem.PodSpec == nil || shardOverrideClusterSpecItem.PodSpec.Persistence == nil) &&
				topLevelPersistenceOverride != nil {
				shardOverrideClusterSpecItem.PodSpec = &mdbv1.MongoDbPodSpec{
					Persistence: topLevelPersistenceOverride.DeepCopy(),
				}
			}
			continue
		}
		finalShardConfigurationClusterSpecItem := finalShardConfiguration.ClusterSpecList[foundIdx]
		if finalShardConfigurationClusterSpecItem.StatefulSetConfiguration != nil {
			if shardOverrideClusterSpecItem.StatefulSetConfiguration == nil {
				shardOverrideClusterSpecItem.StatefulSetConfiguration = finalShardConfigurationClusterSpecItem.StatefulSetConfiguration
			} else {
				shardOverrideClusterSpecItem.StatefulSetConfiguration.SpecWrapper.Spec = merge.StatefulSetSpecs(finalShardConfigurationClusterSpecItem.StatefulSetConfiguration.SpecWrapper.Spec, shardOverrideClusterSpecItem.StatefulSetConfiguration.SpecWrapper.Spec)
			}
		}

		if shardOverrideClusterSpecItem.Members == nil {
			shardOverrideClusterSpecItem.Members = ptr.To(finalShardConfigurationClusterSpecItem.Members)
		}

		if shardOverrideClusterSpecItem.MemberConfig == nil {
			shardOverrideClusterSpecItem.MemberConfig = finalShardConfigurationClusterSpecItem.MemberConfig
		}

		// The two if blocks below make sure that PodSpec (for persistence) defined at the override level applies to all
		// clusters by default, except if it is set at shardOverride.ClusterSpecList.PodSpec level
		if shardOverride.PodSpec != nil {
			finalShardConfigurationClusterSpecItem.PodSpec = shardOverride.PodSpec
		}
		if shardOverrideClusterSpecItem.PodSpec == nil {
			shardOverrideClusterSpecItem.PodSpec = finalShardConfigurationClusterSpecItem.PodSpec
		}
	}

	// we reconstruct clusterSpecList from shardOverride list
	finalShardConfiguration.ClusterSpecList = nil
	for i := range shardOverride.ClusterSpecList {
		so := shardOverride.ClusterSpecList[i].DeepCopy()
		// guaranteed to be non-nil here
		members := *shardOverride.ClusterSpecList[i].Members

		// We need to retrieve the original ExternalAccessConfiguration because shardOverride struct doesn't contain
		// the field
		var externalAccessConfiguration *mdbv1.ExternalAccessConfiguration
		foundIdx := slices.IndexFunc(defaultShardConfiguration.ClusterSpecList, func(item mdbv1.ClusterSpecItem) bool {
			return item.ClusterName == so.ClusterName
		})
		if foundIdx != -1 {
			externalAccessConfiguration = defaultShardConfiguration.ClusterSpecList[foundIdx].ExternalAccessConfiguration
		}

		finalShardConfiguration.ClusterSpecList = append(finalShardConfiguration.ClusterSpecList, mdbv1.ClusterSpecItem{
			ClusterName:                 so.ClusterName,
			ExternalAccessConfiguration: externalAccessConfiguration,
			Members:                     members,
			MemberConfig:                so.MemberConfig,
			StatefulSetConfiguration:    so.StatefulSetConfiguration,
			PodSpec:                     so.PodSpec,
		})
	}

	return finalShardConfiguration
}

// ShardOverrides can apply to multiple shard (e.g shardNames: ["sh-0", "sh-2"])
// we expand overrides to get a list with each entry applying to a single shard
func expandShardOverrides(initialOverrides []mdbv1.ShardOverride) []mdbv1.ShardOverride {
	var expandedShardOverrides []mdbv1.ShardOverride
	for _, shardOverride := range initialOverrides {
		for _, shardName := range shardOverride.ShardNames {
			shardOverrideCopy := shardOverride.DeepCopy()
			shardOverrideCopy.ShardNames = []string{shardName}
			expandedShardOverrides = append(expandedShardOverrides, *shardOverrideCopy)
		}
	}
	return expandedShardOverrides
}

func processShardOverride(spec *mdbv1.MongoDbSpec, shardOverride mdbv1.ShardOverride, defaultShardConfiguration *mdbv1.ShardedClusterComponentSpec, topLevelPodSpecOverride *corev1.PodTemplateSpec, topLevelPersistenceOverride *mdbv1.Persistence) *mdbv1.ShardedClusterComponentSpec {
	if shardOverride.Agent != nil {
		defaultShardConfiguration.Agent = *shardOverride.Agent
	}
	if shardOverride.AdditionalMongodConfig != nil {
		defaultShardConfiguration.AdditionalMongodConfig = shardOverride.AdditionalMongodConfig.DeepCopy()
	}
	// in single cluster, we put members override in a legacy cluster
	if shardOverride.Members != nil && !spec.IsMultiCluster() {
		// it's guaranteed it will have 1 element
		defaultShardConfiguration.ClusterSpecList[0].Members = *shardOverride.Members
	}

	if shardOverride.MemberConfig != nil && !spec.IsMultiCluster() {
		defaultShardConfiguration.ClusterSpecList[0].MemberConfig = shardOverride.MemberConfig
	}

	// in single-cluster we need to override podspec of the first dummy member cluster, as we won't go into shardOverride.ClusterSpecList iteration below
	if shardOverride.PodSpec != nil && !spec.IsMultiCluster() {
		defaultShardConfiguration.ClusterSpecList[0].PodSpec = shardOverride.PodSpec
	}

	// The below loop makes the field ShardOverrides.StatefulSetConfiguration the default configuration for
	// stateful sets in all clusters for that shard. The merge priority order is the following (lowest to highest):
	// ShardSpec.ClusterSpecList.StatefulSetConfiguration -> ShardOverrides.StatefulSetConfiguration -> ShardOverrides.ClusterSpecList.StatefulSetConfiguration
	if shardOverride.StatefulSetConfiguration != nil {
		for idx := range defaultShardConfiguration.ClusterSpecList {
			// Handle case where defaultShardConfiguration.ClusterSpecList[idx].StatefulSetConfiguration is nil
			if defaultShardConfiguration.ClusterSpecList[idx].StatefulSetConfiguration == nil {
				defaultShardConfiguration.ClusterSpecList[idx].StatefulSetConfiguration = &mdbcv1.StatefulSetConfiguration{}
			}
			defaultShardConfiguration.ClusterSpecList[idx].StatefulSetConfiguration.SpecWrapper.Spec = merge.StatefulSetSpecs(defaultShardConfiguration.ClusterSpecList[idx].StatefulSetConfiguration.SpecWrapper.Spec, shardOverride.StatefulSetConfiguration.SpecWrapper.Spec)
		}
	}

	// Merge existing clusterSpecList with clusterSpecList from a specific shard override.
	// In single-cluster shardOverride cannot have any ClusterSpecList elements.
	if shardOverride.ClusterSpecList != nil {
		return mergeOverrideClusterSpecList(shardOverride, defaultShardConfiguration, topLevelPodSpecOverride, topLevelPersistenceOverride)
	} else {
		return defaultShardConfiguration
	}
}

func extractOverridesFromPodSpec(podSpec *mdbv1.MongoDbPodSpec) (*corev1.PodTemplateSpec, *mdbv1.Persistence) {
	var podTemplateOverride *corev1.PodTemplateSpec
	var persistenceOverride *mdbv1.Persistence
	if podSpec != nil {
		if podSpec.PodTemplateWrapper.PodTemplate != nil {
			podTemplateOverride = podSpec.PodTemplateWrapper.PodTemplate
		}
		if podSpec.Persistence != nil {
			persistenceOverride = podSpec.Persistence
		}
	}
	return podTemplateOverride, persistenceOverride
}

// prepareDesiredMongosConfiguration calculates full expected configuration of mongos resource.
// It returns a configuration for all clusters and applying all pods spec overrides.
// In other words, this function is rendering final configuration for the mongos over all member clusters applying all override logic.
// The reconciler implementation should refer to this structure only without taking into consideration complexities of MongoDbSpec wrt mongos.
// We share the same logic and data structures used for Config Server, although some fields are not relevant for mongos
// e.g MemberConfig. They will simply be ignored when the database is constructed
func (r *ShardedClusterReconcileHelper) prepareDesiredMongosConfiguration() *mdbv1.ShardedClusterComponentSpec {
	// We initialize ClusterSpecList to contain a single legacy cluster in case of SingleCluster mode.
	spec := r.sc.Spec.DeepCopy()
	if spec.MongosSpec == nil {
		spec.MongosSpec = &mdbv1.ShardedClusterComponentSpec{}
	}
	spec.MongosSpec.ClusterSpecList = r.getMongosClusterSpecList()
	topLevelPodSpecOverride, topLevelPersistenceOverride := extractOverridesFromPodSpec(spec.MongosPodSpec)
	mongosComponentSpec := spec.MongosSpec.DeepCopy()
	mongosComponentSpec.ClusterSpecList = processClusterSpecList(mongosComponentSpec.ClusterSpecList, topLevelPodSpecOverride, topLevelPersistenceOverride)
	return mongosComponentSpec
}

// prepareDesiredConfigServerConfiguration works the same way as prepareDesiredMongosConfiguration, but for config server
func (r *ShardedClusterReconcileHelper) prepareDesiredConfigServerConfiguration() *mdbv1.ShardedClusterComponentSpec {
	// We initialize ClusterSpecList to contain a single legacy cluster in case of SingleCluster mode.
	spec := r.sc.Spec.DeepCopy()
	if spec.ConfigSrvSpec == nil {
		spec.ConfigSrvSpec = &mdbv1.ShardedClusterComponentSpec{}
	}
	spec.ConfigSrvSpec.ClusterSpecList = r.getConfigSrvClusterSpecList()
	topLevelPodSpecOverride, topLevelPersistenceOverride := extractOverridesFromPodSpec(spec.ConfigSrvPodSpec)
	configSrvComponentSpec := spec.ConfigSrvSpec.DeepCopy()
	configSrvComponentSpec.ClusterSpecList = processClusterSpecList(configSrvComponentSpec.ClusterSpecList, topLevelPodSpecOverride, topLevelPersistenceOverride)
	return configSrvComponentSpec
}

// processClusterSpecList is a function shared by prepare desired configuration functions for shards, mongos and config servers
// it iterates through currently defined clusterSpecLists and set the correct STS configurations and persistence values,
// depending on top level overrides
func processClusterSpecList(
	clusterSpecList []mdbv1.ClusterSpecItem,
	topLevelPodSpecOverride *corev1.PodTemplateSpec,
	topLevelPersistenceOverride *mdbv1.Persistence,
) []mdbv1.ClusterSpecItem {
	for i := range clusterSpecList {
		// we will store final sts overrides for each cluster in clusterSpecItem.StatefulSetOverride
		// therefore we initialize it here and merge into it in case there is anything to override in the first place
		// in case higher level overrides are empty, we just use whatever is specified in clusterSpecItem (maybe nothing as well)
		if topLevelPodSpecOverride != nil {
			if clusterSpecList[i].StatefulSetConfiguration == nil {
				clusterSpecList[i].StatefulSetConfiguration = &mdbcv1.StatefulSetConfiguration{}
			}
			clusterSpecList[i].StatefulSetConfiguration.SpecWrapper.Spec.Template = merge.PodTemplateSpecs(*topLevelPodSpecOverride.DeepCopy(), clusterSpecList[i].StatefulSetConfiguration.SpecWrapper.Spec.Template)
		}
		if clusterSpecList[i].PodSpec == nil {
			clusterSpecList[i].PodSpec = &mdbv1.MongoDbPodSpec{}
		}
		if topLevelPersistenceOverride != nil {
			if clusterSpecList[i].PodSpec.Persistence == nil {
				clusterSpecList[i].PodSpec.Persistence = topLevelPersistenceOverride.DeepCopy()
			}
		}
		// If the MemberConfigs count is smaller than the number of numbers, append default values
		for j := range clusterSpecList[i].Members {
			if j >= len(clusterSpecList[i].MemberConfig) {
				clusterSpecList[i].MemberConfig = append(clusterSpecList[i].MemberConfig, automationconfig.MemberOptions{
					Votes:    ptr.To(1),
					Priority: ptr.To("1"),
					Tags:     nil,
				})
			}
		}
		// Explicitly set PodTemplate field to nil, as the pod template configuration is stored in StatefulSetConfiguration
		// in the processed ShardedClusterComponentSpec structures.
		// PodSpec should only be used for Persistence
		clusterSpecList[i].PodSpec.PodTemplateWrapper.PodTemplate = nil
	}
	return clusterSpecList
}

type ShardedClusterReconcileHelper struct {
	commonController       *ReconcileCommonController
	omConnectionFactory    om.ConnectionFactory
	automationAgentVersion string

	// sc is the resource being reconciled
	sc *mdbv1.MongoDB

	// desired Configurations structs contain the target state - they reflect applying all the override rules to render the final, desired configuration
	desiredShardsConfiguration       map[int]*mdbv1.ShardedClusterComponentSpec
	desiredConfigServerConfiguration *mdbv1.ShardedClusterComponentSpec
	desiredMongosConfiguration       *mdbv1.ShardedClusterComponentSpec

	// all member clusters here contain the number of members set to the current state read from deployment state
	shardsMemberClustersMap map[int][]multicluster.MemberCluster
	allShardsMemberClusters []multicluster.MemberCluster
	configSrvMemberClusters []multicluster.MemberCluster
	mongosMemberClusters    []multicluster.MemberCluster
	allMemberClusters       []multicluster.MemberCluster

	// deploymentState is a helper structure containing the current deployment state
	// It's initialized at the beginning of the reconcile and stored whenever we need to save changes to the deployment state.
	// Also, deploymentState is always persisted in updateStatus method.
	deploymentState *ShardedClusterDeploymentState

	stateStore *StateStore[ShardedClusterDeploymentState]
}

func NewShardedClusterReconcilerHelper(ctx context.Context, reconciler *ReconcileCommonController, sc *mdbv1.MongoDB, globalMemberClustersMap map[string]client.Client, omConnectionFactory om.ConnectionFactory, log *zap.SugaredLogger) (*ShardedClusterReconcileHelper, error) {
	// It's a workaround for single cluster topology to add there __default cluster.
	// With the multi-cluster sharded refactor, we went so far with the multi-cluster first approach so we have very few places with conditional single/multi logic.
	// Therefore, some parts of the reconciler logic uses that globalMemberClusterMap even in single-cluster mode (look for usages of createShardsMemberClusterLists) and expect
	// to have __default member cluster defined in the globalMemberClustersMap as the __default member cluster is artificially added in initializeMemberClusters to clusterSpecList
	// in single-cluster mode to simulate it's a special case of multi-cluster run.
	globalMemberClustersMap = multicluster.InitializeGlobalMemberClusterMapForSingleCluster(globalMemberClustersMap, reconciler.client)

	helper := &ShardedClusterReconcileHelper{
		commonController:    reconciler,
		omConnectionFactory: omConnectionFactory,
	}

	helper.sc = sc
	helper.deploymentState = NewShardedClusterDeploymentState()
	if err := helper.initializeStateStore(ctx, reconciler, sc, log); err != nil {
		return nil, xerrors.Errorf("failed to initialize sharded cluster state store: %w", err)
	}

	helper.desiredShardsConfiguration = helper.prepareDesiredShardsConfiguration()
	helper.desiredConfigServerConfiguration = helper.prepareDesiredConfigServerConfiguration()
	helper.desiredMongosConfiguration = helper.prepareDesiredMongosConfiguration()

	if err := helper.initializeMemberClusters(globalMemberClustersMap, log); err != nil {
		return nil, xerrors.Errorf("failed to initialize sharded cluster controller: %w", err)
	}

	if err := helper.stateStore.WriteState(ctx, helper.deploymentState, log); err != nil {
		return nil, err
	}

	if helper.deploymentState.Status != nil {
		// If we have the status in the deployment state, we make sure that status in the CR is the same.
		// Status in the deployment state takes precedence. E.g. in case of restoring CR from yaml/git, the user-facing Status field will be restored
		// from the deployment state.
		// Most of the operations should mutate only deployment state, but some parts of Sharded Cluster implementation still updates the status directly in the CR.
		// Having Status in CR synced with the deployment state allows to copy CR's Status into deployment state in updateStatus method.
		sc.Status = *helper.deploymentState.Status
	}

	return helper, nil
}

func (r *ShardedClusterReconcileHelper) initializeStateStore(ctx context.Context, reconciler *ReconcileCommonController, sc *mdbv1.MongoDB, log *zap.SugaredLogger) error {
	r.deploymentState = NewShardedClusterDeploymentState()

	r.stateStore = NewStateStore[ShardedClusterDeploymentState](sc.GetNamespace(), sc.Name, reconciler.client)
	if state, err := r.stateStore.ReadState(ctx); err != nil {
		if errors.IsNotFound(err) {
			// If the deployment state config map is missing, then it might be either:
			//  - fresh deployment
			//  - existing deployment, but it's a first reconcile on the operator version with the new deployment state
			//  - existing deployment, but for some reason the deployment state config map has been deleted
			// In all cases, the deployment config map will be recreated from the state we're keeping and maintaining in
			// the old place (in annotations, spec.status, config maps) in order to allow for the downgrade of the operator.
			log.Infof("Migrating deployment state from annotations and status to the configmap based deployment state")
			if err := r.migrateToNewDeploymentState(sc); err != nil {
				return err
			}
			// This will migrate the deployment state to the new structure and this branch of code won't be executed again.
			if err := r.stateStore.WriteState(ctx, r.deploymentState, log); err != nil {
				return err
			}
		} else {
			return err
		}
	} else {
		r.deploymentState = state
		if r.deploymentState.Status.SizeStatusInClusters == nil {
			r.deploymentState.Status.SizeStatusInClusters = &mdbstatus.MongodbShardedSizeStatusInClusters{}
		}
		if r.deploymentState.Status.SizeStatusInClusters.MongosCountInClusters == nil {
			r.deploymentState.Status.SizeStatusInClusters.MongosCountInClusters = map[string]int{}
		}
		if r.deploymentState.Status.SizeStatusInClusters.ConfigServerMongodsInClusters == nil {
			r.deploymentState.Status.SizeStatusInClusters.ConfigServerMongodsInClusters = map[string]int{}
		}
		if r.deploymentState.Status.SizeStatusInClusters.ShardMongodsInClusters == nil {
			r.deploymentState.Status.SizeStatusInClusters.ShardMongodsInClusters = map[string]int{}
		}
		if r.deploymentState.Status.SizeStatusInClusters.ShardOverridesInClusters == nil {
			r.deploymentState.Status.SizeStatusInClusters.ShardOverridesInClusters = map[string]map[string]int{}
		}
	}

	return nil
}

func (r *ReconcileMongoDbShardedCluster) Reconcile(ctx context.Context, request reconcile.Request) (res reconcile.Result, e error) {
	log := zap.S().With("ShardedCluster", request.NamespacedName)
	sc := &mdbv1.MongoDB{}
	reconcileResult, err := r.prepareResourceForReconciliation(ctx, request, sc, log)
	if err != nil {
		if errors.IsNotFound(err) {
			return workflow.Invalid("Object for reconciliation not found").ReconcileResult()
		}
		return reconcileResult, err
	}
	reconcilerHelper, err := NewShardedClusterReconcilerHelper(ctx, r.ReconcileCommonController, sc, r.memberClustersMap, r.omConnectionFactory, log)
	if err != nil {
		return r.updateStatus(ctx, sc, workflow.Failed(xerrors.Errorf("Failed to initialize sharded cluster reconciler: %w", err)), log)
	}
	return reconcilerHelper.Reconcile(ctx, log)
}

// OnDelete tries to complete a Deletion reconciliation event
func (r *ReconcileMongoDbShardedCluster) OnDelete(ctx context.Context, obj runtime.Object, log *zap.SugaredLogger) error {
	reconcilerHelper, err := NewShardedClusterReconcilerHelper(ctx, r.ReconcileCommonController, obj.(*mdbv1.MongoDB), r.memberClustersMap, r.omConnectionFactory, log)
	if err != nil {
		return err
	}
	return reconcilerHelper.OnDelete(ctx, obj, log)
}

func (r *ShardedClusterReconcileHelper) Reconcile(ctx context.Context, log *zap.SugaredLogger) (res reconcile.Result, e error) {
	sc := r.sc
	if err := sc.ProcessValidationsOnReconcile(nil); err != nil {
		return r.commonController.updateStatus(ctx, sc, workflow.Invalid("%s", err.Error()), log)
	}

	if !architectures.IsRunningStaticArchitecture(sc.Annotations) {
		agents.UpgradeAllIfNeeded(ctx, agents.ClientSecret{Client: r.commonController.client, SecretClient: r.commonController.SecretClient}, r.omConnectionFactory, GetWatchedNamespace(), false)
	}

	log.Info("-> ShardedCluster.Reconcile")
	log.Infow("ShardedCluster.Spec", "spec", sc.Spec)
	log.Infow("ShardedCluster.Status", "status", r.deploymentState.Status)
	log.Infow("ShardedCluster.deploymentState", "sizeStatus", r.deploymentState.Status.MongodbShardedClusterSizeConfig, "sizeStatusInClusters", r.deploymentState.Status.SizeStatusInClusters)

	r.logAllScalers(log)

	projectConfig, credsConfig, err := project.ReadConfigAndCredentials(ctx, r.commonController.client, r.commonController.SecretClient, sc, log)
	if err != nil {
		return r.updateStatus(ctx, sc, workflow.Failed(err), log)
	}

	conn, agentAPIKey, err := connection.PrepareOpsManagerConnection(ctx, r.commonController.SecretClient, projectConfig, credsConfig, r.omConnectionFactory, sc.Namespace, log)
	if err != nil {
		return r.updateStatus(ctx, sc, workflow.Failed(err), log)
	}

	if err := r.replicateAgentKeySecret(ctx, conn, agentAPIKey, log); err != nil {
		return r.updateStatus(ctx, sc, workflow.Failed(err), log)
	}
	if err := r.reconcileHostnameOverrideConfigMap(ctx, log); err != nil {
		return r.updateStatus(ctx, sc, workflow.Failed(err), log)
	}
	if err := r.replicateSSLMMSCAConfigMap(ctx, projectConfig, log); err != nil {
		return r.updateStatus(ctx, sc, workflow.Failed(err), log)
	}

	var automationAgentVersion string
	if architectures.IsRunningStaticArchitecture(sc.Annotations) {
		// In case the Agent *is* overridden, its version will be merged into the StatefulSet. The merging process
		// happens after creating the StatefulSet definition.
		if !sc.IsAgentImageOverridden() {
			automationAgentVersion, err = r.commonController.getAgentVersion(conn, conn.OpsManagerVersion().VersionString, false, log)
			if err != nil {
				log.Errorf("Impossible to get agent version, please override the agent image by providing a pod template")
				return r.updateStatus(ctx, sc, workflow.Failed(xerrors.Errorf("Failed to get agent version: %w", err)), log)
			}
		}
	}

	r.automationAgentVersion = automationAgentVersion

	workflowStatus := r.doShardedClusterProcessing(ctx, sc, conn, projectConfig, log)
	if !workflowStatus.IsOK() || workflowStatus.Phase() == mdbstatus.PhaseUnsupported {
		return r.updateStatus(ctx, sc, workflowStatus, log)
	}

	sizeStatusInClusters, sizeStatus := r.calculateSizeStatus(r.sc)

	// this will be true when we finish adding a single node at that time, it's sts is ready,
	// but more than one was added in the CR, so we still need another reconcile to scale the rest
	// Important: scalers, expecially ReplicasThisReconciliation(scaler) will always return the same
	// value throughout a single Reconcile execution.
	if r.isStillScaling() {
		return r.updateStatus(ctx, sc, workflow.Pending("Continuing scaling operation for ShardedCluster %s mongodsPerShardCount ... %+v, mongosCount %+v, configServerCount %+v",
			sc.ObjectKey(),
			sizeStatus.MongodsPerShardCount,
			sizeStatus.MongosCount,
			sizeStatus.ConfigServerCount,
		), log, mdbstatus.ShardedClusterSizeConfigOption{SizeConfig: sizeStatus}, mdbstatus.ShardedClusterSizeStatusInClustersOption{SizeConfigInClusters: sizeStatusInClusters})
	}

	// only remove any stateful sets if we are scaling down
	// Note: we should only remove unused stateful sets once we are fully complete
	// removing members 1 at a time.
	if sc.Spec.ShardCount < r.deploymentState.Status.MongodbShardedClusterSizeConfig.ShardCount {
		r.removeUnusedStatefulsets(ctx, sc, log)
	}

	annotationsToAdd, err := getAnnotationsForResource(sc)
	if err != nil {
		return r.updateStatus(ctx, sc, workflow.Failed(err), log)
	}

	if vault.IsVaultSecretBackend() {
		secrets := sc.GetSecretsMountedIntoDBPod()
		vaultMap := make(map[string]string)
		for _, s := range secrets {
			path := fmt.Sprintf("%s/%s/%s", r.commonController.VaultClient.DatabaseSecretMetadataPath(), sc.Namespace, s)
			vaultMap = merge.StringToStringMap(vaultMap, r.commonController.VaultClient.GetSecretAnnotation(path))
		}
		path := fmt.Sprintf("%s/%s/%s", r.commonController.VaultClient.OperatorScretMetadataPath(), sc.Namespace, sc.Spec.Credentials)
		vaultMap = merge.StringToStringMap(vaultMap, r.commonController.VaultClient.GetSecretAnnotation(path))
		for k, val := range vaultMap {
			annotationsToAdd[k] = val
		}
	}
	// Set annotations that should be saved at the end of the reconciliation, e.g lastAchievedSpec
	if err := annotations.SetAnnotations(ctx, sc, annotationsToAdd, r.commonController.client); err != nil {
		return r.updateStatus(ctx, sc, workflow.Failed(err), log)
	}

	// Save last achieved spec in state
	r.deploymentState.LastAchievedSpec = &sc.Spec
	log.Infof("Finished reconciliation for Sharded Cluster! %s", completionMessage(conn.BaseURL(), conn.GroupID()))
	return r.updateStatus(ctx, sc, workflowStatus, log, mdbstatus.NewBaseUrlOption(deployment.Link(conn.BaseURL(), conn.GroupID())), mdbstatus.ShardedClusterSizeConfigOption{SizeConfig: sizeStatus}, mdbstatus.ShardedClusterSizeStatusInClustersOption{SizeConfigInClusters: sizeStatusInClusters}, mdbstatus.ShardedClusterMongodsPerShardCountOption{Members: r.sc.Spec.ShardCount}, mdbstatus.NewPVCsStatusOptionEmptyStatus())
}

func (r *ShardedClusterReconcileHelper) logAllScalers(log *zap.SugaredLogger) {
	for _, s := range r.getAllScalers() {
		log.Debugf("%+v", s)
	}
}

// implements all the logic to do the sharded cluster thing
func (r *ShardedClusterReconcileHelper) doShardedClusterProcessing(ctx context.Context, obj interface{}, conn om.Connection, projectConfig mdbv1.ProjectConfig, log *zap.SugaredLogger) workflow.Status {
	log.Info("ShardedCluster.doShardedClusterProcessing")
	sc := obj.(*mdbv1.MongoDB)

	if workflowStatus := ensureSupportedOpsManagerVersion(conn); workflowStatus.Phase() != mdbstatus.PhaseRunning {
		return workflowStatus
	}

	r.commonController.SetupCommonWatchers(sc, getTLSSecretNames(sc), getInternalAuthSecretNames(sc), sc.Name)

	reconcileResult := checkIfHasExcessProcesses(conn, sc.Name, log)
	if !reconcileResult.IsOK() {
		return reconcileResult
	}

	security := sc.Spec.Security
	// TODO move to webhook validations
	if security.Authentication != nil && security.Authentication.Enabled && security.Authentication.IsX509Enabled() && !sc.Spec.GetSecurity().IsTLSEnabled() {
		return workflow.Invalid("cannot have a non-tls deployment when x509 authentication is enabled")
	}

	currentAgentAuthMode, err := conn.GetAgentAuthMode()
	if err != nil {
		return workflow.Failed(err)
	}

	podEnvVars := newPodVars(conn, projectConfig, sc.Spec.LogLevel)

	workflowStatus, certSecretTypesForSTS := r.ensureSSLCertificates(ctx, sc, log)
	if !workflowStatus.IsOK() {
		return workflowStatus
	}

	prometheusCertHash, err := certs.EnsureTLSCertsForPrometheus(ctx, r.commonController.SecretClient, sc.GetNamespace(), sc.GetPrometheus(), certs.Database, log)
	if err != nil {
		return workflow.Failed(xerrors.Errorf("Could not generate certificates for Prometheus: %w", err))
	}

	opts := deploymentOptions{
		podEnvVars:           podEnvVars,
		currentAgentAuthMode: currentAgentAuthMode,
		certTLSType:          certSecretTypesForSTS,
	}

	if err = r.prepareScaleDownShardedCluster(conn, log); err != nil {
		return workflow.Failed(xerrors.Errorf("failed to perform scale down preliminary actions: %w", err))
	}

	if workflowStatus := validateMongoDBResource(sc, conn); !workflowStatus.IsOK() {
		return workflowStatus
	}

	// Ensures that all sharded cluster certificates are either of Opaque type (old design)
	// or are all of kubernetes.io/tls type
	// and save the value for future use
	allCertsType, err := getCertTypeForAllShardedClusterCertificates(certSecretTypesForSTS)
	if err != nil {
		return workflow.Failed(err)
	}

	caFilePath := util.CAFilePathInContainer
	if allCertsType == corev1.SecretTypeTLS {
		caFilePath = fmt.Sprintf("%s/ca-pem", util.TLSCaMountPath)
	}

	if workflowStatus := controlledfeature.EnsureFeatureControls(*sc, conn, conn.OpsManagerVersion(), log); !workflowStatus.IsOK() {
		return workflowStatus
	}

	for _, memberCluster := range getHealthyMemberClusters(r.allMemberClusters) {
		certConfigurator := r.prepareX509CertConfigurator(memberCluster)
		if workflowStatus := r.commonController.ensureX509SecretAndCheckTLSType(ctx, certConfigurator, currentAgentAuthMode, log); !workflowStatus.IsOK() {
			return workflowStatus
		}
	}

	if workflowStatus := ensureRoles(sc.Spec.GetSecurity().Roles, conn, log); !workflowStatus.IsOK() {
		return workflowStatus
	}

	agentCertSecretName := sc.GetSecurity().AgentClientCertificateSecretName(sc.Name).Name

	opts = deploymentOptions{
		podEnvVars:           podEnvVars,
		currentAgentAuthMode: currentAgentAuthMode,
		caFilePath:           caFilePath,
		agentCertSecretName:  agentCertSecretName,
		prometheusCertHash:   prometheusCertHash,
	}
	allConfigs := r.getAllConfigs(ctx, *sc, opts, log)

	// Recovery prevents some deadlocks that can occur during reconciliation, e.g. the setting of an incorrect automation
	// configuration and a subsequent attempt to overwrite it later, the operator would be stuck in Pending phase.
	// See CLOUDP-189433 and CLOUDP-229222 for more details.
	if recovery.ShouldTriggerRecovery(r.deploymentState.Status.Phase != mdbstatus.PhaseRunning, r.deploymentState.Status.LastTransition) {
		log.Warnf("Triggering Automatic Recovery. The MongoDB resource %s/%s is in %s state since %s", sc.Namespace, sc.Name, r.deploymentState.Status.Phase, r.deploymentState.Status.LastTransition)
		automationConfigStatus := r.updateOmDeploymentShardedCluster(ctx, conn, sc, opts, true, log).OnErrorPrepend("Failed to create/update (Ops Manager reconciliation phase):")
		deploymentStatus := r.createKubernetesResources(ctx, sc, opts, log)
		if !deploymentStatus.IsOK() {
			log.Errorf("Recovery failed because of deployment errors, %v", deploymentStatus)
		}
		if !automationConfigStatus.IsOK() {
			log.Errorf("Recovery failed because of Automation Config update errors, %v", automationConfigStatus)
		}
	}

	workflowStatus = workflow.RunInGivenOrder(anyStatefulSetNeedsToPublishStateToOM(ctx, *sc, r.commonController.client, r.deploymentState.LastAchievedSpec, allConfigs, log),
		func() workflow.Status {
			return r.updateOmDeploymentShardedCluster(ctx, conn, sc, opts, false, log).OnErrorPrepend("Failed to create/update (Ops Manager reconciliation phase):")
		},
		func() workflow.Status {
			return r.createKubernetesResources(ctx, sc, opts, log).OnErrorPrepend("Failed to create/update (Kubernetes reconciliation phase):")
		})

	if !workflowStatus.IsOK() {
		return workflowStatus
	}
	return reconcileResult
}

// prepareX509CertConfigurator returns x509 configurator for the specified memberCluster.
func (r *ShardedClusterReconcileHelper) prepareX509CertConfigurator(memberCluster multicluster.MemberCluster) certs.ShardedSetX509CertConfigurator {
	var opts []certs.Options

	// we don't have inverted mapping of memberCluster -> shard/configSrv/mongos configuration, so we need to find the specified member cluster first
	for shardIdx := range r.desiredShardsConfiguration {
		for _, shardMemberCluster := range r.shardsMemberClustersMap[shardIdx] {
			if shardMemberCluster.Name == memberCluster.Name {
				opts = append(opts, certs.ShardConfig(*r.sc, shardIdx, r.sc.Spec.GetExternalDomain(), r.GetShardScaler(shardIdx, shardMemberCluster)))
			}
		}
	}

	for _, configSrvMemberCluster := range getHealthyMemberClusters(r.configSrvMemberClusters) {
		if memberCluster.Name == configSrvMemberCluster.Name {
			opts = append(opts, certs.ConfigSrvConfig(*r.sc, r.sc.Spec.GetExternalDomain(), r.GetConfigSrvScaler(configSrvMemberCluster)))
		}
	}

	for _, mongosMemberCluster := range getHealthyMemberClusters(r.mongosMemberClusters) {
		if memberCluster.Name == mongosMemberCluster.Name {
			opts = append(opts, certs.MongosConfig(*r.sc, r.sc.Spec.GetExternalDomain(), r.GetMongosScaler(mongosMemberCluster)))
		}
	}

	certConfigurator := certs.ShardedSetX509CertConfigurator{
		MongoDB:          r.sc,
		SecretReadClient: r.commonController.SecretClient,
		MemberCluster:    memberCluster,
		CertOptions:      opts,
	}

	return certConfigurator
}

func getTLSSecretNames(sc *mdbv1.MongoDB) func() []string {
	return func() []string {
		var secretNames []string
		secretNames = append(secretNames,
			sc.GetSecurity().MemberCertificateSecretName(sc.MongosRsName()),
			sc.GetSecurity().MemberCertificateSecretName(sc.ConfigRsName()),
		)
		for i := 0; i < sc.Spec.ShardCount; i++ {
			secretNames = append(secretNames, sc.GetSecurity().MemberCertificateSecretName(sc.ShardRsName(i)))
		}
		return secretNames
	}
}

func getInternalAuthSecretNames(sc *mdbv1.MongoDB) func() []string {
	return func() []string {
		var secretNames []string
		secretNames = append(secretNames,
			sc.GetSecurity().InternalClusterAuthSecretName(sc.MongosRsName()),
			sc.GetSecurity().InternalClusterAuthSecretName(sc.ConfigRsName()),
		)
		for i := 0; i < sc.Spec.ShardCount; i++ {
			secretNames = append(secretNames, sc.GetSecurity().InternalClusterAuthSecretName(sc.ShardRsName(i)))
		}
		return secretNames
	}
}

// getCertTypeForAllShardedClusterCertificates checks whether all certificates secret are of the same type and returns it.
func getCertTypeForAllShardedClusterCertificates(certTypes map[string]bool) (corev1.SecretType, error) {
	if len(certTypes) == 0 {
		return corev1.SecretTypeTLS, nil
	}
	valueSlice := make([]bool, 0, len(certTypes))
	for _, v := range certTypes {
		valueSlice = append(valueSlice, v)
	}
	curTypeIsTLS := valueSlice[0]
	for i := 1; i < len(valueSlice); i++ {
		if valueSlice[i] != curTypeIsTLS {
			return corev1.SecretTypeOpaque, xerrors.Errorf("TLS Certificates for Sharded cluster must all be of the same type - either kubernetes.io/tls or secrets containing a concatenated pem file")
		}
	}
	if curTypeIsTLS {
		return corev1.SecretTypeTLS, nil
	}
	return corev1.SecretTypeOpaque, nil
}

// anyStatefulSetNeedsToPublishStateToOM checks to see if any stateful set
// of the given sharded cluster needs to publish state to Ops Manager before updating Kubernetes resources
func anyStatefulSetNeedsToPublishStateToOM(ctx context.Context, sc mdbv1.MongoDB, getter ConfigMapStatefulSetSecretGetter, lastSpec *mdbv1.MongoDbSpec, configs []func(mdb mdbv1.MongoDB) construct.DatabaseStatefulSetOptions, log *zap.SugaredLogger) bool {
	for _, cf := range configs {
		if publishAutomationConfigFirst(ctx, getter, sc, lastSpec, cf, log) {
			return true
		}
	}
	return false
}

// getAllConfigs returns a list of all the configuration functions associated with the Sharded Cluster.
// This includes the Mongos, the Config Server and all Shards
func (r *ShardedClusterReconcileHelper) getAllConfigs(ctx context.Context, sc mdbv1.MongoDB, opts deploymentOptions, log *zap.SugaredLogger) []func(mdb mdbv1.MongoDB) construct.DatabaseStatefulSetOptions {
	allConfigs := make([]func(mdb mdbv1.MongoDB) construct.DatabaseStatefulSetOptions, 0)
	for shardIdx, shardSpec := range r.desiredShardsConfiguration {
		for _, memberCluster := range getHealthyMemberClusters(r.shardsMemberClustersMap[shardIdx]) {
			allConfigs = append(allConfigs, r.getShardOptions(ctx, sc, shardIdx, opts, log, shardSpec, memberCluster))
		}
	}
	for _, memberCluster := range getHealthyMemberClusters(r.configSrvMemberClusters) {
		allConfigs = append(allConfigs, r.getConfigServerOptions(ctx, sc, opts, log, r.desiredConfigServerConfiguration, memberCluster))
	}
	for _, memberCluster := range getHealthyMemberClusters(r.mongosMemberClusters) {
		allConfigs = append(allConfigs, r.getMongosOptions(ctx, sc, opts, log, r.desiredMongosConfiguration, memberCluster))
	}
	return allConfigs
}

func getHealthyMemberClusters(memberClusters []multicluster.MemberCluster) []multicluster.MemberCluster {
	var result []multicluster.MemberCluster
	for i := range memberClusters {
		if memberClusters[i].Healthy {
			result = append(result, memberClusters[i])
		}
	}

	return result
}

func (r *ShardedClusterReconcileHelper) removeUnusedStatefulsets(ctx context.Context, sc *mdbv1.MongoDB, log *zap.SugaredLogger) {
	statefulsetsToRemove := r.deploymentState.Status.MongodbShardedClusterSizeConfig.ShardCount - sc.Spec.ShardCount
	shardsCount := r.deploymentState.Status.MongodbShardedClusterSizeConfig.ShardCount

	// we iterate over last 'statefulsetsToRemove' shards if any
	for i := shardsCount - statefulsetsToRemove; i < shardsCount; i++ {
		for _, memberCluster := range r.shardsMemberClustersMap[i] {
			key := kube.ObjectKey(sc.Namespace, r.GetShardStsName(i, memberCluster))
			err := memberCluster.Client.DeleteStatefulSet(ctx, key)
			if err != nil {
				// Most of all the error won't be recoverable, also our sharded cluster is in good shape - we can just warn
				// the error and leave the cleanup work for the admins
				log.Warnf("Failed to delete the statefulset %s in cluster %s: %s", key, memberCluster.Name, err)
			}
			log.Infof("Removed statefulset %s in cluster %s as it's was removed from sharded cluster", key, memberCluster.Name)
		}
	}
}

func (r *ShardedClusterReconcileHelper) ensureSSLCertificates(ctx context.Context, s *mdbv1.MongoDB, log *zap.SugaredLogger) (workflow.Status, map[string]bool) {
	tlsConfig := s.Spec.GetTLSConfig()

	certSecretTypes := map[string]bool{}
	if tlsConfig == nil || !s.Spec.GetSecurity().IsTLSEnabled() {
		return workflow.OK(), certSecretTypes
	}

	if err := r.replicateTLSCAConfigMap(ctx, log); err != nil {
		return workflow.Failed(err), nil
	}

	var workflowStatus workflow.Status = workflow.OK()
	for _, memberCluster := range getHealthyMemberClusters(r.mongosMemberClusters) {
		mongosCert := certs.MongosConfig(*s, r.sc.Spec.GetExternalDomain(), r.GetMongosScaler(memberCluster))
		tStatus := certs.EnsureSSLCertsForStatefulSet(ctx, r.commonController.SecretClient, memberCluster.SecretClient, *s.Spec.Security, mongosCert, log)
		certSecretTypes[mongosCert.CertSecretName] = true
		workflowStatus = workflowStatus.Merge(tStatus)
	}

	for _, memberCluster := range getHealthyMemberClusters(r.configSrvMemberClusters) {
		configSrvCert := certs.ConfigSrvConfig(*s, r.sc.Spec.DbCommonSpec.GetExternalDomain(), r.GetConfigSrvScaler(memberCluster))
		tStatus := certs.EnsureSSLCertsForStatefulSet(ctx, r.commonController.SecretClient, memberCluster.SecretClient, *s.Spec.Security, configSrvCert, log)
		certSecretTypes[configSrvCert.CertSecretName] = true
		workflowStatus = workflowStatus.Merge(tStatus)
	}

	for i := 0; i < s.Spec.ShardCount; i++ {
		for _, memberCluster := range getHealthyMemberClusters(r.shardsMemberClustersMap[i]) {
			shardCert := certs.ShardConfig(*s, i, r.sc.Spec.DbCommonSpec.GetExternalDomain(), r.GetShardScaler(i, memberCluster))
			tStatus := certs.EnsureSSLCertsForStatefulSet(ctx, r.commonController.SecretClient, memberCluster.SecretClient, *s.Spec.Security, shardCert, log)
			certSecretTypes[shardCert.CertSecretName] = true
			workflowStatus = workflowStatus.Merge(tStatus)
		}
	}

	return workflowStatus, certSecretTypes
}

// createKubernetesResources creates all Kubernetes objects that are specified in 'state' parameter.
// This function returns errorStatus if any errors occurred or pendingStatus if the statefulsets are not
// ready yet
// Note, that it doesn't remove any existing shards - this will be done later
func (r *ShardedClusterReconcileHelper) createKubernetesResources(ctx context.Context, s *mdbv1.MongoDB, opts deploymentOptions, log *zap.SugaredLogger) workflow.Status {
	if r.sc.Spec.IsMultiCluster() {
		// for multi-cluster deployment we should create pod-services first, as doing it after is a bit too late
		// statefulset creation loops and waits for sts to become ready, and it's easier for the replica set to be ready if
		// it can connect to other members in the clusters
		// TODO the same should be considered for external services, we should always create them before sts; now external services are created inside DatabaseInKubernetes function;
		if err := r.reconcileServices(ctx, log); err != nil {
			return workflow.Failed(xerrors.Errorf("Failed to create Config Server Stateful Set: %w", err))
		}
	}

	lastSpec := r.deploymentState.LastAchievedSpec
	// In static containers, the operator controls the order of up and downgrades.
	// For sharded clusters, we need to reverse the order of downgrades vs. upgrades.
	// See more here: https://www.mongodb.com/docs/manual/release-notes/6.0-downgrade-sharded-cluster/
	if lastSpec != nil && architectures.IsRunningStaticArchitecture(s.Annotations) && versionutil.IsDowngrade(lastSpec.Version, s.Spec.Version) {
		if mongosStatus := r.createOrUpdateMongos(ctx, s, opts, log); !mongosStatus.IsOK() {
			return mongosStatus
		}

		if shardsStatus := r.createOrUpdateShards(ctx, s, opts, log); !shardsStatus.IsOK() {
			return shardsStatus
		}

		if configStatus := r.createOrUpdateConfigServers(ctx, s, opts, log); !configStatus.IsOK() {
			return configStatus
		}
	} else {
		if configStatus := r.createOrUpdateConfigServers(ctx, s, opts, log); !configStatus.IsOK() {
			return configStatus
		}

		if shardsStatus := r.createOrUpdateShards(ctx, s, opts, log); !shardsStatus.IsOK() {
			return shardsStatus
		}

		if mongosStatus := r.createOrUpdateMongos(ctx, s, opts, log); !mongosStatus.IsOK() {
			return mongosStatus
		}
	}

	return workflow.OK()
}

func (r *ShardedClusterReconcileHelper) createOrUpdateMongos(ctx context.Context, s *mdbv1.MongoDB, opts deploymentOptions, log *zap.SugaredLogger) workflow.Status {
	// we deploy changes to sts to all mongos in all clusters
	for _, memberCluster := range getHealthyMemberClusters(r.mongosMemberClusters) {
		mongosOpts := r.getMongosOptions(ctx, *s, opts, log, r.desiredMongosConfiguration, memberCluster)
		mongosSts := construct.DatabaseStatefulSet(*s, mongosOpts, log)
		if err := create.DatabaseInKubernetes(ctx, memberCluster.Client, *s, mongosSts, mongosOpts, log); err != nil {
			return workflow.Failed(xerrors.Errorf("Failed to create Mongos Stateful Set: %w", err))
		}
	}

	// we wait for mongos statefulsets here
	if workflowStatus := r.getMergedStatefulsetStatus(ctx, s, r.mongosMemberClusters, r.GetMongosStsName); !workflowStatus.IsOK() {
		return workflowStatus
	}

	log.Infow("Created/updated StatefulSet for mongos servers", "name", s.MongosRsName())
	return workflow.OK()
}

func (r *ShardedClusterReconcileHelper) createOrUpdateShards(ctx context.Context, s *mdbv1.MongoDB, opts deploymentOptions, log *zap.SugaredLogger) workflow.Status {
	shardsNames := make([]string, s.Spec.ShardCount)
	for shardIdx := 0; shardIdx < s.Spec.ShardCount; shardIdx++ {
		// it doesn't matter for which cluster we get scaler as we need it only for ScalingFirstTime which is iterating over all member clusters internally anyway
		scalingFirstTime := r.GetShardScaler(shardIdx, r.shardsMemberClustersMap[shardIdx][0]).ScalingFirstTime()
		for _, memberCluster := range getHealthyMemberClusters(r.shardsMemberClustersMap[shardIdx]) {
			// shardsNames contains shard name, not statefulset name
			// in single cluster sts name == shard name
			// in multi cluster sts name contains cluster index, but shard name does not (it's a replicaset name)
			shardsNames[shardIdx] = s.ShardRsName(shardIdx)
			shardOpts := r.getShardOptions(ctx, *s, shardIdx, opts, log, r.desiredShardsConfiguration[shardIdx], memberCluster)
			shardSts := construct.DatabaseStatefulSet(*s, shardOpts, log)

			if workflowStatus := r.handlePVCResize(ctx, memberCluster, &shardSts, log); !workflowStatus.IsOK() {
				return workflowStatus
			}

			if err := create.DatabaseInKubernetes(ctx, memberCluster.Client, *s, shardSts, shardOpts, log); err != nil {
				return workflow.Failed(xerrors.Errorf("Failed to create StatefulSet for shard %s: %w", shardSts.Name, err))
			}

			if !scalingFirstTime {
				// If we scale for the first time, we deploy all statefulsets across all clusters for the given shard.
				// We can do that because when doing the initial deployment there is no automation config, so we can deploy
				// everything in parallel and our pods will be spinning up agents only. After everything is ready
				// (we have the case in readiness for empty AC to return true) we then publish AC with fully constructed processes
				// and all agents are starting to wire things up and configure the replicaset.
				// If we don't scale for the first time we need to wait for each individual sts as we need to scale members of the whole replica set one at a time
				if workflowStatus := getStatefulSetStatus(ctx, s.Namespace, shardSts.Name, memberCluster.Client); !workflowStatus.IsOK() {
					return workflowStatus
				}
			}
		}
		// if we scale for the first time we didn't wait for statefulsets to become ready in the loop over member clusters
		// we need to wait for all sts here instead after all were deployed/scaled up to desired members
		if scalingFirstTime {
			getShardStsName := func(memberCluster multicluster.MemberCluster) string {
				return r.GetShardStsName(shardIdx, memberCluster)
			}
			if workflowStatus := r.getMergedStatefulsetStatus(ctx, s, r.shardsMemberClustersMap[shardIdx], getShardStsName); !workflowStatus.IsOK() {
				return workflowStatus
			}
		}
	}

	log.Infow("Created/updated Stateful Sets for shards in Kubernetes", "shards", shardsNames)
	return workflow.OK()
}

func (r *ShardedClusterReconcileHelper) createOrUpdateConfigServers(ctx context.Context, s *mdbv1.MongoDB, opts deploymentOptions, log *zap.SugaredLogger) workflow.Status {
	// it doesn't matter for which cluster we get scaler here as we need it only
	// for ScalingFirstTime, which is iterating over all member clusters internally anyway
	configSrvScalingFirstTime := r.GetConfigSrvScaler(r.configSrvMemberClusters[0]).ScalingFirstTime()
	for _, memberCluster := range getHealthyMemberClusters(r.configSrvMemberClusters) {
		configSrvOpts := r.getConfigServerOptions(ctx, *s, opts, log, r.desiredConfigServerConfiguration, memberCluster)
		configSrvSts := construct.DatabaseStatefulSet(*s, configSrvOpts, log)

		if workflowStatus := r.handlePVCResize(ctx, memberCluster, &configSrvSts, log); !workflowStatus.IsOK() {
			return workflowStatus
		}

		if err := create.DatabaseInKubernetes(ctx, memberCluster.Client, *s, configSrvSts, configSrvOpts, log); err != nil {
			return workflow.Failed(xerrors.Errorf("Failed to create Config Server Stateful Set: %w", err))
		}

		if !configSrvScalingFirstTime {
			if workflowStatus := getStatefulSetStatus(ctx, s.Namespace, r.GetConfigSrvStsName(memberCluster), memberCluster.Client); !workflowStatus.IsOK() {
				return workflowStatus
			}
		}
	}

	if configSrvScalingFirstTime {
		if workflowStatus := r.getMergedStatefulsetStatus(ctx, s, r.configSrvMemberClusters, r.GetConfigSrvStsName); !workflowStatus.IsOK() {
			return workflowStatus
		}
	}

	log.Infow("Created/updated StatefulSet for config servers", "name", s.ConfigRsName(), "servers count", 0)
	return workflow.OK()
}

func (r *ShardedClusterReconcileHelper) getMergedStatefulsetStatus(ctx context.Context, s *mdbv1.MongoDB,
	memberClusters []multicluster.MemberCluster, stsNameProvider func(multicluster.MemberCluster) string,
) workflow.Status {
	var mergedStatefulSetStatus workflow.Status = workflow.OK()
	for _, memberCluster := range getHealthyMemberClusters(memberClusters) {
		statefulSetStatus := getStatefulSetStatus(ctx, s.Namespace, stsNameProvider(memberCluster), memberCluster.Client)
		mergedStatefulSetStatus = mergedStatefulSetStatus.Merge(statefulSetStatus)
	}

	return mergedStatefulSetStatus
}

func (r *ShardedClusterReconcileHelper) handlePVCResize(ctx context.Context, memberCluster multicluster.MemberCluster, sts *appsv1.StatefulSet, log *zap.SugaredLogger) workflow.Status {
	workflowStatus := create.HandlePVCResize(ctx, memberCluster.Client, sts, log)
	if !workflowStatus.IsOK() {
		return workflowStatus
	}

	if workflow.ContainsPVCOption(workflowStatus.StatusOptions()) {
		if _, err := r.updateStatus(ctx, r.sc, workflow.Pending(""), log, workflowStatus.StatusOptions()...); err != nil {
			return workflow.Failed(xerrors.Errorf("error updating status: %w", err))
		}
	}
	return workflow.OK()
}

func isShardOverridden(shardName string, shardOverrides []mdbv1.ShardOverride) bool {
	expandedOverrides := expandShardOverrides(shardOverrides)
	foundIdx := slices.IndexFunc(expandedOverrides, func(override mdbv1.ShardOverride) bool {
		return len(override.ShardNames) > 0 && override.ShardNames[0] == shardName
	})
	return foundIdx != -1
}

// calculateSizeStatus computes the current sizes of the sharded cluster deployment and return the structures that are going to be saved to the resource's status and the deployment state.
// It computes the sizes according to the deployment state (previous sizes), the desired state and the sizes returned by the scalers.
// What is important to note it the scalers used here and usage of scale.ReplicasThisReconciliation makes the sizes returned consistent throughout a single reconcile execution and
// with the guarantee that only one node can be added at a time to any replicaset.
// That means we use the scale.ReplicasThisReconciliation function with scalers in other parts of the reconciler logic (e.g. for creating sts and processes for AC, here for status).
func (r *ShardedClusterReconcileHelper) calculateSizeStatus(s *mdbv1.MongoDB) (*mdbstatus.MongodbShardedSizeStatusInClusters, *mdbstatus.MongodbShardedClusterSizeConfig) {
	sizeStatusInClusters := r.deploymentState.Status.SizeStatusInClusters.DeepCopy()
	sizeStatus := r.deploymentState.Status.MongodbShardedClusterSizeConfig.DeepCopy()

	// We calculate the current member counts for updating the status at the end of the function, after everything is ready according to the current reconcile loop's scalers
	// Before making the reconcile loop multi-cluster-first, the counts were saved only when workflow result was OK, so we're keeping the same logic here

	// We iterate over all clusters (not only healthy as it would remove the counts from those) and store counts to deployment state
	shardMongodsCountInClusters := map[string]int{}
	shardOverridesInClusters := map[string]map[string]int{}
	// In all shards, we iterate over all clusters (not only healthy as it would remove the counts from those) and store
	// counts to deployment state
	for shardIdx := 0; shardIdx < s.Spec.ShardCount; shardIdx++ {
		shardName := r.sc.ShardRsName(shardIdx)
		isOverridden := isShardOverridden(shardName, r.sc.Spec.ShardOverrides)

		// if all shards are overridden, we have nothing in shardMongodsCountInClusters, followup ticket: https://jira.mongodb.org/browse/CLOUDP-287426
		if isOverridden {
			// Initialize the map for this override if needed
			if shardOverridesInClusters[shardName] == nil {
				shardOverridesInClusters[shardName] = map[string]int{}
			}
			for _, memberCluster := range r.shardsMemberClustersMap[shardIdx] {
				currentReplicas := scale.ReplicasThisReconciliation(r.GetShardScaler(shardIdx, memberCluster))
				shardOverridesInClusters[shardName][memberCluster.Name] = currentReplicas
			}
		} else if len(shardMongodsCountInClusters) == 0 {
			// Without override, shardMongodsCountInClusters will be the same for any shard, we need to populate it
			// only once, if it's empty
			for _, memberCluster := range r.shardsMemberClustersMap[shardIdx] {
				currentReplicas := scale.ReplicasThisReconciliation(r.GetShardScaler(shardIdx, memberCluster))
				shardMongodsCountInClusters[memberCluster.Name] = currentReplicas
			}
		}
		// If shardMongodsCountInClusters is already populated, no action is needed for non-overridden shards
	}

	sizeStatusInClusters.ShardMongodsInClusters = shardMongodsCountInClusters // While we do not address the above to do, this field can be nil in the case where all shards are overridden
	sizeStatusInClusters.ShardOverridesInClusters = shardOverridesInClusters
	// TODO when we allow changes of the number of nodes in particular shards in shard overrides, then this field might become invalid or will become "MongodsPerShardCount" (for the most shards out there)
	sizeStatus.MongodsPerShardCount = sizeStatusInClusters.TotalShardMongodsInClusters()

	// We iterate over all clusters (not only healthy as it would remove the counts from those) and store counts to deployment state
	configSrvMongodsTotalCount := map[string]int{}
	for _, memberCluster := range r.configSrvMemberClusters {
		configSrvMongodsTotalCount[memberCluster.Name] = scale.ReplicasThisReconciliation(r.GetConfigSrvScaler(memberCluster))
		sizeStatusInClusters.ConfigServerMongodsInClusters[memberCluster.Name] = configSrvMongodsTotalCount[memberCluster.Name]
	}
	sizeStatus.ConfigServerCount = sizeStatusInClusters.TotalConfigServerMongodsInClusters()

	mongosCountInClusters := map[string]int{}
	for _, memberCluster := range r.mongosMemberClusters {
		mongosCountInClusters[memberCluster.Name] = scale.ReplicasThisReconciliation(r.GetMongosScaler(memberCluster))
		sizeStatusInClusters.MongosCountInClusters[memberCluster.Name] = mongosCountInClusters[memberCluster.Name]
	}

	sizeStatus.MongosCount = sizeStatusInClusters.TotalMongosCountInClusters()

	return sizeStatusInClusters, sizeStatus
}

func (r *ShardedClusterReconcileHelper) OnDelete(ctx context.Context, obj runtime.Object, log *zap.SugaredLogger) error {
	sc := obj.(*mdbv1.MongoDB)

	var errs error
	if err := r.cleanOpsManagerState(ctx, sc, log); err != nil {
		errs = multierror.Append(errs, err)
	}

	for _, item := range getHealthyMemberClusters(r.allMemberClusters) {
		c := item.Client
		if err := r.deleteClusterResources(ctx, c, sc, log); err != nil {
			errs = multierror.Append(errs, xerrors.Errorf("failed deleting dependant resources in cluster %s: %w", item.Name, err))
		}
	}

	return errs
}

func (r *ShardedClusterReconcileHelper) cleanOpsManagerState(ctx context.Context, sc *mdbv1.MongoDB, log *zap.SugaredLogger) error {
	projectConfig, credsConfig, err := project.ReadConfigAndCredentials(ctx, r.commonController.client, r.commonController.SecretClient, sc, log)
	if err != nil {
		return err
	}

	conn, _, err := connection.PrepareOpsManagerConnection(ctx, r.commonController.SecretClient, projectConfig, credsConfig, r.omConnectionFactory, sc.Namespace, log)
	if err != nil {
		return err
	}

	processNames := make([]string, 0)
	err = conn.ReadUpdateDeployment(
		func(d om.Deployment) error {
			processNames = d.GetProcessNames(om.ShardedCluster{}, sc.Name)
			if e := d.RemoveShardedClusterByName(sc.Name, log); e != nil {
				log.Warnf("Failed to remove sharded cluster from automation config: %s", e)
			}
			return nil
		},
		log,
	)
	if err != nil {
		return err
	}

	if err := om.WaitForReadyState(conn, processNames, false, log); err != nil {
		return err
	}

	if sc.Spec.Backup != nil && sc.Spec.Backup.AutoTerminateOnDeletion {
		if err := backup.StopBackupIfEnabled(conn, conn, sc.Name, backup.ShardedClusterType, log); err != nil {
			return err
		}
	}

	hostsToRemove := r.getAllHostnames(false)
	log.Infow("Stop monitoring removed hosts in Ops Manager", "hostsToBeRemoved", hostsToRemove)

	if err = host.StopMonitoring(conn, hostsToRemove, log); err != nil {
		return err
	}

	if err := r.commonController.clearProjectAuthenticationSettings(ctx, conn, sc, processNames, log); err != nil {
		return err
	}

	log.Infow("Clear feature control for group: %s", "groupID", conn.GroupID())
	if result := controlledfeature.ClearFeatureControls(conn, conn.OpsManagerVersion(), log); !result.IsOK() {
		result.Log(log)
		log.Warnf("Failed to clear feature control from group: %s", conn.GroupID())
	}

	log.Infof("Removed deployment %s from Ops Manager at %s", sc.Name, conn.BaseURL())
	return nil
}

func (r *ShardedClusterReconcileHelper) deleteClusterResources(ctx context.Context, c kubernetesClient.Client, sc *mdbv1.MongoDB, log *zap.SugaredLogger) error {
	var errs error

	// cleanup resources in the namespace as the MongoDB with the corresponding label.
	cleanupOptions := mdbv1.MongodbCleanUpOptions{
		Namespace: sc.Namespace,
		Labels:    mongoDBLabels(sc.Name),
	}

	if err := c.DeleteAllOf(ctx, &corev1.Service{}, &cleanupOptions); err != nil {
		errs = multierror.Append(errs, err)
	} else {
		log.Infof("Removed Serivces associated with %s/%s", sc.Namespace, sc.Name)
	}

	if err := c.DeleteAllOf(ctx, &appsv1.StatefulSet{}, &cleanupOptions); err != nil {
		errs = multierror.Append(errs, err)
	} else {
		log.Infof("Removed StatefulSets associated with %s/%s", sc.Namespace, sc.Name)
	}

	if err := c.DeleteAllOf(ctx, &corev1.ConfigMap{}, &cleanupOptions); err != nil {
		errs = multierror.Append(errs, err)
	} else {
		log.Infof("Removed ConfigMaps associated with %s/%s", sc.Namespace, sc.Name)
	}

	r.commonController.resourceWatcher.RemoveDependentWatchedResources(sc.ObjectKey())

	return errs
}

func AddShardedClusterController(ctx context.Context, mgr manager.Manager, memberClustersMap map[string]cluster.Cluster) error {
	// Create a new controller
	reconciler := newShardedClusterReconciler(ctx, mgr.GetClient(), multicluster.ClustersMapToClientMap(memberClustersMap), om.NewOpsManagerConnection)
	options := controller.Options{Reconciler: reconciler, MaxConcurrentReconciles: env.ReadIntOrDefault(util.MaxConcurrentReconcilesEnv, 1)}
	c, err := controller.New(util.MongoDbShardedClusterController, mgr, options)
	if err != nil {
		return err
	}

	// watch for changes to sharded cluster MongoDB resources
	eventHandler := ResourceEventHandler{deleter: reconciler}
	err = c.Watch(source.Kind(mgr.GetCache(), &mdbv1.MongoDB{}), &eventHandler, watch.PredicatesForMongoDB(mdbv1.ShardedCluster))
	if err != nil {
		return err
	}

	err = c.Watch(
		&source.Channel{Source: OmUpdateChannel},
		&handler.EnqueueRequestForObject{},
		watch.PredicatesForMongoDB(mdbv1.ShardedCluster),
	)
	if err != nil {
		return xerrors.Errorf("not able to setup OmUpdateChannel to listent to update events from OM: %s", err)
	}

	err = c.Watch(source.Kind(mgr.GetCache(), &corev1.ConfigMap{}),
		&watch.ResourcesHandler{ResourceType: watch.ConfigMap, ResourceWatcher: reconciler.resourceWatcher})
	if err != nil {
		return err
	}

	err = c.Watch(source.Kind(mgr.GetCache(), &corev1.Secret{}),
		&watch.ResourcesHandler{ResourceType: watch.Secret, ResourceWatcher: reconciler.resourceWatcher})
	if err != nil {
		return err
	}
	// if vault secret backend is enabled watch for Vault secret change and  reconcile
	if vault.IsVaultSecretBackend() {
		eventChannel := make(chan event.GenericEvent)
		go vaultwatcher.WatchSecretChangeForMDB(ctx, zap.S(), eventChannel, reconciler.client, reconciler.VaultClient, mdbv1.ShardedCluster)

		err = c.Watch(
			&source.Channel{Source: eventChannel},
			&handler.EnqueueRequestForObject{},
		)
		if err != nil {
			zap.S().Errorf("Failed to watch for vault secret changes: %w", err)
		}
	}
	zap.S().Infof("Registered controller %s", util.MongoDbShardedClusterController)

	return nil
}

func (r *ShardedClusterReconcileHelper) getConfigSrvHostnames(memberCluster multicluster.MemberCluster, replicas int) ([]string, []string) {
	externalDomain := r.sc.Spec.ConfigSrvSpec.ClusterSpecList.GetExternalDomainForMemberCluster(memberCluster.Name)
	if externalDomain == nil && r.sc.Spec.IsMultiCluster() {
		externalDomain = r.sc.Spec.DbCommonSpec.GetExternalDomain()
	}
	if !memberCluster.Legacy {
		return dns.GetMultiClusterProcessHostnamesAndPodNames(r.sc.ConfigRsName(), r.sc.Namespace, memberCluster.Index, replicas, r.sc.Spec.GetClusterDomain(), externalDomain)
	} else {
		return dns.GetDNSNames(r.GetConfigSrvStsName(memberCluster), r.sc.ConfigSrvServiceName(), r.sc.Namespace, r.sc.Spec.GetClusterDomain(), replicas, externalDomain)
	}
}

func (r *ShardedClusterReconcileHelper) getShardHostnames(shardIdx int, memberCluster multicluster.MemberCluster, replicas int) ([]string, []string) {
	externalDomain := r.sc.Spec.ShardSpec.ClusterSpecList.GetExternalDomainForMemberCluster(memberCluster.Name)
	if externalDomain == nil && r.sc.Spec.IsMultiCluster() {
		externalDomain = r.sc.Spec.DbCommonSpec.GetExternalDomain()
	}
	if !memberCluster.Legacy {
		return dns.GetMultiClusterProcessHostnamesAndPodNames(r.sc.ShardRsName(shardIdx), r.sc.Namespace, memberCluster.Index, replicas, r.sc.Spec.GetClusterDomain(), externalDomain)
	} else {
		return dns.GetDNSNames(r.GetShardStsName(shardIdx, memberCluster), r.sc.ShardServiceName(), r.sc.Namespace, r.sc.Spec.GetClusterDomain(), replicas, externalDomain)
	}
}

func (r *ShardedClusterReconcileHelper) getMongosHostnames(memberCluster multicluster.MemberCluster, replicas int) ([]string, []string) {
	externalDomain := r.sc.Spec.MongosSpec.ClusterSpecList.GetExternalDomainForMemberCluster(memberCluster.Name)
	if externalDomain == nil && r.sc.Spec.IsMultiCluster() {
		externalDomain = r.sc.Spec.DbCommonSpec.GetExternalDomain()
	}
	if !memberCluster.Legacy {
		return dns.GetMultiClusterProcessHostnamesAndPodNames(r.sc.MongosRsName(), r.sc.Namespace, memberCluster.Index, replicas, r.sc.Spec.GetClusterDomain(), externalDomain)
	} else {
		// In Single Cluster Mode, only Mongos are exposed to the outside consumption. As such, they need to use proper
		// External Domain.
		externalDomain = r.sc.Spec.GetExternalDomain()
		return dns.GetDNSNames(r.GetMongosStsName(memberCluster), r.sc.ServiceName(), r.sc.Namespace, r.sc.Spec.GetClusterDomain(), replicas, externalDomain)
	}
}

// prepareScaleDownShardedCluster collects all replicasets members to scale down, from configservers and shards, accross
// all clusters, and pass them to PrepareScaleDownFromMap, which sets their votes and priorities to 0
func (r *ShardedClusterReconcileHelper) prepareScaleDownShardedCluster(omClient om.Connection, log *zap.SugaredLogger) error {
	membersToScaleDown := make(map[string][]string)
	var healthyProcessesToWaitForGoalState []string

	for _, memberCluster := range r.configSrvMemberClusters {
		currentReplicas := memberCluster.Replicas
		desiredReplicas := scale.ReplicasThisReconciliation(r.GetConfigSrvScaler(memberCluster))
		_, currentPodNames := r.getConfigSrvHostnames(memberCluster, currentReplicas)
		if desiredReplicas < currentReplicas {
			log.Debugf("Detected configSrv in cluster %s is scaling down: desiredReplicas=%d, currentReplicas=%d", memberCluster.Name, desiredReplicas, currentReplicas)
			configRsName := r.sc.ConfigRsName()
			if _, ok := membersToScaleDown[configRsName]; !ok {
				membersToScaleDown[configRsName] = []string{}
			}
			podNamesToScaleDown := currentPodNames[desiredReplicas:currentReplicas]
			membersToScaleDown[configRsName] = append(membersToScaleDown[configRsName], podNamesToScaleDown...)
			if memberCluster.Healthy {
				healthyProcessesToWaitForGoalState = append(healthyProcessesToWaitForGoalState, podNamesToScaleDown...)
			}
		}
	}

	// Scaledown size of each shard
	for shardIdx, memberClusters := range r.shardsMemberClustersMap {
		for _, memberCluster := range memberClusters {
			currentReplicas := memberCluster.Replicas
			desiredReplicas := scale.ReplicasThisReconciliation(r.GetShardScaler(shardIdx, memberCluster))
			_, currentPodNames := r.getShardHostnames(shardIdx, memberCluster, currentReplicas)
			if desiredReplicas < currentReplicas {
				log.Debugf("Detected shard idx=%d in cluster %s is scaling down: desiredReplicas=%d, currentReplicas=%d", shardIdx, memberCluster.Name, desiredReplicas, currentReplicas)
				shardRsName := r.sc.ShardRsName(shardIdx)
				if _, ok := membersToScaleDown[shardRsName]; !ok {
					membersToScaleDown[shardRsName] = []string{}
				}
				podNamesToScaleDown := currentPodNames[desiredReplicas:currentReplicas]
				membersToScaleDown[shardRsName] = append(membersToScaleDown[shardRsName], podNamesToScaleDown...)
				if memberCluster.Healthy {
					healthyProcessesToWaitForGoalState = append(healthyProcessesToWaitForGoalState, podNamesToScaleDown...)
				}
			}
		}
	}

	if len(membersToScaleDown) > 0 {
		if err := replicaset.PrepareScaleDownFromMap(omClient, membersToScaleDown, healthyProcessesToWaitForGoalState, log); err != nil {
			return err
		}
	}
	return nil
}

// deploymentOptions contains fields required for creating the OM deployment for the Sharded Cluster.
type deploymentOptions struct {
	podEnvVars           *env.PodEnvVars
	currentAgentAuthMode string
	caFilePath           string
	agentCertSecretName  string
	certTLSType          map[string]bool
	finalizing           bool
	processNames         []string
	prometheusCertHash   string
}

// updateOmDeploymentShardedCluster performs OM registration operation for the sharded cluster. So the changes will be finally propagated
// to automation agents in containers
// Note that the process may have two phases (if shards number is decreased):
// phase 1: "drain" the shards: remove them from sharded cluster, put replica set names to "draining" array, not remove
// replica sets and processes, wait for agents to reach the goal
// phase 2: remove the "junk" replica sets and their processes, wait for agents to reach the goal.
// The logic is designed to be idempotent: if the reconciliation is retried the controller will never skip the phase 1
// until the agents have performed draining
func (r *ShardedClusterReconcileHelper) updateOmDeploymentShardedCluster(ctx context.Context, conn om.Connection, sc *mdbv1.MongoDB, opts deploymentOptions, isRecovering bool, log *zap.SugaredLogger) workflow.Status {
	err := r.waitForAgentsToRegister(sc, conn, log)
	if err != nil {
		if !isRecovering {
			return workflow.Failed(err)
		}
		logWarnIgnoredDueToRecovery(log, err)
	}

	dep, err := conn.ReadDeployment()
	if err != nil {
		if !isRecovering {
			return workflow.Failed(err)
		}
		logWarnIgnoredDueToRecovery(log, err)
	}

	opts.finalizing = false
	opts.processNames = dep.GetProcessNames(om.ShardedCluster{}, sc.Name)

	processNames, shardsRemoving, workflowStatus := r.publishDeployment(ctx, conn, sc, &opts, isRecovering, log)

	if !workflowStatus.IsOK() {
		if !isRecovering {
			return workflowStatus
		}
		logWarnIgnoredDueToRecovery(log, workflowStatus)
	}

	if err = om.WaitForReadyState(conn, processNames, isRecovering, log); err != nil {
		if !isRecovering {
			if shardsRemoving {
				return workflow.Pending("automation agents haven't reached READY state: shards removal in progress")
			}
			return workflow.Failed(err)
		}
		logWarnIgnoredDueToRecovery(log, err)
	}

	if shardsRemoving {
		opts.finalizing = true

		log.Infof("Some shards were removed from the sharded cluster, we need to remove them from the deployment completely")
		processNames, _, workflowStatus := r.publishDeployment(ctx, conn, sc, &opts, isRecovering, log)
		if !workflowStatus.IsOK() {
			if !isRecovering {
				return workflowStatus
			}
			logWarnIgnoredDueToRecovery(log, workflowStatus)
		}

		if err = om.WaitForReadyState(conn, processNames, isRecovering, log); err != nil {
			if !isRecovering {
				return workflow.Failed(xerrors.Errorf("automation agents haven't reached READY state while cleaning replica set and processes: %w", err))
			}
			logWarnIgnoredDueToRecovery(log, err)
		}
	}

	currentHosts := r.getAllHostnames(false)
	wantedHosts := r.getAllHostnames(true)

	if err = host.CalculateDiffAndStopMonitoring(conn, currentHosts, wantedHosts, log); err != nil {
		if !isRecovering {
			return workflow.Failed(err)
		}
		logWarnIgnoredDueToRecovery(log, err)
	}

	if workflowStatus := r.commonController.ensureBackupConfigurationAndUpdateStatus(ctx, conn, sc, r.commonController.SecretClient, log); !workflowStatus.IsOK() {
		if !isRecovering {
			return workflowStatus
		}
		logWarnIgnoredDueToRecovery(log, err)
	}

	log.Info("Updated Ops Manager for sharded cluster")
	return workflow.OK()
}

func (r *ShardedClusterReconcileHelper) publishDeployment(ctx context.Context, conn om.Connection, sc *mdbv1.MongoDB, opts *deploymentOptions, isRecovering bool, log *zap.SugaredLogger) ([]string, bool, workflow.Status) {
	// Mongos
	var mongosProcesses []om.Process
	// We take here the first cluster arbitrarily because the options are used for irrelevant stuff below, same for
	// config servers and shards below
	mongosMemberCluster := r.mongosMemberClusters[0]
	mongosOptionsFunc := r.getMongosOptions(ctx, *sc, *opts, log, r.desiredMongosConfiguration, mongosMemberCluster)
	mongosOptions := mongosOptionsFunc(*r.sc)
	mongosInternalClusterPath := fmt.Sprintf("%s/%s", util.InternalClusterAuthMountPath, mongosOptions.InternalClusterHash)
	mongosMemberCertPath := fmt.Sprintf("%s/%s", util.TLSCertMountPath, mongosOptions.CertificateHash)
	if mongosOptions.CertificateHash == "" {
		mongosMemberCertPath = util.PEMKeyFilePathInContainer
	}
	mongosProcesses = append(mongosProcesses, r.createDesiredMongosProcesses(mongosMemberCertPath)...)

	// Config server
	configSrvMemberCluster := r.configSrvMemberClusters[0]
	configSrvOptionsFunc := r.getConfigServerOptions(ctx, *sc, *opts, log, r.desiredConfigServerConfiguration, configSrvMemberCluster)
	configSrvOptions := configSrvOptionsFunc(*r.sc)

	configSrvInternalClusterPath := fmt.Sprintf("%s/%s", util.InternalClusterAuthMountPath, configSrvOptions.InternalClusterHash)
	configSrvMemberCertPath := fmt.Sprintf("%s/%s", util.TLSCertMountPath, configSrvOptions.CertificateHash)
	if configSrvOptions.CertificateHash == "" {
		configSrvMemberCertPath = util.PEMKeyFilePathInContainer
	}

	existingDeployment, err := conn.ReadDeployment()
	if err != nil {
		return nil, false, workflow.Failed(err)
	}

	configSrvProcesses, configSrvMemberOptions := r.createDesiredConfigSrvProcessesAndMemberOptions(configSrvMemberCertPath)
	configRs, _ := buildReplicaSetFromProcesses(sc.ConfigRsName(), configSrvProcesses, sc, configSrvMemberOptions, existingDeployment)

	// Shards
	shards := make([]om.ReplicaSetWithProcesses, sc.Spec.ShardCount)
	var shardInternalClusterPaths []string
	for shardIdx := 0; shardIdx < r.sc.Spec.ShardCount; shardIdx++ {
		shardOptionsFunc := r.getShardOptions(ctx, *sc, shardIdx, *opts, log, r.desiredShardsConfiguration[shardIdx], r.shardsMemberClustersMap[shardIdx][0])
		shardOptions := shardOptionsFunc(*r.sc)
		shardInternalClusterPaths = append(shardInternalClusterPaths, fmt.Sprintf("%s/%s", util.InternalClusterAuthMountPath, shardOptions.InternalClusterHash))
		shardMemberCertPath := fmt.Sprintf("%s/%s", util.TLSCertMountPath, shardOptions.CertificateHash)
		desiredShardProcesses, desiredShardMemberOptions := r.createDesiredShardProcessesAndMemberOptions(shardIdx, shardMemberCertPath)
		shards[shardIdx], _ = buildReplicaSetFromProcesses(r.sc.ShardRsName(shardIdx), desiredShardProcesses, sc, desiredShardMemberOptions, existingDeployment)
	}

	// updateOmAuthentication normally takes care of the certfile rotation code, but since sharded-cluster is special pertaining multiple clusterfiles, we code this part here for now.
	// We can look into unifying this into updateOmAuthentication at a later stage.
	if err := conn.ReadUpdateDeployment(func(d om.Deployment) error {
		setInternalAuthClusterFileIfItHasChanged(d, sc.GetSecurity().GetInternalClusterAuthenticationMode(), sc.Name, configSrvInternalClusterPath, mongosInternalClusterPath, shardInternalClusterPaths, isRecovering)
		return nil
	}, log); err != nil {
		return nil, false, workflow.Failed(err)
	}

	workflowStatus, additionalReconciliationRequired := r.commonController.updateOmAuthentication(ctx, conn, opts.processNames, sc, opts.agentCertSecretName, opts.caFilePath, "", isRecovering, log)
	if !workflowStatus.IsOK() {
		if !isRecovering {
			return nil, false, workflowStatus
		}
		logWarnIgnoredDueToRecovery(log, workflowStatus)
	}

	var finalProcesses []string
	shardsRemoving := false
	err = conn.ReadUpdateDeployment(
		func(d om.Deployment) error {
			allProcesses := getAllProcesses(shards, configRs, mongosProcesses)
			// it is not possible to disable internal cluster authentication once enabled
			if sc.Spec.Security.GetInternalClusterAuthenticationMode() == "" && d.ExistingProcessesHaveInternalClusterAuthentication(allProcesses) {
				return xerrors.Errorf("cannot disable x509 internal cluster authentication")
			}
			numberOfOtherMembers := d.GetNumberOfExcessProcesses(sc.Name)
			if numberOfOtherMembers > 0 {
				return xerrors.Errorf("cannot have more than 1 MongoDB Cluster per project (see https://docs.mongodb.com/kubernetes-operator/stable/tutorial/migrate-to-single-resource/)")
			}

			lastConfigServerConf, err := mdbv1.GetLastAdditionalMongodConfigByType(r.deploymentState.LastAchievedSpec, mdbv1.ConfigServerConfig)
			if err != nil {
				return err
			}

			lastShardServerConf, err := mdbv1.GetLastAdditionalMongodConfigByType(r.deploymentState.LastAchievedSpec, mdbv1.ShardConfig)
			if err != nil {
				return err
			}

			lastMongosServerConf, err := mdbv1.GetLastAdditionalMongodConfigByType(r.deploymentState.LastAchievedSpec, mdbv1.MongosConfig)
			if err != nil {
				return err
			}

			mergeOpts := om.DeploymentShardedClusterMergeOptions{
				Name:                                 sc.Name,
				MongosProcesses:                      mongosProcesses,
				ConfigServerRs:                       configRs,
				Shards:                               shards,
				Finalizing:                           opts.finalizing,
				ConfigServerAdditionalOptionsPrev:    lastConfigServerConf.ToMap(),
				ConfigServerAdditionalOptionsDesired: sc.Spec.ConfigSrvSpec.AdditionalMongodConfig.ToMap(),
				ShardAdditionalOptionsPrev:           lastShardServerConf.ToMap(),
				ShardAdditionalOptionsDesired:        sc.Spec.ShardSpec.AdditionalMongodConfig.ToMap(),
				MongosAdditionalOptionsPrev:          lastMongosServerConf.ToMap(),
				MongosAdditionalOptionsDesired:       sc.Spec.MongosSpec.AdditionalMongodConfig.ToMap(),
			}

			if shardsRemoving, err = d.MergeShardedCluster(mergeOpts); err != nil {
				return err
			}

			d.AddMonitoringAndBackup(log, sc.Spec.GetSecurity().IsTLSEnabled(), opts.caFilePath)
			d.ConfigureTLS(sc.Spec.GetSecurity(), opts.caFilePath)

			setupInternalClusterAuth(d, sc.Name, sc.GetSecurity().GetInternalClusterAuthenticationMode(),
				configSrvInternalClusterPath, mongosInternalClusterPath, shardInternalClusterPaths)

			_ = UpdatePrometheus(ctx, &d, conn, sc.GetPrometheus(), r.commonController.SecretClient, sc.GetNamespace(), opts.prometheusCertHash, log)

			finalProcesses = d.GetProcessNames(om.ShardedCluster{}, sc.Name)

			return nil
		},
		log,
	)
	if err != nil {
		return nil, shardsRemoving, workflow.Failed(err)
	}

	// Here we only support sc.Spec.Agent on purpose because logRotation for the agents and all processes
	// are configured the same way, its unrelated what type of process it is.
	if reconcileResult, _ := ReconcileLogRotateSetting(conn, sc.Spec.Agent, log); !reconcileResult.IsOK() {
		return nil, shardsRemoving, reconcileResult
	}

	if err := om.WaitForReadyState(conn, opts.processNames, isRecovering, log); err != nil {
		return nil, shardsRemoving, workflow.Failed(err)
	}

	if additionalReconciliationRequired {
		return nil, shardsRemoving, workflow.Pending("Performing multi stage reconciliation")
	}

	return finalProcesses, shardsRemoving, workflow.OK()
}

func logWarnIgnoredDueToRecovery(log *zap.SugaredLogger, err any) {
	log.Warnf("ignoring error due to automatic recovery process: %v", err)
}

func setInternalAuthClusterFileIfItHasChanged(d om.Deployment, internalAuthMode string, name string, configInternalClusterPath string, mongosInternalClusterPath string, shardsInternalClusterPath []string, isRecovering bool) {
	d.SetInternalClusterFilePathOnlyIfItThePathHasChanged(d.GetShardedClusterConfigProcessNames(name), configInternalClusterPath, internalAuthMode, isRecovering)
	d.SetInternalClusterFilePathOnlyIfItThePathHasChanged(d.GetShardedClusterMongosProcessNames(name), mongosInternalClusterPath, internalAuthMode, isRecovering)
	for i, path := range shardsInternalClusterPath {
		d.SetInternalClusterFilePathOnlyIfItThePathHasChanged(d.GetShardedClusterShardProcessNames(name, i), path, internalAuthMode, isRecovering)
	}
}

func setupInternalClusterAuth(d om.Deployment, name string, internalClusterAuthMode string, configInternalClusterPath string, mongosInternalClusterPath string, shardsInternalClusterPath []string) {
	d.ConfigureInternalClusterAuthentication(d.GetShardedClusterConfigProcessNames(name), internalClusterAuthMode, configInternalClusterPath)
	d.ConfigureInternalClusterAuthentication(d.GetShardedClusterMongosProcessNames(name), internalClusterAuthMode, mongosInternalClusterPath)

	for i, path := range shardsInternalClusterPath {
		d.ConfigureInternalClusterAuthentication(d.GetShardedClusterShardProcessNames(name, i), internalClusterAuthMode, path)
	}
}

func getAllProcesses(shards []om.ReplicaSetWithProcesses, configRs om.ReplicaSetWithProcesses, mongosProcesses []om.Process) []om.Process {
	allProcesses := make([]om.Process, 0)
	for _, shard := range shards {
		allProcesses = append(allProcesses, shard.Processes...)
	}
	allProcesses = append(allProcesses, configRs.Processes...)
	allProcesses = append(allProcesses, mongosProcesses...)
	return allProcesses
}

func (r *ShardedClusterReconcileHelper) waitForAgentsToRegister(sc *mdbv1.MongoDB, conn om.Connection, log *zap.SugaredLogger) error {
	var mongosHostnames []string
	for _, memberCluster := range getHealthyMemberClusters(r.mongosMemberClusters) {
		hostnames, _ := r.getMongosHostnames(memberCluster, scale.ReplicasThisReconciliation(r.GetMongosScaler(memberCluster)))
		mongosHostnames = append(mongosHostnames, hostnames...)
	}

	if err := agents.WaitForRsAgentsToRegisterSpecifiedHostnames(conn, mongosHostnames, log.With("hostnamesOf", "mongos")); err != nil {
		return xerrors.Errorf("Mongos agents didn't register with Ops Manager: %w", err)
	}

	var configSrvHostnames []string
	for _, memberCluster := range getHealthyMemberClusters(r.configSrvMemberClusters) {
		hostnames, _ := r.getConfigSrvHostnames(memberCluster, scale.ReplicasThisReconciliation(r.GetConfigSrvScaler(memberCluster)))
		configSrvHostnames = append(configSrvHostnames, hostnames...)
	}
	if err := agents.WaitForRsAgentsToRegisterSpecifiedHostnames(conn, configSrvHostnames, log.With("hostnamesOf", "configServer")); err != nil {
		return xerrors.Errorf("Config server agents didn't register with Ops Manager: %w", err)
	}

	for shardIdx := 0; shardIdx < sc.Spec.ShardCount; shardIdx++ {
		var shardHostnames []string
		for _, memberCluster := range getHealthyMemberClusters(r.shardsMemberClustersMap[shardIdx]) {
			hostnames, _ := r.getShardHostnames(shardIdx, memberCluster, scale.ReplicasThisReconciliation(r.GetShardScaler(shardIdx, memberCluster)))
			shardHostnames = append(shardHostnames, hostnames...)
		}
		if err := agents.WaitForRsAgentsToRegisterSpecifiedHostnames(conn, shardHostnames, log.With("hostnamesOf", "shard", "shardIdx", shardIdx)); err != nil {
			return xerrors.Errorf("Shards agents didn't register with Ops Manager: %w", err)
		}
	}

	return nil
}

func (r *ShardedClusterReconcileHelper) getAllHostnames(desiredReplicas bool) []string {
	configSrvHostnames, _ := r.getAllConfigSrvHostnamesAndPodNames(desiredReplicas)
	mongosHostnames, _ := r.getAllMongosHostnamesAndPodNames(desiredReplicas)
	shardHostnames, _ := r.getAllShardHostnamesAndPodNames(desiredReplicas)

	var hostnames []string
	hostnames = append(hostnames, configSrvHostnames...)
	hostnames = append(hostnames, mongosHostnames...)
	hostnames = append(hostnames, shardHostnames...)

	return hostnames
}

func (r *ShardedClusterReconcileHelper) getAllConfigSrvHostnamesAndPodNames(desiredReplicas bool) ([]string, []string) {
	var configSrvHostnames []string
	var configSrvPodNames []string
	for _, memberCluster := range r.configSrvMemberClusters {
		replicas := memberCluster.Replicas
		if desiredReplicas {
			replicas = scale.ReplicasThisReconciliation(r.GetConfigSrvScaler(memberCluster))
		}
		hostnames, podNames := r.getConfigSrvHostnames(memberCluster, replicas)
		configSrvHostnames = append(configSrvHostnames, hostnames...)
		configSrvPodNames = append(configSrvPodNames, podNames...)
	}
	return configSrvHostnames, configSrvPodNames
}

func (r *ShardedClusterReconcileHelper) getAllShardHostnamesAndPodNames(desiredReplicas bool) ([]string, []string) {
	var shardHostnames []string
	var shardPodNames []string
	for shardIdx, memberClusterMap := range r.shardsMemberClustersMap {
		for _, memberCluster := range memberClusterMap {
			replicas := memberCluster.Replicas
			if desiredReplicas {
				replicas = scale.ReplicasThisReconciliation(r.GetShardScaler(shardIdx, memberCluster))
			}
			hostnames, podNames := r.getShardHostnames(shardIdx, memberCluster, replicas)
			shardHostnames = append(shardHostnames, hostnames...)
			shardPodNames = append(shardPodNames, podNames...)
		}
	}

	return shardHostnames, shardPodNames
}

func (r *ShardedClusterReconcileHelper) getAllMongosHostnamesAndPodNames(desiredReplicas bool) ([]string, []string) {
	var mongosHostnames []string
	var mongosPodNames []string
	for _, memberCluster := range r.mongosMemberClusters {
		replicas := memberCluster.Replicas
		if desiredReplicas {
			replicas = scale.ReplicasThisReconciliation(r.GetMongosScaler(memberCluster))
		}
		hostnames, podNames := r.getMongosHostnames(memberCluster, replicas)
		mongosHostnames = append(mongosHostnames, hostnames...)
		mongosPodNames = append(mongosPodNames, podNames...)
	}
	return mongosHostnames, mongosPodNames
}

func (r *ShardedClusterReconcileHelper) createDesiredMongosProcesses(certificateFilePath string) []om.Process {
	var processes []om.Process
	for _, memberCluster := range r.mongosMemberClusters {
		hostnames, podNames := r.getMongosHostnames(memberCluster, scale.ReplicasThisReconciliation(r.GetMongosScaler(memberCluster)))
		for i := range hostnames {
			process := om.NewMongosProcess(podNames[i], hostnames[i], r.sc.Spec.MongosSpec.GetAdditionalMongodConfig(), r.sc.GetSpec(), certificateFilePath, r.sc.Annotations, r.sc.CalculateFeatureCompatibilityVersion())
			processes = append(processes, process)
		}
	}

	return processes
}

func (r *ShardedClusterReconcileHelper) createDesiredConfigSrvProcessesAndMemberOptions(certificateFilePath string) ([]om.Process, []automationconfig.MemberOptions) {
	var processes []om.Process
	var memberOptions []automationconfig.MemberOptions
	for _, memberCluster := range r.configSrvMemberClusters {
		hostnames, podNames := r.getConfigSrvHostnames(memberCluster, scale.ReplicasThisReconciliation(r.GetConfigSrvScaler(memberCluster)))
		for i := range hostnames {
			process := om.NewMongodProcess(podNames[i], hostnames[i], r.sc.Spec.ConfigSrvSpec.GetAdditionalMongodConfig(), r.sc.GetSpec(), certificateFilePath, r.sc.Annotations, r.sc.CalculateFeatureCompatibilityVersion())
			processes = append(processes, process)
		}

		specMemberConfig := r.desiredConfigServerConfiguration.GetClusterSpecItem(memberCluster.Name).MemberConfig
		memberOptions = append(memberOptions, specMemberConfig...)
	}

	return processes, memberOptions
}

func (r *ShardedClusterReconcileHelper) createDesiredShardProcessesAndMemberOptions(shardIdx int, certificateFilePath string) ([]om.Process, []automationconfig.MemberOptions) {
	var processes []om.Process
	var memberOptions []automationconfig.MemberOptions
	for _, memberCluster := range r.shardsMemberClustersMap[shardIdx] {
		hostnames, podNames := r.getShardHostnames(shardIdx, memberCluster, scale.ReplicasThisReconciliation(r.GetShardScaler(shardIdx, memberCluster)))
		for i := range hostnames {
			process := om.NewMongodProcess(podNames[i], hostnames[i], r.desiredShardsConfiguration[shardIdx].GetAdditionalMongodConfig(), r.sc.GetSpec(), certificateFilePath, r.sc.Annotations, r.sc.CalculateFeatureCompatibilityVersion())
			processes = append(processes, process)
		}
		specMemberOptions := r.desiredShardsConfiguration[shardIdx].GetClusterSpecItem(memberCluster.Name).MemberConfig
		memberOptions = append(memberOptions, specMemberOptions...)
	}

	return processes, memberOptions
}

func createConfigSrvProcesses(set appsv1.StatefulSet, mdb *mdbv1.MongoDB, certificateFilePath string) []om.Process {
	return createMongodProcessForShardedCluster(set, mdb.Spec.ConfigSrvSpec.GetAdditionalMongodConfig(), mdb, certificateFilePath)
}

func createShardProcesses(set appsv1.StatefulSet, mdb *mdbv1.MongoDB, certificateFilePath string) []om.Process {
	return createMongodProcessForShardedCluster(set, mdb.Spec.ShardSpec.GetAdditionalMongodConfig(), mdb, certificateFilePath)
}

func createMongodProcessForShardedCluster(set appsv1.StatefulSet, additionalMongodConfig *mdbv1.AdditionalMongodConfig, mdb *mdbv1.MongoDB, certificateFilePath string) []om.Process {
	hostnames, names := dns.GetDnsForStatefulSet(set, mdb.Spec.GetClusterDomain(), nil)
	processes := make([]om.Process, len(hostnames))

	for idx, hostname := range hostnames {
		processes[idx] = om.NewMongodProcess(names[idx], hostname, additionalMongodConfig, &mdb.Spec, certificateFilePath, mdb.Annotations, mdb.CalculateFeatureCompatibilityVersion())
	}

	return processes
}

// buildReplicaSetFromProcesses creates the 'ReplicaSetWithProcesses' with specified processes. This is of use only
// for sharded cluster (config server, shards)
func buildReplicaSetFromProcesses(name string, members []om.Process, mdb *mdbv1.MongoDB, memberOptions []automationconfig.MemberOptions, deployment om.Deployment) (om.ReplicaSetWithProcesses, error) {
	replicaSet := om.NewReplicaSet(name, mdb.Spec.GetMongoDBVersion(nil))

	existingProcessIds := getReplicaSetProcessIdsFromReplicaSets(replicaSet.Name(), deployment)
	var rsWithProcesses om.ReplicaSetWithProcesses
	if mdb.Spec.IsMultiCluster() {
		// we're passing nil as connectivity argument as in sharded clusters horizons don't make much sense as we don't expose externally individual shards
		// we don't support exposing externally individual shards in single cluster as well
		// in case of multi-cluster without a service mesh we'll use externalDomains for all shards, so the hostnames will be valid from inside and outside, therefore
		// horizons are not needed
		rsWithProcesses = om.NewMultiClusterReplicaSetWithProcesses(replicaSet, members, memberOptions, existingProcessIds, nil)
	} else {
		rsWithProcesses = om.NewReplicaSetWithProcesses(replicaSet, members, memberOptions)
		rsWithProcesses.SetHorizons(mdb.Spec.Connectivity.ReplicaSetHorizons)
	}
	return rsWithProcesses, nil
}

// getConfigServerOptions returns the Options needed to build the StatefulSet for the config server.
func (r *ShardedClusterReconcileHelper) getConfigServerOptions(ctx context.Context, sc mdbv1.MongoDB, opts deploymentOptions, log *zap.SugaredLogger, configSrvSpec *mdbv1.ShardedClusterComponentSpec, memberCluster multicluster.MemberCluster) func(mdb mdbv1.MongoDB) construct.DatabaseStatefulSetOptions {
	certSecretName := sc.GetSecurity().MemberCertificateSecretName(sc.ConfigRsName())
	internalClusterSecretName := sc.GetSecurity().InternalClusterAuthSecretName(sc.ConfigRsName())

	var vaultConfig vault.VaultConfiguration
	var databaseSecretPath string
	if r.commonController.VaultClient != nil {
		vaultConfig = r.commonController.VaultClient.VaultConfig
		databaseSecretPath = r.commonController.VaultClient.DatabaseSecretPath()
	}

	return construct.ConfigServerOptions(
		configSrvSpec,
		memberCluster.Name,
		Replicas(scale.ReplicasThisReconciliation(r.GetConfigSrvScaler(memberCluster))),
		StatefulSetNameOverride(r.GetConfigSrvStsName(memberCluster)),
		ServiceName(r.GetConfigSrvServiceName(memberCluster)),
		PodEnvVars(opts.podEnvVars),
		CurrentAgentAuthMechanism(opts.currentAgentAuthMode),
		CertificateHash(enterprisepem.ReadHashFromSecret(ctx, r.commonController.SecretClient, sc.Namespace, certSecretName, databaseSecretPath, log)),
		InternalClusterHash(enterprisepem.ReadHashFromSecret(ctx, r.commonController.SecretClient, sc.Namespace, internalClusterSecretName, databaseSecretPath, log)),
		PrometheusTLSCertHash(opts.prometheusCertHash),
		WithVaultConfig(vaultConfig),
		WithAdditionalMongodConfig(configSrvSpec.GetAdditionalMongodConfig()),
		WithAgentVersion(r.automationAgentVersion),
		WithDefaultConfigSrvStorageSize(),
		WithStsLabels(r.statefulsetLabels()),
	)
}

// getMongosOptions returns the Options needed to build the StatefulSet for the mongos.
func (r *ShardedClusterReconcileHelper) getMongosOptions(ctx context.Context, sc mdbv1.MongoDB, opts deploymentOptions, log *zap.SugaredLogger, mongosSpec *mdbv1.ShardedClusterComponentSpec, memberCluster multicluster.MemberCluster) func(mdb mdbv1.MongoDB) construct.DatabaseStatefulSetOptions {
	certSecretName := sc.GetSecurity().MemberCertificateSecretName(sc.MongosRsName())
	internalClusterSecretName := sc.GetSecurity().InternalClusterAuthSecretName(sc.MongosRsName())

	var vaultConfig vault.VaultConfiguration
	if r.commonController.VaultClient != nil {
		vaultConfig = r.commonController.VaultClient.VaultConfig
	}

	return construct.MongosOptions(
		mongosSpec,
		memberCluster.Name,
		Replicas(scale.ReplicasThisReconciliation(r.GetMongosScaler(memberCluster))),
		StatefulSetNameOverride(r.GetMongosStsName(memberCluster)),
		PodEnvVars(opts.podEnvVars),
		CurrentAgentAuthMechanism(opts.currentAgentAuthMode),
		CertificateHash(enterprisepem.ReadHashFromSecret(ctx, r.commonController.SecretClient, sc.Namespace, certSecretName, vaultConfig.DatabaseSecretPath, log)),
		InternalClusterHash(enterprisepem.ReadHashFromSecret(ctx, r.commonController.SecretClient, sc.Namespace, internalClusterSecretName, vaultConfig.DatabaseSecretPath, log)),
		PrometheusTLSCertHash(opts.prometheusCertHash),
		WithVaultConfig(vaultConfig),
		WithAdditionalMongodConfig(sc.Spec.MongosSpec.GetAdditionalMongodConfig()),
		WithAgentVersion(r.automationAgentVersion),
		WithStsLabels(r.statefulsetLabels()),
	)
}

// getShardOptions returns the Options needed to build the StatefulSet for a given shard.
func (r *ShardedClusterReconcileHelper) getShardOptions(ctx context.Context, sc mdbv1.MongoDB, shardNum int, opts deploymentOptions, log *zap.SugaredLogger, shardSpec *mdbv1.ShardedClusterComponentSpec, memberCluster multicluster.MemberCluster) func(mdb mdbv1.MongoDB) construct.DatabaseStatefulSetOptions {
	certSecretName := sc.GetSecurity().MemberCertificateSecretName(sc.ShardRsName(shardNum))
	internalClusterSecretName := sc.GetSecurity().InternalClusterAuthSecretName(sc.ShardRsName(shardNum))

	var vaultConfig vault.VaultConfiguration
	var databaseSecretPath string
	if r.commonController.VaultClient != nil {
		vaultConfig = r.commonController.VaultClient.VaultConfig
		databaseSecretPath = r.commonController.VaultClient.DatabaseSecretPath()
	}

	return construct.ShardOptions(shardNum, r.desiredShardsConfiguration[shardNum], memberCluster.Name,
		Replicas(scale.ReplicasThisReconciliation(r.GetShardScaler(shardNum, memberCluster))),
		StatefulSetNameOverride(r.GetShardStsName(shardNum, memberCluster)),
		PodEnvVars(opts.podEnvVars),
		CurrentAgentAuthMechanism(opts.currentAgentAuthMode),
		CertificateHash(enterprisepem.ReadHashFromSecret(ctx, r.commonController.SecretClient, sc.Namespace, certSecretName, databaseSecretPath, log)),
		InternalClusterHash(enterprisepem.ReadHashFromSecret(ctx, r.commonController.SecretClient, sc.Namespace, internalClusterSecretName, databaseSecretPath, log)),
		PrometheusTLSCertHash(opts.prometheusCertHash),
		WithVaultConfig(vaultConfig),
		WithAdditionalMongodConfig(shardSpec.GetAdditionalMongodConfig()),
		WithAgentVersion(r.automationAgentVersion),
		WithStsLabels(r.statefulsetLabels()),
	)
}

func (r *ShardedClusterReconcileHelper) migrateToNewDeploymentState(sc *mdbv1.MongoDB) error {
	// Try to get the last achieved spec from annotations and store it in state
	if lastAchievedSpec, err := sc.GetLastSpec(); err != nil {
		return err
	} else {
		r.deploymentState.LastAchievedSpec = lastAchievedSpec
	}
	r.deploymentState.Status = sc.Status.DeepCopy()
	if !sc.Spec.IsMultiCluster() {
		r.deploymentState.Status.SizeStatusInClusters = &mdbstatus.MongodbShardedSizeStatusInClusters{
			ShardMongodsInClusters: map[string]int{
				multicluster.LegacyCentralClusterName: r.deploymentState.Status.MongodbShardedClusterSizeConfig.MongodsPerShardCount,
			},
			ShardOverridesInClusters: map[string]map[string]int{},
			MongosCountInClusters: map[string]int{
				multicluster.LegacyCentralClusterName: r.deploymentState.Status.MongodbShardedClusterSizeConfig.MongosCount,
			},
			ConfigServerMongodsInClusters: map[string]int{
				multicluster.LegacyCentralClusterName: r.deploymentState.Status.MongodbShardedClusterSizeConfig.ConfigServerCount,
			},
		}
	} else {
		r.deploymentState.Status.SizeStatusInClusters = &mdbstatus.MongodbShardedSizeStatusInClusters{
			ShardMongodsInClusters:        map[string]int{},
			ShardOverridesInClusters:      map[string]map[string]int{},
			MongosCountInClusters:         map[string]int{},
			ConfigServerMongodsInClusters: map[string]int{},
		}
	}

	return nil
}

func (r *ShardedClusterReconcileHelper) updateStatus(ctx context.Context, resource *mdbv1.MongoDB, status workflow.Status, log *zap.SugaredLogger, statusOptions ...mdbstatus.Option) (reconcile.Result, error) {
	if result, err := r.commonController.updateStatus(ctx, resource, status, log, statusOptions...); err != nil {
		return result, err
	} else {
		// UpdateStatus in the sharded cluster controller should be executed only once per reconcile (always with a return)
		// We are saving the status and writing back to the state configmap at this time
		r.deploymentState.Status = resource.Status.DeepCopy()
		if err := r.stateStore.WriteState(ctx, r.deploymentState, log); err != nil {
			return r.commonController.updateStatus(ctx, resource, workflow.Failed(xerrors.Errorf("Failed to write deployment state after updating status: %w", err)), log, nil)
		}
		return result, nil
	}
}

func (r *ShardedClusterReconcileHelper) GetShardStsName(shardIdx int, memberCluster multicluster.MemberCluster) string {
	if memberCluster.Legacy {
		return r.sc.ShardRsName(shardIdx)
	} else {
		return r.sc.MultiShardRsName(memberCluster.Index, shardIdx)
	}
}

func (r *ShardedClusterReconcileHelper) GetConfigSrvStsName(memberCluster multicluster.MemberCluster) string {
	if memberCluster.Legacy {
		return r.sc.ConfigRsName()
	} else {
		return r.sc.MultiConfigRsName(memberCluster.Index)
	}
}

func (r *ShardedClusterReconcileHelper) GetMongosStsName(memberCluster multicluster.MemberCluster) string {
	if memberCluster.Legacy {
		return r.sc.MongosRsName()
	} else {
		return r.sc.MultiMongosRsName(memberCluster.Index)
	}
}

func (r *ShardedClusterReconcileHelper) GetConfigSrvScaler(memberCluster multicluster.MemberCluster) interfaces.MultiClusterReplicaSetScaler {
	return scalers.NewMultiClusterReplicaSetScaler("configSrv", r.desiredConfigServerConfiguration.ClusterSpecList, memberCluster.Name, memberCluster.Index, r.configSrvMemberClusters)
}

func (r *ShardedClusterReconcileHelper) GetMongosScaler(memberCluster multicluster.MemberCluster) interfaces.MultiClusterReplicaSetScaler {
	return scalers.NewMultiClusterReplicaSetScaler("mongos", r.desiredMongosConfiguration.ClusterSpecList, memberCluster.Name, memberCluster.Index, r.mongosMemberClusters)
}

func (r *ShardedClusterReconcileHelper) GetShardScaler(shardIdx int, memberCluster multicluster.MemberCluster) interfaces.MultiClusterReplicaSetScaler {
	return scalers.NewMultiClusterReplicaSetScaler(fmt.Sprintf("shard idx %d", shardIdx), r.desiredShardsConfiguration[shardIdx].ClusterSpecList, memberCluster.Name, memberCluster.Index, r.shardsMemberClustersMap[shardIdx])
}

func (r *ShardedClusterReconcileHelper) getAllScalers() []scale.ReplicaSetScaler {
	var result []scale.ReplicaSetScaler
	for shardIdx := 0; shardIdx < r.sc.Spec.ShardCount; shardIdx++ {
		for _, memberCluster := range r.shardsMemberClustersMap[shardIdx] {
			result = append(result, r.GetShardScaler(shardIdx, memberCluster))
		}
	}

	for _, memberCluster := range r.configSrvMemberClusters {
		result = append(result, r.GetConfigSrvScaler(memberCluster))
	}

	for _, memberCluster := range r.mongosMemberClusters {
		result = append(result, r.GetMongosScaler(memberCluster))
	}

	return result
}

func (r *ShardedClusterReconcileHelper) GetConfigSrvServiceName(memberCluster multicluster.MemberCluster) string {
	if memberCluster.Legacy {
		return r.sc.ConfigSrvServiceName()
	} else {
		return fmt.Sprintf("%s-%d-cs", r.sc.Name, memberCluster.Index)
	}
}

func (r *ShardedClusterReconcileHelper) GetMongosServiceName(memberCluster multicluster.MemberCluster) string {
	if memberCluster.Legacy {
		return r.sc.ServiceName()
	} else {
		return dns.GetMultiHeadlessServiceName(r.sc.Name, memberCluster.Index)
	}
}

func (r *ShardedClusterReconcileHelper) GetShardServiceName(memberCluster multicluster.MemberCluster) string {
	if memberCluster.Legacy {
		return r.sc.ServiceName()
	} else {
		return r.sc.ShardServiceName()
	}
}

func (r *ShardedClusterReconcileHelper) replicateAgentKeySecret(ctx context.Context, conn om.Connection, agentKey string, log *zap.SugaredLogger) error {
	for _, memberCluster := range getHealthyMemberClusters(r.allMemberClusters) {
		var databaseSecretPath string
		if memberCluster.SecretClient.VaultClient != nil {
			databaseSecretPath = memberCluster.SecretClient.VaultClient.DatabaseSecretPath()
		}
		if _, err := agents.EnsureAgentKeySecretExists(ctx, memberCluster.SecretClient, conn, r.sc.Namespace, agentKey, conn.GroupID(), databaseSecretPath, log); err != nil {
			return xerrors.Errorf("failed to ensure agent key secret in member cluster %s: %w", memberCluster.Name, err)
		}
	}
	return nil
}

func (r *ShardedClusterReconcileHelper) createHostnameOverrideConfigMap() corev1.ConfigMap {
	data := r.createHostnameOverrideConfigMapData()

	cm := corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-hostname-override", r.sc.Name),
			Namespace: r.sc.Namespace,
			Labels:    mongoDBLabels(r.sc.Name),
		},
		Data: data,
	}
	return cm
}

func (r *ShardedClusterReconcileHelper) createHostnameOverrideConfigMapData() map[string]string {
	data := make(map[string]string)

	for _, memberCluster := range r.mongosMemberClusters {
		mongosScaler := r.GetMongosScaler(memberCluster)
		mongosHostnames, mongosPodNames := r.getMongosHostnames(memberCluster, max(mongosScaler.CurrentReplicas(), mongosScaler.DesiredReplicas()))
		for i := range mongosPodNames {
			data[mongosPodNames[i]] = mongosHostnames[i]
		}
	}

	for _, memberCluster := range r.configSrvMemberClusters {
		configSrvScaler := r.GetConfigSrvScaler(memberCluster)
		configSrvHostnames, configSrvPodNames := r.getConfigSrvHostnames(memberCluster, max(configSrvScaler.CurrentReplicas(), configSrvScaler.DesiredReplicas()))
		for i := range configSrvPodNames {
			data[configSrvPodNames[i]] = configSrvHostnames[i]
		}
	}

	for shardIdx := 0; shardIdx < max(r.sc.Spec.ShardCount, r.deploymentState.Status.MongodbShardedClusterSizeConfig.ShardCount); shardIdx++ {
		for _, memberCluster := range r.shardsMemberClustersMap[shardIdx] {
			shardScaler := r.GetShardScaler(shardIdx, memberCluster)
			shardHostnames, shardPodNames := r.getShardHostnames(shardIdx, memberCluster, max(shardScaler.CurrentReplicas(), shardScaler.DesiredReplicas()))
			for i := range shardPodNames {
				data[shardPodNames[i]] = shardHostnames[i]
			}
		}
	}
	return data
}

func (r *ShardedClusterReconcileHelper) reconcileHostnameOverrideConfigMap(ctx context.Context, log *zap.SugaredLogger) error {
	if !r.sc.Spec.IsMultiCluster() {
		if r.sc.Spec.DbCommonSpec.GetExternalDomain() == nil {
			log.Debugf("Skipping creating hostname override config map in SingleCluster topology (with external domain unspecified)")
			return nil
		}
	}

	cm := r.createHostnameOverrideConfigMap()
	for _, memberCluster := range getHealthyMemberClusters(r.allMemberClusters) {
		err := configmap.CreateOrUpdate(ctx, memberCluster.Client, cm)
		if err != nil && !errors.IsAlreadyExists(err) {
			return xerrors.Errorf("failed to create configmap: %s/%s in cluster: %s, err: %w", r.sc.Namespace, cm.Name, memberCluster.Name, err)
		}
		log.Debugf("Successfully ensured configmap: %s/%s in cluster: %s", r.sc.Namespace, cm.Name, memberCluster.Name)
	}

	return nil
}

// reconcileServices creates both internal and external Services.
//
// This method assumes that all overrides have been expanded and are present in the ClusterSpecList. Other fields
// are not taken into consideration. Please ensure you expended and processed them earlier.
func (r *ShardedClusterReconcileHelper) reconcileServices(ctx context.Context, log *zap.SugaredLogger) error {
	var allServices []*corev1.Service
	for _, memberCluster := range getHealthyMemberClusters(r.mongosMemberClusters) {
		if err := r.reconcileMongosServices(ctx, log, memberCluster, allServices); err != nil {
			return err
		}
	}

	for _, memberCluster := range getHealthyMemberClusters(r.configSrvMemberClusters) {
		if err := r.reconcileConfigServerServices(ctx, log, memberCluster, allServices); err != nil {
			return err
		}
	}

	for shardIdx := 0; shardIdx < r.sc.Spec.ShardCount; shardIdx++ {
		for _, memberCluster := range getHealthyMemberClusters(r.shardsMemberClustersMap[shardIdx]) {
			if err := r.reconcileShardServices(ctx, log, shardIdx, memberCluster, allServices); err != nil {
				return err
			}
		}
	}

	if r.sc.Spec.DuplicateServiceObjects != nil && *r.sc.Spec.DuplicateServiceObjects {
		for _, memberCluster := range getHealthyMemberClusters(r.allMemberClusters) {
			// the pod services created in their respective clusters will be updated twice here, but this way the code is cleaner
			for _, svc := range allServices {
				log.Debugf("creating duplicated services for %s in cluster: %s", svc.Name, memberCluster.Name)
				err := mekoService.CreateOrUpdateService(ctx, memberCluster.Client, *svc)
				if err != nil {
					return xerrors.Errorf("failed to create (duplicate) pod service %s in cluster: %s, err: %w", svc.Name, memberCluster.Name, err)
				}
			}
		}
	}

	return nil
}

func (r *ShardedClusterReconcileHelper) reconcileConfigServerServices(ctx context.Context, log *zap.SugaredLogger, memberCluster multicluster.MemberCluster, allServices []*corev1.Service) error {
	portOrDefault := r.desiredConfigServerConfiguration.AdditionalMongodConfig.GetPortOrDefault()
	scaler := r.GetConfigSrvScaler(memberCluster)

	for podNum := 0; podNum < scaler.DesiredReplicas(); podNum++ {
		configServerExternalAccess := r.desiredConfigServerConfiguration.ClusterSpecList.GetExternalAccessConfigurationForMemberCluster(memberCluster.Name)
		if configServerExternalAccess == nil {
			configServerExternalAccess = r.sc.Spec.DbCommonSpec.ExternalAccessConfiguration
		}
		if configServerExternalAccess != nil && configServerExternalAccess.ExternalDomain != nil {
			log.Debugf("creating external services for %s in cluster: %s", r.sc.ConfigRsName(), memberCluster.Name)
			svc, err := r.getPodExternalService(memberCluster,
				r.sc.ConfigRsName(),
				configServerExternalAccess,
				podNum,
				portOrDefault)
			if err != nil {
				return xerrors.Errorf("failed to create an external service %s in cluster: %s, err: %w", svc.Name, memberCluster.Name, err)
			}
			if err = mekoService.CreateOrUpdateService(ctx, memberCluster.Client, svc); err != nil && !errors.IsAlreadyExists(err) {
				return xerrors.Errorf("failed to create external service %s in cluster: %s, err: %w", svc.Name, memberCluster.Name, err)
			}
		} else {
			log.Debugf("creating internal services for %s in cluster: %s", r.sc.ConfigRsName(), memberCluster.Name)
			svc := r.getPodService(r.sc.ConfigRsName(), memberCluster, podNum, portOrDefault)
			if err := mekoService.CreateOrUpdateService(ctx, memberCluster.Client, svc); err != nil && !errors.IsAlreadyExists(err) {
				return xerrors.Errorf("failed to create pod service %s in cluster: %s, err: %w", svc.Name, memberCluster.Name, err)
			}
			_ = append(allServices, &svc)
		}
	}
	if err := createHeadlessServiceForStatefulSet(ctx, r.sc.ConfigRsName(), portOrDefault, r.sc.Namespace, memberCluster); err != nil {
		return err
	}
	return nil
}

func (r *ShardedClusterReconcileHelper) reconcileShardServices(ctx context.Context, log *zap.SugaredLogger, shardIdx int, memberCluster multicluster.MemberCluster, allServices []*corev1.Service) error {
	shardsExternalAccess := r.desiredShardsConfiguration[shardIdx].ClusterSpecList.GetExternalAccessConfigurationForMemberCluster(memberCluster.Name)
	if shardsExternalAccess == nil {
		shardsExternalAccess = r.sc.Spec.DbCommonSpec.ExternalAccessConfiguration
	}
	portOrDefault := r.desiredShardsConfiguration[shardIdx].AdditionalMongodConfig.GetPortOrDefault()
	scaler := r.GetShardScaler(shardIdx, memberCluster)

	for podNum := 0; podNum < scaler.DesiredReplicas(); podNum++ {
		if shardsExternalAccess != nil && shardsExternalAccess.ExternalDomain != nil {
			log.Debugf("creating external services for %s in cluster: %s", r.sc.ShardRsName(shardIdx), memberCluster.Name)
			svc, err := r.getPodExternalService(
				memberCluster,
				r.sc.ShardRsName(shardIdx),
				shardsExternalAccess,
				podNum,
				portOrDefault)
			if err != nil {
				return xerrors.Errorf("failed to create an external service %s in cluster: %s, err: %w", svc.Name, memberCluster.Name, err)
			}
			if err = mekoService.CreateOrUpdateService(ctx, memberCluster.Client, svc); err != nil && !errors.IsAlreadyExists(err) {
				return xerrors.Errorf("failed to create external service %s in cluster: %s, err: %w", svc.Name, memberCluster.Name, err)
			}
		} else {
			log.Debugf("creating internal services for %s in cluster: %s", r.sc.ShardRsName(shardIdx), memberCluster.Name)
			svc := r.getPodService(r.sc.ShardRsName(shardIdx), memberCluster, podNum, portOrDefault)
			if err := mekoService.CreateOrUpdateService(ctx, memberCluster.Client, svc); err != nil {
				return xerrors.Errorf("failed to create pod service %s in cluster: %s, err: %w", svc.Name, memberCluster.Name, err)
			}

			_ = append(allServices, &svc)
		}
	}

	if err := createHeadlessServiceForStatefulSet(ctx, r.sc.ShardRsName(shardIdx), portOrDefault, r.sc.Namespace, memberCluster); err != nil {
		return err
	}
	return nil
}

func (r *ShardedClusterReconcileHelper) reconcileMongosServices(ctx context.Context, log *zap.SugaredLogger, memberCluster multicluster.MemberCluster, allServices []*corev1.Service) error {
	scaler := r.GetMongosScaler(memberCluster)
	portOrDefault := r.desiredMongosConfiguration.AdditionalMongodConfig.GetPortOrDefault()
	for podNum := 0; podNum < scaler.DesiredReplicas(); podNum++ {
		mongosExternalAccess := r.desiredMongosConfiguration.ClusterSpecList.GetExternalAccessConfigurationForMemberCluster(memberCluster.Name)
		if mongosExternalAccess == nil {
			mongosExternalAccess = r.sc.Spec.DbCommonSpec.ExternalAccessConfiguration
		}
		if mongosExternalAccess != nil {
			log.Debugf("creating external services for %s in cluster: %s", r.sc.MongosRsName(), memberCluster.Name)
			svc, err := r.getPodExternalService(memberCluster,
				r.sc.MongosRsName(),
				mongosExternalAccess,
				podNum,
				portOrDefault)
			if err != nil {
				return xerrors.Errorf("failed to create an external service %s in cluster: %s, err: %w", svc.Name, memberCluster.Name, err)
			}
			if err = mekoService.CreateOrUpdateService(ctx, memberCluster.Client, svc); err != nil && !errors.IsAlreadyExists(err) {
				return xerrors.Errorf("failed to create external service %s in cluster: %s, err: %w", svc.Name, memberCluster.Name, err)
			}
		} else {
			log.Debugf("creating internal services for %s in cluster: %s", r.sc.MongosRsName(), memberCluster.Name)
			svc := r.getPodService(r.sc.MongosRsName(), memberCluster, podNum, portOrDefault)
			if err := mekoService.CreateOrUpdateService(ctx, memberCluster.Client, svc); err != nil && !errors.IsAlreadyExists(err) {
				return xerrors.Errorf("failed to create pod service %s in cluster: %s, err: %w", svc.Name, memberCluster.Name, err)
			}

			_ = append(allServices, &svc)
		}
		if err := createHeadlessServiceForStatefulSet(ctx, r.sc.MongosRsName(), portOrDefault, r.sc.Namespace, memberCluster); err != nil {
			return err
		}
	}
	return nil
}

func createHeadlessServiceForStatefulSet(ctx context.Context, stsName string, port int32, namespace string, memberCluster multicluster.MemberCluster) error {
	headlessServiceName := dns.GetMultiHeadlessServiceName(stsName, memberCluster.Index)
	nameSpacedName := kube.ObjectKey(namespace, headlessServiceName)
	headlessService := create.BuildService(nameSpacedName, nil, ptr.To(headlessServiceName), nil, port, omv1.MongoDBOpsManagerServiceDefinition{Type: corev1.ServiceTypeClusterIP})
	err := mekoService.CreateOrUpdateService(ctx, memberCluster.Client, headlessService)
	if err != nil && !errors.IsAlreadyExists(err) {
		return xerrors.Errorf("failed to create pod service %s in cluster: %s, err: %w", headlessService.Name, memberCluster.Name, err)
	}
	return nil
}

func (r *ShardedClusterReconcileHelper) getPodExternalService(memberCluster multicluster.MemberCluster, statefulSetName string, externalAccessConfiguration *mdbv1.ExternalAccessConfiguration, podNum int, port int32) (corev1.Service, error) {
	svc := r.getPodService(statefulSetName, memberCluster, podNum, port)
	svc.Name = dns.GetMultiExternalServiceName(statefulSetName, memberCluster.Index, podNum)
	svc.Spec.Type = corev1.ServiceTypeLoadBalancer

	if externalAccessConfiguration != nil {
		svc.Annotations = merge.StringToStringMap(svc.Annotations, externalAccessConfiguration.ExternalService.Annotations)
	}

	placeholderReplacer := create.GetMultiClusterMongoDBPlaceholderReplacer(r.sc.Name, statefulSetName, r.sc.Namespace, memberCluster.Name, memberCluster.Index, externalAccessConfiguration.ExternalDomain, r.sc.Spec.ClusterDomain, podNum)
	if processedAnnotations, replacedFlag, err := placeholderReplacer.ProcessMap(svc.Annotations); err != nil {
		return corev1.Service{}, xerrors.Errorf("failed to process annotations in external service %s in cluster %s: %w", svc.Name, memberCluster.Name, err)
	} else if replacedFlag {
		svc.Annotations = processedAnnotations
	}
	return svc, nil
}

func (r *ShardedClusterReconcileHelper) replicateTLSCAConfigMap(ctx context.Context, log *zap.SugaredLogger) error {
	if !r.sc.Spec.IsMultiCluster() {
		return nil
	}
	caConfigMapName := r.sc.GetSecurity().TLSConfig.CA
	if caConfigMapName == "" || !r.sc.Spec.IsMultiCluster() {
		return nil
	}

	operatorCAConfigMap, err := r.commonController.client.GetConfigMap(ctx, kube.ObjectKey(r.sc.Namespace, caConfigMapName))
	if err != nil {
		return xerrors.Errorf("expected CA ConfigMap not found on the operator cluster: %s", caConfigMapName)
	}
	for _, memberCluster := range getHealthyMemberClusters(r.allMemberClusters) {
		memberCAConfigMap := configmap.Builder().
			SetName(caConfigMapName).
			SetNamespace(r.sc.Namespace).
			SetData(operatorCAConfigMap.Data).
			Build()
		err = configmap.CreateOrUpdate(ctx, memberCluster.Client, memberCAConfigMap)
		if err != nil && !errors.IsAlreadyExists(err) {
			return xerrors.Errorf("failed to replicate CA ConfigMap from the operator cluster to cluster %s, err: %w", memberCluster.Name, err)
		}
		log.Debugf("Successfully ensured configmap: %s/%s in cluster: %s", r.sc.Namespace, caConfigMapName, memberCluster.Name)
	}

	return nil
}

func (r *ShardedClusterReconcileHelper) replicateSSLMMSCAConfigMap(ctx context.Context, projectConfig mdbv1.ProjectConfig, log *zap.SugaredLogger) error {
	if !r.sc.Spec.IsMultiCluster() || projectConfig.SSLMMSCAConfigMap == "" {
		return nil
	}

	cm, err := r.commonController.client.GetConfigMap(ctx, kube.ObjectKey(r.sc.Namespace, projectConfig.SSLMMSCAConfigMap))
	if err != nil {
		return xerrors.Errorf("expected SSLMMSCAConfigMap not found on operator cluster: %s", projectConfig.SSLMMSCAConfigMap)
	}

	for _, memberCluster := range getHealthyMemberClusters(r.allMemberClusters) {
		memberCm := configmap.Builder().
			SetName(projectConfig.SSLMMSCAConfigMap).
			SetNamespace(r.sc.Namespace).
			SetData(cm.Data).
			Build()
		err = configmap.CreateOrUpdate(ctx, memberCluster.Client, memberCm)

		if err != nil && !errors.IsAlreadyExists(err) {
			return xerrors.Errorf("failed to sync SSLMMSCAConfigMap to cluster: %s, err: %w", memberCluster.Name, err)
		}
		log.Debugf("Successfully ensured configmap: %s/%s in cluster: %s", r.sc.Namespace, projectConfig.SSLMMSCAConfigMap, memberCluster.Name)
	}

	return nil
}

func (r *ShardedClusterReconcileHelper) isStillScaling() bool {
	for _, s := range r.getAllScalers() {
		if scale.ReplicasThisReconciliation(s) != s.(*scalers.MultiClusterReplicaSetScaler).TargetReplicas() {
			return true
		}
	}

	return false
}

func (r *ShardedClusterReconcileHelper) getPodService(stsName string, memberCluster multicluster.MemberCluster, podNum int, port int32) corev1.Service {
	svcLabels := map[string]string{
		"statefulset.kubernetes.io/pod-name": dns.GetMultiPodName(stsName, memberCluster.Index, podNum),
		"controller":                         "mongodb-enterprise-operator",
		mdbv1.LabelMongoDBResourceOwner:      r.sc.Name,
	}

	labelSelectors := map[string]string{
		"statefulset.kubernetes.io/pod-name": dns.GetMultiPodName(stsName, memberCluster.Index, podNum),
		"controller":                         "mongodb-enterprise-operator",
	}

	svc := service.Builder().
		SetName(dns.GetMultiServiceName(stsName, memberCluster.Index, podNum)).
		SetNamespace(r.sc.Namespace).
		SetSelector(labelSelectors).
		SetLabels(svcLabels).
		SetPublishNotReadyAddresses(true).
		AddPort(&corev1.ServicePort{Port: port, Name: "mongodb"}).
		// Note: in the agent-launcher.sh We explicitly pass an offset of 1. When port N is exposed
		// the agent would use port N+1 for the spinning up of the ephemeral mongod process, which is used for backup
		AddPort(&corev1.ServicePort{Port: create.GetNonEphemeralBackupPort(port), Name: "backup", TargetPort: intstr.IntOrString{IntVal: create.GetNonEphemeralBackupPort(port)}}).
		Build()

	return svc
}

func (r *ShardedClusterReconcileHelper) statefulsetLabels() map[string]string {
	return merge.StringToStringMap(r.sc.Labels, mongoDBLabels(r.sc.Name))
}

func mongoDBLabels(name string) map[string]string {
	return map[string]string{
		construct.ControllerLabelName:   util.OperatorName,
		mdbv1.LabelMongoDBResourceOwner: name,
	}
}

func (r *ShardedClusterReconcileHelper) ShardsMemberClustersMap() map[int][]multicluster.MemberCluster {
	return r.shardsMemberClustersMap
}

func (r *ShardedClusterReconcileHelper) ConfigSrvMemberClusters() []multicluster.MemberCluster {
	return r.configSrvMemberClusters
}

func (r *ShardedClusterReconcileHelper) MongosMemberClusters() []multicluster.MemberCluster {
	return r.mongosMemberClusters
}

func (r *ShardedClusterReconcileHelper) AllShardsMemberClusters() []multicluster.MemberCluster {
	return r.allShardsMemberClusters
}

func (r *ShardedClusterReconcileHelper) AllMemberClusters() []multicluster.MemberCluster {
	return r.allMemberClusters
}
