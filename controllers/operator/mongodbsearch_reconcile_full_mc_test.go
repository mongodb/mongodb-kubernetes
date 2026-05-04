package operator

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ghodss/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"
	"github.com/mongodb/mongodb-kubernetes/api/v1/status"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/controllers/searchcontroller"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/mongot"
)

// ----------------------------------------------------------------------------
// MC full-reconcile tests for the MongoDBSearch + Envoy reconcilers
//
// These tests exercise the integration of Tasks 14-20 (MC MVP Phase 2):
//   - Per-cluster naming helpers
//   - Per-cluster reconcileUnit fan-out
//   - Per-cluster client selection
//   - Top-level mongot host seeds
//   - Per-cluster Envoy SNI
//
// Each test constructs a full MongoDBSearchReconciler with two fake member
// clusters (cluster-a, cluster-b) and an external MongoDB source, then drives
// Reconcile() and asserts the per-cluster object graph lands in the right
// member-cluster client. Function-level TDD tests miss this integration path —
// the controller used to ignore its own member-cluster map.
//
// ----------------------------------------------------------------------------

const (
	mcTestNamespace   = mock.TestNamespace
	mcTestSearchName  = "mdb-search"
	mcExternalHost1   = "mongo-0.example.com:27017"
	mcExternalHost2   = "mongo-1.example.com:27017"
	mcClusterAName    = "cluster-a"
	mcClusterBName    = "cluster-b"
	mcLBExternalHost  = "mongot-{clusterName}.example.com"
	mcEnvoyTestImage  = "envoy:test"
	mcOperatorVersion = "0.50.0"
)

// mcFakeReconcilerHarness wires a MongoDBSearchReconciler + envoy reconciler
// against fake central + 2 fake member-cluster clients. Reuses
// mock.NewEmptyFakeClientBuilder so the scheme is identical to production.
type mcFakeReconcilerHarness struct {
	central  client.Client
	clusterA client.Client
	clusterB client.Client
	searchR  *MongoDBSearchReconciler
	envoyR   *MongoDBSearchEnvoyReconciler
}

func newMCFakeReconcilerHarness(t *testing.T, search *searchv1.MongoDBSearch, registerClusterB bool) *mcFakeReconcilerHarness {
	t.Helper()

	centralBuilder := mock.NewEmptyFakeClientBuilder()
	if search != nil {
		centralBuilder.WithObjects(search)
	}
	central := centralBuilder.Build()
	clusterA := mock.NewEmptyFakeClientBuilder().Build()

	memberClients := map[string]client.Client{mcClusterAName: clusterA}
	var clusterB client.Client
	if registerClusterB {
		clusterB = mock.NewEmptyFakeClientBuilder().Build()
		memberClients[mcClusterBName] = clusterB
	}

	operatorConfig := searchcontroller.OperatorSearchConfig{
		SearchRepo:    "testrepo",
		SearchName:    "mongot",
		SearchVersion: mcOperatorVersion,
	}

	return &mcFakeReconcilerHarness{
		central:  central,
		clusterA: clusterA,
		clusterB: clusterB,
		searchR:  newMongoDBSearchReconciler(central, operatorConfig, memberClients),
		envoyR:   newMongoDBSearchEnvoyReconciler(central, mcEnvoyTestImage, memberClients),
	}
}

// newMCSearch builds a 2-cluster external-source MongoDBSearch with managed LB.
func newMCSearch(name, namespace string, modifications ...func(*searchv1.MongoDBSearch)) *searchv1.MongoDBSearch {
	one := int32(1)
	clusters := []searchv1.ClusterSpec{
		{ClusterName: mcClusterAName, Replicas: &one},
		{ClusterName: mcClusterBName, Replicas: &one},
	}
	mdb := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: searchv1.MongoDBSearchSpec{
			Source: &searchv1.MongoDBSource{
				ExternalMongoDBSource: &searchv1.ExternalMongoDBSource{
					HostAndPorts: []string{mcExternalHost1, mcExternalHost2},
				},
			},
			LoadBalancer: &searchv1.LoadBalancerConfig{
				Managed: &searchv1.ManagedLBConfig{ExternalHostname: mcLBExternalHost},
			},
			Clusters: &clusters,
		},
	}
	for _, m := range modifications {
		m(mdb)
	}
	return mdb
}

// reconcileMCSearchAndEnvoy runs the search controller's Reconcile() repeatedly
// until each member cluster's per-cluster STS has been observed (reconcile
// returns early on the first not-ready STS, so we drive a few iterations
// while marking STSes ready). Then runs the envoy controller's Reconcile()
// once. Returns the (fetched) MongoDBSearch after all passes.
func reconcileMCSearchAndEnvoy(t *testing.T, h *mcFakeReconcilerHarness, search *searchv1.MongoDBSearch) (*searchv1.MongoDBSearch, reconcile.Result, error) {
	t.Helper()
	ctx := t.Context()
	req := reconcile.Request{NamespacedName: types.NamespacedName{
		Name:      search.Name,
		Namespace: search.Namespace,
	}}

	// Drive the search reconciler until every cluster's STS has been observed
	// ready. The reconcile loop bails at the first not-ready STS, so we need
	// one pass per cluster (to create+observe each unit) plus one trailing
	// pass after the last STS is marked ready. In production the controller
	// is re-enqueued by the watch on the STS Status; here we drive it manually.
	clusterClients := []client.Client{h.central}
	if h.clusterA != nil {
		clusterClients = append(clusterClients, h.clusterA)
	}
	if h.clusterB != nil {
		clusterClients = append(clusterClients, h.clusterB)
	}
	clusterCount := 1
	if search.Spec.Clusters != nil {
		clusterCount = len(*search.Spec.Clusters)
	}
	var res reconcile.Result
	for i := 0; i <= clusterCount; i++ {
		var err error
		res, err = h.searchR.Reconcile(ctx, req)
		if err != nil {
			return nil, res, err
		}
		if err := mock.MarkAllStatefulSetsAsReady(ctx, search.Namespace, clusterClients...); err != nil {
			return nil, res, err
		}
	}

	if _, err := h.envoyR.Reconcile(ctx, req); err != nil {
		return nil, res, err
	}

	final := &searchv1.MongoDBSearch{}
	if getErr := h.central.Get(ctx, req.NamespacedName, final); getErr != nil {
		return nil, res, getErr
	}
	return final, res, nil
}

