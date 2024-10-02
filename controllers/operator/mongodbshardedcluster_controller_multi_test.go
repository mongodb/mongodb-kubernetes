package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"testing"

	"github.com/ghodss/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/yudai/gojsondiff"
	"github.com/yudai/gojsondiff/formatter"
	"go.uber.org/zap"
	"golang.org/x/exp/constraints"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"

	kubernetesClient "github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/client"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/mock"
	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
)

// Creates a list of ClusterSpecItems based on names and distribution
// The two input list must have the same size
func createClusterSpecList(clusterNames []string, shardCounts map[string]int) mdbv1.ClusterSpecList {
	specList := make(mdbv1.ClusterSpecList, len(clusterNames))
	for i := range clusterNames {
		specList[i] = mdbv1.ClusterSpecItem{
			ClusterName: clusterNames[i],
			Members:     shardCounts[clusterNames[i]],
		}
	}
	return specList
}

func newShardedClusterReconcilerForMultiCluster(ctx context.Context, sc *mdbv1.MongoDB, globalMemberClustersMap map[string]cluster.Cluster, kubeClient kubernetesClient.Client, omConnectionFactory *om.CachedOMConnectionFactory) (*ReconcileMongoDbShardedCluster, *ShardedClusterReconcileHelper, error) {
	r := &ReconcileMongoDbShardedCluster{
		ReconcileCommonController: newReconcileCommonController(ctx, kubeClient),
		omConnectionFactory:       omConnectionFactory.GetConnectionFunc,
		memberClustersMap:         globalMemberClustersMap,
	}
	reconcileHelper, err := NewShardedClusterReconcilerHelper(ctx, r.ReconcileCommonController, sc, globalMemberClustersMap, omConnectionFactory.GetConnectionFunc, zap.S())
	if err != nil {
		return nil, nil, err
	}
	return r, reconcileHelper, nil
}

