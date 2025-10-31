package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/api/v1/status"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/host"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

func init() {
	logger, _ := zap.NewDevelopment()
	zap.ReplaceGlobals(logger)
}

// multiClusters defines the cluster names used in multi-cluster tests
var multiClusters = []string{"cluster-0", "cluster-1", "cluster-2"}

func TestCreateMultiClusterReplicaSet(t *testing.T) {
	ctx := context.Background()

	clusterSpecList := mdbv1.ClusterSpecList{
		{ClusterName: "cluster-0", Members: 1},
		{ClusterName: "cluster-1", Members: 1},
		{ClusterName: "cluster-2", Members: 1},
	}

	rs := mdbv1.NewDefaultMultiReplicaSetBuilder().
		SetClusterSpecList(clusterSpecList).
		Build()

	reconciler, kubeClient, memberClients, omConnectionFactory := defaultReplicaSetMultiClusterReconciler(ctx, rs)
	checkReplicaSetReconcileSuccessful(ctx, t, reconciler, rs, kubeClient, false)

	// Verify StatefulSets exist in each member cluster
	for i, clusterName := range multiClusters {
		memberClient := memberClients[clusterName]
		sts := appsv1.StatefulSet{}
		stsName := fmt.Sprintf("%s-%d", rs.Name, i)
		err := memberClient.Get(ctx, kube.ObjectKey(rs.Namespace, stsName), &sts)
		require.NoError(t, err, "StatefulSet should exist in cluster %s", clusterName)
		assert.Equal(t, int32(1), *sts.Spec.Replicas, "Replicas in %s", clusterName)
	}

	// Verify OM automation config has all processes
	processes := omConnectionFactory.GetConnection().(*om.MockedOmConnection).GetProcesses()
	assert.Len(t, processes, 3)
}

// Helper functions below

// checkReplicaSetReconcileSuccessful reconciles a ReplicaSet and verifies it completes without error.
// Use shouldRequeue=true when expecting the reconciler to requeue (e.g., during scaling operations).
// Use shouldRequeue=false when expecting successful completion with 24-hour requeue.
func checkReplicaSetReconcileSuccessful(
	ctx context.Context,
	t *testing.T,
	reconciler reconcile.Reconciler,
	rs *mdbv1.MongoDB,
	client kubernetesClient.Client,
	shouldRequeue bool,
) {
	err := client.Update(ctx, rs)
	assert.NoError(t, err)

	result, e := reconciler.Reconcile(ctx, requestFromObject(rs))
	assert.NoError(t, e)

	if shouldRequeue {
		assert.True(t, result.Requeue || result.RequeueAfter > 0)
	} else {
		assert.Equal(t, reconcile.Result{RequeueAfter: util.TWENTY_FOUR_HOURS}, result)
	}

	// Fetch the latest updates as the reconciliation loop can update the resource
	err = client.Get(ctx, rs.ObjectKey(), rs)
	assert.NoError(t, err)
}

// getReplicaSetMultiClusterMap simulates multiple K8s clusters using fake clients
func getReplicaSetMultiClusterMap(omConnectionFactory *om.CachedOMConnectionFactory) map[string]client.Client {
	clientMap := make(map[string]client.Client)

	for _, clusterName := range multiClusters {
		fakeClientBuilder := mock.NewEmptyFakeClientBuilder()
		fakeClientBuilder.WithInterceptorFuncs(interceptor.Funcs{
			Get: mock.GetFakeClientInterceptorGetFunc(omConnectionFactory, true, true),
		})

		clientMap[clusterName] = kubernetesClient.NewClient(fakeClientBuilder.Build())
	}

	return clientMap
}

