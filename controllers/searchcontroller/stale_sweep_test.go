package searchcontroller

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	searchv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/search"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	khandler "github.com/mongodb/mongodb-kubernetes/pkg/handler"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
)

// ownedCentral stamps the central-cluster ownership signal (owner ref by UID).
func ownedCentral(obj client.Object, search *searchv1.MongoDBSearch) client.Object {
	obj.SetOwnerReferences([]metav1.OwnerReference{{UID: search.UID}})
	return obj
}

// ownedMember stamps the member-cluster ownership signal (owner labels). When
// owned is false the labels point at a foreign CR and the sweep must skip it.
func ownedMember(obj client.Object, search *searchv1.MongoDBSearch, owned bool) client.Object {
	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	if owned {
		labels[khandler.MongoDBSearchOwnerNameLabel] = search.Name
		labels[khandler.MongoDBSearchOwnerNamespaceLabel] = search.Namespace
	} else {
		labels[khandler.MongoDBSearchOwnerNameLabel] = "other-search"
		labels[khandler.MongoDBSearchOwnerNamespaceLabel] = search.Namespace
	}
	obj.SetLabels(labels)
	return obj
}

func sweepSts(name, component string) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{
		Name: name, Namespace: "ns", Labels: map[string]string{componentLabelKey: component},
	}}
}

func sweepSvc(name, component string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns", Labels: map[string]string{componentLabelKey: component}},
		Spec:       corev1.ServiceSpec{ClusterIP: "10.0.0.1", Ports: []corev1.ServicePort{{Port: 27028}}},
	}
}

func sweepCM(name, component string) *corev1.ConfigMap {
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name: name, Namespace: "ns", Labels: map[string]string{componentLabelKey: component},
	}}
}

func notFound(t *testing.T, c kubernetesClient.Client, obj client.Object, namespace, name string) {
	t.Helper()
	err := c.Get(t.Context(), types.NamespacedName{Name: name, Namespace: namespace}, obj)
	require.True(t, apierrors.IsNotFound(err), "%s must be deleted, got err=%v", name, err)
}

func exists(t *testing.T, c kubernetesClient.Client, obj client.Object, namespace, name string) {
	t.Helper()
	require.NoError(t, c.Get(t.Context(), types.NamespacedName{Name: name, Namespace: namespace}, obj), "%s must survive cleanup", name)
}

// Case 1: stale mongot STS + headless Svc + CM + proxy Svc (old shard name) in
// the central cluster, owner-ref'd to the CR → deleted; expected ones survive.
func TestCleanupStaleResources_CentralClusterByOwnerRef(t *testing.T) {
	search := newTestMongoDBSearch("mdb-search", "ns", func(s *searchv1.MongoDBSearch) {
		s.UID = "search-uid"
		s.Spec.LoadBalancer = &searchv1.LoadBalancerConfig{Managed: &searchv1.ManagedLBConfig{}}
	})

	objs := []client.Object{search,
		ownedCentral(sweepSts("mdb-search-search-0-sh-0", mongotComponent), search),
		ownedCentral(sweepSvc("mdb-search-search-0-sh-0-svc", mongotComponent), search),
		ownedCentral(sweepCM("mdb-search-search-0-sh-0-config", mongotComponent), search),
		ownedCentral(sweepSvc("mdb-search-search-0-sh-0-proxy-svc", proxyServiceComponent), search),
		// Stale (old shard name) — all four kinds, owned.
		ownedCentral(sweepSts("mdb-search-search-0-sh-old", mongotComponent), search),
		ownedCentral(sweepSvc("mdb-search-search-0-sh-old-svc", mongotComponent), search),
		ownedCentral(sweepCM("mdb-search-search-0-sh-old-config", mongotComponent), search),
		ownedCentral(sweepSvc("mdb-search-search-0-sh-old-proxy-svc", proxyServiceComponent), search),
	}
	c := newTestFakeClient(objs...)
	r := &MongoDBSearchReconcileHelper{mdbSearch: search, client: c}

	expected := staleSweepExpectations{
		statefulSets: map[string]bool{"mdb-search-search-0-sh-0": true},
		services:     map[string]bool{"mdb-search-search-0-sh-0-svc": true, "mdb-search-search-0-sh-0-proxy-svc": true},
		configMaps:   map[string]bool{"mdb-search-search-0-sh-0-config": true},
	}
	require.NoError(t, r.cleanupStaleResources(t.Context(), zap.S(), expected))

	exists(t, c, &appsv1.StatefulSet{}, search.Namespace, "mdb-search-search-0-sh-0")
	exists(t, c, &corev1.Service{}, search.Namespace, "mdb-search-search-0-sh-0-svc")
	exists(t, c, &corev1.ConfigMap{}, search.Namespace, "mdb-search-search-0-sh-0-config")
	exists(t, c, &corev1.Service{}, search.Namespace, "mdb-search-search-0-sh-0-proxy-svc")

	notFound(t, c, &appsv1.StatefulSet{}, search.Namespace, "mdb-search-search-0-sh-old")
	notFound(t, c, &corev1.Service{}, search.Namespace, "mdb-search-search-0-sh-old-svc")
	notFound(t, c, &corev1.ConfigMap{}, search.Namespace, "mdb-search-search-0-sh-old-config")
	notFound(t, c, &corev1.Service{}, search.Namespace, "mdb-search-search-0-sh-old-proxy-svc")
}