func TestReconcileCreateMultiClusterShardedCluster(t *testing.T) {
	cluster1 := "member-cluster-1"
	cluster2 := "member-cluster-2"
	memberClusterNames := []string{
		cluster1,
		cluster2,
	}

	shardCount := 2
	// Two Kubernetes clusters, 2 replicaset members of each shard on the first one, 3 on the second one
	// This means a MongodPerShardCount of 5
	shardDistribution := []map[string]int{
		{cluster1: 2, cluster2: 3},
		{cluster1: 2, cluster2: 3},
	}
	shardClusterSpecList := createClusterSpecList(memberClusterNames, shardDistribution[0])

	// For Mongos and Config servers, 2 replicaset members on the first one, 1 on the second one
	mongosDistribution := map[string]int{cluster1: 2, cluster2: 1}
	mongosAndConfigSrvClusterSpecList := createClusterSpecList(memberClusterNames, mongosDistribution)

	configSrvDistribution := map[string]int{cluster1: 2, cluster2: 1}
	configSrvDistributionClusterSpecList := createClusterSpecList(memberClusterNames, configSrvDistribution)

	ctx := context.Background()
	sc := DefaultClusterBuilder().
		SetTopology(mdbv1.ClusterTopologyMultiCluster).
		SetShardCountSpec(shardCount).
		// The below parameters should be ignored when a clusterSpecList is configured/for multiClusterTopology
		SetMongodsPerShardCountSpec(0).
		SetConfigServerCountSpec(0).
		SetMongosCountSpec(0).
		// Same pods repartition for
		SetShardClusterSpec(shardClusterSpecList).
		SetConfigSrvClusterSpec(configSrvDistributionClusterSpecList).
		SetMongosClusterSpec(mongosAndConfigSrvClusterSpecList).
		Build()

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()

	fakeClient := mock.NewEmptyFakeClientBuilder().WithObjects(sc).WithObjects(mock.GetDefaultResources()...).Build()
	kubeClient := kubernetesClient.NewClient(fakeClient)
	memberClusterMap := getFakeMultiClusterMapWithConfiguredInterceptor(memberClusterNames, omConnectionFactory, true, true)

	reconciler, reconcilerHelper, err := newShardedClusterReconcilerForMultiCluster(ctx, sc, memberClusterMap, kubeClient, omConnectionFactory)
	clusterMapping := reconcilerHelper.deploymentState.ClusterMapping
	omConnectionFactory.SetPostCreateHook(func(connection om.Connection) {
		allHostnames, _ := generateAllHosts(sc, mongosDistribution, clusterMapping, configSrvDistribution, shardDistribution)
		connection.(*om.MockedOmConnection).AddHosts(allHostnames)
	})

	require.NoError(t, err)
	checkReconcileSuccessful(ctx, t, reconciler, sc, kubeClient)
	checkCorrectShardDistributionInStatus(t, sc)

	expectedHostnameOverrideMap := createExpectedHostnameOverrideMap(sc, clusterMapping, mongosDistribution, configSrvDistribution, shardDistribution)

	for clusterIdx, clusterSpecItem := range shardClusterSpecList {
		memberClusterClient := memberClusterMap[clusterSpecItem.ClusterName]
		memberClusterChecks := newShardedClusterMCChecks(t, sc, clusterSpecItem.ClusterName, memberClusterClient.GetClient(), clusterIdx)
		// Shards statefulsets should have the names shardname-0-0, shardname-0-1, shardname-1-0...
		for shardIdx := 0; shardIdx < shardCount; shardIdx++ {
			memberClusterChecks.checkStatefulSet(ctx, fmt.Sprintf("%s-%d-%d", sc.Name, shardIdx, clusterIdx), shardDistribution[shardIdx][clusterSpecItem.ClusterName])
		}
		// Config servers statefulsets should have the names mongoName-config-0, mongoName-config-1
		configSrvStsName := fmt.Sprintf("%s-config-%d", sc.Name, clusterIdx)
		memberClusterChecks.checkStatefulSet(ctx, configSrvStsName, configSrvDistribution[clusterSpecItem.ClusterName])
		memberClusterChecks.checkServices(ctx, configSrvStsName, configSrvDistribution[clusterSpecItem.ClusterName])
		// Mongos statefulsets should have the names mongoName-mongos-0, mongoName-mongos-1
		mongosStsName := fmt.Sprintf("%s-mongos-%d", sc.Name, clusterIdx)
		memberClusterChecks.checkStatefulSet(ctx, mongosStsName, mongosDistribution[clusterSpecItem.ClusterName])
		memberClusterChecks.checkServices(ctx, mongosStsName, mongosDistribution[clusterSpecItem.ClusterName])
		memberClusterChecks.checkAgentAPIKeySecret(ctx, om.TestGroupID)
		memberClusterChecks.checkHostnameOverrideConfigMap(ctx, fmt.Sprintf("%s-hostname-override", sc.Name), expectedHostnameOverrideMap)
	}
}

func createExpectedHostnameOverrideMap(sc *mdbv1.MongoDB, clusterMapping map[string]int, mongosDistribution map[string]int, configSrvDistribution map[string]int, shardDistribution []map[string]int) map[string]string {
	expectedHostnameOverrideMap := map[string]string{}
	allHostnames, allPodNames := generateAllHosts(sc, mongosDistribution, clusterMapping, configSrvDistribution, shardDistribution)
	for i := range allPodNames {
		expectedHostnameOverrideMap[allPodNames[i]] = allHostnames[i]
	}
	return expectedHostnameOverrideMap
}