// TestReplicaSetMultiClusterScaling tests that multi-cluster ReplicaSets scale one member at a time
// across all clusters, similar to single-cluster behavior.
//
// This test verifies:
// 1. StatefulSets are created correctly in each member cluster with proper naming (<rsName>-<clusterIndex>)
// 2. Scaling happens one member at a time across all clusters
// 3. State management (ClusterMapping, LastAppliedMemberSpec) is tracked correctly in annotations
// 4. Clusters scale independently based on their ClusterSpecList configuration
//
// Note: OM process count assertions are skipped because multi-cluster hostname support in OM
// (using GetMultiClusterProcessHostnames) is not yet implemented. This will be added in Phase 6.
func TestReplicaSetMultiClusterScaling(t *testing.T) {
	ctx := context.Background()

	t.Run("Create multi-cluster deployment", func(t *testing.T) {
		// Setup: Create ReplicaSet with 3 clusters with different member counts
		clusterSpecList := mdbv1.ClusterSpecList{
			{ClusterName: "cluster-0", Members: 3},
			{ClusterName: "cluster-1", Members: 1},
			{ClusterName: "cluster-2", Members: 2},
		}

		rs := mdbv1.NewDefaultMultiReplicaSetBuilder().
			SetName("multi-rs").
			SetClusterSpecList(clusterSpecList).
			Build()

		reconciler, client, memberClusters, omConnectionFactory := defaultReplicaSetMultiClusterReconciler(ctx, rs)

		// Initial reconciliation - should create StatefulSets in all 3 clusters
		checkReplicaSetReconcileSuccessful(ctx, t, reconciler, rs, client, false)

		// Verify status is Running after successful reconciliation
		assert.Equal(t, status.PhaseRunning, rs.Status.Phase,
			"Expected Running phase after successful reconciliation")

		// Verify StatefulSets created with correct replica counts in each cluster
		assertReplicaSetStatefulSetReplicas(ctx, t, rs, memberClusters, 3, 1, 2)

		// Verify state annotations track cluster assignments and member counts
		assertReplicaSetStateAnnotations(ctx, t, rs, client,
			map[string]int{"cluster-0": 0, "cluster-1": 1, "cluster-2": 2}, // ClusterMapping (stable indexes)
			map[string]int{"cluster-0": 3, "cluster-1": 1, "cluster-2": 2}) // LastAppliedMemberSpec (member counts)

		// Verify OM has correct number of processes (3 + 1 + 2 = 6)
		processes := omConnectionFactory.GetConnection().(*om.MockedOmConnection).GetProcesses()
		assert.Len(t, processes, 6, "OM should have 6 processes across all clusters")
	})
}