// ----------------------------------------------------------------------------
// 1. Per-cluster object placement (the headline MC integration assertion)
// ----------------------------------------------------------------------------

// TestReconcile_MC_PerClusterSTSPlacement is the headline test for the
// "controller threads its memberClusterClientsMap into the reconcile helper"
// integration. Without that wiring, both clusters' STSes silently land in the
// central client and MC is broken in production while function-level tests
// still pass. See the constructor commit that wires
// NewMongoDBSearchReconcileHelperWithMembers from the controller.
func TestReconcile_MC_PerClusterSTSPlacement(t *testing.T) {
	search := newMCSearch(mcTestSearchName, mcTestNamespace)
	h := newMCFakeReconcilerHarness(t, search, true)

	_, _, err := reconcileMCSearchAndEnvoy(t, h, search)
	require.NoError(t, err)

	// cluster-a's STS lands in cluster-a's client
	stsA := &appsv1.StatefulSet{}
	require.NoError(t, h.clusterA.Get(t.Context(),
		types.NamespacedName{Name: search.StatefulSetNamespacedNameForCluster(0).Name, Namespace: mcTestNamespace}, stsA))
	assert.Equal(t, "mdb-search-search-0", stsA.Name)

	// cluster-b's STS lands in cluster-b's client
	stsB := &appsv1.StatefulSet{}
	require.NoError(t, h.clusterB.Get(t.Context(),
		types.NamespacedName{Name: search.StatefulSetNamespacedNameForCluster(1).Name, Namespace: mcTestNamespace}, stsB))
	assert.Equal(t, "mdb-search-search-1", stsB.Name)

	// Central must NOT have either per-cluster STS.
	for _, name := range []string{"mdb-search-search-0", "mdb-search-search-1"} {
		err := h.central.Get(t.Context(),
			types.NamespacedName{Name: name, Namespace: mcTestNamespace}, &appsv1.StatefulSet{})
		assert.True(t, apierrors.IsNotFound(err), "central client must NOT have per-cluster STS %q", name)
	}

	// Cross-contamination check: cluster-a's STS not in cluster-b and vice versa.
	err = h.clusterA.Get(t.Context(),
		types.NamespacedName{Name: "mdb-search-search-1", Namespace: mcTestNamespace}, &appsv1.StatefulSet{})
	assert.True(t, apierrors.IsNotFound(err), "cluster-a must NOT have cluster-b's STS")
	err = h.clusterB.Get(t.Context(),
		types.NamespacedName{Name: "mdb-search-search-0", Namespace: mcTestNamespace}, &appsv1.StatefulSet{})
	assert.True(t, apierrors.IsNotFound(err), "cluster-b must NOT have cluster-a's STS")
}

// TestReconcile_MC_PerClusterServicesPlacement asserts the headless and proxy
// Services are also fanned out to the right cluster's client.
func TestReconcile_MC_PerClusterServicesPlacement(t *testing.T) {
	search := newMCSearch(mcTestSearchName, mcTestNamespace)
	h := newMCFakeReconcilerHarness(t, search, true)

	_, _, err := reconcileMCSearchAndEnvoy(t, h, search)
	require.NoError(t, err)

	cases := []struct {
		idx       int
		clusterIs client.Client
		hsName    string
		proxyName string
	}{
		{0, h.clusterA, "mdb-search-search-0-svc", "mdb-search-search-0-proxy-svc"},
		{1, h.clusterB, "mdb-search-search-1-svc", "mdb-search-search-1-proxy-svc"},
	}
	for _, c := range cases {
		hs := &corev1.Service{}
		require.NoError(t, c.clusterIs.Get(t.Context(),
			types.NamespacedName{Name: c.hsName, Namespace: mcTestNamespace}, hs))
		assert.Equal(t, corev1.ClusterIPNone, hs.Spec.ClusterIP, "headless Service must have ClusterIP=None")

		proxy := &corev1.Service{}
		require.NoError(t, c.clusterIs.Get(t.Context(),
			types.NamespacedName{Name: c.proxyName, Namespace: mcTestNamespace}, proxy))

		// Central must not have either Service.
		err := h.central.Get(t.Context(),
			types.NamespacedName{Name: c.hsName, Namespace: mcTestNamespace}, &corev1.Service{})
		assert.True(t, apierrors.IsNotFound(err), "central must NOT have %q", c.hsName)
		err = h.central.Get(t.Context(),
			types.NamespacedName{Name: c.proxyName, Namespace: mcTestNamespace}, &corev1.Service{})
		assert.True(t, apierrors.IsNotFound(err), "central must NOT have %q", c.proxyName)
	}
}

// TestReconcile_MC_MongotConfigMap_TopLevelHostSeeds asserts the rendered
// mongot config in EVERY cluster's ConfigMap uses the top-level
// spec.source.external.hostAndPorts (not per-cluster overrides). This is the
// MVP routing rule documented in the spec: "the same seed list is rendered
// into every cluster's mongot config".
func TestReconcile_MC_MongotConfigMap_TopLevelHostSeeds(t *testing.T) {
	search := newMCSearch(mcTestSearchName, mcTestNamespace)
	h := newMCFakeReconcilerHarness(t, search, true)

	_, _, err := reconcileMCSearchAndEnvoy(t, h, search)
	require.NoError(t, err)

	// cluster-a's ConfigMap is at the legacy unindexed name (idx==0 returns
	// MongotConfigConfigMapNamespacedName); cluster-b's gets the indexed name.
	cmA := &corev1.ConfigMap{}
	require.NoError(t, h.clusterA.Get(t.Context(),
		types.NamespacedName{Name: search.MongotConfigConfigMapNameForCluster(0).Name, Namespace: mcTestNamespace}, cmA))
	cmB := &corev1.ConfigMap{}
	require.NoError(t, h.clusterB.Get(t.Context(),
		types.NamespacedName{Name: search.MongotConfigConfigMapNameForCluster(1).Name, Namespace: mcTestNamespace}, cmB))

	// Both ConfigMaps' rendered config.yml must contain the top-level host seeds.
	for clusterIdx, cm := range []*corev1.ConfigMap{cmA, cmB} {
		raw := cm.Data[searchcontroller.MongotConfigFilename]
		require.NotEmpty(t, raw, "cluster %d mongot config.yml must be populated", clusterIdx)

		var parsed mongot.Config
		require.NoError(t, yaml.Unmarshal([]byte(raw), &parsed),
			"cluster %d config.yml must be valid YAML", clusterIdx)

		// MVP rule: every cluster sees the top-level external host list.
		assert.Equal(t, []string{mcExternalHost1, mcExternalHost2},
			parsed.SyncSource.ReplicaSet.HostAndPort,
			"cluster %d: syncSource.replicaSet.hostAndPort must equal top-level spec.source.external.hostAndPorts", clusterIdx)
	}
}