func TestReconcileForComplexMultiClusterYaml(t *testing.T) {
	ctx := context.Background()
	sc, err := loadMongoDBResource("testdata/mdb-sharded-multi-cluster-complex.yaml")
	require.NoError(t, err)

	cluster0 := "cluster-0"
	cluster1 := "cluster-1"
	cluster2 := "cluster-2"
	clusterAnalytics := "cluster-analytics"
	memberClusterNames := []string{
		cluster0,
		cluster1,
		cluster2,
		clusterAnalytics,
	}

	// expected distributions of shards are copied from testdata/mdb-sharded-multi-cluster-complex-expected-shardmap.yaml
	shardDistribution := []map[string]int{
		{
			cluster0: 2,
			cluster1: 2,
			cluster2: 1,
		}, // shard 0
		{
			cluster0: 1,
			cluster1: 2,
			cluster2: 3,
		}, // shard 1
		{
			cluster0:         2,
			cluster1:         3,
			cluster2:         0,
			clusterAnalytics: 1,
		}, // shard 2
		{
			cluster0: 2,
			cluster1: 2,
			cluster2: 1,
		}, // shard 3
	}

	// expected distributions of mongos and config srv are copied from testdata/mdb-sharded-multi-cluster-complex.yaml
	mongosDistribution := map[string]int{
		cluster0: 1,
		cluster1: 1,
		cluster2: 0,
	}

	configSrvDistribution := map[string]int{
		cluster0: 2,
		cluster1: 2,
		cluster2: 1,
	}

	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(sc)
	memberClusterMap := getFakeMultiClusterMapWithClusters(memberClusterNames, omConnectionFactory)

	reconciler, reconcilerHelper, err := newShardedClusterReconcilerForMultiCluster(ctx, sc, memberClusterMap, kubeClient, omConnectionFactory)
	clusterMapping := reconcilerHelper.deploymentState.ClusterMapping
	require.NoError(t, err)

	omConnectionFactory.SetPostCreateHook(func(connection om.Connection) {
		hosts, _ := generateAllHosts(sc, mongosDistribution, clusterMapping, configSrvDistribution, shardDistribution)
		connection.(*om.MockedOmConnection).AddHosts(hosts)
	})

	checkReconcileSuccessful(ctx, t, reconciler, sc, kubeClient)

	expectedReplicaSets, err := loadExpectedReplicaSets("testdata/mdb-sharded-multi-cluster-complex-expected-replicasets.yaml")
	require.NoError(t, err)
	normalizedExpectedReplicaSets, err := normalizeObjectToInterfaceMap(expectedReplicaSets)
	require.NoError(t, err)
	automationConfig, err := omConnectionFactory.GetConnection().ReadAutomationConfig()
	require.NoError(t, err)
	normalizedActualReplicaSets, err := normalizeObjectToInterfaceMap(map[string]any{"replicaSets": automationConfig.Deployment.ReplicaSets()})
	require.NoError(t, err)
	if !assert.Equal(t, normalizedExpectedReplicaSets, normalizedActualReplicaSets) {
		visualDiff, err := getVisualJsonDiff(normalizedExpectedReplicaSets, normalizedActualReplicaSets)
		require.NoError(t, err)
		fmt.Printf("\n%s\n", visualDiff)
	}

	for shardIdx := 0; shardIdx < sc.Spec.ShardCount; shardIdx++ {
		for clusterName, expectedMembersCount := range shardDistribution[shardIdx] {
			memberClusterChecks := newShardedClusterMCChecks(t, sc, clusterName, memberClusterMap[clusterName].GetClient(), clusterMapping[clusterName])
			if expectedMembersCount > 0 {
				memberClusterChecks.checkStatefulSet(ctx, sc.MultiShardRsName(clusterMapping[clusterName], shardIdx), expectedMembersCount)
			} else {
				memberClusterChecks.checkStatefulSetDoesNotExist(ctx, sc.MultiShardRsName(clusterMapping[clusterName], shardIdx))
			}
		}
	}

	for clusterName, expectedMembersCount := range mongosDistribution {
		memberClusterChecks := newShardedClusterMCChecks(t, sc, clusterName, memberClusterMap[clusterName].GetClient(), clusterMapping[clusterName])
		if expectedMembersCount > 0 {
			memberClusterChecks.checkStatefulSet(ctx, sc.MultiMongosRsName(clusterMapping[clusterName]), expectedMembersCount)
		} else {
			memberClusterChecks.checkStatefulSetDoesNotExist(ctx, sc.MultiMongosRsName(clusterMapping[clusterName]))
		}
	}

	for clusterName, expectedMembersCount := range configSrvDistribution {
		memberClusterChecks := newShardedClusterMCChecks(t, sc, clusterName, memberClusterMap[clusterName].GetClient(), clusterMapping[clusterName])
		memberClusterChecks.checkStatefulSet(ctx, sc.MultiConfigRsName(clusterMapping[clusterName]), expectedMembersCount)
	}

	expectedHostnameOverrideMap := createExpectedHostnameOverrideMap(sc, clusterMapping, mongosDistribution, configSrvDistribution, shardDistribution)
	for _, clusterName := range memberClusterNames {
		memberClusterChecks := newShardedClusterMCChecks(t, sc, clusterName, memberClusterMap[clusterName].GetClient(), clusterMapping[clusterName])
		memberClusterChecks.checkHostnameOverrideConfigMap(ctx, fmt.Sprintf("%s-hostname-override", sc.Name), expectedHostnameOverrideMap)
	}
}