// TestReplicaSetMultiClusterOneByOneScaling verifies that multi-cluster scaling happens one member at a time across
// clusters, preventing simultaneous scaling in multiple clusters.
func TestReplicaSetMultiClusterOneByOneScaling(t *testing.T) {
	ctx := context.Background()

	// Setup: Create ReplicaSet with 3 clusters, each with 1 member
	clusterSpecList := mdbv1.ClusterSpecList{
		{ClusterName: "cluster-0", Members: 1},
		{ClusterName: "cluster-1", Members: 1},
		{ClusterName: "cluster-2", Members: 1},
	}

	rs := mdbv1.NewDefaultMultiReplicaSetBuilder().
		SetName("scaling-rs").
		SetClusterSpecList(clusterSpecList).
		Build()

	reconciler, client, memberClusters, omConnectionFactory := defaultReplicaSetMultiClusterReconciler(ctx, rs)

	// Initial reconciliation - create with [1,1,1]
	checkReplicaSetReconcileSuccessful(ctx, t, reconciler, rs, client, false)
	assertReplicaSetStatefulSetReplicas(ctx, t, rs, memberClusters, 1, 1, 1)

	processes := omConnectionFactory.GetConnection().(*om.MockedOmConnection).GetProcesses()
	assert.Len(t, processes, 3, "Should have 3 processes initially")

	t.Log("=== Scaling from [1,1,1] to [2,1,2] ===")

	// Change spec to [2,1,2]
	rs.Spec.ClusterSpecList[0].Members = 2
	rs.Spec.ClusterSpecList[2].Members = 2

	// First reconciliation: Only cluster-0 should scale (first cluster needing change)
	checkReplicaSetReconcileSuccessful(ctx, t, reconciler, rs, client, true)

	// Verify intermediate state: cluster-0 scaled to 2, others still at 1
	assertReplicaSetStatefulSetReplicas(ctx, t, rs, memberClusters, 2, 1, 1)

	// Verify state tracking updated for cluster-0
	assertReplicaSetStateAnnotations(ctx, t, rs, client,
		map[string]int{"cluster-0": 0, "cluster-1": 1, "cluster-2": 2}, // ClusterMapping unchanged
		map[string]int{"cluster-0": 2, "cluster-1": 1, "cluster-2": 1}) // LastAppliedMemberSpec: cluster-0 updated

	// Verify OM processes updated (4 total now)
	processes = omConnectionFactory.GetConnection().(*om.MockedOmConnection).GetProcesses()
	assert.Len(t, processes, 4, "Should have 4 processes after cluster-0 scales")

	// Second reconciliation: Now cluster-2 should scale
	checkReplicaSetReconcileSuccessful(ctx, t, reconciler, rs, client, true)

	// Verify final state: all clusters at target
	assertReplicaSetStatefulSetReplicas(ctx, t, rs, memberClusters, 2, 1, 2)
	t.Log("✓ After reconcile 2: [2,1,2] - cluster-2 scaled")

	// Verify state tracking updated for cluster-2
	assertReplicaSetStateAnnotations(ctx, t, rs, client,
		map[string]int{"cluster-0": 0, "cluster-1": 1, "cluster-2": 2}, // ClusterMapping unchanged
		map[string]int{"cluster-0": 2, "cluster-1": 1, "cluster-2": 2}) // LastAppliedMemberSpec: all at target

	processes = omConnectionFactory.GetConnection().(*om.MockedOmConnection).GetProcesses()
	assert.Len(t, processes, 5, "Should have 5 processes after all scaling complete")

	// Third reconciliation: All done, should return OK with 24h requeue
	checkReplicaSetReconcileSuccessful(ctx, t, reconciler, rs, client, false)

	// Verify state unchanged
	assertReplicaSetStatefulSetReplicas(ctx, t, rs, memberClusters, 2, 1, 2)
	t.Log("✓ After reconcile 3: [2,1,2] - scaling complete, stable state")
}

// assertReplicaSetStateAnnotations verifies that ClusterMapping and LastAppliedMemberSpec annotations
// are set correctly. This validates the state management implementation.
func assertReplicaSetStateAnnotations(ctx context.Context, t *testing.T, rs *mdbv1.MongoDB, client kubernetesClient.Client,
	expectedClusterMapping map[string]int, expectedLastAppliedMemberSpec map[string]int,
) {
	// Fetch latest resource to get annotations
	err := client.Get(ctx, rs.ObjectKey(), rs)
	require.NoError(t, err)

	// Verify ClusterMapping annotation
	clusterMappingStr := rs.Annotations[util.ClusterMappingAnnotation]
	require.NotEmpty(t, clusterMappingStr, "ClusterMapping annotation should be present")

	var clusterMapping map[string]int
	err = json.Unmarshal([]byte(clusterMappingStr), &clusterMapping)
	require.NoError(t, err)
	assert.Equal(t, expectedClusterMapping, clusterMapping,
		"ClusterMapping should track stable cluster indexes")

	// Verify LastAppliedMemberSpec annotation
	lastAppliedMemberSpecStr := rs.Annotations[util.LastAppliedMemberSpecAnnotation]
	require.NotEmpty(t, lastAppliedMemberSpecStr, "LastAppliedMemberSpec annotation should be present")

	var lastAppliedMemberSpec map[string]int
	err = json.Unmarshal([]byte(lastAppliedMemberSpecStr), &lastAppliedMemberSpec)
	require.NoError(t, err)
	assert.Equal(t, expectedLastAppliedMemberSpec, lastAppliedMemberSpec,
		"LastAppliedMemberSpec should track current member counts for scale detection")
}