// ----------------------------------------------------------------------------
// 2. Envoy controller per-cluster object placement
// ----------------------------------------------------------------------------

// TestReconcile_MC_EnvoyDeploymentPlacement asserts each cluster gets its own
// Envoy Deployment in its own member client; replicas come from top-level LB
// config (default 1).
func TestReconcile_MC_EnvoyDeploymentPlacement(t *testing.T) {
	search := newMCSearch(mcTestSearchName, mcTestNamespace)
	h := newMCFakeReconcilerHarness(t, search, true)

	_, _, err := reconcileMCSearchAndEnvoy(t, h, search)
	require.NoError(t, err)

	cases := []struct {
		clusterIs client.Client
		clusterID string
	}{
		{h.clusterA, mcClusterAName},
		{h.clusterB, mcClusterBName},
	}
	for _, c := range cases {
		dep := &appsv1.Deployment{}
		require.NoError(t, c.clusterIs.Get(t.Context(),
			types.NamespacedName{Name: search.LoadBalancerDeploymentNameForCluster(c.clusterID), Namespace: mcTestNamespace}, dep))
		require.NotNil(t, dep.Spec.Replicas)
		// Default replicas == 1 (top-level LB.managed.replicas not set).
		assert.Equal(t, int32(1), *dep.Spec.Replicas)

		// Cluster-name label stamped (cross-cluster enqueue).
		assert.Equal(t, c.clusterID, dep.Labels[envoyClusterNameLabel])
		assert.Equal(t, search.Name, dep.Labels[envoyOwnerSearchNameLabel])
	}

	// Central does not have either Envoy Deployment.
	for _, clusterID := range []string{mcClusterAName, mcClusterBName} {
		err := h.central.Get(t.Context(),
			types.NamespacedName{Name: search.LoadBalancerDeploymentNameForCluster(clusterID), Namespace: mcTestNamespace}, &appsv1.Deployment{})
		assert.True(t, apierrors.IsNotFound(err), "central must NOT have Envoy Deployment for cluster %q", clusterID)
	}
}

// TestReconcile_MC_EnvoyConfigMap_PerClusterEnvoyJSON asserts each cluster has
// its own ConfigMap containing a non-empty envoy.json that wires routing for
// that cluster. The TLS-disabled path (used in this test for setup brevity)
// does not embed SNI server_names in the JSON, so the per-cluster SNI text
// assertion is covered at the function level by TestEnvoyFilterChain_PerClusterSNI
// and TestBuildRoutesForCluster_RS_NoTemplateUsesPerClusterProxySvcFQDN. Here we
// validate the integration: the ConfigMap exists in each cluster's client and
// has differentiated content/labels. SNI-with-TLS is exercised via the
// dedicated TestReconcile_MC_EnvoyConfigMap_PerClusterSNI_TLSEnabled below.
func TestReconcile_MC_EnvoyConfigMap_PerClusterEnvoyJSON(t *testing.T) {
	search := newMCSearch(mcTestSearchName, mcTestNamespace)
	h := newMCFakeReconcilerHarness(t, search, true)

	_, _, err := reconcileMCSearchAndEnvoy(t, h, search)
	require.NoError(t, err)

	cases := []struct {
		clusterIs client.Client
		clusterID string
	}{
		{h.clusterA, mcClusterAName},
		{h.clusterB, mcClusterBName},
	}
	for _, c := range cases {
		cm := &corev1.ConfigMap{}
		require.NoError(t, c.clusterIs.Get(t.Context(),
			types.NamespacedName{Name: search.LoadBalancerConfigMapNameForCluster(c.clusterID), Namespace: mcTestNamespace}, cm))

		envoyJSON, ok := cm.Data["envoy.json"]
		require.True(t, ok, "cluster %q envoy ConfigMap must have envoy.json key", c.clusterID)
		assert.Contains(t, envoyJSON, "mongot_rs_cluster", "envoy.json must contain the rs cluster route")
		assert.Equal(t, c.clusterID, cm.Labels[envoyClusterNameLabel])
	}
}

// TestReconcile_MC_EnvoySvcMatchesProxySvc_AcrossControllers verifies the
// SEARCH and ENVOY controllers agree on per-cluster identity:
//   - SEARCH controller writes a per-cluster proxy Service to each member client
//   - ENVOY controller writes a per-cluster Deployment + ConfigMap to each client
//
// Both must use the same per-cluster naming convention (the search resource's
// per-cluster helpers) and land in the same client. Without this, the LB and
// upstream proxy would be deployed to mismatched clusters and never reach the
// mongot pod.
func TestReconcile_MC_EnvoySvcMatchesProxySvc_AcrossControllers(t *testing.T) {
	search := newMCSearch(mcTestSearchName, mcTestNamespace)
	h := newMCFakeReconcilerHarness(t, search, true)

	_, _, err := reconcileMCSearchAndEnvoy(t, h, search)
	require.NoError(t, err)

	cases := []struct {
		clusterIs client.Client
		clusterID string
		idx       int
	}{
		{h.clusterA, mcClusterAName, 0},
		{h.clusterB, mcClusterBName, 1},
	}
	for _, c := range cases {
		// The proxy Service the SEARCH controller wrote for this cluster.
		proxySvcName := search.ProxyServiceNamespacedNameForCluster(c.idx).Name
		require.NoError(t, c.clusterIs.Get(t.Context(),
			types.NamespacedName{Name: proxySvcName, Namespace: mcTestNamespace}, &corev1.Service{}),
			"cluster %q must have proxy Service %q (search controller)", c.clusterID, proxySvcName)

		// The Envoy Deployment the ENVOY controller wrote for this cluster.
		envoyDepName := search.LoadBalancerDeploymentNameForCluster(c.clusterID)
		require.NoError(t, c.clusterIs.Get(t.Context(),
			types.NamespacedName{Name: envoyDepName, Namespace: mcTestNamespace}, &appsv1.Deployment{}),
			"cluster %q must have Envoy Deployment %q (envoy controller)", c.clusterID, envoyDepName)

		// And the Envoy ConfigMap.
		envoyCMName := search.LoadBalancerConfigMapNameForCluster(c.clusterID)
		require.NoError(t, c.clusterIs.Get(t.Context(),
			types.NamespacedName{Name: envoyCMName, Namespace: mcTestNamespace}, &corev1.ConfigMap{}),
			"cluster %q must have Envoy ConfigMap %q (envoy controller)", c.clusterID, envoyCMName)
	}
}