func generateAllHosts(sc *mdbv1.MongoDB, mongosDistribution map[string]int, clusterMapping map[string]int, configSrvDistribution map[string]int, shardDistribution []map[string]int) ([]string, []string) {
	var allHosts []string
	var allPodNames []string
	podNames, hosts := generateHostsWithDistribution(sc.MongosRsName(), sc.Namespace, mongosDistribution, clusterMapping)
	allHosts = append(allHosts, hosts...)
	allPodNames = append(allPodNames, podNames...)

	podNames, hosts = generateHostsWithDistribution(sc.ConfigRsName(), sc.Namespace, configSrvDistribution, clusterMapping)
	allHosts = append(allHosts, hosts...)
	allPodNames = append(allPodNames, podNames...)

	for shardIdx := 0; shardIdx < sc.Spec.ShardCount; shardIdx++ {
		podNames, hosts = generateHostsWithDistribution(sc.ShardRsName(shardIdx), sc.Namespace, shardDistribution[shardIdx], clusterMapping)
		allHosts = append(allHosts, hosts...)
		allPodNames = append(allPodNames, podNames...)
	}
	return allHosts, allPodNames
}

func TestMigrateToNewDeploymentState(t *testing.T) {
	ctx := context.Background()

	// These annotations should be preserved, but not appear in the migrated config map
	initialAnnotations := map[string]string{
		"key1": "value1",
		"key2": "value2",
	}
	sc := DefaultClusterBuilder().
		SetAnnotations(initialAnnotations).
		Build()

	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(sc)
	memberClusterMap := getFakeMultiClusterMapWithClusters([]string{"member-cluster-1"}, omConnectionFactory)

	reconciler, _, err := newShardedClusterReconcilerForMultiCluster(ctx, sc, memberClusterMap, kubeClient, omConnectionFactory)
	require.NoError(t, err)

	// Migration is performed at reconciliation, when needed
	checkReconcileSuccessful(ctx, t, reconciler, sc, kubeClient)

	// Ensure that reconciliation generated the correct deployment state
	configMapName := fmt.Sprintf("%s-state", sc.Name)
	stateConfigMap := &corev1.ConfigMap{}
	err = kubeClient.Get(ctx, types.NamespacedName{Name: configMapName, Namespace: sc.Namespace}, stateConfigMap)
	require.NoError(t, err)

	expectedDeploymentState := generateExpectedDeploymentState(t, sc)
	require.Contains(t, stateConfigMap.Data, stateKey)
	require.JSONEq(t, expectedDeploymentState, stateConfigMap.Data[stateKey])

	// Original annotations must be preserved
	updatedSc := &mdbv1.MongoDB{}
	err = kubeClient.Get(ctx, types.NamespacedName{Name: sc.Name, Namespace: sc.Namespace}, updatedSc)
	require.NoError(t, err)
	for key, value := range initialAnnotations {
		require.Equal(t, value, updatedSc.Annotations[key], "Annotation %s should be preserved", key)
	}

	// Verify that we also store the state in the annotations (everything below)
	// This way, downgrading the operator is possible without breaking the state
	require.Contains(t, updatedSc.Annotations, util.LastAchievedSpec)
	actualLastAchievedSpec := updatedSc.Annotations[util.LastAchievedSpec]

	var configMapData, actualLastAchievedSpecData map[string]interface{}
	// Deserialize the JSON data from the  annotation
	err = json.Unmarshal([]byte(actualLastAchievedSpec), &actualLastAchievedSpecData)
	require.NoError(t, err)

	// Extract lastAchievedSpec from the state Config Map
	err = json.Unmarshal([]byte(stateConfigMap.Data[stateKey]), &configMapData)
	require.NoError(t, err)
	expectedLastAchievedSpec, ok := configMapData["lastAchievedSpec"].(map[string]interface{})
	require.True(t, ok, "Expected lastAchievedSpec field is missing or invalid")

	require.Equal(t, expectedLastAchievedSpec, actualLastAchievedSpecData)
}