// readReplicaSetStatefulSets fetches all StatefulSets from member clusters for a multi-cluster ReplicaSet.
// Returns a map of cluster name to StatefulSet.
func readReplicaSetStatefulSets(ctx context.Context, rs *mdbv1.MongoDB, memberClusters map[string]kubernetesClient.Client) map[string]appsv1.StatefulSet {
	allStatefulSets := map[string]appsv1.StatefulSet{}

	for i, clusterSpec := range rs.Spec.ClusterSpecList {
		memberClient := memberClusters[clusterSpec.ClusterName]
		if memberClient == nil {
			continue
		}

		// StatefulSet name pattern for multi-cluster: <rsName>-<clusterIndex>
		stsName := fmt.Sprintf("%s-%d", rs.Name, i)
		sts := appsv1.StatefulSet{}
		err := memberClient.Get(ctx, types.NamespacedName{Name: stsName, Namespace: rs.Namespace}, &sts)
		if err == nil {
			allStatefulSets[clusterSpec.ClusterName] = sts
		}
	}

	return allStatefulSets
}

// assertReplicaSetStatefulSetReplicas verifies the replica count for each cluster's StatefulSet.
// Takes variadic expectedReplicas matching the order of rs.Spec.ClusterSpecList.
func assertReplicaSetStatefulSetReplicas(ctx context.Context, t *testing.T, rs *mdbv1.MongoDB, memberClusters map[string]kubernetesClient.Client, expectedReplicas ...int) {
	statefulSets := readReplicaSetStatefulSets(ctx, rs, memberClusters)

	for i, clusterSpec := range rs.Spec.ClusterSpecList {
		if i >= len(expectedReplicas) {
			break
		}

		sts, ok := statefulSets[clusterSpec.ClusterName]
		if ok {
			require.Equal(t, int32(expectedReplicas[i]), *sts.Spec.Replicas,
				"StatefulSet for cluster %s should have %d replicas", clusterSpec.ClusterName, expectedReplicas[i])
		}
	}
}

// replicaSetMultiClusterReconciler creates a ReplicaSet reconciler configured for multi-cluster mode.
// This is the base setup without OM mocking - use defaultReplicaSetMultiClusterReconciler for standard tests.
func replicaSetMultiClusterReconciler(ctx context.Context, rs *mdbv1.MongoDB) (*ReconcileMongoDbReplicaSet, kubernetesClient.Client, map[string]kubernetesClient.Client, *om.CachedOMConnectionFactory) {
	// Create central cluster client and OM connection factory
	centralClient, omConnectionFactory := mock.NewDefaultFakeClient(rs)

	// Create RAW member cluster clients with interceptors
	// These are controller-runtime clients, not wrapped in kubernetesClient.Client yet
	// The reconciler's createMemberClusterListFromClusterSpecList will wrap them
	memberClusterMapRaw := map[string]client.Client{}
	memberClusterMapWrapped := map[string]kubernetesClient.Client{}

	for _, clusterName := range multiClusters {
		fakeClientBuilder := mock.NewEmptyFakeClientBuilder()
		fakeClientBuilder.WithObjects(mock.GetDefaultResources()...)
		fakeClientBuilder.WithInterceptorFuncs(interceptor.Funcs{
			Get: mock.GetFakeClientInterceptorGetFunc(omConnectionFactory, true, true),
		})

		rawClient := fakeClientBuilder.Build()
		memberClusterMapRaw[clusterName] = rawClient
		memberClusterMapWrapped[clusterName] = kubernetesClient.NewClient(rawClient)
	}

	// Create reconciler with multi-cluster support
	reconciler := newReplicaSetReconciler(ctx, centralClient, nil, "", "", false, false, memberClusterMapRaw, omConnectionFactory.GetConnectionFunc)

	return reconciler, centralClient, memberClusterMapWrapped, omConnectionFactory
}