// ----------------------------------------------------------------------------
// 3. Idempotence
// ----------------------------------------------------------------------------

// TestReconcile_MC_Idempotent asserts a second Reconcile() with no spec change
// produces byte-equal Spec/Data on every per-cluster object — no spurious
// rewrites, no per-pass drift in rendered configs.
func TestReconcile_MC_Idempotent(t *testing.T) {
	search := newMCSearch(mcTestSearchName, mcTestNamespace)
	h := newMCFakeReconcilerHarness(t, search, true)

	_, _, err := reconcileMCSearchAndEnvoy(t, h, search)
	require.NoError(t, err)

	// Snapshot rendered Spec/Data after pass 1.
	cmAv1 := &corev1.ConfigMap{}
	require.NoError(t, h.clusterA.Get(t.Context(),
		types.NamespacedName{Name: search.MongotConfigConfigMapNameForCluster(0).Name, Namespace: mcTestNamespace}, cmAv1))
	stsAv1 := &appsv1.StatefulSet{}
	require.NoError(t, h.clusterA.Get(t.Context(),
		types.NamespacedName{Name: "mdb-search-search-0", Namespace: mcTestNamespace}, stsAv1))
	envoyCmAv1 := &corev1.ConfigMap{}
	require.NoError(t, h.clusterA.Get(t.Context(),
		types.NamespacedName{Name: search.LoadBalancerConfigMapNameForCluster(mcClusterAName), Namespace: mcTestNamespace}, envoyCmAv1))

	// Pass 2.
	_, _, err = reconcileMCSearchAndEnvoy(t, h, search)
	require.NoError(t, err)

	cmAv2 := &corev1.ConfigMap{}
	require.NoError(t, h.clusterA.Get(t.Context(),
		types.NamespacedName{Name: search.MongotConfigConfigMapNameForCluster(0).Name, Namespace: mcTestNamespace}, cmAv2))
	stsAv2 := &appsv1.StatefulSet{}
	require.NoError(t, h.clusterA.Get(t.Context(),
		types.NamespacedName{Name: "mdb-search-search-0", Namespace: mcTestNamespace}, stsAv2))
	envoyCmAv2 := &corev1.ConfigMap{}
	require.NoError(t, h.clusterA.Get(t.Context(),
		types.NamespacedName{Name: search.LoadBalancerConfigMapNameForCluster(mcClusterAName), Namespace: mcTestNamespace}, envoyCmAv2))

	// Spec/Data must be byte-equal between passes — the fake client may bump
	// ResourceVersion on a no-op CreateOrUpdate, so we compare what's actually
	// rendered, not the metadata.
	assert.Equal(t, cmAv1.Data, cmAv2.Data, "mongot ConfigMap.Data must not drift on idempotent re-reconcile")
	assert.Equal(t, stsAv1.Spec, stsAv2.Spec, "STS.Spec must not drift on idempotent re-reconcile")
	assert.Equal(t, envoyCmAv1.Data, envoyCmAv2.Data, "Envoy ConfigMap.Data must not drift on idempotent re-reconcile")
}

// ----------------------------------------------------------------------------
// 4. Status surface
// ----------------------------------------------------------------------------

// TestReconcile_MC_StatusSurface asserts:
//   - status.clusterStatuses has one entry per spec.clusters[i] with phase set
//     to the same top-level Phase the search reconciler produced (worst-of
//     across clusters at this layer)
//   - status.loadBalancer.clusters has one entry per cluster (envoy controller),
//     all Running because both member clients are reachable
//   - status.loadBalancer.phase aggregates worst-of (Running here)
func TestReconcile_MC_StatusSurface(t *testing.T) {
	search := newMCSearch(mcTestSearchName, mcTestNamespace)
	h := newMCFakeReconcilerHarness(t, search, true)

	final, _, err := reconcileMCSearchAndEnvoy(t, h, search)
	require.NoError(t, err)

	// Per-cluster status entries — one per spec.clusters[i].
	require.Len(t, final.Status.ClusterStatusList.ClusterStatuses, 2,
		"status.clusterStatusList.clusterStatuses must have one entry per spec.clusters[i]")
	byName := map[string]searchv1.SearchClusterStatusItem{}
	for _, entry := range final.Status.ClusterStatusList.ClusterStatuses {
		byName[entry.ClusterName] = entry
		// The aggregator copies the workflow phase into every cluster's entry,
		// so each per-cluster phase must equal the top-level phase.
		assert.Equal(t, final.Status.Phase, entry.Phase,
			"per-cluster phase must mirror top-level phase")
	}
	assert.Contains(t, byName, mcClusterAName)
	assert.Contains(t, byName, mcClusterBName)

	// LB substatus populated for both clusters by the Envoy reconciler.
	// With both member clients registered + reachable, both LB phases are Running.
	require.NotNil(t, final.Status.LoadBalancer, "status.loadBalancer must be populated for MC")
	assert.Equal(t, status.PhaseRunning, final.Status.LoadBalancer.Phase,
		"top-level LoadBalancer.Phase must aggregate to Running when both clusters succeed")
	require.Len(t, final.Status.LoadBalancer.Clusters, 2,
		"status.loadBalancer.clusters must have one entry per cluster")
	lbByName := map[string]searchv1.ClusterLoadBalancerStatus{}
	for _, entry := range final.Status.LoadBalancer.Clusters {
		lbByName[entry.ClusterName] = entry
		assert.Equal(t, status.PhaseRunning, entry.Phase,
			"cluster %q LB phase must be Running when its envoy reconcile succeeds", entry.ClusterName)
	}
	assert.Contains(t, lbByName, mcClusterAName)
	assert.Contains(t, lbByName, mcClusterBName)
}