func TestShardMapForComplexMultiClusterYaml(t *testing.T) {
	ctx := context.Background()
	sc, err := loadMongoDBResource("testdata/mdb-sharded-multi-cluster-complex.yaml")
	require.NoError(t, err)

	memberClusterNames := []string{"cluster-0", "cluster-1", "cluster-2", "cluster-analytics"}
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(sc)
	memberClusterMap := getFakeMultiClusterMapWithClusters(memberClusterNames, omConnectionFactory)

	_, reconcilerHelper, err := newShardedClusterReconcilerForMultiCluster(ctx, sc, memberClusterMap, kubeClient, omConnectionFactory)
	require.NoError(t, err)

	// no reconcile here, we just test prepareDesiredShardsConfiguration
	shardsMap := reconcilerHelper.prepareDesiredShardsConfiguration(&sc.Spec)
	expectedShardsMap, err := loadExpectedShardsMap("testdata/mdb-sharded-multi-cluster-complex-expected-shardmap.yaml")
	require.NoError(t, err)
	normalizedShardsMap, err := normalizeObjectToInterfaceMap(shardsMap)
	require.NoError(t, err)
	normalizedExpectedShardsMap, err := normalizeObjectToInterfaceMap(expectedShardsMap)
	require.NoError(t, err)
	assert.Equal(t, normalizedExpectedShardsMap, normalizedShardsMap)
	visualDiff, err := getVisualJsonDiff(normalizedExpectedShardsMap, normalizedShardsMap)
	require.NoError(t, err)
	if !assert.Empty(t, visualDiff) {
		// it is extremely difficult to diagnose problems in IDE's console as the diff dump is very large >400 lines,
		// therefore we're saving visual diffs in ops-manager-kubernetes/tmp dir to a temp file
		tmpFile, err := os.CreateTemp(path.Join(os.Getenv("PROJECT_DIR"), "tmp"), "jsondiff")
		if err != nil {
			// ignore the error, it's not part of the actual test
			fmt.Printf("error saving diff to tmp file: %v", err)
		} else {
			if tmpFile != nil {
				defer func() { _ = tmpFile.Close() }()
			}
			_, _ = tmpFile.WriteString(visualDiff)
			if tmpFile != nil {
				fmt.Printf("Diff written to %s\n", tmpFile.Name())
			}
		}
	}
}

func TestMultiClusterShardedSetRace(t *testing.T) {
	cluster1 := "cluster-member-1"
	cluster2 := "cluster-member-2"

	memberClusterNames := []string{
		cluster1,
		cluster2,
	}

	shardCount := 2
	// Two Kubernetes clusters, 2 replicaset members of each shard on the first one, 3 on the second one
	// This means a MongodPerShardCount of 5
	shardDistribution := []map[string]int{
		{cluster1: 2, cluster2: 3},
		{cluster1: 2, cluster2: 3},
	}
	shardClusterSpecList := createClusterSpecList(memberClusterNames, shardDistribution[0])

	// For Mongos and Config servers, 2 replicaset members on the first one, 1 on the second one
	mongosDistribution := map[string]int{cluster1: 2, cluster2: 1}
	mongosAndConfigSrvClusterSpecList := createClusterSpecList(memberClusterNames, mongosDistribution)

	configSrvDistribution := map[string]int{cluster1: 2, cluster2: 1}
	configSrvDistributionClusterSpecList := createClusterSpecList(memberClusterNames, configSrvDistribution)

	sc, cfgMap, projectName := buildShardedClusterWithCustomProjectName("mc-sharded", shardCount, shardClusterSpecList, mongosAndConfigSrvClusterSpecList, configSrvDistributionClusterSpecList)
	sc1, cfgMap1, projectName1 := buildShardedClusterWithCustomProjectName("mc-sharded-1", shardCount, shardClusterSpecList, mongosAndConfigSrvClusterSpecList, configSrvDistributionClusterSpecList)
	sc2, cfgMap2, projectName2 := buildShardedClusterWithCustomProjectName("mc-sharded-2", shardCount, shardClusterSpecList, mongosAndConfigSrvClusterSpecList, configSrvDistributionClusterSpecList)

	resourceToProjectMapping := map[string]string{
		"mc-sharded":   projectName,
		"mc-sharded-1": projectName1,
		"mc-sharded-2": projectName2,
	}

	fakeClient := mock.NewEmptyFakeClientBuilder().
		WithObjects(sc, sc1, sc2).
		WithObjects(cfgMap, cfgMap1, cfgMap2).
		WithObjects(mock.GetCredentialsSecret(om.TestUser, om.TestApiKey)).
		Build()

	kubeClient := kubernetesClient.NewClient(fakeClient)
	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory().WithResourceToProjectMapping(resourceToProjectMapping)
	globalMemberClustersMap := getFakeMultiClusterMapWithConfiguredInterceptor(memberClusterNames, omConnectionFactory, true, false)

	ctx := context.Background()
	reconciler := &ReconcileMongoDbShardedCluster{
		ReconcileCommonController: newReconcileCommonController(ctx, kubeClient),
		omConnectionFactory:       omConnectionFactory.GetConnectionFunc,
		memberClustersMap:         globalMemberClustersMap,
	}

	allHostnames := generateHostsForCluster(ctx, reconciler, sc, mongosDistribution, configSrvDistribution, shardDistribution)
	allHostnames1 := generateHostsForCluster(ctx, reconciler, sc1, mongosDistribution, configSrvDistribution, shardDistribution)
	allHostnames2 := generateHostsForCluster(ctx, reconciler, sc2, mongosDistribution, configSrvDistribution, shardDistribution)

	projectHostMapping := map[string][]string{
		projectName:  allHostnames,
		projectName1: allHostnames1,
		projectName2: allHostnames2,
	}

	omConnectionFactory.SetPostCreateHook(func(connection om.Connection) {
		hostnames := projectHostMapping[connection.GroupName()]
		connection.(*om.MockedOmConnection).AddHosts(hostnames)
	})

	testConcurrentReconciles(ctx, t, fakeClient, reconciler, sc, sc1, sc2)
}