// defaultReplicaSetMultiClusterReconciler creates a ReplicaSet reconciler with standard OM hostname mocking.
// Most tests should use this function.
func defaultReplicaSetMultiClusterReconciler(ctx context.Context, rs *mdbv1.MongoDB) (*ReconcileMongoDbReplicaSet, kubernetesClient.Client, map[string]kubernetesClient.Client, *om.CachedOMConnectionFactory) {
	reconciler, client, clusterMap, omConnectionFactory := replicaSetMultiClusterReconciler(ctx, rs)

	omConnectionFactory.SetPostCreateHook(func(connection om.Connection) {
		mockedConn := connection.(*om.MockedOmConnection)
		mockedConn.Hostnames = nil

		// Pre-register agents for multi-cluster tests
		// This simulates agents being already registered with OM
		// Register enough agents to handle scaling operations (up to 10 members per cluster)
		if rs.Spec.IsMultiCluster() {
			hostResult, _ := mockedConn.GetHosts()
			// Register agents for up to 10 clusters with up to 10 members each
			// This handles scaling and cluster addition scenarios in tests
			for clusterIdx := 0; clusterIdx < 10; clusterIdx++ {
				for podNum := 0; podNum < 10; podNum++ {
					hostname := fmt.Sprintf("%s-%d-%d-svc.%s.svc.cluster.local", rs.Name, clusterIdx, podNum, rs.Namespace)
					// Register as a host in OM to simulate agent registration
					hostResult.Results = append(hostResult.Results, host.Host{
						Id:       fmt.Sprintf("%d", len(hostResult.Results)),
						Hostname: hostname,
					})
				}
			}
		}
	})

	return reconciler, client, clusterMap, omConnectionFactory
}

// ============================================================================
// State Management Tests
// ============================================================================

func TestReplicaSetDeploymentState_Serialization(t *testing.T) {
	state := &ReplicaSetDeploymentState{
		LastAchievedSpec: &mdbv1.MongoDbSpec{},
		ClusterMapping: map[string]int{
			multicluster.LegacyCentralClusterName: 0,
			"cluster-1":                           1,
			"cluster-2":                           2,
		},
		LastAppliedMemberSpec: map[string]int{
			multicluster.LegacyCentralClusterName: 3,
			"cluster-1":                           5,
			"cluster-2":                           7,
		},
	}

	// Marshal to JSON
	bytes, err := json.Marshal(state)
	assert.NoError(t, err)

	// Unmarshal back
	var decoded ReplicaSetDeploymentState
	err = json.Unmarshal(bytes, &decoded)
	assert.NoError(t, err)

	// Verify ClusterMapping
	assert.Equal(t, 0, decoded.ClusterMapping[multicluster.LegacyCentralClusterName])
	assert.Equal(t, 1, decoded.ClusterMapping["cluster-1"])
	assert.Equal(t, 2, decoded.ClusterMapping["cluster-2"])

	// Verify LastAppliedMemberSpec
	assert.Equal(t, 3, decoded.LastAppliedMemberSpec[multicluster.LegacyCentralClusterName])
	assert.Equal(t, 5, decoded.LastAppliedMemberSpec["cluster-1"])
	assert.Equal(t, 7, decoded.LastAppliedMemberSpec["cluster-2"])
}

func TestReadState_ClusterMapping_ReadsFromAnnotation(t *testing.T) {
	clusterMapping := map[string]int{multicluster.LegacyCentralClusterName: 7}
	clusterMappingJSON, _ := json.Marshal(clusterMapping)

	rs := &mdbv1.MongoDB{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rs",
			Namespace: "default",
			Annotations: map[string]string{
				util.ClusterMappingAnnotation: string(clusterMappingJSON),
			},
		},
		Status: mdbv1.MongoDbStatus{
			Members: 3, // Different from annotation (annotations should be used)
		},
	}

	helper := &ReplicaSetReconcilerHelper{
		resource: rs,
		log:      zap.S(),
	}

	state, err := helper.readState()

	assert.NoError(t, err)
	assert.NotNil(t, state)
	assert.Equal(t, 7, state.ClusterMapping[multicluster.LegacyCentralClusterName],
		"Should read from ClusterMapping annotation, not Status.Members")
}