// Case 2: stale member-cluster resources matched by owner labels are deleted.
// cluster-b is present in memberClusterClients but NOT in spec.clusters (the
// shape after a cluster is dropped from the spec); its owned stale resources
// must still be swept — the sweep iterates every registered client, not just
// the plan's clusters.
func TestCleanupStaleResources_MemberClusterByOwnerLabels(t *testing.T) {
	search := newTestMongoDBSearch("mdb-search", "ns", func(s *searchv1.MongoDBSearch) {
		s.UID = "search-uid"
		s.Spec.LoadBalancer = &searchv1.LoadBalancerConfig{Managed: &searchv1.ManagedLBConfig{}}
		s.Spec.Clusters = []searchv1.ClusterSpec{{ClusterName: "cluster-a"}}
	})

	member := kubernetesClient.NewClient(mock.NewEmptyFakeClientBuilder().WithObjects(
		ownedMember(sweepSts("mdb-search-search-0-sh-0", mongotComponent), search, true),
		ownedMember(sweepSvc("mdb-search-search-0-sh-0-svc", mongotComponent), search, true),
		ownedMember(sweepCM("mdb-search-search-0-sh-0-config", mongotComponent), search, true),
		ownedMember(sweepSvc("mdb-search-search-0-sh-0-proxy-svc", proxyServiceComponent), search, true),
		ownedMember(sweepSts("mdb-search-search-0-sh-old", mongotComponent), search, true),
		ownedMember(sweepSvc("mdb-search-search-0-sh-old-svc", mongotComponent), search, true),
		ownedMember(sweepCM("mdb-search-search-0-sh-old-config", mongotComponent), search, true),
		ownedMember(sweepSvc("mdb-search-search-0-sh-old-proxy-svc", proxyServiceComponent), search, true),
	).Build())

	// cluster-b: registered client, NOT in spec.clusters, all resources stale.
	droppedMember := kubernetesClient.NewClient(mock.NewEmptyFakeClientBuilder().WithObjects(
		ownedMember(sweepSts("mdb-search-search-1-sh-0", mongotComponent), search, true),
		ownedMember(sweepSvc("mdb-search-search-1-sh-0-svc", mongotComponent), search, true),
		ownedMember(sweepCM("mdb-search-search-1-sh-0-config", mongotComponent), search, true),
		ownedMember(sweepSvc("mdb-search-search-1-sh-0-proxy-svc", proxyServiceComponent), search, true),
	).Build())

	r := &MongoDBSearchReconcileHelper{
		mdbSearch:            search,
		client:               newTestFakeClient(search),
		memberClusterClients: map[string]kubernetesClient.Client{"cluster-a": member, "cluster-b": droppedMember},
	}

	expected := staleSweepExpectations{
		statefulSets: map[string]bool{"mdb-search-search-0-sh-0": true},
		services:     map[string]bool{"mdb-search-search-0-sh-0-svc": true, "mdb-search-search-0-sh-0-proxy-svc": true},
		configMaps:   map[string]bool{"mdb-search-search-0-sh-0-config": true},
	}
	require.NoError(t, r.cleanupStaleResources(t.Context(), zap.S(), expected))

	exists(t, member, &appsv1.StatefulSet{}, search.Namespace, "mdb-search-search-0-sh-0")
	exists(t, member, &corev1.Service{}, search.Namespace, "mdb-search-search-0-sh-0-svc")
	exists(t, member, &corev1.ConfigMap{}, search.Namespace, "mdb-search-search-0-sh-0-config")
	exists(t, member, &corev1.Service{}, search.Namespace, "mdb-search-search-0-sh-0-proxy-svc")

	notFound(t, member, &appsv1.StatefulSet{}, search.Namespace, "mdb-search-search-0-sh-old")
	notFound(t, member, &corev1.Service{}, search.Namespace, "mdb-search-search-0-sh-old-svc")
	notFound(t, member, &corev1.ConfigMap{}, search.Namespace, "mdb-search-search-0-sh-old-config")
	notFound(t, member, &corev1.Service{}, search.Namespace, "mdb-search-search-0-sh-old-proxy-svc")

	// The dropped cluster's resources are all swept even though it's absent from spec.clusters.
	notFound(t, droppedMember, &appsv1.StatefulSet{}, search.Namespace, "mdb-search-search-1-sh-0")
	notFound(t, droppedMember, &corev1.Service{}, search.Namespace, "mdb-search-search-1-sh-0-svc")
	notFound(t, droppedMember, &corev1.ConfigMap{}, search.Namespace, "mdb-search-search-1-sh-0-config")
	notFound(t, droppedMember, &corev1.Service{}, search.Namespace, "mdb-search-search-1-sh-0-proxy-svc")
}