func generateHostsForCluster(ctx context.Context, reconciler *ReconcileMongoDbShardedCluster, sc *mdbv1.MongoDB, mongosDistribution map[string]int, configSrvDistribution map[string]int, shardDistribution []map[string]int) []string {
	reconcileHelper, _ := NewShardedClusterReconcilerHelper(ctx, reconciler.ReconcileCommonController, sc, reconciler.memberClustersMap, reconciler.omConnectionFactory, zap.S())
	allHostnames, _ := generateAllHosts(sc, mongosDistribution, reconcileHelper.deploymentState.ClusterMapping, configSrvDistribution, shardDistribution)

	return allHostnames
}

func buildShardedClusterWithCustomProjectName(mcShardedClusterName string, shardCount int, shardClusterSpecList mdbv1.ClusterSpecList, mongosAndConfigSrvClusterSpecList mdbv1.ClusterSpecList, configSrvDistributionClusterSpecList mdbv1.ClusterSpecList) (*mdbv1.MongoDB, *corev1.ConfigMap, string) {
	configMapName := mock.TestProjectConfigMapName + "-" + mcShardedClusterName
	projectName := om.TestGroupName + "-" + mcShardedClusterName

	return DefaultClusterBuilder().
		SetName(mcShardedClusterName).
		SetOpsManagerConfigMapName(configMapName).
		SetTopology(mdbv1.ClusterTopologyMultiCluster).
		SetShardCountSpec(shardCount).
		// The below parameters should be ignored when a clusterSpecList is configured/for multiClusterTopology
		SetMongodsPerShardCountSpec(0).
		SetConfigServerCountSpec(0).
		SetMongosCountSpec(0).
		// Same pods repartition for
		SetShardClusterSpec(shardClusterSpecList).
		SetConfigSrvClusterSpec(configSrvDistributionClusterSpecList).
		SetMongosClusterSpec(mongosAndConfigSrvClusterSpecList).
		Build(), mock.GetProjectConfigMap(configMapName, projectName, ""), projectName
}

type shardedClusterMCChecks struct {
	t            *testing.T
	namespace    string
	clusterName  string
	kubeClient   client.Client
	clusterIndex int
}

func newShardedClusterMCChecks(t *testing.T, shardedCluster *mdbv1.MongoDB, clusterName string, kubeClient client.Client, clusterIndex int) *shardedClusterMCChecks {
	result := shardedClusterMCChecks{
		t:            t,
		namespace:    shardedCluster.Namespace,
		clusterName:  clusterName,
		kubeClient:   kubeClient,
		clusterIndex: clusterIndex,
	}

	return &result
}