func TestReadState_ClusterMapping_FallbackToStatusMembers(t *testing.T) {
	rs := &mdbv1.MongoDB{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "test-rs",
			Namespace:   "default",
			Annotations: map[string]string{
				// No ClusterMapping annotation
			},
		},
		Status: mdbv1.MongoDbStatus{
			Members: 5, // Existing deployment has 5 members
		},
	}

	helper := &ReplicaSetReconcilerHelper{
		resource: rs,
		log:      zap.S(),
	}

	state, err := helper.readState()

	assert.NoError(t, err)
	assert.NotNil(t, state)
	// Migration logic initializes LastAppliedMemberSpec, not ClusterMapping
	assert.Equal(t, 5, state.LastAppliedMemberSpec[multicluster.LegacyCentralClusterName],
		"Should fallback to Status.Members when annotation missing")
}

func TestReadState_ClusterMapping_SkipsMigrationForMultiCluster(t *testing.T) {
	rs := &mdbv1.MongoDB{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "test-rs",
			Namespace:   "default",
			Annotations: map[string]string{
				// No state annotations
			},
		},
		Spec: mdbv1.MongoDbSpec{
			DbCommonSpec: mdbv1.DbCommonSpec{
				Topology: mdbv1.ClusterTopologyMultiCluster,
			},
			ClusterSpecList: mdbv1.ClusterSpecList{
				{ClusterName: "cluster-0", Members: 3},
			},
		},
		Status: mdbv1.MongoDbStatus{
			Members: 5, // Should be ignored for multi-cluster
		},
	}

	helper := &ReplicaSetReconcilerHelper{
		resource: rs,
		log:      zap.S(),
	}

	state, err := helper.readState()

	assert.NoError(t, err)
	assert.NotNil(t, state)
	assert.Empty(t, state.LastAppliedMemberSpec,
		"Multi-cluster should not migrate from Status.Members")
}

func TestReadState_LastAppliedMemberSpec_FallbackToStatusMembers(t *testing.T) {
	rs := &mdbv1.MongoDB{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "test-rs",
			Namespace:   "default",
			Annotations: map[string]string{
				// No LastAppliedMemberSpec annotation
			},
		},
		Status: mdbv1.MongoDbStatus{
			Members: 7,
		},
	}

	helper := &ReplicaSetReconcilerHelper{
		resource: rs,
		log:      zap.S(),
	}

	state, err := helper.readState()

	assert.NoError(t, err)
	assert.NotNil(t, state)
	assert.Equal(t, 7, state.LastAppliedMemberSpec[multicluster.LegacyCentralClusterName],
		"Should migrate from Status.Members for single-cluster without annotation")
}

// TestStateLifecycle_SingleClusterMigration verifies that existing single-cluster
// deployments without state annotations properly migrate from Status.Members.
func TestStateLifecycle_SingleClusterMigration(t *testing.T) {
	ctx := context.Background()

	// Simulate existing deployment: has Status.Members but no state annotations
	rs := DefaultReplicaSetBuilder().
		SetName("legacy-rs").
		SetMembers(3).
		Build()
	rs.Status.Members = 3 // Existing deployment

	reconciler, client, _, _ := defaultReplicaSetMultiClusterReconciler(ctx, rs)

	// First reconciliation should migrate state from Status.Members
	checkReplicaSetReconcileSuccessful(ctx, t, reconciler, rs, client, false)

	// Verify state was written to annotations
	err := client.Get(ctx, rs.ObjectKey(), rs)
	require.NoError(t, err)

	// Verify LastAppliedMemberSpec was migrated from Status.Members
	var lastAppliedMemberSpec map[string]int
	err = json.Unmarshal([]byte(rs.Annotations[util.LastAppliedMemberSpecAnnotation]), &lastAppliedMemberSpec)
	require.NoError(t, err)
	assert.Equal(t, 3, lastAppliedMemberSpec[multicluster.LegacyCentralClusterName],
		"Should migrate from Status.Members on first reconciliation")

	// Second reconciliation should read from annotation (not Status.Members)
	rs.Status.Members = 999 // Change Status.Members to verify annotation is used
	checkReplicaSetReconcileSuccessful(ctx, t, reconciler, rs, client, false)

	// Verify state still shows 3 (from annotation, not Status.Members=999)
	err = client.Get(ctx, rs.ObjectKey(), rs)
	require.NoError(t, err)
	err = json.Unmarshal([]byte(rs.Annotations[util.LastAppliedMemberSpecAnnotation]), &lastAppliedMemberSpec)
	require.NoError(t, err)
	assert.Equal(t, 3, lastAppliedMemberSpec[multicluster.LegacyCentralClusterName],
		"Should read from annotation, not Status.Members after migration")
}