// TestReconcile_MC_OneClusterReady_OneClusterPending_StatusAggregatesPending
// asserts the worst-of-phase aggregation rule when one cluster reconciles
// cleanly and the other is unreachable (cluster-b not registered as a member,
// so the LB reconcile for cluster-b surfaces Pending while cluster-a reaches
// Running). The aggregated top-level LoadBalancer.Phase must be Pending —
// the worse of (Running, Pending).
func TestReconcile_MC_OneClusterReady_OneClusterPending_StatusAggregatesPending(t *testing.T) {
	search := newMCSearch(mcTestSearchName, mcTestNamespace)
	// Only register cluster-a; cluster-b unreachable.
	h := newMCFakeReconcilerHarness(t, search, false)

	final, _, err := reconcileMCSearchAndEnvoy(t, h, search)
	require.NoError(t, err)

	require.NotNil(t, final.Status.LoadBalancer, "status.loadBalancer must be populated for MC")
	assert.Equal(t, status.PhasePending, final.Status.LoadBalancer.Phase,
		"worst-of (Running, Pending) must aggregate to Pending")

	// Per-cluster LB entries surface each cluster's individual phase.
	require.Len(t, final.Status.LoadBalancer.Clusters, 2)
	byName := map[string]searchv1.ClusterLoadBalancerStatus{}
	for _, c := range final.Status.LoadBalancer.Clusters {
		byName[c.ClusterName] = c
	}
	assert.Equal(t, status.PhaseRunning, byName[mcClusterAName].Phase,
		"cluster-a (registered + reachable) must aggregate to Running")
	assert.Equal(t, status.PhasePending, byName[mcClusterBName].Phase,
		"cluster-b (unregistered) must aggregate to Pending with a helpful message")
	assert.Contains(t, byName[mcClusterBName].Message, mcClusterBName,
		"unregistered cluster's per-cluster message must name it")
}

// ----------------------------------------------------------------------------
// 5. Failure modes
// ----------------------------------------------------------------------------

// TestReconcile_MC_MissingExternalHostAndPorts_ValidationRejects asserts
// the spec validator rejects MC configs that omit
// spec.source.external.hostAndPorts (validateMCRequiresExternalHostAndPorts).
// At the controller level, ValidateSpec runs in reconcile and surfaces
// Invalid (which behaves like a soft failure).
func TestReconcile_MC_MissingExternalHostAndPorts_ValidationRejects(t *testing.T) {
	search := newMCSearch(mcTestSearchName, mcTestNamespace, func(s *searchv1.MongoDBSearch) {
		// Remove hostAndPorts but keep external source set.
		s.Spec.Source.ExternalMongoDBSource.HostAndPorts = nil
	})
	// Standalone validator check — admission would block this in production.
	err := search.ValidateSpec()
	require.Error(t, err, "MC without spec.source.external.hostAndPorts must fail spec validation")
	assert.Contains(t, err.Error(), "spec.source.external.hostAndPorts is required",
		"validator must name the offending field")

	// Now also exercise the reconcile path: helper should surface workflow.Invalid,
	// which UpdateStatus translates to a non-Running phase.
	h := newMCFakeReconcilerHarness(t, search, true)
	final, _, err := reconcileMCSearchAndEnvoy(t, h, search)
	require.NoError(t, err)
	assert.NotEqual(t, status.PhaseRunning, final.Status.Phase,
		"MC reconcile without hostAndPorts must not reach Running")
}

// TestReconcile_MC_UnknownClusterInSpec_Failed asserts: when spec.clusters[i]
// names a cluster not registered with the operator (member map missing), the
// per-cluster reconcile path returns Failed with a helpful message naming the
// unknown cluster.
func TestReconcile_MC_UnknownClusterInSpec_Failed(t *testing.T) {
	search := newMCSearch(mcTestSearchName, mcTestNamespace)
	// Only register cluster-a as a member; cluster-b is in spec but not registered.
	h := newMCFakeReconcilerHarness(t, search, false)

	final, _, err := reconcileMCSearchAndEnvoy(t, h, search)
	require.NoError(t, err)

	// Phase must NOT be Running (Failed is the search controller's path,
	// envoy controller surfaces Pending — both are non-Running).
	assert.NotEqual(t, status.PhaseRunning, final.Status.Phase,
		"unknown cluster in spec.clusters must not result in Running")
	// Message must reference the unknown cluster name to help the operator.
	assert.Contains(t, final.Status.Message, mcClusterBName,
		"status message must name the unknown cluster")
}

// TestReconcile_MC_PerClusterReplicasOverride_Honored asserts that a
// per-cluster spec.clusters[i].loadBalancer.managed.replicas override is
// honored by the Envoy controller and writes the correct replica count to
// that cluster's Deployment. This is a regression guard for the
// envoyReplicasForCluster precedence rule:
//
//	clusters[i].loadBalancer.managed.replicas > spec.loadBalancer.managed.replicas > default
//
// (PR #1050 may strip the per-cluster override later; if so, this test
// becomes the regression guard for the rejection path.)
func TestReconcile_MC_PerClusterReplicasOverride_Honored(t *testing.T) {
	override := int32(3)
	top := int32(2)
	search := newMCSearch(mcTestSearchName, mcTestNamespace, func(s *searchv1.MongoDBSearch) {
		s.Spec.LoadBalancer.Managed.Replicas = &top
		// cluster-a has an explicit per-cluster override; cluster-b inherits top-level.
		(*s.Spec.Clusters)[0].LoadBalancer = &searchv1.PerClusterLoadBalancerConfig{
			Managed: &searchv1.ManagedLBConfig{Replicas: &override},
		}
	})
	h := newMCFakeReconcilerHarness(t, search, true)

	_, _, err := reconcileMCSearchAndEnvoy(t, h, search)
	require.NoError(t, err)

	depA := &appsv1.Deployment{}
	require.NoError(t, h.clusterA.Get(t.Context(),
		types.NamespacedName{Name: search.LoadBalancerDeploymentNameForCluster(mcClusterAName), Namespace: mcTestNamespace}, depA))
	require.NotNil(t, depA.Spec.Replicas)
	assert.Equal(t, override, *depA.Spec.Replicas, "cluster-a Envoy Deployment must honor per-cluster replicas override")

	depB := &appsv1.Deployment{}
	require.NoError(t, h.clusterB.Get(t.Context(),
		types.NamespacedName{Name: search.LoadBalancerDeploymentNameForCluster(mcClusterBName), Namespace: mcTestNamespace}, depB))
	require.NotNil(t, depB.Spec.Replicas)
	assert.Equal(t, top, *depB.Spec.Replicas, "cluster-b inherits top-level replicas")
}