func (c *shardedClusterMCChecks) checkStatefulSet(ctx context.Context, statefulSetName string, expectedMembers int) {
	sts := appsv1.StatefulSet{}
	err := c.kubeClient.Get(ctx, kube.ObjectKey(c.namespace, statefulSetName), &sts)
	require.NoError(c.t, err, "clusterName: %s stsName: %s", c.clusterName, statefulSetName)
	require.Equal(c.t, expectedMembers, int(*sts.Spec.Replicas))
	require.Equal(c.t, statefulSetName, sts.ObjectMeta.Name)
}

func (c *shardedClusterMCChecks) checkAgentAPIKeySecret(ctx context.Context, projectID string) string {
	sec := corev1.Secret{}
	err := c.kubeClient.Get(ctx, kube.ObjectKey(c.namespace, agentAPIKeySecretName(projectID)), &sec)
	require.NoError(c.t, err, "clusterName: %s", c.clusterName)
	require.Contains(c.t, sec.Data, util.OmAgentApiKey, "clusterName: %s", c.clusterName)
	return string(sec.Data[util.OmAgentApiKey])
}

func (c *shardedClusterMCChecks) checkHostnameOverrideConfigMap(ctx context.Context, configMapName string, expectedPodNameToHostnameMap map[string]string) {
	cm := corev1.ConfigMap{}
	err := c.kubeClient.Get(ctx, kube.ObjectKey(c.namespace, configMapName), &cm)
	require.NoError(c.t, err, "clusterName: %s", c.clusterName)
	require.Equal(c.t, expectedPodNameToHostnameMap, cm.Data)
}

func (c *shardedClusterMCChecks) checkStatefulSetDoesNotExist(ctx context.Context, statefulSetName string) {
	sts := appsv1.StatefulSet{}
	err := c.kubeClient.Get(ctx, kube.ObjectKey(c.namespace, statefulSetName), &sts)
	require.True(c.t, apiErrors.IsNotFound(err))
}

func (c *shardedClusterMCChecks) checkServices(ctx context.Context, statefulSetName string, expectedMembers int) {
	for podIdx := 0; podIdx < expectedMembers; podIdx++ {
		svc := corev1.Service{}
		serviceName := fmt.Sprintf("%s-%d-svc", statefulSetName, podIdx)
		err := c.kubeClient.Get(ctx, kube.ObjectKey(c.namespace, serviceName), &svc)
		require.NoError(c.t, err, "clusterName: %s", c.clusterName)

		assert.Equal(c.t, map[string]string{
			"controller":                         "mongodb-enterprise-operator",
			"statefulset.kubernetes.io/pod-name": fmt.Sprintf("%s-%d", statefulSetName, podIdx),
		},
			svc.Spec.Selector)
	}
}

func checkCorrectShardDistributionInStatus(t *testing.T, sc *mdbv1.MongoDB) {
	clusterSpecItemToClusterNameMembers := func(clusterSpecItem mdbv1.ClusterSpecItem, _ int) (string, int) {
		return clusterSpecItem.ClusterName, clusterSpecItem.Members
	}
	expectedMongosSizeStatusInClusters := util.TransformToMap(sc.Spec.MongosSpec.ClusterSpecList, clusterSpecItemToClusterNameMembers)
	expectedConfigSrvSizeStatusInClusters := util.TransformToMap(sc.Spec.ConfigSrvSpec.ClusterSpecList, clusterSpecItemToClusterNameMembers)
	expectedShardSizeStatusInClusters := util.TransformToMap(sc.Spec.ShardSpec.ClusterSpecList, clusterSpecItemToClusterNameMembers)

	assert.Equal(t, expectedMongosSizeStatusInClusters, sc.Status.SizeStatusInClusters.MongosCountInClusters)
	assert.Equal(t, expectedShardSizeStatusInClusters, sc.Status.SizeStatusInClusters.ShardMongodsInClusters)
	assert.Equal(t, expectedConfigSrvSizeStatusInClusters, sc.Status.SizeStatusInClusters.ConfigServerMongodsInClusters)

	clusterSpecItemToMembers := func(item mdbv1.ClusterSpecItem) int {
		return item.Members
	}
	assert.Equal(t, sumSlice(util.Transform(sc.Spec.MongosSpec.ClusterSpecList, clusterSpecItemToMembers)), sc.Status.MongodbShardedClusterSizeConfig.MongosCount)
	assert.Equal(t, sumSlice(util.Transform(sc.Spec.ConfigSrvSpec.ClusterSpecList, clusterSpecItemToMembers)), sc.Status.MongodbShardedClusterSizeConfig.ConfigServerCount)
	assert.Equal(t, sumSlice(util.Transform(sc.Spec.ShardSpec.ClusterSpecList, clusterSpecItemToMembers)), sc.Status.MongodbShardedClusterSizeConfig.MongodsPerShardCount)
}

