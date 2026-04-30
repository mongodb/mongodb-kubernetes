package operator

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/controllers/searchcontroller"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
)

// newTestSearch builds a MongoDBSearch with the given cluster names for use in state tests.
// An empty string in clusterNames produces a single-cluster degenerate spec (clusterName omitted).
func newTestSearch(name, namespace string, clusterNames ...string) *searchv1.MongoDBSearch {
	clusters := make([]searchv1.SearchClusterSpecItem, len(clusterNames))
	for i, cn := range clusterNames {
		clusters[i] = searchv1.SearchClusterSpecItem{ClusterName: cn, Replicas: 1}
	}
	return &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: searchv1.MongoDBSearchSpec{
			Clusters: clusters,
		},
	}
}

// readSearchDeploymentState reads the state ConfigMap from the fake client and deserialises
// the MongoDBSearchDeploymentState stored in the "state" key.
func readSearchDeploymentState(ctx context.Context, t *testing.T, c client.Client, namespace, resourceName string) *searchcontroller.MongoDBSearchDeploymentState {
	t.Helper()
	cm := corev1.ConfigMap{}
	require.NoError(t, c.Get(ctx, kube.ObjectKey(namespace, resourceName+"-state"), &cm))
	state := new(searchcontroller.MongoDBSearchDeploymentState)
	require.NoError(t, json.Unmarshal([]byte(cm.Data["state"]), state))
	return state
}

// stepStateReconcile simulates one full state-management cycle by constructing a
// MongoDBSearchReconcileHelper (which handles state init and cluster-mapping internally).
// Returns the helper for further assertions on ClusterIndexFor.
func stepStateReconcile(ctx context.Context, t *testing.T, fakeClient client.Client, search *searchv1.MongoDBSearch) *searchcontroller.MongoDBSearchReconcileHelper {
	t.Helper()
	log := zap.NewNop().Sugar()
	kubeC := kubernetesClient.NewClient(fakeClient)
	stateStore := NewStateStore[searchcontroller.MongoDBSearchDeploymentState](search, kube.BaseOwnerReference(search), kubeC)
	helper, err := searchcontroller.NewMongoDBSearchReconcileHelper(ctx, kubeC, search, nil, searchcontroller.OperatorSearchConfig{}, stateStore, log)
	require.NoError(t, err)
	return helper
}

// TestMongoDBSearch_StateStore_SingleCluster verifies that a single-cluster degenerate spec
// (clusterName omitted) yields index 0 for the empty-string key.
func TestMongoDBSearch_StateStore_SingleCluster(t *testing.T) {
	ctx := context.Background()
	fakeClient := mock.NewEmptyFakeClientBuilder().Build()
	search := newTestSearch("my-search", mock.TestNamespace, "")

	helper := stepStateReconcile(ctx, t, fakeClient, search)

	state := readSearchDeploymentState(ctx, t, fakeClient, mock.TestNamespace, "my-search")
	assert.Equal(t, map[string]int{"": 0}, state.ClusterMapping)
	assert.Equal(t, 0, helper.ClusterIndexFor(""))
}