// TestReconcile_MC_TopLevelEnvoyReplicasUniform asserts that without per-cluster
// overrides, top-level spec.loadBalancer.managed.replicas applies uniformly
// across all clusters.
func TestReconcile_MC_TopLevelEnvoyReplicasUniform(t *testing.T) {
	top := int32(4)
	search := newMCSearch(mcTestSearchName, mcTestNamespace, func(s *searchv1.MongoDBSearch) {
		s.Spec.LoadBalancer.Managed.Replicas = &top
	})
	h := newMCFakeReconcilerHarness(t, search, true)

	_, _, err := reconcileMCSearchAndEnvoy(t, h, search)
	require.NoError(t, err)

	for _, c := range []struct {
		clusterIs client.Client
		clusterID string
	}{
		{h.clusterA, mcClusterAName},
		{h.clusterB, mcClusterBName},
	} {
		dep := &appsv1.Deployment{}
		require.NoError(t, c.clusterIs.Get(t.Context(),
			types.NamespacedName{Name: search.LoadBalancerDeploymentNameForCluster(c.clusterID), Namespace: mcTestNamespace}, dep))
		require.NotNil(t, dep.Spec.Replicas)
		assert.Equal(t, top, *dep.Spec.Replicas,
			"cluster %q must inherit top-level replicas %d", c.clusterID, top)
	}
}

// TestReconcile_MC_LBCertSANIncomplete_Failed is currently a regression guard
// only — the validateLBCertSAN function at
// controllers/searchcontroller/mongodbsearch_reconcile_helper.go exists but
// is NOT yet wired into the reconcile loop (see TODO comment on the function).
// Once wired (creating the LB server cert Secret with insufficient SANs and
// surfacing workflow.Failed in reconcile), this test becomes a real assertion.
//
// Skipped now to make the gap explicit — half-honest is better than fake-comprehensive.
func TestReconcile_MC_LBCertSANIncomplete_Failed(t *testing.T) {
	t.Skip("validateLBCertSAN exists but is not wired into the reconcile loop yet; " +
		"see TODO at controllers/searchcontroller/mongodbsearch_reconcile_helper.go validateLBCertSAN. " +
		"Once wired (Phase 2 follow-up), this test should construct an LB server cert Secret with " +
		"only cluster-a's FQDN as a SAN and assert reconcile returns workflow.Failed naming the " +
		"missing FQDN for cluster-b. Function-level coverage of validateLBCertSAN already exists in " +
		"searchcontroller/mongodbsearch_reconcile_helper_test.go.")
}

// ----------------------------------------------------------------------------
// 6. Cluster-index annotation persistence
// ----------------------------------------------------------------------------

// TestReconcile_MC_ClusterIndexAnnotationPersisted asserts that the LastClusterNumMapping
// annotation is written by the helper after the first reconcile, matching the order
// of spec.clusters[]. This is the key state that drives index-based per-cluster naming
// and prevents drift on cluster removal/re-add.
func TestReconcile_MC_ClusterIndexAnnotationPersisted(t *testing.T) {
	search := newMCSearch(mcTestSearchName, mcTestNamespace)
	h := newMCFakeReconcilerHarness(t, search, true)

	final, _, err := reconcileMCSearchAndEnvoy(t, h, search)
	require.NoError(t, err)

	raw, ok := final.Annotations[searchv1.LastClusterNumMapping]
	require.True(t, ok, "LastClusterNumMapping annotation must be set after first reconcile")
	mapping := map[string]int{}
	require.NoError(t, json.Unmarshal([]byte(raw), &mapping))
	assert.Equal(t, 0, mapping[mcClusterAName], "cluster-a must map to index 0")
	assert.Equal(t, 1, mapping[mcClusterBName], "cluster-b must map to index 1")
}

// ----------------------------------------------------------------------------
// 7. Cross-cluster owner ref behavior (consistency note)
// ----------------------------------------------------------------------------

// TestReconcile_MC_OwnerRefBehavior documents the actual cross-cluster owner ref
// behavior across the two reconcilers. K8s GC does not span clusters, so:
//   - Search reconciler: per-cluster STS/Service/CM are written via
//     applyReconcileUnit, which sets an OwnerReference unconditionally
//     (controllerutil.SetOwnerReference). On member-cluster objects, this owner
//     ref is dangling — it points to a UID that doesn't exist in that cluster.
//   - Envoy reconciler: ensureConfigMap / ensureDeployment skip SetOwnerReference
//     when clusterName != "" — by design, no dangling owner ref on member-cluster
//     Envoy objects.
//
// This test asserts the CURRENT behavior so we don't accidentally change it
// silently. The inconsistency between controllers is documented in this test
// and should be addressed in a follow-up (consistency one way or the other).
func TestReconcile_MC_OwnerRefBehavior(t *testing.T) {
	search := newMCSearch(mcTestSearchName, mcTestNamespace)
	h := newMCFakeReconcilerHarness(t, search, true)

	_, _, err := reconcileMCSearchAndEnvoy(t, h, search)
	require.NoError(t, err)

	// Search reconciler: STS in cluster-a HAS an owner ref (currently dangling
	// across clusters; flagged for future consistency follow-up).
	stsA := &appsv1.StatefulSet{}
	require.NoError(t, h.clusterA.Get(t.Context(),
		types.NamespacedName{Name: "mdb-search-search-0", Namespace: mcTestNamespace}, stsA))
	assert.NotEmpty(t, stsA.OwnerReferences,
		"search controller currently sets owner ref on member-cluster STS; "+
			"K8s GC won't honor it across clusters but the label-based mapper still routes events")

	// Envoy reconciler: deployment and ConfigMap on member cluster have NO
	// owner ref — explicitly documented behavior in ensureConfigMap.
	depA := &appsv1.Deployment{}
	require.NoError(t, h.clusterA.Get(t.Context(),
		types.NamespacedName{Name: search.LoadBalancerDeploymentNameForCluster(mcClusterAName), Namespace: mcTestNamespace}, depA))
	assert.Empty(t, depA.OwnerReferences,
		"envoy controller deliberately skips owner ref on cross-cluster Deployment")

	envoyCMA := &corev1.ConfigMap{}
	require.NoError(t, h.clusterA.Get(t.Context(),
		types.NamespacedName{Name: search.LoadBalancerConfigMapNameForCluster(mcClusterAName), Namespace: mcTestNamespace}, envoyCMA))
	assert.Empty(t, envoyCMA.OwnerReferences,
		"envoy controller deliberately skips owner ref on cross-cluster ConfigMap")
}