// Case 3: objects with the component labels but a DIFFERENT owner (foreign UID
// on central, foreign labels on member) → untouched.
func TestCleanupStaleResources_ForeignOwnerUntouched(t *testing.T) {
	search := newTestMongoDBSearch("mdb-search", "ns", func(s *searchv1.MongoDBSearch) {
		s.UID = "search-uid"
		s.Spec.LoadBalancer = &searchv1.LoadBalancerConfig{Managed: &searchv1.ManagedLBConfig{}}
		s.Spec.Clusters = []searchv1.ClusterSpec{{ClusterName: "cluster-a"}}
	})

	// Central: stale-named mongot STS owned by a different CR (foreign UID).
	foreignCentralSts := sweepSts("mdb-search-search-0-sh-old", mongotComponent)
	foreignCentralSts.OwnerReferences = []metav1.OwnerReference{{UID: "other-uid"}}

	c := newTestFakeClient(search, foreignCentralSts)

	member := kubernetesClient.NewClient(mock.NewEmptyFakeClientBuilder().WithObjects(
		// Member: stale-named mongot STS with foreign owner labels.
		ownedMember(sweepSts("mdb-search-search-0-sh-old", mongotComponent), search, false),
	).Build())

	r := &MongoDBSearchReconcileHelper{
		mdbSearch:            search,
		client:               c,
		memberClusterClients: map[string]kubernetesClient.Client{"cluster-a": member},
	}

	require.NoError(t, r.cleanupStaleResources(t.Context(), zap.S(), staleSweepExpectations{}))

	exists(t, c, &appsv1.StatefulSet{}, search.Namespace, "mdb-search-search-0-sh-old")
	exists(t, member, &appsv1.StatefulSet{}, search.Namespace, "mdb-search-search-0-sh-old")
}