func sumSlice[T constraints.Integer](s []T) int {
	result := 0
	for i := range s {
		result += int(s[i])
	}
	return result
}

func generateHostsWithDistribution(stsName string, namespace string, distribution map[string]int, clusterIndexMapping map[string]int) ([]string, []string) {
	var hosts []string
	var podNames []string
	for memberClusterName, memberCount := range distribution {
		for podIdx := 0; podIdx < memberCount; podIdx++ {
			hosts = append(hosts, getMultiClusterFQDN(stsName, namespace, clusterIndexMapping[memberClusterName], podIdx, "cluster.local"))
			podNames = append(podNames, getPodName(stsName, clusterIndexMapping[memberClusterName], podIdx))
		}
	}

	return podNames, hosts
}

func getPodName(stsName string, clusterIdx int, podIdx int) string {
	return fmt.Sprintf("%s-%d-%d", stsName, clusterIdx, podIdx)
}

func getMultiClusterFQDN(stsName string, namespace string, clusterIdx int, podIdx int, clusterDomain string) string {
	return fmt.Sprintf("%s-svc.%s.svc.%s", getPodName(stsName, clusterIdx, podIdx), namespace, clusterDomain)
}

func generateExpectedDeploymentState(t *testing.T, sc *mdbv1.MongoDB) string {
	lastSpec, _ := sc.GetLastSpec()
	expectedState := ShardedClusterDeploymentState{
		CommonDeploymentState: CommonDeploymentState{
			ClusterMapping: map[string]int{},
		},
		LastAchievedSpec: lastSpec,
		Status:           &sc.Status,
	}
	lastSpecBytes, err := json.Marshal(expectedState)
	require.NoError(t, err)
	return string(lastSpecBytes)
}

func loadMongoDBResource(resourceYamlPath string) (*mdbv1.MongoDB, error) {
	mdbBytes, err := os.ReadFile(resourceYamlPath)
	if err != nil {
		return nil, err
	}

	mdb := mdbv1.MongoDB{}
	if err := yaml.Unmarshal(mdbBytes, &mdb); err != nil {
		return nil, err
	}
	return &mdb, nil
}

func loadExpectedShardsMap(path string) (map[int]*mdbv1.ShardedClusterComponentSpec, error) {
	bytes, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	shardMap := map[int]*mdbv1.ShardedClusterComponentSpec{}
	if err := yaml.Unmarshal(bytes, &shardMap); err != nil {
		return nil, err
	}
	return shardMap, nil
}

func loadExpectedReplicaSets(path string) (map[string]any, error) {
	bytes, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var ac map[string]any
	if err := yaml.Unmarshal(bytes, &ac); err != nil {
		return nil, err
	}
	return ac, nil
}

func normalizeObjectToInterfaceMap(obj any) (map[string]interface{}, error) {
	objJson, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	result := map[string]interface{}{}
	err = json.Unmarshal(objJson, &result)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func visualJsonDiffOfAnyObjects(t *testing.T, expectedObj any, actualObj any) string {
	normalizedExpectedObj, err := normalizeObjectToInterfaceMap(expectedObj)
	require.NoError(t, err)
	normalizedActualObj, err := normalizeObjectToInterfaceMap(actualObj)
	require.NoError(t, err)

	visualDiff, err := getVisualJsonDiff(normalizedExpectedObj, normalizedActualObj)
	require.NoError(t, err)

	return visualDiff
}

func getVisualJsonDiff(expectedMap map[string]interface{}, actualMap map[string]interface{}) (string, error) {
	differ := gojsondiff.New()
	diff := differ.CompareObjects(expectedMap, actualMap)
	if !diff.Modified() {
		fmt.Println("No diffs found")
		return "", nil
	}
	jsonFormatter := formatter.NewAsciiFormatter(expectedMap, formatter.AsciiFormatterConfig{
		ShowArrayIndex: false,
		Coloring:       true,
	})

	diffString, err := jsonFormatter.Format(diff)
	if err != nil {
		return "", err
	}

	return diffString, nil
}