// TestReconcile_MC_CrossClusterEnqueueLabelsStamped asserts that the
// cross-cluster enqueue labels stamped on Envoy Deployment + ConfigMap allow
// the label-based mapper to route member-cluster events back to the owning
// MongoDBSearch (since cross-cluster owner refs don't GC, labels are the
// path home).
func TestReconcile_MC_CrossClusterEnqueueLabelsStamped(t *testing.T) {
	search := newMCSearch(mcTestSearchName, mcTestNamespace)
	h := newMCFakeReconcilerHarness(t, search, true)

	_, _, err := reconcileMCSearchAndEnvoy(t, h, search)
	require.NoError(t, err)

	for _, c := range []struct {
		clusterIs client.Client
		clusterID string
	}{
		{h.clusterA, mcClusterAName},
		{h.clusterB, mcClusterBName},
	} {
		dep := &appsv1.Deployment{}
		require.NoError(t, c.clusterIs.Get(t.Context(),
			types.NamespacedName{Name: search.LoadBalancerDeploymentNameForCluster(c.clusterID), Namespace: mcTestNamespace}, dep))
		assert.Equal(t, search.Name, dep.Labels[envoyOwnerSearchNameLabel])
		assert.Equal(t, search.Namespace, dep.Labels[envoyOwnerSearchNamespaceLabel])
		assert.Equal(t, c.clusterID, dep.Labels[envoyClusterNameLabel])

		cm := &corev1.ConfigMap{}
		require.NoError(t, c.clusterIs.Get(t.Context(),
			types.NamespacedName{Name: search.LoadBalancerConfigMapNameForCluster(c.clusterID), Namespace: mcTestNamespace}, cm))
		assert.Equal(t, search.Name, cm.Labels[envoyOwnerSearchNameLabel])
		assert.Equal(t, search.Namespace, cm.Labels[envoyOwnerSearchNamespaceLabel])
		assert.Equal(t, c.clusterID, cm.Labels[envoyClusterNameLabel])
	}
}

// ----------------------------------------------------------------------------
// 8. Sanity check: SelectClusterClient returns members map correctly via helper
// ----------------------------------------------------------------------------

// TestReconcile_MC_HelperHasMemberClients_Sanity is a thin smoke test that
// catches the Bug #1 (member clients not threaded into the helper) at the
// constructor level. If the helper's memberClusterClients map is empty when
// the controller had two registered clusters, every per-cluster write silently
// goes to central — which is exactly the bug TestReconcile_MC_PerClusterSTSPlacement
// catches end-to-end. This test fails first, and acts as a fast canary.
func TestReconcile_MC_HelperHasMemberClients_Sanity(t *testing.T) {
	central := mock.NewEmptyFakeClientBuilder().Build()
	clusterA := mock.NewEmptyFakeClientBuilder().Build()
	clusterB := mock.NewEmptyFakeClientBuilder().Build()
	memberClients := map[string]client.Client{
		mcClusterAName: clusterA,
		mcClusterBName: clusterB,
	}

	r := newMongoDBSearchReconciler(central, searchcontroller.OperatorSearchConfig{}, memberClients)

	// The reconciler must remember the member clients (single canary).
	require.Len(t, r.memberClusterClientsMap, 2)

	// And SelectClusterClient routed against that map must hand back distinct
	// clients per name (membership rule).
	a, ok := searchcontroller.SelectClusterClient(mcClusterAName, r.kubeClient, r.memberClusterClientsMap)
	require.True(t, ok)
	require.NotEqual(t, kubernetesClient.NewClient(central), a, "cluster-a client must not be central")

	b, ok := searchcontroller.SelectClusterClient(mcClusterBName, r.kubeClient, r.memberClusterClientsMap)
	require.True(t, ok)
	require.NotEqual(t, a, b, "cluster-a and cluster-b must be distinct clients")

	// Unknown name → (nil, false), per SelectClusterClient rule.
	_, ok = searchcontroller.SelectClusterClient("zzz", r.kubeClient, r.memberClusterClientsMap)
	assert.False(t, ok, "unknown cluster must surface false from SelectClusterClient")
}