// Case 4: the Envoy controller's ConfigMap — stamped component=search-proxy AND
// both search-owner labels (the real production shape) — must survive. The CM
// sweep matches component=mongot only, so even though ownership matches, the
// ConfigMap kind never lists or deletes a search-proxy-labelled CM.
func TestCleanupStaleResources_NonMongotConfigMapUntouched(t *testing.T) {
	search := newTestMongoDBSearch("mdb-search", "ns", func(s *searchv1.MongoDBSearch) {
		s.UID = "search-uid"
		s.Spec.LoadBalancer = &searchv1.LoadBalancerConfig{Managed: &searchv1.ManagedLBConfig{}}
	})

	envoyCM := sweepCM("mdb-search-envoy-0-config", proxyServiceComponent)
	envoyCM.OwnerReferences = []metav1.OwnerReference{{UID: search.UID}}
	envoyCM.Labels[khandler.MongoDBSearchOwnerNameLabel] = search.Name
	envoyCM.Labels[khandler.MongoDBSearchOwnerNamespaceLabel] = search.Namespace

	c := newTestFakeClient(search, envoyCM)
	r := &MongoDBSearchReconcileHelper{mdbSearch: search, client: c}

	require.NoError(t, r.cleanupStaleResources(t.Context(), zap.S(), staleSweepExpectations{}))

	exists(t, c, &corev1.ConfigMap{}, search.Namespace, "mdb-search-envoy-0-config")
}

// Case 5: member cluster A's List returns an injected error → cluster B is still
// swept, and the returned error mentions A.
func TestCleanupStaleResources_OneClusterErrorDoesNotAbortOthers(t *testing.T) {
	search := newTestMongoDBSearch("mdb-search", "ns", func(s *searchv1.MongoDBSearch) {
		s.UID = "search-uid"
		s.Spec.LoadBalancer = &searchv1.LoadBalancerConfig{Managed: &searchv1.ManagedLBConfig{}}
		s.Spec.Clusters = []searchv1.ClusterSpec{{ClusterName: "cluster-a"}, {ClusterName: "cluster-b"}}
	})

	clusterA := kubernetesClient.NewClient(mock.NewEmptyFakeClientBuilder().
		WithObjects(ownedMember(sweepSts("mdb-search-search-0-sh-old", mongotComponent), search, true)).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				return assert.AnError
			},
		}).Build())

	clusterB := kubernetesClient.NewClient(mock.NewEmptyFakeClientBuilder().WithObjects(
		ownedMember(sweepSts("mdb-search-search-1-sh-old", mongotComponent), search, true),
	).Build())

	r := &MongoDBSearchReconcileHelper{
		mdbSearch:            search,
		client:               newTestFakeClient(search),
		memberClusterClients: map[string]kubernetesClient.Client{"cluster-a": clusterA, "cluster-b": clusterB},
	}

	err := r.cleanupStaleResources(t.Context(), zap.S(), staleSweepExpectations{})
	require.Error(t, err, "List error on cluster-a must surface")
	assert.Contains(t, err.Error(), "cluster-a", "error must name the failed cluster")

	// cluster-b was swept to completion despite cluster-a erroring.
	notFound(t, clusterB, &appsv1.StatefulSet{}, search.Namespace, "mdb-search-search-1-sh-old")
}