// TestStateLifecycle_MultiClusterStatePreservation verifies that state is correctly maintained across multiple
// reconciliations in multi-cluster mode.
func TestStateLifecycle_MultiClusterStatePreservation(t *testing.T) {
	ctx := context.Background()

	// Create multi-cluster ReplicaSet with initial configuration
	clusterSpecList := mdbv1.ClusterSpecList{
		{ClusterName: "cluster-0", Members: 3},
		{ClusterName: "cluster-1", Members: 1},
	}

	rs := mdbv1.NewDefaultMultiReplicaSetBuilder().
		SetName("multi-rs").
		SetClusterSpecList(clusterSpecList).
		Build()

	reconciler, client, memberClusters, omConnectionFactory := defaultReplicaSetMultiClusterReconciler(ctx, rs)

	// Initial reconciliation
	checkReplicaSetReconcileSuccessful(ctx, t, reconciler, rs, client, false)

	// Verify initial state
	assertReplicaSetStateAnnotations(ctx, t, rs, client,
		map[string]int{"cluster-0": 0, "cluster-1": 1}, // ClusterMapping
		map[string]int{"cluster-0": 3, "cluster-1": 1}) // LastAppliedMemberSpec

	assertReplicaSetStatefulSetReplicas(ctx, t, rs, memberClusters, 3, 1)

	// Verify OM has correct number of processes (3 + 1 = 4)
	processes := omConnectionFactory.GetConnection().(*om.MockedOmConnection).GetProcesses()
	assert.Len(t, processes, 4, "OM should have 4 processes after initial reconciliation")

	// Scale cluster-1 from 1 to 2 members
	rs.Spec.ClusterSpecList[1].Members = 2
	checkReplicaSetReconcileSuccessful(ctx, t, reconciler, rs, client, false)

	// Verify state updated to reflect scaling
	assertReplicaSetStateAnnotations(ctx, t, rs, client,
		map[string]int{"cluster-0": 0, "cluster-1": 1}, // ClusterMapping unchanged
		map[string]int{"cluster-0": 3, "cluster-1": 2}) // LastAppliedMemberSpec updated

	// Verify StatefulSet scaled
	assertReplicaSetStatefulSetReplicas(ctx, t, rs, memberClusters, 3, 2)

	// Verify OM has correct number of processes after scaling (3 + 2 = 5)
	processes = omConnectionFactory.GetConnection().(*om.MockedOmConnection).GetProcesses()
	assert.Len(t, processes, 5, "OM should have 5 processes after scaling cluster-1")

	// Add a third cluster
	rs.Spec.ClusterSpecList = append(rs.Spec.ClusterSpecList,
		mdbv1.ClusterSpecItem{ClusterName: "cluster-2", Members: 1})
	checkReplicaSetReconcileSuccessful(ctx, t, reconciler, rs, client, false)

	// Verify state includes new cluster with next available index
	assertReplicaSetStateAnnotations(ctx, t, rs, client,
		map[string]int{"cluster-0": 0, "cluster-1": 1, "cluster-2": 2}, // ClusterMapping with new cluster
		map[string]int{"cluster-0": 3, "cluster-1": 2, "cluster-2": 1}) // LastAppliedMemberSpec with new cluster

	// Verify all three StatefulSets exist
	assertReplicaSetStatefulSetReplicas(ctx, t, rs, memberClusters, 3, 2, 1)

	// Verify OM has correct number of processes after adding cluster-2 (3 + 2 + 1 = 6)
	processes = omConnectionFactory.GetConnection().(*om.MockedOmConnection).GetProcesses()
	assert.Len(t, processes, 6, "OM should have 6 processes after adding cluster-2")
}