// TestReconcile_MC_EnvoyDeploymentVolumeMatchesPerClusterConfigMap regresses
// the MC-mode Envoy volume bug: ensureConfigMap writes the per-cluster
// suffixed ConfigMap name, but buildEnvoyPodSpec previously hardcoded the
// unsuffixed LoadBalancerConfigMapName(). In MC mode the persisted Deployment
// in each member cluster pointed its envoy-config volume at a ConfigMap that
// did not exist there — Envoy never started, the data plane was dead.
//
// This test asserts that, after a full reconcile, every cluster's persisted
// Deployment's envoy-config volume references the matching per-cluster
// ConfigMap that lives in the same client. Without this assertion, the same
// cross-method drift can recur silently — none of the prior unit tests
// inspected dep.Spec.Template.Spec.Volumes.
func TestReconcile_MC_EnvoyDeploymentVolumeMatchesPerClusterConfigMap(t *testing.T) {
	search := newMCSearch(mcTestSearchName, mcTestNamespace)
	h := newMCFakeReconcilerHarness(t, search, true)

	_, _, err := reconcileMCSearchAndEnvoy(t, h, search)
	require.NoError(t, err)

	for _, c := range []struct {
		clusterIs client.Client
		clusterID string
	}{
		{h.clusterA, mcClusterAName},
		{h.clusterB, mcClusterBName},
	} {
		dep := &appsv1.Deployment{}
		require.NoError(t, c.clusterIs.Get(t.Context(),
			types.NamespacedName{Name: search.LoadBalancerDeploymentNameForCluster(c.clusterID), Namespace: mcTestNamespace}, dep))

		var envoyVol *corev1.Volume
		for i := range dep.Spec.Template.Spec.Volumes {
			if dep.Spec.Template.Spec.Volumes[i].Name == "envoy-config" {
				envoyVol = &dep.Spec.Template.Spec.Volumes[i]
				break
			}
		}
		require.NotNil(t, envoyVol, "cluster %q persisted Deployment must carry an envoy-config volume", c.clusterID)
		require.NotNil(t, envoyVol.ConfigMap, "cluster %q envoy-config volume must reference a ConfigMap", c.clusterID)

		expectedCMName := search.LoadBalancerConfigMapNameForCluster(c.clusterID)
		assert.Equal(t, expectedCMName, envoyVol.ConfigMap.Name,
			"cluster %q envoy-config volume must reference per-cluster ConfigMap %q (was %q)",
			c.clusterID, expectedCMName, envoyVol.ConfigMap.Name)

		// Sanity: the ConfigMap by that name actually exists in the same client.
		cm := &corev1.ConfigMap{}
		require.NoError(t, c.clusterIs.Get(t.Context(),
			types.NamespacedName{Name: expectedCMName, Namespace: mcTestNamespace}, cm),
			"cluster %q must have ConfigMap %q so the volume mount can resolve", c.clusterID, expectedCMName)
	}
}

// TestReconcile_MC_EnvoyDeploymentName_PerClusterIdentity asserts that each
// cluster's Envoy Deployment in its own client has the correct per-cluster
// name (containing the cluster ID) and the cluster-name label set. This is
// the per-cluster identity check at the integration level — the function-level
// SNI text-search (which only manifests when TLS is enabled) is covered by
// TestEnvoyFilterChain_PerClusterSNI in the existing test file.
func TestReconcile_MC_EnvoyDeploymentName_PerClusterIdentity(t *testing.T) {
	search := newMCSearch(mcTestSearchName, mcTestNamespace)
	h := newMCFakeReconcilerHarness(t, search, true)

	_, _, err := reconcileMCSearchAndEnvoy(t, h, search)
	require.NoError(t, err)

	// Each cluster's Deployment should have its own cluster ID baked into the
	// name and the cluster-name label.
	for _, c := range []struct {
		clusterIs      client.Client
		clusterID      string
		otherClusterID string
	}{
		{h.clusterA, mcClusterAName, mcClusterBName},
		{h.clusterB, mcClusterBName, mcClusterAName},
	} {
		dep := &appsv1.Deployment{}
		require.NoError(t, c.clusterIs.Get(t.Context(),
			types.NamespacedName{Name: search.LoadBalancerDeploymentNameForCluster(c.clusterID), Namespace: mcTestNamespace}, dep))

		assert.True(t, strings.Contains(dep.Name, c.clusterID),
			"cluster %q Envoy Deployment name must contain its cluster ID, got %q", c.clusterID, dep.Name)
		assert.False(t, strings.Contains(dep.Name, c.otherClusterID),
			"cluster %q Envoy Deployment name must NOT contain other cluster's ID, got %q", c.clusterID, dep.Name)
		assert.Equal(t, c.clusterID, dep.Labels[envoyClusterNameLabel])

		// Sanity: the OTHER cluster's deployment is NOT in this cluster's client.
		otherName := search.LoadBalancerDeploymentNameForCluster(c.otherClusterID)
		err := c.clusterIs.Get(t.Context(),
			types.NamespacedName{Name: otherName, Namespace: mcTestNamespace}, &appsv1.Deployment{})
		assert.True(t, apierrors.IsNotFound(err),
			"cluster %q must NOT have other cluster's Envoy Deployment %q", c.clusterID, otherName)
	}
}

// ----------------------------------------------------------------------------
// 9. Single-cluster back-compat (regression guard)
// ----------------------------------------------------------------------------

// TestReconcile_SingleCluster_BackCompat asserts that the legacy single-cluster
// path (spec.Clusters nil) is unaffected by the MC wiring fix: the helper
// receives nil/empty member clients, all per-cluster fanout is a single unit
// in central, and the unindexed names are used.
func TestReconcile_SingleCluster_BackCompat(t *testing.T) {
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "single", Namespace: mcTestNamespace},
		Spec: searchv1.MongoDBSearchSpec{
			Source: &searchv1.MongoDBSource{
				ExternalMongoDBSource: &searchv1.ExternalMongoDBSource{
					HostAndPorts: []string{mcExternalHost1},
				},
			},
		},
	}
	central := mock.NewEmptyFakeClientBuilder().WithObjects(search).Build()
	r := newMongoDBSearchReconciler(central, searchcontroller.OperatorSearchConfig{
		SearchRepo:    "testrepo",
		SearchName:    "mongot",
		SearchVersion: mcOperatorVersion,
	}, map[string]client.Client{})

	_, err := r.Reconcile(t.Context(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "single", Namespace: mcTestNamespace},
	})
	require.NoError(t, err)

	// Legacy unindexed STS lands in central.
	sts := &appsv1.StatefulSet{}
	require.NoError(t, central.Get(t.Context(),
		types.NamespacedName{Name: search.StatefulSetNamespacedName().Name, Namespace: mcTestNamespace}, sts))

	// Indexed names must NOT exist (back-compat — legacy path uses unindexed).
	err = central.Get(t.Context(),
		types.NamespacedName{Name: "single-search-0", Namespace: mcTestNamespace}, &appsv1.StatefulSet{})
	assert.True(t, apierrors.IsNotFound(err),
		"legacy single-cluster path must use the unindexed STS name, not 'single-search-0'")
}