// Case 5b: a Delete failure (not just List) on one stale object must not abort
// the rest. The sweep continues to the next stale object on the SAME cluster,
// tolerates NotFound, and the joined error names the failed object.
func TestCleanupStaleResources_DeleteFailureDoesNotAbortRemainingItems(t *testing.T) {
	search := newTestMongoDBSearch("mdb-search", "ns", func(s *searchv1.MongoDBSearch) {
		s.UID = "search-uid"
		s.Spec.LoadBalancer = &searchv1.LoadBalancerConfig{Managed: &searchv1.ManagedLBConfig{}}
	})

	// The tracker sorts List results by name, so names are chosen for sweep
	// order: sh-a-wedged (Delete fails) < sh-gone (Delete NotFound) < sh-z-stale
	// (deletes normally). The successful delete is processed AFTER the failure,
	// so it genuinely exercises the per-item continuation branch.
	c := kubernetesClient.NewClient(mock.NewEmptyFakeClientBuilder().
		WithObjects(search,
			ownedCentral(sweepSts("mdb-search-search-0-sh-a-wedged", mongotComponent), search),
			ownedCentral(sweepSts("mdb-search-search-0-sh-gone", mongotComponent), search),
			ownedCentral(sweepSts("mdb-search-search-0-sh-z-stale", mongotComponent), search),
		).
		WithInterceptorFuncs(interceptor.Funcs{
			Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				switch obj.GetName() {
				case "mdb-search-search-0-sh-a-wedged":
					return assert.AnError
				case "mdb-search-search-0-sh-gone":
					// Its Delete is intercepted to return NotFound, simulating concurrent deletion.
					return apierrors.NewNotFound(appsv1.Resource("statefulsets"), obj.GetName())
				default:
					return cl.Delete(ctx, obj, opts...)
				}
			},
		}).Build())

	r := &MongoDBSearchReconcileHelper{mdbSearch: search, client: c}

	err := r.cleanupStaleResources(t.Context(), zap.S(), staleSweepExpectations{})
	require.Error(t, err, "the wedged Delete must surface")
	assert.Contains(t, err.Error(), "mdb-search-search-0-sh-a-wedged", "error must name the failed object")

	// Per-item continuation: sh-z-stale sorts AFTER the wedged failure yet is still deleted.
	notFound(t, c, &appsv1.StatefulSet{}, search.Namespace, "mdb-search-search-0-sh-z-stale")
	// NotFound on delete is tolerated — it does NOT add to the joined error.
	assert.NotContains(t, err.Error(), "sh-gone", "NotFound on delete must be tolerated, not surfaced")
}

// Single-cluster managed-LB sharded deployment where shards 0/1 are current and
// shard-2 is stale: the owned shard-2 proxy Service is deleted while the active
// shards and a foreign-owned Service survive.
func TestCleanupStaleResources_ShardedCentral(t *testing.T) {
	search := newTestMongoDBSearch("test-search", "test-ns", func(s *searchv1.MongoDBSearch) {
		s.UID = "search-uid"
		s.Spec.LoadBalancer = &searchv1.LoadBalancerConfig{Managed: &searchv1.ManagedLBConfig{}}
	})

	proxySvc := func(shard string, owned bool) *corev1.Service {
		svc := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name: search.ProxyServiceNameForClusterShard(0, shard).Name, Namespace: "test-ns",
				Labels: map[string]string{componentLabelKey: proxyServiceComponent},
			},
			Spec: corev1.ServiceSpec{ClusterIP: "10.0.0.1", Ports: []corev1.ServicePort{{Port: 27028}}},
		}
		if owned {
			svc.OwnerReferences = []metav1.OwnerReference{{UID: search.UID}}
		} else {
			svc.OwnerReferences = []metav1.OwnerReference{{UID: "other-uid"}}
		}
		return svc
	}

	fakeClient := newTestFakeClient(search,
		proxySvc("shard-0", true),  // active, owned
		proxySvc("shard-1", true),  // active, owned
		proxySvc("shard-2", true),  // stale, owned
		proxySvc("shard-x", false), // different owner
	)
	r := &MongoDBSearchReconcileHelper{mdbSearch: search, client: fakeClient}

	expected := staleSweepExpectations{services: map[string]bool{
		search.ProxyServiceNameForClusterShard(0, "shard-0").Name: true,
		search.ProxyServiceNameForClusterShard(0, "shard-1").Name: true,
	}}
	require.NoError(t, r.cleanupStaleResources(t.Context(), zap.S(), expected))

	exists(t, fakeClient, &corev1.Service{}, search.Namespace, search.ProxyServiceNameForClusterShard(0, "shard-0").Name)
	exists(t, fakeClient, &corev1.Service{}, search.Namespace, search.ProxyServiceNameForClusterShard(0, "shard-1").Name)
	notFound(t, fakeClient, &corev1.Service{}, search.Namespace, search.ProxyServiceNameForClusterShard(0, "shard-2").Name)
	exists(t, fakeClient, &corev1.Service{}, search.Namespace, search.ProxyServiceNameForClusterShard(0, "shard-x").Name)
}