// TestMongoDBSearch_StateStore_ClusterMapping runs sequential sub-tests that share a fake kube
// client, verifying durable cluster-index allocation across multiple simulated reconcile cycles.
// The pattern mirrors TestAppDB_MultiCluster_ClusterMapping.
func TestMongoDBSearch_StateStore_ClusterMapping(t *testing.T) {
	ctx := context.Background()
	const (
		ns         = mock.TestNamespace
		searchName = "my-search"
		cluster1   = "cluster-east"
		cluster2   = "cluster-west"
		cluster3   = "cluster-central"
		cluster4   = "cluster-south"
	)

	fakeClient := mock.NewEmptyFakeClientBuilder().Build()

	t.Run("initial two-cluster deploy assigns sequential indices", func(t *testing.T) {
		search := newTestSearch(searchName, ns, cluster1, cluster2)
		helper := stepStateReconcile(ctx, t, fakeClient, search)

		state := readSearchDeploymentState(ctx, t, fakeClient, ns, searchName)
		assert.Equal(t, map[string]int{cluster1: 0, cluster2: 1}, state.ClusterMapping)
		assert.Equal(t, 0, helper.ClusterIndexFor(cluster1))
		assert.Equal(t, 1, helper.ClusterIndexFor(cluster2))
	})

	t.Run("add cluster: new entry gets next available index", func(t *testing.T) {
		search := newTestSearch(searchName, ns, cluster1, cluster2, cluster3)
		helper := stepStateReconcile(ctx, t, fakeClient, search)

		state := readSearchDeploymentState(ctx, t, fakeClient, ns, searchName)
		assert.Equal(t, map[string]int{cluster1: 0, cluster2: 1, cluster3: 2}, state.ClusterMapping)
		assert.Equal(t, 2, helper.ClusterIndexFor(cluster3))
	})

	t.Run("remove cluster: stale index is preserved (never reused)", func(t *testing.T) {
		// Remove cluster2; its index 1 must remain in the persisted mapping.
		search := newTestSearch(searchName, ns, cluster1, cluster3)
		helper := stepStateReconcile(ctx, t, fakeClient, search)

		state := readSearchDeploymentState(ctx, t, fakeClient, ns, searchName)
		// cluster2's entry (index 1) must still be present so the index is never reassigned.
		assert.Equal(t, 1, state.ClusterMapping[cluster2], "removed cluster index must be preserved")
		assert.Equal(t, 0, helper.ClusterIndexFor(cluster1))
		assert.Equal(t, 2, helper.ClusterIndexFor(cluster3))
	})

	t.Run("new cluster gets fresh index beyond all preserved indices", func(t *testing.T) {
		// cluster4 is brand-new; indices 0,1,2 are already taken → must get 3.
		search := newTestSearch(searchName, ns, cluster1, cluster3, cluster4)
		helper := stepStateReconcile(ctx, t, fakeClient, search)

		state := readSearchDeploymentState(ctx, t, fakeClient, ns, searchName)
		assert.Equal(t, 3, state.ClusterMapping[cluster4])
		assert.Equal(t, 3, helper.ClusterIndexFor(cluster4))
	})

	t.Run("state ConfigMap is recreated after deletion; mapping is re-established from current spec", func(t *testing.T) {
		// Simulate manual ConfigMap deletion: the next Reconcile starts fresh and re-assigns.
		cm := corev1.ConfigMap{}
		require.NoError(t, fakeClient.Get(ctx, kube.ObjectKey(ns, searchName+"-state"), &cm))
		require.NoError(t, fakeClient.Delete(ctx, &cm))

		// After deletion, initializeDeploymentState sees NotFound → empty state.
		// updateClusterMapping re-assigns from the current spec (cluster1, cluster3, cluster4).
		search := newTestSearch(searchName, ns, cluster1, cluster3, cluster4)
		helper := stepStateReconcile(ctx, t, fakeClient, search)

		state := readSearchDeploymentState(ctx, t, fakeClient, ns, searchName)
		require.Contains(t, state.ClusterMapping, cluster1)
		require.Contains(t, state.ClusterMapping, cluster3)
		require.Contains(t, state.ClusterMapping, cluster4)
		// Indices are re-assigned sequentially from 0 (previous history was in the deleted CM).
		assert.Equal(t, state.ClusterMapping[cluster1], helper.ClusterIndexFor(cluster1))
		assert.Equal(t, state.ClusterMapping[cluster3], helper.ClusterIndexFor(cluster3))
		assert.Equal(t, state.ClusterMapping[cluster4], helper.ClusterIndexFor(cluster4))
	})
}

// TestMongoDBSearch_StateStore_CrossCR_Independence verifies that two different MongoDBSearch CRs
// have independent deployment states. This is the core invariant M3 preserves: moving
// stateStore and deploymentState onto the per-Reconcile helper (rather than the long-lived
// MongoDBSearchReconciler struct) eliminates the cross-CR race.
func TestMongoDBSearch_StateStore_CrossCR_Independence(t *testing.T) {
	ctx := context.Background()
	fakeClient := mock.NewEmptyFakeClientBuilder().Build()
	log := zap.NewNop().Sugar()
	kubeC := kubernetesClient.NewClient(fakeClient)

	searchAlpha := newTestSearch("search-alpha", mock.TestNamespace, "cluster-east")
	searchBeta := newTestSearch("search-beta", mock.TestNamespace, "cluster-west")

	ss1 := NewStateStore[searchcontroller.MongoDBSearchDeploymentState](searchAlpha, kube.BaseOwnerReference(searchAlpha), kubeC)
	ss2 := NewStateStore[searchcontroller.MongoDBSearchDeploymentState](searchBeta, kube.BaseOwnerReference(searchBeta), kubeC)

	h1, err := searchcontroller.NewMongoDBSearchReconcileHelper(ctx, kubeC, searchAlpha, nil, searchcontroller.OperatorSearchConfig{}, ss1, log)
	require.NoError(t, err)
	h2, err := searchcontroller.NewMongoDBSearchReconcileHelper(ctx, kubeC, searchBeta, nil, searchcontroller.OperatorSearchConfig{}, ss2, log)
	require.NoError(t, err)

	// Both CRs independently assign index 0 to their sole cluster.
	assert.Equal(t, 0, h1.ClusterIndexFor("cluster-east"))
	assert.Equal(t, 0, h2.ClusterIndexFor("cluster-west"))

	// h1's mapping does not bleed into h2 (unmapped cluster returns zero value, not a ghost entry).
	assert.Equal(t, 0, h2.ClusterIndexFor("cluster-east"), "cluster-east is not mapped in search-beta")

	// Verify each CR has its own distinct ConfigMap.
	stateAlpha := readSearchDeploymentState(ctx, t, fakeClient, mock.TestNamespace, "search-alpha")
	stateBeta := readSearchDeploymentState(ctx, t, fakeClient, mock.TestNamespace, "search-beta")
	assert.Equal(t, map[string]int{"cluster-east": 0}, stateAlpha.ClusterMapping)
	assert.Equal(t, map[string]int{"cluster-west": 0}, stateBeta.ClusterMapping)
}