// The sweep reaches into both member clusters: active per-shard and cluster-level
// proxy Services survive, stale per-shard proxy Services are deleted, and
// foreign-owned Services are untouched.
func TestCleanupStaleResources_ShardedMCFanOut(t *testing.T) {
	search := newTestMongoDBSearch("mdb-search", "ns", func(s *searchv1.MongoDBSearch) {
		s.UID = "search-uid"
		s.Spec.LoadBalancer = &searchv1.LoadBalancerConfig{Managed: &searchv1.ManagedLBConfig{}}
		s.Spec.Clusters = []searchv1.ClusterSpec{
			{ClusterName: "cluster-a"},
			{ClusterName: "cluster-b"},
		}
	})

	memberProxySvc := func(name string, owned bool) *corev1.Service {
		labels := map[string]string{componentLabelKey: proxyServiceComponent}
		if owned {
			labels[khandler.MongoDBSearchOwnerNameLabel] = search.Name
			labels[khandler.MongoDBSearchOwnerNamespaceLabel] = search.Namespace
		}
		return &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns", Labels: labels},
			Spec:       corev1.ServiceSpec{ClusterIP: "10.0.0.1", Ports: []corev1.ServicePort{{Port: 27028}}},
		}
	}

	clusterA := kubernetesClient.NewClient(mock.NewEmptyFakeClientBuilder().WithObjects(
		memberProxySvc("mdb-search-search-0-sh-0-proxy-svc", true),
		memberProxySvc("mdb-search-search-0-sh-stale-proxy-svc", true),
		memberProxySvc("mdb-search-search-0-proxy-svc", true),
		memberProxySvc("foreign-svc", false), // not search-owned
	).Build())
	clusterB := kubernetesClient.NewClient(mock.NewEmptyFakeClientBuilder().WithObjects(
		memberProxySvc("mdb-search-search-1-sh-0-proxy-svc", true),
		memberProxySvc("mdb-search-search-1-sh-stale-proxy-svc", true),
		memberProxySvc("mdb-search-search-1-proxy-svc", true),
		func() *corev1.Service {
			svc := memberProxySvc("other-search-search-1-sh-0-proxy-svc", true)
			svc.Labels[khandler.MongoDBSearchOwnerNameLabel] = "other-search"
			return svc
		}(),
	).Build())
	central := kubernetesClient.NewClient(mock.NewEmptyFakeClientBuilder().Build())

	r := &MongoDBSearchReconcileHelper{
		mdbSearch:            search,
		client:               central,
		memberClusterClients: map[string]kubernetesClient.Client{"cluster-a": clusterA, "cluster-b": clusterB},
		clusterMapping:       map[string]int{"cluster-a": 0, "cluster-b": 1},
	}

	expected := staleSweepExpectations{services: map[string]bool{
		"mdb-search-search-0-sh-0-proxy-svc": true,
		"mdb-search-search-0-proxy-svc":      true,
		"mdb-search-search-1-sh-0-proxy-svc": true,
		"mdb-search-search-1-proxy-svc":      true,
	}}
	require.NoError(t, r.cleanupStaleResources(t.Context(), zap.S(), expected))

	for _, tc := range []struct {
		c       kubernetesClient.Client
		idx     int
		cluster string
		foreign string
	}{
		{clusterA, 0, "cluster-a", "foreign-svc"},
		{clusterB, 1, "cluster-b", "other-search-search-1-sh-0-proxy-svc"},
	} {
		active := fmt.Sprintf("mdb-search-search-%d-sh-0-proxy-svc", tc.idx)
		stale := fmt.Sprintf("mdb-search-search-%d-sh-stale-proxy-svc", tc.idx)
		clusterLevel := fmt.Sprintf("mdb-search-search-%d-proxy-svc", tc.idx)

		exists(t, tc.c, &corev1.Service{}, search.Namespace, active)
		exists(t, tc.c, &corev1.Service{}, search.Namespace, clusterLevel)
		notFound(t, tc.c, &corev1.Service{}, search.Namespace, stale)
		exists(t, tc.c, &corev1.Service{}, search.Namespace, tc.foreign)
	}
}
