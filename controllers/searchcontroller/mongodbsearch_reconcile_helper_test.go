package searchcontroller

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/ghodss/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	searchv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/search"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	userv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/user"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	mdbcv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1" //nolint:depguard
	khandler "github.com/mongodb/mongodb-kubernetes/pkg/handler"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/mongot"
	"github.com/mongodb/mongodb-kubernetes/pkg/statefulset"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/maputil"
)

func init() {
	logger, _ := zap.NewDevelopment()
	zap.ReplaceGlobals(logger)
}

func newTestMongoDBSearch(name, namespace string, modifications ...func(*searchv1.MongoDBSearch)) *searchv1.MongoDBSearch {
	mdbSearch := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: searchv1.MongoDBSearchSpec{
			Clusters: []searchv1.ClusterSpec{{}},
			Source: &searchv1.MongoDBSource{
				MongoDBResourceRef: &userv1.MongoDBResourceRef{
					Name: "test-mongodb",
				},
			},
			// immitate apiserver defaulting for .observability and .observability.prometheus
			Observability: searchv1.ObservabilityConfig{
				Prometheus: searchv1.Prometheus{
					Port: int(searchv1.MongotDefaultPrometheusPort),
				},
			},
		},
	}

	for _, modify := range modifications {
		modify(mdbSearch)
	}

	return mdbSearch
}

func newTestMongoDBCommunity(name, namespace string, modifications ...func(*mdbcv1.MongoDBCommunity)) *mdbcv1.MongoDBCommunity {
	mdbc := &mdbcv1.MongoDBCommunity{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: mdbcv1.MongoDBCommunitySpec{
			Version: "8.2.0",
			Members: 3,
		},
	}

	for _, modify := range modifications {
		modify(mdbc)
	}

	return mdbc
}

func newTestOperatorSearchConfig() OperatorSearchConfig {
	config := OperatorSearchConfig{
		SearchRepo:    "test-repo",
		SearchName:    "mongot",
		SearchVersion: minSupportedSearchVersion,
	}

	return config
}

func newTestFakeClient(objects ...client.Object) kubernetesClient.Client {
	clientBuilder := mock.NewEmptyFakeClientBuilder()

	clientBuilder.WithObjects(objects...)
	return kubernetesClient.NewClient(clientBuilder.Build())
}

func reconcileMongoDBSearch(ctx context.Context, fakeClient kubernetesClient.Client, mdbSearch *searchv1.MongoDBSearch, mdbc *mdbcv1.MongoDBCommunity, operatorConfig OperatorSearchConfig) workflow.Status {
	helper := NewMongoDBSearchReconcileHelper(
		fakeClient,
		mdbSearch,
		NewCommunityResourceSearchSource(mdbc),
		operatorConfig,
		nil, nil,
	)

	return helper.Reconcile(ctx, zap.S())
}

func TestMongoDBSearchReconcileHelper_ValidateSingleMongoDBSearchForSearchSource(t *testing.T) {
	mdbSearchSpec := searchv1.MongoDBSearchSpec{
		Source: &searchv1.MongoDBSource{
			MongoDBResourceRef: &userv1.MongoDBResourceRef{
				Name: "test-mongodb",
			},
		},
	}

	mdbSearch := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-mongodb-search",
			Namespace: "test",
		},
		Spec: mdbSearchSpec,
	}

	mdbc := &mdbcv1.MongoDBCommunity{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-mongodb",
			Namespace: "test",
		},
	}

	cases := []struct {
		name          string
		objects       []*searchv1.MongoDBSearch
		expectedError string
	}{
		{
			name: "No MongoDBSearch",
		},
		{
			name:    "Single MongoDBSearch",
			objects: []*searchv1.MongoDBSearch{mdbSearch},
		},
		{
			name: "Multiple MongoDBSearch",
			objects: []*searchv1.MongoDBSearch{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-mongodb-search-1",
						Namespace: "test",
					},
					Spec: mdbSearchSpec,
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-mongodb-search-2",
						Namespace: "test",
					},
					Spec: mdbSearchSpec,
				},
			},
			expectedError: "Found multiple MongoDBSearch resources for search source 'test-mongodb': test-mongodb-search-1, test-mongodb-search-2",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clientBuilder := mock.NewEmptyFakeClientBuilder()

			for _, v := range c.objects {
				// TODO: why doesn't clientBuilder.WithObjects(c.objects...) work?
				clientBuilder.WithObjects(v)
			}

			helper := NewMongoDBSearchReconcileHelper(kubernetesClient.NewClient(clientBuilder.Build()), mdbSearch, NewCommunityResourceSearchSource(mdbc), OperatorSearchConfig{}, nil, nil)
			err := helper.ValidateSingleMongoDBSearchForSearchSource(t.Context())
			if c.expectedError == "" {
				assert.NoError(t, err)
			} else {
				assert.EqualError(t, err, c.expectedError)
			}
		})
	}
}

func TestGetMongodConfigParameters_TransportAndPorts(t *testing.T) {
	cases := []struct {
		name            string
		withWireproto   bool
		expectedUseGrpc bool
	}{
		{
			name:            "grpc only (default)",
			withWireproto:   false,
			expectedUseGrpc: true,
		},
		{
			name:            "grpc + wireproto via annotation",
			withWireproto:   true,
			expectedUseGrpc: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			search := &searchv1.MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-mongodb-search",
					Namespace: "test",
				},
			}
			if tc.withWireproto {
				search.Annotations = map[string]string{searchv1.ForceWireprotoAnnotation: "true"}
			}

			clusterDomain := "cluster.local"
			params := GetMongodConfigParameters(search, clusterDomain, 0)

			setParams := params["setParameter"].(map[string]any)

			useGrpc := setParams["useGrpcForSearch"].(bool)
			assert.Equal(t, tc.expectedUseGrpc, useGrpc)

			expectedPort := search.GetMongotGrpcPort()
			if tc.withWireproto {
				expectedPort = search.GetMongotWireprotoPort()
			}
			// No LB: headless pod-0 FQDN = <sts>-0.<svc>.<ns>.svc.<domain>
			expectedPrefix := fmt.Sprintf("%s-0.%s.%s.svc.%s", search.Name+"-search-0", search.Name+"-search-0-svc", search.Namespace, clusterDomain)
			expectedSuffix := fmt.Sprintf(":%d", expectedPort)

			for _, key := range []string{"mongotHost", "searchIndexManagementHostAndPort"} {
				value := setParams[key].(string)
				if !strings.HasPrefix(value, expectedPrefix) || !strings.HasSuffix(value, expectedSuffix) {
					t.Fatalf("%s mismatch: expected prefix %q and suffix %q, got %q", key, expectedPrefix, expectedSuffix, value)
				}
			}
		})
	}
}

func TestGetMongodConfigParameters_ManagedLB(t *testing.T) {
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-mongodb-search",
			Namespace: "test",
		},
		Spec: searchv1.MongoDBSearchSpec{
			Clusters: []searchv1.ClusterSpec{{
				LoadBalancer: &searchv1.LoadBalancerConfig{
					Managed: &searchv1.ManagedLBConfig{},
				},
			}},
		},
	}

	clusterDomain := "cluster.local"
	params := GetMongodConfigParameters(search, clusterDomain, 0)

	setParams := params["setParameter"].(map[string]any)

	expectedEndpoint := "test-mongodb-search-search-0-proxy-svc.test.svc.cluster.local:27028"
	assert.Equal(t, expectedEndpoint, setParams["mongotHost"])
	assert.Equal(t, expectedEndpoint, setParams["searchIndexManagementHostAndPort"])
	assert.Equal(t, true, setParams["useGrpcForSearch"])
}

func TestGetMongodConfigParameters_NoLB(t *testing.T) {
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-mongodb-search",
			Namespace: "test",
		},
	}

	clusterDomain := "cluster.local"
	params := GetMongodConfigParameters(search, clusterDomain, 0)

	setParams := params["setParameter"].(map[string]any)

	// Without LB, should point to the first pod's headless FQDN
	expectedEndpoint := "test-mongodb-search-search-0-0.test-mongodb-search-search-0-svc.test.svc.cluster.local:27028"
	assert.Equal(t, expectedEndpoint, setParams["mongotHost"])
	assert.Equal(t, expectedEndpoint, setParams["searchIndexManagementHostAndPort"])
}

// ResolveSingleClusterIndex returns the 1-entry spec's pinned ClusterIndex (else 0)
// so the AC mongotHost wiring and secret probing compute the names the per-cluster
// writers created.
func TestResolveSingleClusterIndex(t *testing.T) {
	tests := []struct {
		name     string
		clusters []searchv1.ClusterSpec
		want     int
	}{
		{name: "no clusters", want: 0},
		{name: "single unpinned entry", clusters: []searchv1.ClusterSpec{{}}, want: 0},
		{
			name:     "single pinned entry",
			clusters: []searchv1.ClusterSpec{{Name: "cluster-a", Index: ptr.To(int32(7))}},
			want:     7,
		},
		{
			name: "multi-entry spec resolves to 0",
			clusters: []searchv1.ClusterSpec{
				{Name: "a", Index: ptr.To(int32(1))},
				{Name: "b", Index: ptr.To(int32(2))},
			},
			want: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			search := &searchv1.MongoDBSearch{Spec: searchv1.MongoDBSearchSpec{Clusters: tt.clusters}}
			assert.Equal(t, tt.want, ResolveSingleClusterIndex(search))
		})
	}
}

// Reader-writer consistency: a 1-entry CR pinned to index 7 has its resources
// created at index 7 by the per-cluster writers, so the internal-source AC
// wiring must emit index-7 names, not index-0 ones.
func TestGetMongodConfigParameters_PinnedClusterIndex(t *testing.T) {
	newPinnedSearch := func(lb *searchv1.LoadBalancerConfig) *searchv1.MongoDBSearch {
		return &searchv1.MongoDBSearch{
			ObjectMeta: metav1.ObjectMeta{Name: "test-mongodb-search", Namespace: "test"},
			Spec: searchv1.MongoDBSearchSpec{
				Clusters: []searchv1.ClusterSpec{{Name: "cluster-a", Index: ptr.To(int32(7)), LoadBalancer: lb}},
			},
		}
	}

	t.Run("managed LB targets the index-7 proxy Service", func(t *testing.T) {
		search := newPinnedSearch(&searchv1.LoadBalancerConfig{Managed: &searchv1.ManagedLBConfig{}})
		params := GetMongodConfigParameters(search, "cluster.local", ResolveSingleClusterIndex(search))
		setParams := params["setParameter"].(map[string]any)

		proxyName := search.ProxyServiceNamespacedNameForCluster(7)
		expected := fmt.Sprintf("%s.%s.svc.cluster.local:27028", proxyName.Name, proxyName.Namespace)
		assert.Equal(t, expected, setParams["mongotHost"])
		assert.Equal(t, expected, setParams["searchIndexManagementHostAndPort"])
	})

	t.Run("no LB targets the index-7 StatefulSet pod-0 headless FQDN", func(t *testing.T) {
		search := newPinnedSearch(nil)
		params := GetMongodConfigParameters(search, "cluster.local", ResolveSingleClusterIndex(search))
		setParams := params["setParameter"].(map[string]any)

		stsName := search.StatefulSetNamespacedNameForCluster(7)
		svcName := search.SearchServiceNamespacedNameForCluster(7)
		expected := fmt.Sprintf("%s-0.%s.%s.svc.cluster.local:27028", stsName.Name, svcName.Name, svcName.Namespace)
		assert.Equal(t, expected, setParams["mongotHost"])
		assert.Equal(t, expected, setParams["searchIndexManagementHostAndPort"])
	})

	t.Run("per-shard endpoint targets the index-7 shard resources", func(t *testing.T) {
		search := newPinnedSearch(nil)
		config := GetMongodConfigParametersForShard(search, "sh-0", "cluster.local", ResolveSingleClusterIndex(search))
		setParams := config["setParameter"].(map[string]any)

		stsName := search.MongotStatefulSetForClusterShard(7, "sh-0")
		svcName := search.MongotServiceForClusterShard(7, "sh-0")
		expected := fmt.Sprintf("%s-0.%s.%s.svc.cluster.local:27028", stsName.Name, svcName.Name, svcName.Namespace)
		assert.Equal(t, expected, setParams["mongotHost"])
	})
}

// newTestRSUnit builds a reconcileUnit for a ReplicaSet topology.
func newTestRSUnit(search *searchv1.MongoDBSearch) reconcileUnit {
	svcName := search.SearchServiceNamespacedName().Name
	return reconcileUnit{
		stsName:       search.StatefulSetNamespacedName(),
		headlessSvc:   search.SearchServiceNamespacedName(),
		proxySvc:      search.ProxyServiceNamespacedName(),
		configMapName: search.MongotConfigConfigMapNamespacedName(),
		podLabels:     map[string]string{appLabelKey: svcName},
	}
}

// newTestShardUnit builds a reconcileUnit for a specific shard.
func newTestShardUnit(search *searchv1.MongoDBSearch, shardName string) reconcileUnit {
	stsName := search.MongotStatefulSetForClusterShard(0, shardName)
	return reconcileUnit{
		stsName:             stsName,
		headlessSvc:         search.MongotServiceForClusterShard(0, shardName),
		proxySvc:            search.ProxyServiceNameForClusterShard(0, shardName),
		configMapName:       search.MongotConfigMapForClusterShard(0, shardName),
		podLabels:           map[string]string{appLabelKey: stsName.Name, shardLabelKey: shardName},
		additionalSvcLabels: map[string]string{shardLabelKey: shardName},
	}
}

func TestBuildProxyService_NoLB(t *testing.T) {
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "ns"},
	}
	unit := newTestRSUnit(search)
	svc := buildProxyService(search, unit)

	assert.Equal(t, "test-search-0-proxy-svc", svc.Name)
	assert.Empty(t, svc.OwnerReferences)
	assert.Equal(t, map[string]string{"app": "test-search-svc"}, svc.Spec.Selector)
	assert.Equal(t, int32(27028), svc.Spec.Ports[0].Port)
	assert.Equal(t, int32(27028), svc.Spec.Ports[0].TargetPort.IntVal)
}

func TestBuildProxyService_ManagedLB_NotReady(t *testing.T) {
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "ns"},
		Spec: searchv1.MongoDBSearchSpec{
			Clusters: []searchv1.ClusterSpec{{LoadBalancer: &searchv1.LoadBalancerConfig{Managed: &searchv1.ManagedLBConfig{}}}},
		},
		// No status.loadBalancer → IsLoadBalancerReady() = false
	}
	unit := newTestRSUnit(search)
	svc := buildProxyService(search, unit)

	// Selector stays on mongot pods while Envoy is not ready
	assert.Equal(t, map[string]string{"app": "test-search-svc"}, svc.Spec.Selector)
	assert.Equal(t, int32(27028), svc.Spec.Ports[0].TargetPort.IntVal)
}

func TestBuildProxyService_ManagedLB_Ready(t *testing.T) {
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "ns"},
		Spec: searchv1.MongoDBSearchSpec{
			Clusters: []searchv1.ClusterSpec{{LoadBalancer: &searchv1.LoadBalancerConfig{Managed: &searchv1.ManagedLBConfig{}}}},
		},
		Status: searchv1.MongoDBSearchStatus{
			LoadBalancer: &searchv1.LoadBalancerStatus{Phase: status.PhaseRunning},
		},
	}
	unit := newTestRSUnit(search)
	svc := buildProxyService(search, unit)

	// Selector flips to Envoy pods when LB is ready; must match the index-0 Deployment label
	// so that traffic is not black-holed after the Commit-2 app-label rename.
	assert.Equal(t, search.LoadBalancerDeploymentNameForCluster(0), svc.Spec.Selector["app"])
	assert.Equal(t, int32(27028), svc.Spec.Ports[0].TargetPort.IntVal)
}

func TestBuildProxyServiceForShard_ManagedLB_NotReady(t *testing.T) {
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "ns"},
		Spec: searchv1.MongoDBSearchSpec{
			Clusters: []searchv1.ClusterSpec{{LoadBalancer: &searchv1.LoadBalancerConfig{Managed: &searchv1.ManagedLBConfig{}}}},
		},
	}
	unit := newTestShardUnit(search, "shard-0")
	svc := buildProxyService(search, unit)

	stsName := search.MongotStatefulSetForClusterShard(0, "shard-0").Name
	assert.Equal(t, map[string]string{"app": stsName}, svc.Spec.Selector)
}

func TestBuildProxyServiceForShard_ManagedLB_Ready(t *testing.T) {
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "ns"},
		Spec: searchv1.MongoDBSearchSpec{
			Clusters: []searchv1.ClusterSpec{{LoadBalancer: &searchv1.LoadBalancerConfig{Managed: &searchv1.ManagedLBConfig{}}}},
		},
		Status: searchv1.MongoDBSearchStatus{
			LoadBalancer: &searchv1.LoadBalancerStatus{Phase: status.PhaseRunning},
		},
	}
	unit := newTestShardUnit(search, "shard-0")
	svc := buildProxyService(search, unit)

	assert.Equal(t, search.LoadBalancerDeploymentNameForCluster(0), svc.Spec.Selector["app"])
}

// Per-cluster proxy Service selector must match the per-cluster Envoy
// Deployment label so endpoints are non-empty in MC installs.
func TestBuildProxyService_ManagedLB_Ready_PerCluster(t *testing.T) {
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "ns"},
		Spec: searchv1.MongoDBSearchSpec{
			Clusters: []searchv1.ClusterSpec{{LoadBalancer: &searchv1.LoadBalancerConfig{Managed: &searchv1.ManagedLBConfig{}}}},
		},
		Status: searchv1.MongoDBSearchStatus{
			LoadBalancer: &searchv1.LoadBalancerStatus{Phase: status.PhaseRunning},
		},
	}
	unit := newTestRSUnit(search)
	unit.clusterName = "kind-e2e-cluster-1"
	unit.clusterIndex = 0

	svc := buildProxyService(search, unit)

	expected := search.LoadBalancerDeploymentNameForCluster(0)
	assert.Equal(t, map[string]string{"app": expected}, svc.Spec.Selector)
	assert.Equal(t, "test-search-lb-0", expected,
		"naming convention must match the per-cluster Envoy Deployment name")
}

// Back-compat: empty unit.clusterName must still produce a working selector.
func TestBuildProxyService_ManagedLB_Ready_SingleCluster(t *testing.T) {
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "ns"},
		Spec: searchv1.MongoDBSearchSpec{
			Clusters: []searchv1.ClusterSpec{{LoadBalancer: &searchv1.LoadBalancerConfig{Managed: &searchv1.ManagedLBConfig{}}}},
		},
		Status: searchv1.MongoDBSearchStatus{
			LoadBalancer: &searchv1.LoadBalancerStatus{Phase: status.PhaseRunning},
		},
	}
	unit := newTestRSUnit(search)

	svc := buildProxyService(search, unit)

	assert.Equal(t, map[string]string{"app": "test-search-lb-0"}, svc.Spec.Selector)
}

func assertServiceBasicProperties(t *testing.T, svc corev1.Service, mdbSearch *searchv1.MongoDBSearch) {
	t.Helper()
	svcName := mdbSearch.SearchServiceNamespacedNameForCluster(0)

	assert.Equal(t, svcName.Name, svc.Name)
	assert.Equal(t, svcName.Namespace, svc.Namespace)
	assert.Equal(t, "ClusterIP", string(svc.Spec.Type))
	assert.Equal(t, "None", svc.Spec.ClusterIP)
	assert.False(t, svc.Spec.PublishNotReadyAddresses)

	expectedAppLabel := svcName.Name
	assert.Equal(t, expectedAppLabel, svc.Labels["app"])
	assert.Equal(t, expectedAppLabel, svc.Spec.Selector["app"])
}

func assertServicePorts(t *testing.T, svc corev1.Service, expectedPorts map[string]int32) {
	t.Helper()

	portMap := make(map[string]int32)
	for _, port := range svc.Spec.Ports {
		portMap[port.Name] = port.Port
	}

	assert.Len(t, svc.Spec.Ports, len(expectedPorts), "Expected %d ports but got %d", len(expectedPorts), len(svc.Spec.Ports))

	for portName, expectedPort := range expectedPorts {
		actualPort, exists := portMap[portName]
		assert.True(t, exists, "Expected port %s to exist", portName)
		assert.Equal(t, expectedPort, actualPort, "Port %s has wrong value", portName)
	}
}

func TestMongoDBSearchReconcileHelper_ServiceCreation(t *testing.T) {
	cases := []struct {
		name          string
		modifySearch  func(*searchv1.MongoDBSearch)
		expectedPorts map[string]int32
	}{
		{
			name:         "Prometheus implicitly enabled with default port",
			modifySearch: func(search *searchv1.MongoDBSearch) {},
			expectedPorts: map[string]int32{
				"mongot-grpc": searchv1.MongotDefaultGrpcPort,
				"prometheus":  searchv1.MongotDefaultPrometheusPort,
				"healthcheck": searchv1.MongotDefautHealthCheckPort,
			},
		},
		{
			name: "Prometheus enabled with custom port",
			modifySearch: func(search *searchv1.MongoDBSearch) {
				search.Spec.Observability.Prometheus = searchv1.Prometheus{
					Mode: searchv1.PrometheusModeEnabled,
					Port: 9999,
				}
			},
			expectedPorts: map[string]int32{
				"mongot-grpc": searchv1.MongotDefaultGrpcPort,
				"prometheus":  9999,
				"healthcheck": searchv1.MongotDefautHealthCheckPort,
			},
		},
		{
			name: "Prometheus disabled",
			modifySearch: func(search *searchv1.MongoDBSearch) {
				search.Spec.Observability.Prometheus = searchv1.Prometheus{
					Mode: searchv1.PrometheusModeDisabled,
				}
			},
			expectedPorts: map[string]int32{
				"mongot-grpc": searchv1.MongotDefaultGrpcPort,
				"healthcheck": searchv1.MongotDefautHealthCheckPort,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mdbSearch := newTestMongoDBSearch("test-mongodb-search", "test", tc.modifySearch)
			mdbc := newTestMongoDBCommunity("test-mongodb", "test")
			fakeClient := newTestFakeClient(mdbSearch, mdbc)

			reconcileMongoDBSearch(t.Context(), fakeClient, mdbSearch, mdbc, newTestOperatorSearchConfig())

			svcName := mdbSearch.SearchServiceNamespacedNameForCluster(0)
			svc, err := fakeClient.GetService(t.Context(), svcName)
			require.NoError(t, err)
			require.NotNil(t, svc)

			assertServiceBasicProperties(t, svc, mdbSearch)
			assertServicePorts(t, svc, tc.expectedPorts)
		})
	}
}

func TestStatefulSetOverridePreservesProtectedSearchLabels(t *testing.T) {
	tests := []struct {
		name        string
		clusterName string
		wantCluster string
	}{
		{name: "member cluster restores cluster label", clusterName: "member-a", wantCluster: "member-a"},
		{name: "legacy single cluster removes injected cluster label", clusterName: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			search := newTestMongoDBSearch("test-search", "test-ns", func(search *searchv1.MongoDBSearch) {
				search.UID = "search-uid"
				search.Spec.Clusters = []searchv1.ClusterSpec{{
					Name: tc.clusterName,
					StatefulSetConfiguration: &v1.StatefulSetConfiguration{
						MetadataWrapper: v1.StatefulSetMetadataWrapper{
							Labels: map[string]string{
								"custom-label":                            "custom-value",
								khandler.MongoDBSearchOwnerNameLabel:      "wrong-name",
								khandler.MongoDBSearchOwnerNamespaceLabel: "wrong-namespace",
								khandler.MongoDBSearchOwnerUIDLabel:       "wrong-uid",
								khandler.MongoDBSearchClusterNameLabel:    "wrong-cluster",
								khandler.MongoDBSearchComponentLabel:      "wrong-component",
							},
						},
					},
				}}
			})
			sizing := search.EffectiveClusters()[0]
			sts := statefulset.New(
				CreateSearchStatefulSetFunc(search, sizing, "test-search-search-0", search.Namespace, "test-search-search-0-svc", "test-search-search-0-config", map[string]string{"app": "test-search-search-0"}, "mongot:latest", false),
				StatefulSetOverrideModification(sizing.StatefulSetConfiguration),
				withSearchOwnerLabels(search, tc.clusterName),
			)

			assert.Equal(t, "custom-value", sts.Labels["custom-label"])
			assert.Equal(t, search.Name, sts.Labels[khandler.MongoDBSearchOwnerNameLabel])
			assert.Equal(t, search.Namespace, sts.Labels[khandler.MongoDBSearchOwnerNamespaceLabel])
			assert.Equal(t, string(search.UID), sts.Labels[khandler.MongoDBSearchOwnerUIDLabel])
			if tc.wantCluster == "" {
				assert.NotContains(t, sts.Labels, khandler.MongoDBSearchClusterNameLabel)
			} else {
				assert.Equal(t, tc.wantCluster, sts.Labels[khandler.MongoDBSearchClusterNameLabel])
			}
			assert.Equal(t, mongotComponent, sts.Labels[khandler.MongoDBSearchComponentLabel])
		})
	}
}

var (
	testApiKeySecretName = "api-key-secret"
	embeddingWriterTrue  = true
	mode                 = int32(400)
	expectedVolumes      = []corev1.Volume{
		{
			Name: embeddingKeyVolumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName:  testApiKeySecretName,
					DefaultMode: &mode,
				},
			},
		},
		{
			Name: apiKeysTempVolumeName,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
	}
)

var expectedVolumeMount = []corev1.VolumeMount{
	{
		Name:      apiKeysTempVolumeName,
		MountPath: embeddingKeyFilePath,
		ReadOnly:  false,
	},
	{
		Name:      embeddingKeyVolumeName,
		MountPath: apiKeysTempVolumeMount,
		ReadOnly:  true,
	},
}

var apiKeySecret = &corev1.Secret{
	ObjectMeta: metav1.ObjectMeta{
		Name:      "api-key-secret",
		Namespace: "mongodb",
	},
	Data: map[string][]byte{
		"indexing-key": []byte(""),
		"query-key":    []byte(""),
	},
}

func TestEnsureEmbeddingConfig_APIKeySecretAndProviderEndpont(t *testing.T) {
	providerEndpoint := "https://api.voyageai.com/v1/embeddings"

	search := newTestMongoDBSearch("mdb-searh", "mongodb", func(s *searchv1.MongoDBSearch) {
		s.Spec.AutoEmbedding = &searchv1.EmbeddingConfig{
			ProviderEndpoint: providerEndpoint,
			EmbeddingModelAPIKeySecret: corev1.LocalObjectReference{
				Name: testApiKeySecretName,
			},
		}
	})

	conf := &mongot.Config{}
	sts := &appsv1.StatefulSet{
		Spec: appsv1.StatefulSetSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Args:         []string{"echo", "test"},
							Name:         MongotContainerName,
							Image:        "searchimage:tag",
							VolumeMounts: []corev1.VolumeMount{},
						},
					},
					Volumes: []corev1.Volume{},
				},
			},
		},
	}

	embeddingWriterTrue := true
	expectedMongotConfig := mongot.Config{
		Embedding: &mongot.EmbeddingConfig{
			ProviderEndpoint:          providerEndpoint,
			IsAutoEmbeddingViewWriter: &embeddingWriterTrue,
			QueryKeyFile:              fmt.Sprintf("%s/%s", embeddingKeyFilePath, queryKeyName),
			IndexingKeyFile:           fmt.Sprintf("%s/%s", embeddingKeyFilePath, indexingKeyName),
		},
	}

	ctx := context.TODO()
	fakeClient := newTestFakeClient(search, apiKeySecret)
	helper := NewMongoDBSearchReconcileHelper(fakeClient, search, nil, OperatorSearchConfig{
		SearchVersion: "0.60.0",
	}, nil, nil)
	mongotModif, stsModif, err := helper.ensureEmbeddingConfig(ctx, nil)
	assert.Nil(t, err)

	mongotModif(conf)
	stsModif(sts)

	assert.Equal(t, expectedVolumeMount, sts.Spec.Template.Spec.Containers[0].VolumeMounts)
	assert.Equal(t, expectedVolumes, sts.Spec.Template.Spec.Volumes)
	assert.Equal(t, expectedMongotConfig.Embedding, conf.Embedding)
}

func voyageAIService(name, namespace string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":       "voyageai",
				"app.kubernetes.io/managed-by": "mongodb-kubernetes-operator",
			},
		},
	}
}

func TestEnsureEmbeddingConfig_InternalVoyageAI_NoSecretRequired(t *testing.T) {
	ctx := context.TODO()
	endpoint := "http://voyage-embedding-svc.mongodb.svc.cluster.local:8080/embeddings"
	search := newTestMongoDBSearch("mdb-search", "mongodb", func(s *searchv1.MongoDBSearch) {
		s.Spec.AutoEmbedding = &searchv1.EmbeddingConfig{ProviderEndpoint: endpoint} // no API key secret
	})
	// The Service backing the endpoint is an operator-managed VoyageAI service.
	fakeClient := newTestFakeClient(search, voyageAIService("voyage-embedding-svc", "mongodb"))
	helper := NewMongoDBSearchReconcileHelper(fakeClient, search, nil, OperatorSearchConfig{SearchVersion: "0.60.0"}, nil, nil)

	mongotModif, stsModif, err := helper.ensureEmbeddingConfig(ctx, nil)
	require.NoError(t, err)

	// mongot config: endpoint set, and key file paths set (mongot requires them);
	// the operator fabricates the files since the VoyageAI server ignores the keys.
	conf := &mongot.Config{}
	mongotModif(conf)
	require.NotNil(t, conf.Embedding)
	assert.Equal(t, endpoint, conf.Embedding.ProviderEndpoint)
	assert.Equal(t, fmt.Sprintf("%s/%s", embeddingKeyFilePath, indexingKeyName), conf.Embedding.IndexingKeyFile)
	assert.Equal(t, fmt.Sprintf("%s/%s", embeddingKeyFilePath, queryKeyName), conf.Embedding.QueryKeyFile)
	assert.True(t, *conf.Embedding.IsAutoEmbeddingViewWriter)

	// Only the emptyDir is mounted (no user secret volume), and the container writes
	// placeholder key files.
	sts := &appsv1.StatefulSet{Spec: appsv1.StatefulSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
		Containers: []corev1.Container{{Name: MongotContainerName, Args: []string{"-c", "exec mongot"}}}, Volumes: []corev1.Volume{},
	}}}}
	stsModif(sts)
	volNames := map[string]bool{}
	for _, v := range sts.Spec.Template.Spec.Volumes {
		volNames[v.Name] = true
	}
	assert.True(t, volNames[apiKeysTempVolumeName], "emptyDir for key files should be mounted")
	assert.False(t, volNames[embeddingKeyVolumeName], "no user secret volume should be mounted")
	assert.Contains(t, sts.Spec.Template.Spec.Containers[0].Args[1], queryKeyName)
	assert.Contains(t, sts.Spec.Template.Spec.Containers[0].Args[1], "chmod 0400")
}

// Without an API key secret, the secret is required for any endpoint that is not
// an in-cluster VoyageAI service: external hosts, in-cluster non-VoyageAI Services,
// and an empty endpoint all error.
func TestEnsureEmbeddingConfig_SecretRequiredForNonInternal(t *testing.T) {
	for _, tc := range []struct {
		name     string
		endpoint string
		objects  []client.Object // extra objects, e.g. a non-VoyageAI Service at the endpoint host
	}{
		{
			name:     "external endpoint",
			endpoint: "https://api.voyageai.com/v1/embeddings",
		},
		{
			name:     "in-cluster non-VoyageAI service",
			endpoint: "http://some-svc.mongodb.svc.cluster.local:8080/embeddings",
			objects:  []client.Object{&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "some-svc", Namespace: "mongodb"}}},
		},
		{
			name:     "empty endpoint",
			endpoint: "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			search := newTestMongoDBSearch("mdb-search", "mongodb", func(s *searchv1.MongoDBSearch) {
				s.Spec.AutoEmbedding = &searchv1.EmbeddingConfig{ProviderEndpoint: tc.endpoint}
			})
			fakeClient := newTestFakeClient(append([]client.Object{search}, tc.objects...)...)
			helper := NewMongoDBSearchReconcileHelper(fakeClient, search, nil, OperatorSearchConfig{SearchVersion: "0.60.0"}, nil, nil)

			_, _, err := helper.ensureEmbeddingConfig(context.TODO(), nil)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "embeddingModelAPIKeySecret is required")
		})
	}
}

func TestEnsureEmbeddingConfig_WOAutoEmbedding(t *testing.T) {
	mongotCMWithoutEmbedding := `healthCheck:
  address: ""
logging:
  verbosity: ""
metrics:
  address: ""
  enabled: false
server: {}
storage:
  dataPath: ""
syncSource:
  replicaSet:
    hostAndPort: null`

	search := newTestMongoDBSearch("mdb-searh", "mongodb")
	fakeClient := newTestFakeClient(search)
	helper := NewMongoDBSearchReconcileHelper(fakeClient, search, nil, OperatorSearchConfig{
		SearchVersion: "0.58.0",
	}, nil, nil)
	ctx := context.TODO()
	mongotModif, stsModif, err := helper.ensureEmbeddingConfig(ctx, nil)
	assert.Nil(t, err)

	conf := &mongot.Config{}
	sts := &appsv1.StatefulSet{
		Spec: appsv1.StatefulSetSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Args:         []string{"echo", "test"},
							Name:         MongotContainerName,
							Image:        "searchimage:tag",
							VolumeMounts: []corev1.VolumeMount{},
						},
					},
					Volumes: []corev1.Volume{},
				},
			},
		},
	}

	mongotModif(conf)
	stsModif(sts)

	// verify that if the embedding config is not provided in the CR, the mongot config's YAML representation
	// doesn't have even zero values of embedding config.
	cm, err := yaml.Marshal(conf)
	assert.Nil(t, err)
	assert.YAMLEq(t, mongotCMWithoutEmbedding, string(cm))

	// because search CR didn't have autoEmbedding configured, there wont be any change in conf or sts
	assert.Equal(t, sts, sts)
	assert.Equal(t, conf, conf)
}

func TestEnsureEmbeddingConfig_JustAPIKeys(t *testing.T) {
	search := newTestMongoDBSearch("mdb-search", "mongodb", func(s *searchv1.MongoDBSearch) {
		s.Spec.AutoEmbedding = &searchv1.EmbeddingConfig{
			EmbeddingModelAPIKeySecret: corev1.LocalObjectReference{
				Name: testApiKeySecretName,
			},
		}
	})
	fakeClient := newTestFakeClient(search, apiKeySecret)
	helper := NewMongoDBSearchReconcileHelper(fakeClient, search, nil, OperatorSearchConfig{
		SearchVersion: "0.60.0",
	}, nil, nil)
	ctx := context.TODO()
	mongotModif, stsModif, err := helper.ensureEmbeddingConfig(ctx, nil)
	assert.Nil(t, err)

	conf := &mongot.Config{}
	sts := &appsv1.StatefulSet{
		Spec: appsv1.StatefulSetSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Args:         []string{"echo", "test"},
							Name:         MongotContainerName,
							Image:        "searchimage:tag",
							VolumeMounts: []corev1.VolumeMount{},
						},
					},
					Volumes: []corev1.Volume{},
				},
			},
		},
	}

	mongotModif(conf)
	stsModif(sts)

	// We are just providing the autoEmbedding API Key secret, that's why we will only see that in the config
	// and we will see the volumes, mounts in sts
	assert.Equal(t, &mongot.EmbeddingConfig{
		QueryKeyFile:              fmt.Sprintf("%s/%s", embeddingKeyFilePath, queryKeyName),
		IndexingKeyFile:           fmt.Sprintf("%s/%s", embeddingKeyFilePath, indexingKeyName),
		IsAutoEmbeddingViewWriter: &embeddingWriterTrue,
		ProviderEndpoint:          "",
	}, conf.Embedding)

	assert.Equal(t, expectedVolumeMount, sts.Spec.Template.Spec.Containers[0].VolumeMounts)
	assert.Equal(t, expectedVolumes, sts.Spec.Template.Spec.Volumes)
}

func TestValidateSearchResource(t *testing.T) {
	search := newTestMongoDBSearch("mdb-search", "mongodb", func(s *searchv1.MongoDBSearch) {
		s.Spec.AutoEmbedding = &searchv1.EmbeddingConfig{
			EmbeddingModelAPIKeySecret: corev1.LocalObjectReference{
				Name: testApiKeySecretName,
			},
		}
	})
	ctx := context.TODO()
	for _, tc := range []struct {
		apiKeySecret  *corev1.Secret
		errAssertion  assert.ErrorAssertionFunc
		errMsg        string
		searchVersion string
	}{
		{
			apiKeySecret: &corev1.Secret{},
			errAssertion: assert.Error,
			errMsg:       fmt.Sprintf("secrets \"%s\" not found", testApiKeySecretName),
		},
		{
			apiKeySecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name: testApiKeySecretName,
				},
			},
			errAssertion: assert.Error,
			errMsg:       fmt.Sprintf("secrets \"%s\" not found", testApiKeySecretName),
		},
		{
			apiKeySecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testApiKeySecretName,
					Namespace: "mongodb",
				},
			},
			errAssertion: assert.Error,
			errMsg:       fmt.Sprintf("required key \"%s\" is not present in the Secret mongodb/%s", indexingKeyName, testApiKeySecretName),
		},
		{
			apiKeySecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testApiKeySecretName,
					Namespace: "mongodb",
				},
				Data: map[string][]byte{
					"indexing-key": []byte(""),
				},
			},
			errAssertion: assert.Error,
			errMsg:       fmt.Sprintf("required key \"%s\" is not present in the Secret mongodb/%s", queryKeyName, testApiKeySecretName),
		},
		{
			apiKeySecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testApiKeySecretName,
					Namespace: "mongodb",
				},
				Data: map[string][]byte{
					"indexing-key": []byte(""),
					"query-key":    []byte(""),
				},
			},
			errAssertion: assert.NoError,
			errMsg:       "",
		},
	} {
		fakeClient := newTestFakeClient(search, tc.apiKeySecret)
		helper := NewMongoDBSearchReconcileHelper(fakeClient, search, nil, OperatorSearchConfig{
			SearchVersion: tc.searchVersion,
		}, nil, nil)
		_, _, err := helper.ensureEmbeddingConfig(ctx, nil)
		tc.errAssertion(t, err)
		if tc.errMsg != "" {
			assert.Equal(t, tc.errMsg, err.Error())
		}
	}
}

func TestValidateSearchImageVersion(t *testing.T) {
	search := newTestMongoDBSearch("mdb-search", "mongodb")
	helper := NewMongoDBSearchReconcileHelper(newTestFakeClient(search), search, nil, OperatorSearchConfig{}, nil, nil)

	for _, tc := range []struct {
		name         string
		version      string
		errAssertion assert.ErrorAssertionFunc
	}{
		{
			name:         "equal to minimum is allowed",
			version:      minSupportedSearchVersion,
			errAssertion: assert.NoError,
		},
		{
			name:         "newer than minimum is allowed",
			version:      "1.71.0",
			errAssertion: assert.NoError,
		},
		{
			name:         "older than minimum is rejected",
			version:      "1.64.0",
			errAssertion: assert.Error,
		},
		{
			name:         "much older is rejected",
			version:      "0.64.0",
			errAssertion: assert.Error,
		},
		{
			name:         "non-semver dev tag is allowed",
			version:      "72ae26a806",
			errAssertion: assert.NoError,
		},
		{
			name:         "empty version is rejected",
			version:      "",
			errAssertion: assert.Error,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := helper.ValidateSearchImageVersion(tc.version)
			tc.errAssertion(t, err)
			if err != nil {
				assert.Contains(t, err.Error(), minSupportedSearchVersion)
			}
		})
	}
}

func TestEnsureMongotConfig_PerPodModes(t *testing.T) {
	cases := []struct {
		name             string
		replicas         int32
		hasAutoEmbedding bool
		expectedKeys     []string
		notExpectedKeys  []string
	}{
		{
			name:             "single config mode - no embedding",
			replicas:         1,
			hasAutoEmbedding: false,
			expectedKeys:     []string{MongotConfigFilename},
			notExpectedKeys:  []string{MongotConfigLeaderFilename, MongotConfigFollowerFilename},
		},
		{
			name:             "per-pod config mode - single replica with embedding",
			replicas:         1,
			hasAutoEmbedding: true,
			expectedKeys:     []string{MongotConfigLeaderFilename, MongotConfigFollowerFilename, "test-search-search-0"},
			notExpectedKeys:  []string{MongotConfigFilename},
		},
		{
			name:             "per-pod config mode - 3 replicas with embedding",
			replicas:         3,
			hasAutoEmbedding: true,
			expectedKeys:     []string{MongotConfigLeaderFilename, MongotConfigFollowerFilename, "test-search-search-0", "test-search-search-1", "test-search-search-2"},
			notExpectedKeys:  []string{MongotConfigFilename},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			search := newTestMongoDBSearch("test-search", "test-ns")
			search.Spec.Clusters = []searchv1.ClusterSpec{{Replicas: ptr.To(tc.replicas)}}
			if tc.hasAutoEmbedding {
				search.Spec.AutoEmbedding = &searchv1.EmbeddingConfig{}
			}
			fakeClient := newTestFakeClient(search)
			helper := NewMongoDBSearchReconcileHelper(fakeClient, search, nil, newTestOperatorSearchConfig(), nil, nil)
			cmName := search.MongotConfigConfigMapNamespacedName()
			stsName := search.StatefulSetNamespacedName().Name

			embeddingMod := func(c *mongot.Config) {
				c.Embedding = &mongot.EmbeddingConfig{IsAutoEmbeddingViewWriter: ptr.To(true)}
			}
			_, err := helper.ensureMongotConfig(t.Context(), zap.S(), fakeClient, cmName, stsName, "", int(tc.replicas), embeddingMod)
			require.NoError(t, err)

			cm, err := fakeClient.GetConfigMap(t.Context(), cmName)
			require.NoError(t, err)

			for _, key := range tc.expectedKeys {
				assert.Contains(t, cm.Data, key)
			}
			for _, key := range tc.notExpectedKeys {
				assert.NotContains(t, cm.Data, key)
			}

			if tc.hasAutoEmbedding {
				var leaderConfig, followerConfig mongot.Config
				require.NoError(t, yaml.Unmarshal([]byte(cm.Data[MongotConfigLeaderFilename]), &leaderConfig))
				require.NoError(t, yaml.Unmarshal([]byte(cm.Data[MongotConfigFollowerFilename]), &followerConfig))
				assert.True(t, *leaderConfig.Embedding.IsAutoEmbeddingViewWriter)
				assert.False(t, *followerConfig.Embedding.IsAutoEmbeddingViewWriter)
			}
		})
	}
}

func TestEnsureMongotConfig_TransitionBetweenModes(t *testing.T) {
	search := newTestMongoDBSearch("test-search", "test-ns")
	search.Spec.Clusters = []searchv1.ClusterSpec{{Replicas: ptr.To(int32(1))}}
	fakeClient := newTestFakeClient(search)
	helper := NewMongoDBSearchReconcileHelper(fakeClient, search, nil, newTestOperatorSearchConfig(), nil, nil)
	cmName := search.MongotConfigConfigMapNamespacedName()
	stsName := search.StatefulSetNamespacedName().Name

	embeddingMod := func(c *mongot.Config) {
		c.Embedding = &mongot.EmbeddingConfig{IsAutoEmbeddingViewWriter: ptr.To(true)}
	}

	// Create ConfigMap in single config mode
	_, err := helper.ensureMongotConfig(t.Context(), zap.S(), fakeClient, cmName, stsName, "", 1, embeddingMod)
	require.NoError(t, err)

	// Transition to per-pod config mode - verify old key is cleaned up
	search.Spec.AutoEmbedding = &searchv1.EmbeddingConfig{}
	_, err = helper.ensureMongotConfig(t.Context(), zap.S(), fakeClient, cmName, stsName, "", 1, embeddingMod)
	require.NoError(t, err)

	cm, err := fakeClient.GetConfigMap(t.Context(), cmName)
	require.NoError(t, err)
	assert.NotContains(t, cm.Data, MongotConfigFilename, "config.yml should be removed after transition")

	// Transition back to single config mode - verify per-pod keys are cleaned up
	search.Spec.AutoEmbedding = nil
	_, err = helper.ensureMongotConfig(t.Context(), zap.S(), fakeClient, cmName, stsName, "", 1, embeddingMod)
	require.NoError(t, err)

	cm, err = fakeClient.GetConfigMap(t.Context(), cmName)
	require.NoError(t, err)
	assert.NotContains(t, cm.Data, MongotConfigLeaderFilename, "config-leader.yml should be removed after transition")
	assert.NotContains(t, cm.Data, MongotConfigFollowerFilename, "config-follower.yml should be removed after transition")
	assert.NotContains(t, cm.Data, stsName+"-0", "pod role key should be removed after transition")
	assert.NotContains(t, cm.Data, stsName+"-1", "pod role key should be removed after transition")
}

func renderMongotConfig(t *testing.T, search *searchv1.MongoDBSearch, mods ...mongot.Modification) map[string]string {
	fakeClient := newTestFakeClient(search)
	helper := NewMongoDBSearchReconcileHelper(fakeClient, search, nil, newTestOperatorSearchConfig(), nil, nil)
	cmName := search.MongotConfigConfigMapNamespacedName()
	stsName := search.StatefulSetNamespacedName().Name

	_, err := helper.ensureMongotConfig(t.Context(), zap.S(), fakeClient, cmName, stsName, "", 1, mods...)
	require.NoError(t, err)

	cm, err := fakeClient.GetConfigMap(t.Context(), cmName)
	require.NoError(t, err)
	return cm.Data
}

func withAdvancedMongotConfigs(t *testing.T, search *searchv1.MongoDBSearch, jsonConfig string) {
	withClusterAdvancedMongotConfigs(t, search, 0, jsonConfig)
}

func withClusterAdvancedMongotConfigs(t *testing.T, search *searchv1.MongoDBSearch, clusterIdx int, jsonConfig string) {
	search.Spec.Clusters[clusterIdx].AdvancedMongotConfigs = &searchv1.AdvancedMongotConfigs{}
	require.NoError(t, json.Unmarshal([]byte(jsonConfig), search.Spec.Clusters[clusterIdx].AdvancedMongotConfigs))
}

func TestEnsureMongotConfig_AdvancedMongotConfigs(t *testing.T) {
	operatorMod := func(c *mongot.Config) {
		c.Storage.DataPath = "/mongot/data"
	}
	search := newTestMongoDBSearch("test-search", "test-ns")
	withAdvancedMongotConfigs(t, search, `{"indexing":{"lucene":{"fieldLimit":1000}},"querying":{"lucene":{"enableConcurrentSearch":true}}}`)

	data := renderMongotConfig(t, search, operatorMod)

	rendered := map[string]interface{}{}
	require.NoError(t, yaml.Unmarshal([]byte(data[MongotConfigFilename]), &rendered))
	assert.Equal(t, 1000, maputil.ReadMapValueAsInt(rendered, "advancedConfigs", "indexing", "lucene", "fieldLimit"))
	assert.Equal(t, true, maputil.ReadMapValueAsInterface(rendered, "advancedConfigs", "querying", "lucene", "enableConcurrentSearch"))
	assert.Equal(t, "/mongot/data", maputil.ReadMapValueAsString(rendered, "storage", "dataPath"))
}

func TestEnsureMongotConfig_AdvancedMongotConfigsPerCluster(t *testing.T) {
	search := newTestMongoDBSearch("test-search", "test-ns")
	search.Spec.Clusters = []searchv1.ClusterSpec{{Name: "cluster-a"}, {Name: "cluster-b"}}
	withClusterAdvancedMongotConfigs(t, search, 0, `{"indexing":{"lucene":{"fieldLimit":1000}}}`)
	withClusterAdvancedMongotConfigs(t, search, 1, `{"querying":{"lucene":{"maxClauseLimit":2048}}}`)

	fakeClient := newTestFakeClient(search)
	helper := NewMongoDBSearchReconcileHelper(fakeClient, search, nil, newTestOperatorSearchConfig(), nil, nil)
	stsName := search.StatefulSetNamespacedName().Name

	renderCluster := func(clusterName string, clusterIdx int) map[string]interface{} {
		cmName := search.MongotConfigConfigMapNameForCluster(clusterIdx)
		_, err := helper.ensureMongotConfig(t.Context(), zap.S(), fakeClient, cmName, stsName, clusterName, 1)
		require.NoError(t, err)
		cm, err := fakeClient.GetConfigMap(t.Context(), cmName)
		require.NoError(t, err)
		rendered := map[string]interface{}{}
		require.NoError(t, yaml.Unmarshal([]byte(cm.Data[MongotConfigFilename]), &rendered))
		return rendered
	}

	renderedA := renderCluster("cluster-a", 0)
	renderedB := renderCluster("cluster-b", 1)

	assert.Equal(t, 1000, maputil.ReadMapValueAsInt(renderedA, "advancedConfigs", "indexing", "lucene", "fieldLimit"))
	assert.Nil(t, maputil.ReadMapValueAsInterface(renderedA, "advancedConfigs", "querying"), "cluster-a must not get cluster-b's config")
	assert.Equal(t, 2048, maputil.ReadMapValueAsInt(renderedB, "advancedConfigs", "querying", "lucene", "maxClauseLimit"))
	assert.Nil(t, maputil.ReadMapValueAsInterface(renderedB, "advancedConfigs", "indexing"), "cluster-b must not get cluster-a's config")
}

func TestEnsureMongotConfig_AdvancedMongotConfigsAbsentUnchanged(t *testing.T) {
	operatorMod := func(c *mongot.Config) {
		c.Storage.DataPath = "/mongot/data"
	}
	baseline := renderMongotConfig(t, newTestMongoDBSearch("test-search", "test-ns"), operatorMod)

	empty := newTestMongoDBSearch("test-search", "test-ns")
	empty.Spec.Clusters[0].AdvancedMongotConfigs = &searchv1.AdvancedMongotConfigs{}
	assert.Equal(t, baseline[MongotConfigFilename], renderMongotConfig(t, empty, operatorMod)[MongotConfigFilename])
}

func TestReconcileReplicaSet_AdvancedMongotConfigs(t *testing.T) {
	search := newTestMongoDBSearch("test-search", "test-ns")
	withAdvancedMongotConfigs(t, search, `{"indexing":{"lucene":{"fieldLimit":1000}},"syncSource":{"replicaSet":{"hostAndPort":["evil:1"]}}}`)
	mdbc := newTestMongoDBCommunity("test-mongodb", "test-ns")
	fakeClient := newTestFakeClient(search, mdbc)

	helper := NewMongoDBSearchReconcileHelper(
		fakeClient,
		search,
		NewCommunityResourceSearchSource(mdbc),
		newTestOperatorSearchConfig(),
		nil, nil,
	)

	result := helper.reconcile(t.Context(), zap.S())
	assert.False(t, result.IsOK())
	require.NoError(t, mock.MarkAllStatefulSetsAsReady(t.Context(), search.Namespace, fakeClient))
	result = helper.reconcile(t.Context(), zap.S())
	assert.True(t, result.IsOK())

	cm, err := fakeClient.GetConfigMap(t.Context(), search.MongotConfigConfigMapNameForCluster(0))
	require.NoError(t, err)

	rendered := map[string]interface{}{}
	require.NoError(t, yaml.Unmarshal([]byte(cm.Data[MongotConfigFilename]), &rendered))

	assert.Equal(t, 1000, maputil.ReadMapValueAsInt(rendered, "advancedConfigs", "indexing", "lucene", "fieldLimit"))
	hosts := maputil.ReadMapValueAsInterface(rendered, "syncSource", "replicaSet", "hostAndPort")
	assert.NotEmpty(t, hosts, "operator-derived sync source must be rendered untouched")
	assert.NotContains(t, hosts, "evil:1", "the block must never leak into operator sections")
	assert.Equal(t, []interface{}{"evil:1"},
		maputil.ReadMapValueAsInterface(rendered, "advancedConfigs", "syncSource", "replicaSet", "hostAndPort"),
		"the block appears verbatim under the advancedConfigs key")
}

func TestCreateSearchStatefulSetFunc_ConfigMounting(t *testing.T) {
	search := newTestMongoDBSearch("test-search", "test-ns")
	labels := map[string]string{"app": "test-svc"}

	// Single config mode
	sts := &appsv1.StatefulSet{}
	stsFunc := CreateSearchStatefulSetFunc(search, resolvedSizing(t, search, "", ""), "sts", "ns", "svc", "cm", labels, "img:v1", false)
	stsFunc(sts)
	assert.Contains(t, sts.Spec.Template.Spec.Containers[0].Args[1], MongotConfigPath)

	// Per-pod config mode
	sts = &appsv1.StatefulSet{}
	stsFunc = CreateSearchStatefulSetFunc(search, resolvedSizing(t, search, "", ""), "sts", "ns", "svc", "cm", labels, "img:v1", true)
	stsFunc(sts)
	startupCmd := sts.Spec.Template.Spec.Containers[0].Args[1]
	assert.Contains(t, startupCmd, MongotPerPodConfigDirPath)
	assert.Contains(t, startupCmd, "ROLE=$(cat")
}

func TestGetMongodConfigParametersForShard(t *testing.T) {
	tests := []struct {
		name           string
		search         *searchv1.MongoDBSearch
		shardName      string
		clusterDomain  string
		expectedHost   string
		useUnmanagedLB bool
	}{
		{
			name: "No LB - headless pod-0 FQDN for shard",
			search: &searchv1.MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-search",
					Namespace: "test-ns",
				},
				Spec: searchv1.MongoDBSearchSpec{
					Source: &searchv1.MongoDBSource{
						MongoDBResourceRef: &userv1.MongoDBResourceRef{
							Name: "test-mdb",
						},
					},
				},
			},
			shardName:      "test-mdb-0",
			clusterDomain:  "cluster.local",
			expectedHost:   "test-search-search-0-test-mdb-0-0.test-search-search-0-test-mdb-0-svc.test-ns.svc.cluster.local:27028",
			useUnmanagedLB: false,
		},
		{
			name: "Unmanaged LB endpoint for shard via template",
			search: &searchv1.MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-search",
					Namespace: "test-ns",
				},
				Spec: searchv1.MongoDBSearchSpec{
					Source: &searchv1.MongoDBSource{
						MongoDBResourceRef: &userv1.MongoDBResourceRef{
							Name: "test-mdb",
						},
					},
					Clusters: []searchv1.ClusterSpec{{
						LoadBalancer: &searchv1.LoadBalancerConfig{
							Unmanaged: &searchv1.UnmanagedLBConfig{Endpoint: "lb-{shardName}.example.com:27028"},
						},
					}},
				},
			},
			shardName:      "test-mdb-0",
			clusterDomain:  "cluster.local",
			expectedHost:   "lb-test-mdb-0.example.com:27028",
			useUnmanagedLB: true,
		},
		{
			name: "Unmanaged LB endpoint for second shard via template",
			search: &searchv1.MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-search",
					Namespace: "test-ns",
				},
				Spec: searchv1.MongoDBSearchSpec{
					Source: &searchv1.MongoDBSource{
						MongoDBResourceRef: &userv1.MongoDBResourceRef{
							Name: "test-mdb",
						},
					},
					Clusters: []searchv1.ClusterSpec{{
						LoadBalancer: &searchv1.LoadBalancerConfig{
							Unmanaged: &searchv1.UnmanagedLBConfig{Endpoint: "lb-{shardName}.example.com:27028"},
						},
					}},
				},
			},
			shardName:      "test-mdb-1",
			clusterDomain:  "cluster.local",
			expectedHost:   "lb-test-mdb-1.example.com:27028",
			useUnmanagedLB: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			config := GetMongodConfigParametersForShard(tc.search, tc.shardName, tc.clusterDomain, 0)

			setParameter, ok := config["setParameter"].(map[string]any)
			require.True(t, ok, "setParameter should be a map")

			mongotHost, ok := setParameter["mongotHost"].(string)
			require.True(t, ok, "mongotHost should be a string")
			assert.Equal(t, tc.expectedHost, mongotHost)

			searchIndexHost, ok := setParameter["searchIndexManagementHostAndPort"].(string)
			require.True(t, ok, "searchIndexManagementHostAndPort should be a string")
			assert.Equal(t, tc.expectedHost, searchIndexHost)
		})
	}
}

func TestCreateShardMongotConfig(t *testing.T) {
	search := newTestMongoDBSearch("test-search", "test", func(s *searchv1.MongoDBSearch) {
		s.Spec.Clusters[0].LoadBalancer = &searchv1.LoadBalancerConfig{
			Unmanaged: &searchv1.UnmanagedLBConfig{Endpoint: "lb-{shardName}.example.com:27028"},
		}
	})

	shardedSource := &mockShardedSource{
		shardNames: []string{"my-cluster-0", "my-cluster-1"},
		hostSeeds: map[string][]string{
			"my-cluster-0": {"my-cluster-0-0.svc:27017", "my-cluster-0-1.svc:27017", "my-cluster-0-2.svc:27017"},
			"my-cluster-1": {"my-cluster-1-0.svc:27017", "my-cluster-1-1.svc:27017", "my-cluster-1-2.svc:27017"},
		},
	}

	seeds0, _ := shardedSource.HostSeeds(shardedSource.shardNames[0])
	config := mongot.Config{}
	mongot.Apply(baseMongotConfig(search, seeds0), routerMongotMod(search, shardedSource), featureFlagsMongotMod(search))(&config)

	assert.Equal(t, []string{"my-cluster-0-0.svc:27017", "my-cluster-0-1.svc:27017", "my-cluster-0-2.svc:27017"}, config.SyncSource.ReplicaSet.HostAndPort)
	assert.Equal(t, search.SourceUsername(), config.SyncSource.ReplicaSet.ScramAuth.Username)

	// OverloadRetrySignal defaults to true even when featureFlags is not set in CR
	require.NotNil(t, config.FeatureFlags)
	assert.Equal(t, true, *config.FeatureFlags.OverloadRetrySignal)

	// Explicitly disable feature flag and verify it's absent from config
	search.Spec.FeatureFlags = &searchv1.FeatureFlags{EnableOverloadRetrySignal: ptr.To(false)}
	configDisabled := mongot.Config{}
	mongot.Apply(baseMongotConfig(search, seeds0), routerMongotMod(search, shardedSource), featureFlagsMongotMod(search))(&configDisabled)

	assert.Nil(t, configDisabled.FeatureFlags, "featureflags should be absent when explicitly disabled")

	seeds1, _ := shardedSource.HostSeeds(shardedSource.shardNames[1])
	config2 := mongot.Config{}
	mongot.Apply(baseMongotConfig(search, seeds1), routerMongotMod(search, shardedSource))(&config2)

	assert.Equal(t, []string{"my-cluster-1-0.svc:27017", "my-cluster-1-1.svc:27017", "my-cluster-1-2.svc:27017"}, config2.SyncSource.ReplicaSet.HostAndPort)
}

func TestShardedMongotConfigWithTLS(t *testing.T) {
	search := newTestMongoDBSearch("test-search", "test", func(s *searchv1.MongoDBSearch) {
		s.Spec.Clusters[0].LoadBalancer = &searchv1.LoadBalancerConfig{
			Unmanaged: &searchv1.UnmanagedLBConfig{Endpoint: "lb-{shardName}.example.com:27028"},
		}
	})

	shardedSource := &mockShardedSource{
		shardNames: []string{"my-cluster-0", "my-cluster-1"},
		hostSeeds: map[string][]string{
			"my-cluster-0": {"my-cluster-0-0.svc:27017", "my-cluster-0-1.svc:27017", "my-cluster-0-2.svc:27017"},
			"my-cluster-1": {"my-cluster-1-0.svc:27017", "my-cluster-1-1.svc:27017", "my-cluster-1-2.svc:27017"},
		},
		tlsConfig: &TLSSourceConfig{
			CAFileName: "ca-pem",
		},
	}

	seedsTLS, _ := shardedSource.HostSeeds(shardedSource.shardNames[0])
	config := mongot.Config{}
	mongot.Apply(baseMongotConfig(search, seedsTLS), routerMongotMod(search, shardedSource))(&config)

	require.NotNil(t, config.SyncSource.ReplicaSet.ScramAuth.TLS.Enabled)
	assert.False(t, config.SyncSource.ReplicaSet.ScramAuth.TLS.Enabled, "ReplicaSet TLS should initially be false")
	require.NotNil(t, config.SyncSource.Router)
	require.NotNil(t, config.SyncSource.Router.ScramAuth.TLS.Enabled)
	assert.False(t, config.SyncSource.Router.ScramAuth.TLS.Enabled, "Router TLS should initially be false")

	// Simulate what ensureEgressTlsConfig does when TLS is enabled
	tlsSourceConfig := shardedSource.TLSConfig()
	require.NotNil(t, tlsSourceConfig, "TLS config should not be nil")

	// Apply the TLS modification (simulating ensureEgressTlsConfig behavior)
	config.SyncSource.ReplicaSet.ScramAuth.TLS.Enabled = true
	config.SyncSource.ReplicaSet.ScramAuth.TLS.CertificateAuthorityFile = ptr.To("/mongodb-automation/ca/" + tlsSourceConfig.CAFileName)
	if config.SyncSource.Router != nil {
		config.SyncSource.Router.ScramAuth.TLS.Enabled = true
	}

	assert.True(t, config.SyncSource.ReplicaSet.ScramAuth.TLS.Enabled, "ReplicaSet TLS should be enabled")
	require.NotNil(t, config.SyncSource.ReplicaSet.ScramAuth.TLS.CertificateAuthorityFile)
	assert.Equal(t, "/mongodb-automation/ca/ca-pem", *config.SyncSource.ReplicaSet.ScramAuth.TLS.CertificateAuthorityFile)
	assert.True(t, config.SyncSource.Router.ScramAuth.TLS.Enabled, "Router TLS should be enabled for sharded clusters")
}

func TestShardedMongotConfigWithoutTLS(t *testing.T) {
	search := newTestMongoDBSearch("test-search", "test", func(s *searchv1.MongoDBSearch) {
		s.Spec.Clusters[0].LoadBalancer = &searchv1.LoadBalancerConfig{
			Unmanaged: &searchv1.UnmanagedLBConfig{Endpoint: "lb-{shardName}.example.com:27028"},
		}
	})

	shardedSource := &mockShardedSource{
		shardNames: []string{"my-cluster-0"},
		hostSeeds: map[string][]string{
			"my-cluster-0": {"my-cluster-0-0.svc:27017"},
		},
		tlsConfig: nil, // No TLS
	}

	seedsNoTLS, _ := shardedSource.HostSeeds(shardedSource.shardNames[0])
	config := mongot.Config{}
	mongot.Apply(baseMongotConfig(search, seedsNoTLS), routerMongotMod(search, shardedSource))(&config)

	require.NotNil(t, config.SyncSource.ReplicaSet.ScramAuth.TLS.Enabled)
	assert.False(t, config.SyncSource.ReplicaSet.ScramAuth.TLS.Enabled, "ReplicaSet TLS should be false when source has no TLS")
	require.NotNil(t, config.SyncSource.Router)
	require.NotNil(t, config.SyncSource.Router.ScramAuth.TLS.Enabled)
	assert.False(t, config.SyncSource.Router.ScramAuth.TLS.Enabled, "Router TLS should be false when source has no TLS")
	assert.Nil(t, config.SyncSource.ReplicaSet.ScramAuth.TLS.CertificateAuthorityFile)
}

// mockShardedSource is a mock implementation of ShardedSearchSourceDBResource for testing
type mockShardedSource struct {
	shardNames []string
	hostSeeds  map[string][]string
	tlsConfig  *TLSSourceConfig
}

func (m *mockShardedSource) GetShardCount() int {
	return len(m.shardNames)
}

func (m *mockShardedSource) GetShardNames() []string {
	return m.shardNames
}

func (m *mockShardedSource) GetUnmanagedLBEndpointForShard(shardName string) string {
	return ""
}

func (m *mockShardedSource) MongosHostsAndPorts() []string {
	return []string{"mongos-svc.test-ns.svc.cluster.local:27017"}
}

// Implement SearchSourceDBResource interface
func (m *mockShardedSource) HostSeeds(shardName string) ([]string, error) {
	return m.hostSeeds[shardName], nil
}

func (m *mockShardedSource) Validate() error {
	return nil
}

func (m *mockShardedSource) KeyfileSecretName() string {
	return ""
}

func (m *mockShardedSource) TLSConfig() *TLSSourceConfig {
	return m.tlsConfig
}

func (m *mockShardedSource) ResourceType() mdbv1.ResourceType {
	return mdbv1.ShardedCluster
}

func TestBuildShardSearchHeadlessService(t *testing.T) {
	search := newTestMongoDBSearch("test-search", "test")
	shardName := "my-cluster-0"

	unit := newTestShardUnit(search, shardName)
	svc := buildHeadlessService(search, unit)

	assert.Equal(t, "test-search-search-0-my-cluster-0-svc", svc.Name)
	assert.Empty(t, svc.OwnerReferences)
	assert.Equal(t, "test", svc.Namespace)
	assert.Equal(t, corev1.ClusterIPNone, svc.Spec.ClusterIP)
	assert.Equal(t, corev1.ServiceTypeClusterIP, svc.Spec.Type)
	assert.False(t, svc.Spec.PublishNotReadyAddresses)

	// Check selector points to the shard StatefulSet
	assert.Equal(t, "test-search-search-0-my-cluster-0", svc.Spec.Selector["app"])

	// Check ports
	var grpcPort, healthPort *corev1.ServicePort
	for i := range svc.Spec.Ports {
		switch svc.Spec.Ports[i].Name {
		case "mongot-grpc":
			grpcPort = &svc.Spec.Ports[i]
		case "healthcheck":
			healthPort = &svc.Spec.Ports[i]
		}
	}

	require.NotNil(t, grpcPort, "grpc port should exist")
	assert.Equal(t, int32(27028), grpcPort.Port)

	require.NotNil(t, healthPort, "healthcheck port should exist")
	assert.Equal(t, int32(8080), healthPort.Port)
}

func TestValidateManagedLBShardedTLS(t *testing.T) {
	mdbc := newTestMongoDBCommunity("test-mongodb", "test")

	tests := []struct {
		name          string
		search        *searchv1.MongoDBSearch
		source        SearchSourceDBResource
		expectedError string
	}{
		{
			name: "non-sharded source, managed LB, no TLS - ok",
			search: newTestMongoDBSearch("test-search", "test", func(s *searchv1.MongoDBSearch) {
				s.Spec.Clusters[0].LoadBalancer = &searchv1.LoadBalancerConfig{
					Managed: &searchv1.ManagedLBConfig{},
				}
			}),
			source: NewCommunityResourceSearchSource(mdbc),
		},
		{
			name:   "sharded source, no LB - ok",
			search: newTestMongoDBSearch("test-search", "test"),
			source: &mockShardedSource{shardNames: []string{"shard-0"}},
		},
		{
			name: "sharded source, managed LB, TLS configured - ok",
			search: newTestMongoDBSearch("test-search", "test", func(s *searchv1.MongoDBSearch) {
				s.Spec.Clusters[0].LoadBalancer = &searchv1.LoadBalancerConfig{
					Managed: &searchv1.ManagedLBConfig{},
				}
				s.Spec.Security.TLS = &searchv1.TLS{CertsSecretPrefix: "prefix"}
			}),
			source: &mockShardedSource{shardNames: []string{"shard-0"}},
		},
		{
			name: "sharded source, managed LB, no TLS - error",
			search: newTestMongoDBSearch("test-search", "test", func(s *searchv1.MongoDBSearch) {
				s.Spec.Clusters[0].LoadBalancer = &searchv1.LoadBalancerConfig{
					Managed: &searchv1.ManagedLBConfig{},
				}
			}),
			source:        &mockShardedSource{shardNames: []string{"shard-0"}},
			expectedError: "TLS (spec.security.tls) is required when using managed load balancer with a sharded cluster",
		},
		{
			name: "sharded source, unmanaged LB, no TLS - ok",
			search: newTestMongoDBSearch("test-search", "test", func(s *searchv1.MongoDBSearch) {
				s.Spec.Clusters[0].LoadBalancer = &searchv1.LoadBalancerConfig{
					Unmanaged: &searchv1.UnmanagedLBConfig{Endpoint: "lb.example.com:27028"},
				}
			}),
			source: &mockShardedSource{shardNames: []string{"shard-0"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clientBuilder := mock.NewEmptyFakeClientBuilder()
			clientBuilder.WithObjects(mdbc)

			helper := NewMongoDBSearchReconcileHelper(
				kubernetesClient.NewClient(clientBuilder.Build()),
				tc.search,
				tc.source,
				OperatorSearchConfig{},
				nil, nil,
			)

			err := helper.ValidateManagedLBShardedTLS()
			if tc.expectedError == "" {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tc.expectedError)
			}
		})
	}
}

func TestValidateMultipleReplicasUnmanagedLBTopology(t *testing.T) {
	mdbc := newTestMongoDBCommunity("test-mongodb", "test")

	multiReplica := func(s *searchv1.MongoDBSearch) {
		s.Spec.Clusters = []searchv1.ClusterSpec{{Replicas: ptr.To(int32(2))}}
	}
	withUnmanaged := func(endpoint string) func(*searchv1.MongoDBSearch) {
		return func(s *searchv1.MongoDBSearch) {
			s.Spec.Clusters[0].LoadBalancer = &searchv1.LoadBalancerConfig{
				Unmanaged: &searchv1.UnmanagedLBConfig{Endpoint: endpoint},
			}
		}
	}

	tests := []struct {
		name          string
		search        *searchv1.MongoDBSearch
		source        SearchSourceDBResource
		expectedError string
	}{
		{
			name:          "sharded source, multiple replicas, endpoint without {shardName} - error",
			search:        newTestMongoDBSearch("test-search", "test", multiReplica, withUnmanaged("lb.example.com:27028")),
			source:        &mockShardedSource{shardNames: []string{"shard-0"}},
			expectedError: "must contain a {shardName} placeholder for a sharded source",
		},
		{
			name:   "sharded source, multiple replicas, endpoint with {shardName} - ok",
			search: newTestMongoDBSearch("test-search", "test", multiReplica, withUnmanaged("lb-{shardName}.example.com:27028")),
			source: &mockShardedSource{shardNames: []string{"shard-0"}},
		},
		{
			name:          "replica set source, multiple replicas, endpoint with {shardName} - error",
			search:        newTestMongoDBSearch("test-search", "test", multiReplica, withUnmanaged("lb-{shardName}.example.com:27028")),
			source:        NewCommunityResourceSearchSource(mdbc),
			expectedError: "must not contain a {shardName} placeholder for a replica set source",
		},
		{
			name:   "replica set source, multiple replicas, endpoint without template - ok",
			search: newTestMongoDBSearch("test-search", "test", multiReplica, withUnmanaged("lb.example.com:27028")),
			source: NewCommunityResourceSearchSource(mdbc),
		},
		{
			name:   "single replica, sharded source, endpoint without {shardName} - ok",
			search: newTestMongoDBSearch("test-search", "test", withUnmanaged("lb.example.com:27028")),
			source: &mockShardedSource{shardNames: []string{"shard-0"}},
		},
		{
			name: "multiple replicas, managed LB - ok",
			search: newTestMongoDBSearch("test-search", "test", multiReplica, func(s *searchv1.MongoDBSearch) {
				s.Spec.Clusters[0].LoadBalancer = &searchv1.LoadBalancerConfig{Managed: &searchv1.ManagedLBConfig{}}
			}),
			source: &mockShardedSource{shardNames: []string{"shard-0"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clientBuilder := mock.NewEmptyFakeClientBuilder()
			clientBuilder.WithObjects(mdbc)

			helper := NewMongoDBSearchReconcileHelper(
				kubernetesClient.NewClient(clientBuilder.Build()),
				tc.search,
				tc.source,
				OperatorSearchConfig{},
				nil, nil,
			)

			err := helper.ValidateMultipleReplicasUnmanagedLBTopology()
			if tc.expectedError == "" {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tc.expectedError)
			}
		})
	}
}

func TestGetMongosConfigParametersForSharded(t *testing.T) {
	tests := []struct {
		name          string
		search        *searchv1.MongoDBSearch
		clusterIndex  int
		clusterName   string
		shardNames    []string
		clusterDomain string
		expectedHost  string
	}{
		{
			name: "No LB - uses first shard's proxy svc endpoint (in per-shard cert SANs)",
			search: &searchv1.MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-search",
					Namespace: "test-ns",
				},
				Spec: searchv1.MongoDBSearchSpec{
					Source: &searchv1.MongoDBSource{
						MongoDBResourceRef: &userv1.MongoDBResourceRef{
							Name: "test-mdb",
						},
					},
				},
			},
			shardNames:    []string{"test-mdb-0", "test-mdb-1"},
			clusterDomain: "cluster.local",
			expectedHost:  "test-search-search-0-test-mdb-0-proxy-svc.test-ns.svc.cluster.local:27028",
		},
		{
			name: "Managed LB - uses cluster-level proxy service endpoint",
			search: &searchv1.MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-search",
					Namespace: "test-ns",
				},
				Spec: searchv1.MongoDBSearchSpec{
					Source: &searchv1.MongoDBSource{
						MongoDBResourceRef: &userv1.MongoDBResourceRef{
							Name: "test-mdb",
						},
					},
					Clusters: []searchv1.ClusterSpec{{
						LoadBalancer: &searchv1.LoadBalancerConfig{Managed: &searchv1.ManagedLBConfig{}},
					}},
				},
			},
			shardNames:    []string{"test-mdb-0", "test-mdb-1"},
			clusterDomain: "cluster.local",
			expectedHost:  "test-search-search-0-proxy-svc.test-ns.svc.cluster.local:27028",
		},
		{
			name: "Unmanaged LB endpoint - mongos still uses first shard's endpoint via template",
			search: &searchv1.MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-search",
					Namespace: "test-ns",
				},
				Spec: searchv1.MongoDBSearchSpec{
					Source: &searchv1.MongoDBSource{
						MongoDBResourceRef: &userv1.MongoDBResourceRef{
							Name: "test-mdb",
						},
					},
					Clusters: []searchv1.ClusterSpec{{
						LoadBalancer: &searchv1.LoadBalancerConfig{
							Unmanaged: &searchv1.UnmanagedLBConfig{Endpoint: "lb-{shardName}.example.com:27028"},
						},
					}},
				},
			},
			shardNames:    []string{"test-mdb-0", "test-mdb-1"},
			clusterDomain: "cluster.local",
			// Unmanaged LB endpoint template is shard-scoped (no cluster-level form supported),
			// so mongos falls back to the first shard's substituted endpoint.
			expectedHost: "lb-test-mdb-0.example.com:27028",
		},
		{
			name: "Empty shard names (no LB) - cluster-level svc still resolvable",
			search: &searchv1.MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-search",
					Namespace: "test-ns",
				},
				Spec: searchv1.MongoDBSearchSpec{},
			},
			shardNames:    []string{},
			clusterDomain: "cluster.local",
			expectedHost:  "test-search-search-0-proxy-svc.test-ns.svc.cluster.local:27028",
		},
		{
			name: "Managed LB with routerHostname - uses it verbatim for mongos",
			search: &searchv1.MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-search",
					Namespace: "test-ns",
				},
				Spec: searchv1.MongoDBSearchSpec{
					Source: &searchv1.MongoDBSource{
						MongoDBResourceRef: &userv1.MongoDBResourceRef{
							Name: "test-mdb",
						},
					},
					Clusters: []searchv1.ClusterSpec{{
						LoadBalancer: &searchv1.LoadBalancerConfig{
							Managed: &searchv1.ManagedLBConfig{
								ExternalHostname: "{shardName}.search.example.com:443",
								RouterHostname:   "search.example.com:443",
							},
						},
					}},
				},
			},
			shardNames:    []string{"test-mdb-0", "test-mdb-1"},
			clusterDomain: "cluster.local",
			// mongos uses routerHostname verbatim.
			expectedHost: "search.example.com:443",
		},
		{
			name: "MC managed LB - mongos uses the named cluster's routerHostname for clusterIndex>0",
			search: &searchv1.MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-search",
					Namespace: "test-ns",
				},
				Spec: searchv1.MongoDBSearchSpec{
					Source: &searchv1.MongoDBSource{
						MongoDBResourceRef: &userv1.MongoDBResourceRef{
							Name: "test-mdb",
						},
					},
					Clusters: []searchv1.ClusterSpec{
						{Name: "us-east-k8s", LoadBalancer: &searchv1.LoadBalancerConfig{
							Managed: &searchv1.ManagedLBConfig{
								ExternalHostname: "{shardName}.us-east-k8s.search.example.com:443",
								RouterHostname:   "us-east-k8s.search.example.com:443",
							},
						}},
						{Name: "eu-west-k8s", LoadBalancer: &searchv1.LoadBalancerConfig{
							Managed: &searchv1.ManagedLBConfig{
								ExternalHostname: "{shardName}.eu-west-k8s.search.example.com:443",
								RouterHostname:   "eu-west-k8s.search.example.com:443",
							},
						}},
					},
				},
			},
			clusterIndex:  1,
			clusterName:   "eu-west-k8s",
			shardNames:    []string{"test-mdb-0", "test-mdb-1"},
			clusterDomain: "cluster.local",
			// mongos uses spec.clusters[1]'s routerHostname verbatim.
			expectedHost: "eu-west-k8s.search.example.com:443",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			config := GetMongosConfigParametersForSharded(tc.search, tc.clusterIndex, tc.clusterName, tc.shardNames, tc.clusterDomain)

			setParameter, ok := config["setParameter"].(map[string]any)
			require.True(t, ok, "setParameter should be a map")

			mongotHost, ok := setParameter["mongotHost"].(string)
			require.True(t, ok, "mongotHost should be a string")
			assert.Equal(t, tc.expectedHost, mongotHost)

			searchIndexHost, ok := setParameter["searchIndexManagementHostAndPort"].(string)
			require.True(t, ok, "searchIndexManagementHostAndPort should be a string")
			assert.Equal(t, tc.expectedHost, searchIndexHost)

			// useGrpcForSearch must always be true for mongos
			useGrpc, ok := setParameter["useGrpcForSearch"].(bool)
			require.True(t, ok, "useGrpcForSearch should be a bool")
			assert.True(t, useGrpc, "useGrpcForSearch must be true for mongos")
		})
	}
}

// TestGetMongosConfigParametersForSharded_PinnedIndexNotSpecPosition: the
// clusterIndex callers pass is the spec.clusters[i] pin, decoupled from spec
// position. The endpoint must still resolve to the named cluster's
// externalHostname regardless of the index.
func TestGetMongosConfigParametersForSharded_PinnedIndexNotSpecPosition(t *testing.T) {
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "test-search", Namespace: "test-ns"},
		Spec: searchv1.MongoDBSearchSpec{
			Source: &searchv1.MongoDBSource{
				MongoDBResourceRef: &userv1.MongoDBResourceRef{Name: "test-mdb"},
			},
			Clusters: []searchv1.ClusterSpec{
				{Name: "cluster-b", LoadBalancer: &searchv1.LoadBalancerConfig{
					Managed: &searchv1.ManagedLBConfig{ExternalHostname: "{shardName}.b.example.com:443", RouterHostname: "b.example.com:443"},
				}},
			},
		},
	}

	config := GetMongosConfigParametersForSharded(search, 1, "cluster-b", []string{"test-mdb-0"}, "cluster.local")
	setParameter, ok := config["setParameter"].(map[string]any)
	require.True(t, ok, "setParameter should be a map")
	assert.Equal(t, "b.example.com:443", setParameter["mongotHost"],
		"mongos must get cluster-b's routerHostname even though its pinned index (1) is not its spec position (0)")
}

func TestMongotHostAndPort_ReplicaSet(t *testing.T) {
	tests := []struct {
		name         string
		search       *searchv1.MongoDBSearch
		expectedHost string
	}{
		{
			name: "No LB - uses first pod headless FQDN",
			search: &searchv1.MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "ns"},
			},
			expectedHost: "test-search-0-0.test-search-0-svc.ns.svc.cluster.local:27028",
		},
		{
			name: "Managed LB - uses proxy service",
			search: &searchv1.MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "ns"},
				Spec: searchv1.MongoDBSearchSpec{
					Clusters: []searchv1.ClusterSpec{{LoadBalancer: &searchv1.LoadBalancerConfig{Managed: &searchv1.ManagedLBConfig{}}}},
				},
			},
			expectedHost: "test-search-0-proxy-svc.ns.svc.cluster.local:27028",
		},
		{
			name: "Unmanaged LB - uses user-provided endpoint",
			search: &searchv1.MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "ns"},
				Spec: searchv1.MongoDBSearchSpec{
					Clusters: []searchv1.ClusterSpec{{
						LoadBalancer: &searchv1.LoadBalancerConfig{
							Unmanaged: &searchv1.UnmanagedLBConfig{Endpoint: "my-lb.example.com:27028"},
						},
					}},
				},
			},
			expectedHost: "my-lb.example.com:27028",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			host := mongotHostAndPort(tc.search, "cluster.local", 0)
			assert.Equal(t, tc.expectedHost, host)
		})
	}
}

func TestMongotEndpointForShard(t *testing.T) {
	tests := []struct {
		name         string
		search       *searchv1.MongoDBSearch
		shardName    string
		expectedHost string
	}{
		{
			name: "No LB - uses first pod headless FQDN for shard",
			search: &searchv1.MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "ns"},
			},
			shardName:    "shard-0",
			expectedHost: "test-search-0-shard-0-0.test-search-0-shard-0-svc.ns.svc.cluster.local:27028",
		},
		{
			name: "Managed LB - uses per-shard proxy service",
			search: &searchv1.MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "ns"},
				Spec: searchv1.MongoDBSearchSpec{
					Clusters: []searchv1.ClusterSpec{{LoadBalancer: &searchv1.LoadBalancerConfig{Managed: &searchv1.ManagedLBConfig{}}}},
				},
			},
			shardName:    "shard-0",
			expectedHost: "test-search-0-shard-0-proxy-svc.ns.svc.cluster.local:27028",
		},
		{
			name: "Unmanaged LB - uses template endpoint",
			search: &searchv1.MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "ns"},
				Spec: searchv1.MongoDBSearchSpec{
					Clusters: []searchv1.ClusterSpec{{
						LoadBalancer: &searchv1.LoadBalancerConfig{
							Unmanaged: &searchv1.UnmanagedLBConfig{Endpoint: "lb-{shardName}.example.com:27028"},
						},
					}},
				},
			},
			shardName:    "shard-0",
			expectedHost: "lb-shard-0.example.com:27028",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			host := mongotEndpointForShard(tc.search, tc.shardName, "cluster.local", 0)
			assert.Equal(t, tc.expectedHost, host)
		})
	}
}

func TestEndpointTemplateSubstitution(t *testing.T) {
	testCases := []struct {
		name             string
		endpointTemplate string
		shardName        string
		expectedEndpoint string
	}{
		{
			name:             "simple template substitution",
			endpointTemplate: "lb-{shardName}.example.com:27028",
			shardName:        "my-cluster-0",
			expectedEndpoint: "lb-my-cluster-0.example.com:27028",
		},
		{
			name:             "template with shard name at end",
			endpointTemplate: "mongot-lb-{shardName}:27028",
			shardName:        "shard-1",
			expectedEndpoint: "mongot-lb-shard-1:27028",
		},
		{
			name:             "template with complex shard name",
			endpointTemplate: "lb-{shardName}.search.mongodb.svc.cluster.local:27028",
			shardName:        "my-sharded-cluster-shard-0",
			expectedEndpoint: "lb-my-sharded-cluster-shard-0.search.mongodb.svc.cluster.local:27028",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			search := newTestMongoDBSearch("test-search", "default", func(s *searchv1.MongoDBSearch) {
				s.Spec.Clusters[0].LoadBalancer = &searchv1.LoadBalancerConfig{
					Unmanaged: &searchv1.UnmanagedLBConfig{Endpoint: tc.endpointTemplate},
				}
			})

			assert.True(t, search.HasEndpointTemplate())
			assert.True(t, search.IsShardedUnmanagedLB())
			assert.False(t, search.IsReplicaSetUnmanagedLB())

			endpoint := search.GetEndpointForShard(tc.shardName)
			assert.Equal(t, tc.expectedEndpoint, endpoint)
		})
	}
}

func TestTLSSecretPrefixNaming(t *testing.T) {
	testCases := []struct {
		name               string
		secretName         string
		secretPrefix       string
		resourceName       string
		expectedSecretName string
	}{
		{
			name:               "explicit secret name takes precedence",
			secretName:         "my-explicit-secret",
			secretPrefix:       "my-prefix",
			resourceName:       "my-search",
			expectedSecretName: "my-explicit-secret",
		},
		{
			name:               "prefix-based naming when no explicit name",
			secretName:         "",
			secretPrefix:       "my-prefix",
			resourceName:       "my-search",
			expectedSecretName: "my-prefix-my-search-search-cert",
		},
		{
			name:               "only explicit name specified",
			secretName:         "only-explicit",
			secretPrefix:       "",
			resourceName:       "my-search",
			expectedSecretName: "only-explicit",
		},
		{
			name:               "default naming when both empty",
			secretName:         "",
			secretPrefix:       "",
			resourceName:       "my-search",
			expectedSecretName: "my-search-search-cert",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			search := newTestMongoDBSearch(tc.resourceName, "default", func(s *searchv1.MongoDBSearch) {
				s.Spec.Security = searchv1.Security{
					TLS: &searchv1.TLS{
						CertificateKeySecret: corev1.LocalObjectReference{
							Name: tc.secretName,
						},
						CertsSecretPrefix: tc.secretPrefix,
					},
				}
			})

			secretNsName := search.TLSSecretNamespacedName()
			assert.Equal(t, tc.expectedSecretName, secretNsName.Name)
			assert.Equal(t, "default", secretNsName.Namespace)
		})
	}
}

func TestValidateEndpointTemplate(t *testing.T) {
	testCases := []struct {
		name          string
		endpoint      string
		expectError   bool
		errorContains string
	}{
		{
			name:        "valid template",
			endpoint:    "lb-{shardName}.example.com:27028",
			expectError: false,
		},
		{
			name:        "valid template with placeholder at end",
			endpoint:    "mongot-{shardName}:27028",
			expectError: false,
		},
		{
			name:          "only placeholder is invalid",
			endpoint:      "{shardName}",
			expectError:   true,
			errorContains: "must contain more than just",
		},
		{
			name:        "multiple placeholders are supported",
			endpoint:    "lb-{shardName}-{shardName}.example.com:27028",
			expectError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			search := newTestMongoDBSearch("test-search", "default", func(s *searchv1.MongoDBSearch) {
				// Use an external sharded source so that {shardName} templates are valid
				s.Spec.Source = &searchv1.MongoDBSource{
					ExternalMongoDBSource: &searchv1.ExternalMongoDBSource{
						ShardedCluster: &searchv1.ExternalShardedClusterConfig{
							Router: searchv1.ExternalRouterConfig{Hosts: []string{"mongos.example.com:27017"}},
							Shards: []searchv1.ExternalShardConfig{
								{ShardName: "shard0", Hosts: []string{"host:27017"}},
							},
						},
					},
				}
				s.Spec.Clusters[0].LoadBalancer = &searchv1.LoadBalancerConfig{
					Unmanaged: &searchv1.UnmanagedLBConfig{Endpoint: tc.endpoint},
				}
			})

			err := search.ValidateSpec()
			if tc.expectError {
				assert.Error(t, err)
				if tc.errorContains != "" {
					assert.Contains(t, err.Error(), tc.errorContains)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TODO: explicit secret name for sharded clusters is not supported
func TestValidateTLSConfig(t *testing.T) {
	testCases := []struct {
		name          string
		secretName    string
		secretPrefix  string
		expectError   bool
		errorContains string
	}{
		{
			name:        "explicit secret name is valid",
			secretName:  "my-secret",
			expectError: false,
		},
		{
			name:         "prefix is valid",
			secretPrefix: "my-prefix",
			expectError:  false,
		},
		{
			name:         "both specified is valid",
			secretName:   "my-secret",
			secretPrefix: "my-prefix",
			expectError:  false,
		},
		{
			name:         "neither specified uses default",
			secretName:   "",
			secretPrefix: "",
			expectError:  false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			search := newTestMongoDBSearch("test-search", "default", func(s *searchv1.MongoDBSearch) {
				s.Spec.Security = searchv1.Security{
					TLS: &searchv1.TLS{
						CertificateKeySecret: corev1.LocalObjectReference{
							Name: tc.secretName,
						},
						CertsSecretPrefix: tc.secretPrefix,
					},
				}
			})

			err := search.ValidateSpec()
			if tc.expectError {
				assert.Error(t, err)
				if tc.errorContains != "" {
					assert.Contains(t, err.Error(), tc.errorContains)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestIsTLSConfigured(t *testing.T) {
	testCases := []struct {
		name           string
		setup          func(*searchv1.MongoDBSearch)
		expectedResult bool
	}{
		{
			name: "TLS with explicit secret name",
			setup: func(s *searchv1.MongoDBSearch) {
				s.Spec.Security = searchv1.Security{
					TLS: &searchv1.TLS{
						CertificateKeySecret: corev1.LocalObjectReference{Name: "my-secret"},
					},
				}
			},
			expectedResult: true,
		},
		{
			name: "TLS with prefix",
			setup: func(s *searchv1.MongoDBSearch) {
				s.Spec.Security = searchv1.Security{
					TLS: &searchv1.TLS{
						CertsSecretPrefix: "my-prefix",
					},
				}
			},
			expectedResult: true,
		},
		{
			name: "TLS with both",
			setup: func(s *searchv1.MongoDBSearch) {
				s.Spec.Security = searchv1.Security{
					TLS: &searchv1.TLS{
						CertificateKeySecret: corev1.LocalObjectReference{Name: "my-secret"},
						CertsSecretPrefix:    "my-prefix",
					},
				}
			},
			expectedResult: true,
		},
		{
			name: "TLS with neither uses default",
			setup: func(s *searchv1.MongoDBSearch) {
				s.Spec.Security = searchv1.Security{
					TLS: &searchv1.TLS{},
				}
			},
			expectedResult: true,
		},
		{
			name: "no TLS config",
			setup: func(s *searchv1.MongoDBSearch) {
				s.Spec.Security = searchv1.Security{}
			},
			expectedResult: false,
		},
		{
			name: "security but no TLS",
			setup: func(s *searchv1.MongoDBSearch) {
				s.Spec.Security = searchv1.Security{
					TLS: nil,
				}
			},
			expectedResult: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			search := newTestMongoDBSearch("test-search", "default", tc.setup)
			assert.Equal(t, tc.expectedResult, search.IsTLSConfigured())
		})
	}
}

func TestTLSSecretNamespacedNameForShard(t *testing.T) {
	testCases := []struct {
		name               string
		secretPrefix       string
		shardName          string
		namespace          string
		expectedSecretName string
	}{
		{
			name:               "with prefix",
			secretPrefix:       "my-prefix",
			shardName:          "my-cluster-0",
			namespace:          "test-ns",
			expectedSecretName: "my-prefix-test-search-search-0-my-cluster-0-cert",
		},
		{
			name:               "without prefix",
			secretPrefix:       "",
			shardName:          "my-cluster-0",
			namespace:          "test-ns",
			expectedSecretName: "test-search-search-0-my-cluster-0-cert",
		},
		{
			name:               "with prefix - second shard",
			secretPrefix:       "prod",
			shardName:          "shard-1",
			namespace:          "mongodb",
			expectedSecretName: "prod-test-search-search-0-shard-1-cert",
		},
		{
			name:               "without prefix - different shard",
			secretPrefix:       "",
			shardName:          "shard-2",
			namespace:          "mongodb",
			expectedSecretName: "test-search-search-0-shard-2-cert",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			search := newTestMongoDBSearch("test-search", tc.namespace, func(s *searchv1.MongoDBSearch) {
				s.Spec.Security = searchv1.Security{
					TLS: &searchv1.TLS{
						CertsSecretPrefix: tc.secretPrefix,
					},
				}
			})

			secretNsName := search.TLSSecretForClusterShard(0, tc.shardName)
			assert.Equal(t, tc.expectedSecretName, secretNsName.Name)
			assert.Equal(t, tc.namespace, secretNsName.Namespace)
		})
	}
}

func TestReconcileSharded_CertificateKeySecretRefRejected(t *testing.T) {
	search := newTestMongoDBSearch("test-search", "test-ns", func(s *searchv1.MongoDBSearch) {
		s.Spec.Security = searchv1.Security{
			TLS: &searchv1.TLS{
				CertificateKeySecret: corev1.LocalObjectReference{Name: "shared-cert"},
			},
		}
	})

	shardedSource := &mockShardedSource{
		shardNames: []string{"shard-0", "shard-1"},
	}

	helper := NewMongoDBSearchReconcileHelper(
		newTestFakeClient(search),
		search,
		shardedSource,
		newTestOperatorSearchConfig(),
		nil, nil,
	)

	result := helper.reconcile(t.Context(), zap.S())

	assert.False(t, result.IsOK())
	assert.Equal(t, status.PhaseFailed, result.Phase())

	msgOpt, exists := status.GetOption(result.StatusOptions(), status.MessageOption{})
	require.True(t, exists)
	assert.Contains(t, msgOpt.(status.MessageOption).Message, "spec.security.tls.certificateKeySecretRef is not supported for sharded clusters")
}

func TestValidatePerShardTLSSecrets(t *testing.T) {
	testCases := []struct {
		name           string
		setup          func(*searchv1.MongoDBSearch)
		shardNames     []string
		existingSecret string // Name of secret to create (empty = no secrets)
		expectedOK     bool
		expectedPhase  status.Phase // status.PhasePending or status.PhaseFailed or "" for OK
	}{
		{
			name: "TLS not configured - returns OK",
			setup: func(s *searchv1.MongoDBSearch) {
				s.Spec.Security = searchv1.Security{TLS: nil}
			},
			shardNames:    []string{"shard-0", "shard-1"},
			expectedOK:    true,
			expectedPhase: "",
		},
		{
			name: "per-shard mode - missing secret returns Pending",
			setup: func(s *searchv1.MongoDBSearch) {
				s.Spec.Security = searchv1.Security{
					TLS: &searchv1.TLS{
						CertsSecretPrefix: "my-prefix",
					},
				}
			},
			shardNames:     []string{"shard-0", "shard-1"},
			existingSecret: "", // No secrets exist
			expectedOK:     false,
			expectedPhase:  status.PhasePending,
		},
		{
			name: "per-shard mode - first secret exists, second missing returns Pending",
			setup: func(s *searchv1.MongoDBSearch) {
				s.Spec.Security = searchv1.Security{
					TLS: &searchv1.TLS{
						CertsSecretPrefix: "my-prefix",
					},
				}
			},
			shardNames:     []string{"shard-0", "shard-1"},
			existingSecret: "my-prefix-test-search-search-0-shard-0-cert", // Only first shard's secret exists
			expectedOK:     false,
			expectedPhase:  status.PhasePending,
		},
		{
			name: "per-shard mode - all secrets exist returns OK",
			setup: func(s *searchv1.MongoDBSearch) {
				s.Spec.Security = searchv1.Security{
					TLS: &searchv1.TLS{
						CertsSecretPrefix: "my-prefix",
					},
				}
			},
			shardNames:     []string{"shard-0"},
			existingSecret: "my-prefix-test-search-search-0-shard-0-cert",
			expectedOK:     true,
			expectedPhase:  "",
		},
		{
			name: "per-shard mode without prefix - missing secret returns Pending",
			setup: func(s *searchv1.MongoDBSearch) {
				s.Spec.Security = searchv1.Security{
					TLS: &searchv1.TLS{},
				}
			},
			shardNames:     []string{"shard-0"},
			existingSecret: "", // No secrets exist
			expectedOK:     false,
			expectedPhase:  status.PhasePending,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			search := newTestMongoDBSearch("test-search", "test-ns", tc.setup)

			var objects []client.Object
			objects = append(objects, search)

			// Create the existing secret if specified
			if tc.existingSecret != "" {
				secret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      tc.existingSecret,
						Namespace: "test-ns",
					},
					Data: map[string][]byte{
						"tls.crt": []byte("cert-data"),
						"tls.key": []byte("key-data"),
					},
				}
				objects = append(objects, secret)
			}

			fakeClient := newTestFakeClient(objects...)

			// Create a mock sharded source
			shardedSource := &mockShardedSource{
				shardNames: tc.shardNames,
			}

			helper := NewMongoDBSearchReconcileHelper(
				fakeClient,
				search,
				shardedSource,
				newTestOperatorSearchConfig(),
				nil, nil,
			)

			status := helper.validatePerShardTLSSecrets(t.Context(), zap.S(), tc.shardNames)

			if tc.expectedOK {
				assert.True(t, status.IsOK(), "Expected status to be OK")
			} else {
				assert.False(t, status.IsOK(), "Expected status to not be OK")
				assert.Equal(t, tc.expectedPhase, status.Phase())
			}
		})
	}
}

func TestValidatePerShardTLSSecretsAllExist(t *testing.T) {
	search := newTestMongoDBSearch("test-search", "test-ns", func(s *searchv1.MongoDBSearch) {
		s.Spec.Security = searchv1.Security{
			TLS: &searchv1.TLS{
				CertsSecretPrefix: "my-prefix",
			},
		}
	})

	shardNames := []string{"shard-0", "shard-1", "shard-2"}

	var objects []client.Object
	objects = append(objects, search)

	// Create all per-shard secrets
	for _, shardName := range shardNames {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("my-prefix-test-search-search-0-%s-cert", shardName),
				Namespace: "test-ns",
			},
			Data: map[string][]byte{
				"tls.crt": []byte("cert-data"),
				"tls.key": []byte("key-data"),
			},
		}
		objects = append(objects, secret)
	}

	fakeClient := newTestFakeClient(objects...)

	shardedSource := &mockShardedSource{
		shardNames: shardNames,
	}

	helper := NewMongoDBSearchReconcileHelper(
		fakeClient,
		search,
		shardedSource,
		newTestOperatorSearchConfig(),
		nil, nil,
	)

	status := helper.validatePerShardTLSSecrets(t.Context(), zap.S(), shardNames)
	assert.True(t, status.IsOK(), "Expected status to be OK when all secrets exist")
}

func TestReconcileSharded_PerShardIngressTLSSecretLabels(t *testing.T) {
	search := newTestMongoDBSearch("test-search", "test-ns", func(s *searchv1.MongoDBSearch) {
		s.Spec.Security = searchv1.Security{
			TLS: &searchv1.TLS{
				CertsSecretPrefix: "my-prefix",
			},
		}
	})
	shardName := "shard-0"
	sourceSecretName := search.TLSSecretForClusterShard(0, shardName)
	sourceSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: sourceSecretName.Name, Namespace: sourceSecretName.Namespace},
		Data: map[string][]byte{
			"tls.crt": []byte("cert-data"),
			"tls.key": []byte("key-data"),
		},
	}
	shardedSource := &mockShardedSource{
		shardNames: []string{shardName},
		hostSeeds: map[string][]string{
			shardName: {"shard-0-0.shard-0.test-ns.svc.cluster.local:27017"},
		},
	}

	fakeClient := newTestFakeClient(search, sourceSecret)
	helper := NewMongoDBSearchReconcileHelper(fakeClient, search, shardedSource, newTestOperatorSearchConfig(), nil, nil)

	st := helper.reconcile(t.Context(), zap.S())
	assert.False(t, st.IsOK())

	operatorSecret, err := fakeClient.GetSecret(t.Context(), search.TLSOperatorSecretForClusterShard(0, shardName))
	require.NoError(t, err)
	assert.Empty(t, operatorSecret.OwnerReferences)
	assert.Equal(t, mongotComponent, operatorSecret.Labels[khandler.MongoDBSearchComponentLabel])
	assert.Equal(t, search.Name, operatorSecret.Labels[khandler.MongoDBSearchOwnerNameLabel])
	assert.Equal(t, search.Namespace, operatorSecret.Labels[khandler.MongoDBSearchOwnerNamespaceLabel])
	assert.Empty(t, operatorSecret.Labels[khandler.MongoDBSearchClusterNameLabel])
}

func TestEnsureX509ClientCertConfig_NoopWhenNotConfigured(t *testing.T) {
	search := newTestMongoDBSearch("test-search", "test-ns")

	fakeClient := newTestFakeClient(search)
	helper := NewMongoDBSearchReconcileHelper(fakeClient, search, nil, newTestOperatorSearchConfig(), nil, nil)

	mongotMod, stsMod, err := helper.ensureX509ClientCertConfig(t.Context(), fakeClient, searchOwnerLabels(search, ""))
	require.NoError(t, err)

	// Apply modifications and verify no changes
	config := &mongot.Config{
		SyncSource: mongot.ConfigSyncSource{
			ReplicaSet: mongot.ConfigReplicaSet{
				ScramAuth: &mongot.ConfigScramAuth{
					Username:     "original-user",
					PasswordFile: "/original/path",
					AuthSource:   ptr.To("admin"),
				},
			},
		},
	}
	mongotMod(config)

	assert.Equal(t, "original-user", config.SyncSource.ReplicaSet.ScramAuth.Username)
	assert.Equal(t, "/original/path", config.SyncSource.ReplicaSet.ScramAuth.PasswordFile)
	assert.Equal(t, "admin", *config.SyncSource.ReplicaSet.ScramAuth.AuthSource)
	assert.Nil(t, config.SyncSource.ReplicaSet.X509)

	sts := newBaseMongotStatefulSet()
	stsMod(sts)
	assert.Empty(t, sts.Spec.Template.Spec.Volumes)
}

func TestEnsureX509ClientCertConfig_ErrorWhenTLSNotConfigured(t *testing.T) {
	// should error out if the x509 auth is configured between mongot -> mongod but tls is not enabled
	// for search source
	search := newTestMongoDBSearch("test-search", "test-ns", func(s *searchv1.MongoDBSearch) {
		s.Spec.Source.X509 = &searchv1.X509Auth{
			ClientCertificateSecret: corev1.LocalObjectReference{Name: "x509-cert"},
		}
	})

	dbSource := &mockShardedSource{tlsConfig: nil} // No TLS on source

	fakeClient := newTestFakeClient(search)
	helper := NewMongoDBSearchReconcileHelper(fakeClient, search, dbSource, newTestOperatorSearchConfig(), nil, nil)

	_, _, err := helper.ensureX509ClientCertConfig(t.Context(), fakeClient, searchOwnerLabels(search, ""))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tls must be enabled")
}

func TestEnsureX509ClientCertConfig_MongotAndStsModification(t *testing.T) {
	search := newTestMongoDBSearch("test-search", "test-ns", func(s *searchv1.MongoDBSearch) {
		s.Spec.Source.X509 = &searchv1.X509Auth{
			ClientCertificateSecret: corev1.LocalObjectReference{Name: "x509-cert"},
		}
	})

	dbSource := &mockShardedSource{tlsConfig: &TLSSourceConfig{CAFileName: "ca-pem"}}

	x509Secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "x509-cert", Namespace: "test-ns"},
		Data: map[string][]byte{
			"tls.crt": []byte("cert-data"),
			"tls.key": []byte("key-data"),
		},
	}

	fakeClient := newTestFakeClient(search, x509Secret)
	helper := NewMongoDBSearchReconcileHelper(fakeClient, search, dbSource, newTestOperatorSearchConfig(), nil, nil)

	mongotMod, stsMod, err := helper.ensureX509ClientCertConfig(t.Context(), fakeClient, searchOwnerLabels(search, ""))
	require.NoError(t, err)
	operatorSecret, err := fakeClient.GetSecret(t.Context(), search.X509OperatorManagedSecret())
	require.NoError(t, err)
	assert.Empty(t, operatorSecret.OwnerReferences)
	assert.Equal(t, search.Name, operatorSecret.Labels[khandler.MongoDBSearchOwnerNameLabel])
	assert.Equal(t, search.Namespace, operatorSecret.Labels[khandler.MongoDBSearchOwnerNamespaceLabel])

	// Apply mongot modification to a config with both ReplicaSet and Router (sharded scenario)
	config := &mongot.Config{
		SyncSource: mongot.ConfigSyncSource{
			ReplicaSet: mongot.ConfigReplicaSet{
				ScramAuth: &mongot.ConfigScramAuth{
					Username:     "search-sync-source",
					PasswordFile: TempSourceUserPasswordPath,
					AuthSource:   ptr.To("admin"),
				},
			},
			Router: &mongot.ConfigRouter{
				HostAndPort: []string{"mongos-svc:27017"},
				ScramAuth: &mongot.ConfigScramAuth{
					Username:     "search-sync-source",
					PasswordFile: TempSourceUserPasswordPath,
				},
			},
		},
	}
	mongotMod(config)

	// ReplicaSet: scramAuth cleared, x509 cert path set
	assert.Nil(t, config.SyncSource.ReplicaSet.ScramAuth)
	require.NotNil(t, config.SyncSource.ReplicaSet.X509)
	require.NotNil(t, config.SyncSource.ReplicaSet.X509.TLSCertificateKeyFile)
	assert.True(t, strings.HasPrefix(*config.SyncSource.ReplicaSet.X509.TLSCertificateKeyFile, X509ClientCertOperatorMountPath))
	assert.True(t, strings.HasSuffix(*config.SyncSource.ReplicaSet.X509.TLSCertificateKeyFile, ".pem"))
	assert.Nil(t, config.SyncSource.ReplicaSet.X509.TLSCertificateKeyFilePasswordFile)

	// Router: same x509 modifications, cert path matches ReplicaSet
	assert.Nil(t, config.SyncSource.Router.ScramAuth)
	require.NotNil(t, config.SyncSource.Router.X509)
	require.NotNil(t, config.SyncSource.Router.X509.TLSCertificateKeyFile)
	assert.Equal(t, *config.SyncSource.ReplicaSet.X509.TLSCertificateKeyFile, *config.SyncSource.Router.X509.TLSCertificateKeyFile)
	assert.Nil(t, config.SyncSource.Router.X509.TLSCertificateKeyFilePasswordFile)

	// Apply STS modification and verify volumes
	sts := newBaseMongotStatefulSet()
	stsMod(sts)

	// Verify x509 volume exists and points to operator-managed secret
	var x509Volume *corev1.Volume
	for i := range sts.Spec.Template.Spec.Volumes {
		if sts.Spec.Template.Spec.Volumes[i].Name == "x509-client-cert" {
			x509Volume = &sts.Spec.Template.Spec.Volumes[i]
		}
	}
	require.NotNil(t, x509Volume, "x509-client-cert volume should exist")
	assert.Equal(t, "test-search-x509-client-cert", x509Volume.Secret.SecretName)

	// Verify x509 volume mount on mongot container
	mongotContainer := sts.Spec.Template.Spec.Containers[0]
	var x509Mount *corev1.VolumeMount
	for i := range mongotContainer.VolumeMounts {
		if mongotContainer.VolumeMounts[i].Name == "x509-client-cert" {
			x509Mount = &mongotContainer.VolumeMounts[i]
		}
	}
	require.NotNil(t, x509Mount, "x509-client-cert volume mount should exist")
	assert.Equal(t, X509ClientCertOperatorMountPath, x509Mount.MountPath)
	assert.True(t, x509Mount.ReadOnly)

	// No key password volume should exist
	for _, v := range sts.Spec.Template.Spec.Volumes {
		assert.NotEqual(t, "x509-key-password", v.Name, "x509-key-password volume should not exist")
	}
}

func TestEnsureX509ClientCertConfig_KeyPassword(t *testing.T) {
	search := newTestMongoDBSearch("test-search", "test-ns", func(s *searchv1.MongoDBSearch) {
		s.Spec.Source.X509 = &searchv1.X509Auth{
			ClientCertificateSecret: corev1.LocalObjectReference{Name: "x509-cert"},
			KeyFilePasswordSecret:   corev1.LocalObjectReference{Name: "x509-key-password-secret"},
		}
	})

	dbSource := &mockShardedSource{tlsConfig: &TLSSourceConfig{CAFileName: "ca-pem"}}

	// Cert secret holds only cert material; password lives in a dedicated secret.
	x509Secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "x509-cert", Namespace: "test-ns"},
		Data: map[string][]byte{
			"tls.crt": []byte("cert-data"),
			"tls.key": []byte("key-data"),
		},
	}
	keyPasswordSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "x509-key-password-secret", Namespace: "test-ns"},
		Data: map[string][]byte{
			KeyFilePasswordSecretKey: []byte("my-key-password"),
		},
	}

	fakeClient := newTestFakeClient(search, x509Secret, keyPasswordSecret)
	helper := NewMongoDBSearchReconcileHelper(fakeClient, search, dbSource, newTestOperatorSearchConfig(), nil, nil)

	mongotMod, stsMod, err := helper.ensureX509ClientCertConfig(t.Context(), fakeClient, searchOwnerLabels(search, ""))
	require.NoError(t, err)

	// Verify mongot config has key password path
	config := &mongot.Config{
		SyncSource: mongot.ConfigSyncSource{
			ReplicaSet: mongot.ConfigReplicaSet{
				ScramAuth: &mongot.ConfigScramAuth{
					Username:     "search-sync-source",
					PasswordFile: TempSourceUserPasswordPath,
				},
			},
		},
	}
	mongotMod(config)

	require.NotNil(t, config.SyncSource.ReplicaSet.X509)
	require.NotNil(t, config.SyncSource.ReplicaSet.X509.TLSCertificateKeyFilePasswordFile)
	assert.Equal(t, TempX509KeyPasswordPath, *config.SyncSource.ReplicaSet.X509.TLSCertificateKeyFilePasswordFile)

	// Verify STS has key password volume and mount
	sts := newBaseMongotStatefulSet()
	stsMod(sts)

	var keyPasswordVolume *corev1.Volume
	for i := range sts.Spec.Template.Spec.Volumes {
		if sts.Spec.Template.Spec.Volumes[i].Name == "x509-key-password" {
			keyPasswordVolume = &sts.Spec.Template.Spec.Volumes[i]
		}
	}
	require.NotNil(t, keyPasswordVolume, "x509-key-password volume should exist")
	assert.Equal(t, "x509-key-password-secret", keyPasswordVolume.Secret.SecretName)

	mongotContainer := sts.Spec.Template.Spec.Containers[0]
	var keyPasswordMount *corev1.VolumeMount
	for i := range mongotContainer.VolumeMounts {
		if mongotContainer.VolumeMounts[i].Name == "x509-key-password" {
			keyPasswordMount = &mongotContainer.VolumeMounts[i]
		}
	}
	require.NotNil(t, keyPasswordMount, "x509-key-password volume mount should exist")
	assert.Equal(t, X509KeyPasswordMountPath, keyPasswordMount.MountPath)
	assert.Equal(t, KeyFilePasswordSecretKey, keyPasswordMount.SubPath)

	// Verify prepend command for file permissions
	assert.True(t, len(mongotContainer.Args) > 0)
	argsJoined := strings.Join(mongotContainer.Args, " ")
	assert.Contains(t, argsJoined, "x509-key-password")
}

func TestKeyFilePasswordContentHash(t *testing.T) {
	newHelper := func(t *testing.T, search *searchv1.MongoDBSearch, secrets ...client.Object) (*MongoDBSearchReconcileHelper, kubernetesClient.Client) {
		t.Helper()
		objs := append([]client.Object{search}, secrets...)
		fakeClient := newTestFakeClient(objs...)
		dbSource := &mockShardedSource{tlsConfig: &TLSSourceConfig{CAFileName: "ca-pem"}}
		return NewMongoDBSearchReconcileHelper(fakeClient, search, dbSource, newTestOperatorSearchConfig(), nil, nil), fakeClient
	}

	passwordSecret := func(name, password string) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "test-ns"},
			Data:       map[string][]byte{KeyFilePasswordSecretKey: []byte(password)},
		}
	}

	t.Run("empty when no password secret configured", func(t *testing.T) {
		search := newTestMongoDBSearch("test-search", "test-ns")
		helper, kubeClient := newHelper(t, search)

		hash, err := helper.keyFilePasswordContentHash(t.Context(), kubeClient)
		require.NoError(t, err)
		assert.Empty(t, hash)
	})

	t.Run("non-empty and stable when configured", func(t *testing.T) {
		search := newTestMongoDBSearch("test-search", "test-ns", func(s *searchv1.MongoDBSearch) {
			s.Spec.Source.X509 = &searchv1.X509Auth{
				ClientCertificateSecret: corev1.LocalObjectReference{Name: "x509-cert"},
				KeyFilePasswordSecret:   corev1.LocalObjectReference{Name: "x509-key-password-secret"},
			}
		})
		helper, kubeClient := newHelper(t, search, passwordSecret("x509-key-password-secret", "pw-1"))

		hash, err := helper.keyFilePasswordContentHash(t.Context(), kubeClient)
		require.NoError(t, err)
		assert.NotEmpty(t, hash)

		again, err := helper.keyFilePasswordContentHash(t.Context(), kubeClient)
		require.NoError(t, err)
		assert.Equal(t, hash, again, "hash must be stable for unchanged content")
	})

	t.Run("changes when password content changes", func(t *testing.T) {
		mk := func(password string) string {
			search := newTestMongoDBSearch("test-search", "test-ns", func(s *searchv1.MongoDBSearch) {
				s.Spec.Source.X509 = &searchv1.X509Auth{
					ClientCertificateSecret: corev1.LocalObjectReference{Name: "x509-cert"},
					KeyFilePasswordSecret:   corev1.LocalObjectReference{Name: "x509-key-password-secret"},
				}
			})
			helper, kubeClient := newHelper(t, search, passwordSecret("x509-key-password-secret", password))
			hash, err := helper.keyFilePasswordContentHash(t.Context(), kubeClient)
			require.NoError(t, err)
			return hash
		}

		assert.NotEqual(t, mk("pw-1"), mk("pw-2"), "different password content must yield a different hash")
	})

	t.Run("errors when configured secret is missing", func(t *testing.T) {
		search := newTestMongoDBSearch("test-search", "test-ns", func(s *searchv1.MongoDBSearch) {
			s.Spec.Source.X509 = &searchv1.X509Auth{
				ClientCertificateSecret: corev1.LocalObjectReference{Name: "x509-cert"},
				KeyFilePasswordSecret:   corev1.LocalObjectReference{Name: "missing-secret"},
			}
		})
		helper, kubeClient := newHelper(t, search)

		_, err := helper.keyFilePasswordContentHash(t.Context(), kubeClient)
		require.Error(t, err)
	})
}

// newBaseMongotStatefulSet creates a minimal StatefulSet with a mongot container for testing modifications.
func newBaseMongotStatefulSet() *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		Spec: appsv1.StatefulSetSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:         MongotContainerName,
							Image:        "searchimage:tag",
							Args:         []string{"echo", "test"},
							VolumeMounts: []corev1.VolumeMount{},
						},
					},
					Volumes: []corev1.Volume{},
				},
			},
		},
	}
}

func TestReconcileSharded_CreatesPerShardResources(t *testing.T) {
	search := newTestMongoDBSearch("test-search", "test-ns")
	withAdvancedMongotConfigs(t, search, `{"indexing":{"lucene":{"fieldLimit":1000}}}`)

	shardedSource := &mockShardedSource{
		shardNames: []string{"my-cluster-0", "my-cluster-1"},
		hostSeeds: map[string][]string{
			"my-cluster-0": {"my-cluster-0-0.my-cluster-sh.test-ns.svc.cluster.local:27017"},
			"my-cluster-1": {"my-cluster-1-0.my-cluster-sh.test-ns.svc.cluster.local:27017"},
		},
	}

	fakeClient := newTestFakeClient(search)

	helper := NewMongoDBSearchReconcileHelper(
		fakeClient,
		search,
		shardedSource,
		newTestOperatorSearchConfig(),
		nil, nil,
	)

	// Pass 1: applies resources for ALL shards in a single pass, then the
	// readiness check sees none ready → returns Pending.
	result := helper.reconcile(t.Context(), zap.S())
	assert.False(t, result.IsOK())
	require.NoError(t, mock.MarkAllStatefulSetsAsReady(t.Context(), search.Namespace, fakeClient))

	// Pass 2: all shards already exist from pass 1 and are now Ready → OK.
	result = helper.reconcile(t.Context(), zap.S())
	assert.True(t, result.IsOK())

	// Verify per-shard Services
	for _, shardName := range shardedSource.GetShardNames() {
		svcNsName := search.MongotServiceForClusterShard(0, shardName)
		svc, err := fakeClient.GetService(t.Context(), svcNsName)
		require.NoError(t, err)

		assert.Equal(t, fmt.Sprintf("test-search-search-0-%s-svc", shardName), svc.Name)
		assert.Equal(t, "test-ns", svc.Namespace)
		assert.Equal(t, corev1.ClusterIPNone, svc.Spec.ClusterIP)
		assert.Equal(t, fmt.Sprintf("test-search-search-0-%s", shardName), svc.Spec.Selector["app"])

		portMap := make(map[string]int32)
		for _, p := range svc.Spec.Ports {
			portMap[p.Name] = p.Port
		}
		assert.Equal(t, int32(27028), portMap["mongot-grpc"])
		assert.Equal(t, int32(8080), portMap["healthcheck"])
	}

	// Verify per-shard StatefulSets
	for _, shardName := range shardedSource.GetShardNames() {
		stsNsName := search.MongotStatefulSetForClusterShard(0, shardName)
		sts, err := fakeClient.GetStatefulSet(t.Context(), stsNsName)
		require.NoError(t, err)

		assert.Equal(t, fmt.Sprintf("test-search-search-0-%s", shardName), sts.Name)
		assert.Equal(t, "test-ns", sts.Namespace)
		assert.Equal(t, shardName, sts.Labels["shard"])
	}

	// Verify per-shard ConfigMaps; each shard's config.yml carries the cluster's
	// advancedMongotConfigs verbatim (the block is per-cluster, identical across shards).
	for _, shardName := range shardedSource.GetShardNames() {
		cmNsName := search.MongotConfigMapForClusterShard(0, shardName)
		cm, err := fakeClient.GetConfigMap(t.Context(), cmNsName)
		require.NoError(t, err)

		assert.Equal(t, fmt.Sprintf("test-search-search-0-%s-config", shardName), cm.Name)
		assert.Contains(t, cm.Data, MongotConfigFilename)

		rendered := map[string]interface{}{}
		require.NoError(t, yaml.Unmarshal([]byte(cm.Data[MongotConfigFilename]), &rendered))
		assert.Equal(t, 1000, maputil.ReadMapValueAsInt(rendered, "advancedConfigs", "indexing", "lucene", "fieldLimit"))
	}
}

func TestCleanupStaleShardResources(t *testing.T) {
	search := newTestMongoDBSearch("test-search", "test-ns", func(s *searchv1.MongoDBSearch) {
		s.UID = "search-uid"
		s.Spec.Security = searchv1.Security{TLS: &searchv1.TLS{CertsSecretPrefix: "certs"}}
		s.Spec.Clusters[0].LoadBalancer = &searchv1.LoadBalancerConfig{Managed: &searchv1.ManagedLBConfig{}}
	})

	withOwner := func(labels map[string]string, owned bool) map[string]string {
		if owned {
			labels[khandler.MongoDBSearchOwnerNameLabel] = search.Name
			labels[khandler.MongoDBSearchOwnerNamespaceLabel] = search.Namespace
		}
		return labels
	}
	proxySvc := func(shard string, owned bool) *corev1.Service {
		return &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name: search.ProxyServiceNameForClusterShard(0, shard).Name, Namespace: "test-ns",
				Labels: withOwner(map[string]string{"component": proxyServiceComponent}, owned),
			},
			Spec: corev1.ServiceSpec{ClusterIP: "10.0.0.1", Ports: []corev1.ServicePort{{Port: 27028}}},
		}
	}
	mongotSTS := func(shard string, owned bool) *appsv1.StatefulSet {
		return &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{
			Name: search.MongotStatefulSetForClusterShard(0, shard).Name, Namespace: "test-ns", Labels: withOwner(map[string]string{}, owned),
		}}
	}
	mongotHeadless := func(shard string, owned bool) *corev1.Service {
		return &corev1.Service{ObjectMeta: metav1.ObjectMeta{
			Name: search.MongotServiceForClusterShard(0, shard).Name, Namespace: "test-ns", Labels: withOwner(map[string]string{"component": mongotComponent}, owned),
		}, Spec: corev1.ServiceSpec{ClusterIP: "10.0.0.2", Ports: []corev1.ServicePort{{Port: 27027}}}}
	}
	mongotCM := func(shard string, owned bool) *corev1.ConfigMap {
		return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
			Name: search.MongotConfigMapForClusterShard(0, shard).Name, Namespace: "test-ns", Labels: withOwner(map[string]string{"component": mongotComponent}, owned),
		}}
	}
	mongotTLSSecret := func(shard string, owned bool) *corev1.Secret {
		return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Name: search.TLSOperatorSecretForClusterShard(0, shard).Name, Namespace: "test-ns", Labels: withOwner(map[string]string{"component": mongotComponent}, owned),
		}}
	}

	fakeClient := newTestFakeClient(search,
		proxySvc("shard-0", true),  // active, owned
		proxySvc("shard-1", true),  // active, owned
		proxySvc("shard-2", true),  // stale, owned
		proxySvc("shard-x", false), // different owner
		mongotSTS("shard-1", true), mongotSTS("shard-2", true),
		mongotHeadless("shard-1", true), mongotHeadless("shard-2", true), mongotHeadless("shard-x", false),
		mongotCM("shard-1", true), mongotCM("shard-2", true), mongotCM("shard-x", false),
		mongotTLSSecret("shard-1", true), mongotTLSSecret("shard-2", true), mongotTLSSecret("shard-x", false),
		// Name collision: live shard "x"'s headless Svc name == stale "x-svc"'s STS name.
		// Separate per-kind expected sets keep them apart; a merged set would leak the stale STS.
		mongotHeadless("x", true), mongotSTS("x-svc", true),
	)
	helper := NewMongoDBSearchReconcileHelper(fakeClient, search, nil, newTestOperatorSearchConfig(), nil, nil)
	require.NoError(t, helper.cleanupStaleShardResources(t.Context(), zap.S(), []string{"shard-0", "shard-1", "x"}))

	_, err := fakeClient.GetService(t.Context(), search.ProxyServiceNameForClusterShard(0, "shard-0"))
	assert.NoError(t, err, "active shard preserved")
	_, err = fakeClient.GetService(t.Context(), search.ProxyServiceNameForClusterShard(0, "shard-1"))
	assert.NoError(t, err, "active shard preserved")
	_, err = fakeClient.GetService(t.Context(), search.ProxyServiceNameForClusterShard(0, "shard-2"))
	assert.Error(t, err, "stale shard deleted")
	_, err = fakeClient.GetService(t.Context(), search.ProxyServiceNameForClusterShard(0, "shard-x"))
	assert.NoError(t, err, "different owner untouched")

	gone := func(nn types.NamespacedName, obj client.Object, msg string) {
		assert.True(t, apierrors.IsNotFound(fakeClient.Get(t.Context(), nn, obj)), msg)
	}
	present := func(nn types.NamespacedName, obj client.Object, msg string) {
		assert.NoError(t, fakeClient.Get(t.Context(), nn, obj), msg)
	}
	present(search.MongotStatefulSetForClusterShard(0, "shard-1"), &appsv1.StatefulSet{}, "active shard STS preserved")
	gone(search.MongotStatefulSetForClusterShard(0, "shard-2"), &appsv1.StatefulSet{}, "stale shard STS deleted")
	present(search.MongotServiceForClusterShard(0, "shard-1"), &corev1.Service{}, "active shard headless Service preserved")
	gone(search.MongotServiceForClusterShard(0, "shard-2"), &corev1.Service{}, "stale shard headless Service deleted")
	present(search.MongotServiceForClusterShard(0, "shard-x"), &corev1.Service{}, "different-owner headless Service untouched")
	present(search.MongotConfigMapForClusterShard(0, "shard-1"), &corev1.ConfigMap{}, "active shard ConfigMap preserved")
	gone(search.MongotConfigMapForClusterShard(0, "shard-2"), &corev1.ConfigMap{}, "stale shard ConfigMap deleted")
	present(search.MongotConfigMapForClusterShard(0, "shard-x"), &corev1.ConfigMap{}, "different-owner ConfigMap untouched")
	present(search.TLSOperatorSecretForClusterShard(0, "shard-1"), &corev1.Secret{}, "active shard TLS operator Secret preserved")
	gone(search.TLSOperatorSecretForClusterShard(0, "shard-2"), &corev1.Secret{}, "stale shard TLS operator Secret deleted")
	present(search.TLSOperatorSecretForClusterShard(0, "shard-x"), &corev1.Secret{}, "different-owner TLS operator Secret untouched")

	present(search.MongotServiceForClusterShard(0, "x"), &corev1.Service{}, "live shard x headless Service preserved despite name-collision with stale x-svc STS")
	gone(search.MongotStatefulSetForClusterShard(0, "x-svc"), &appsv1.StatefulSet{}, "stale x-svc STS reaped despite name-collision with live x headless Service")
}

func TestReconcileReplicaSet_CreatesResources(t *testing.T) {
	search := newTestMongoDBSearch("test-search", "test-ns")
	mdbc := newTestMongoDBCommunity("test-mongodb", "test-ns")
	fakeClient := newTestFakeClient(search, mdbc)

	helper := NewMongoDBSearchReconcileHelper(
		fakeClient,
		search,
		NewCommunityResourceSearchSource(mdbc),
		newTestOperatorSearchConfig(),
		nil, nil,
	)

	// Pass 1: creates resources, returns Pending (StatefulSet not ready)
	result := helper.reconcile(t.Context(), zap.S())
	assert.False(t, result.IsOK())
	require.NoError(t, mock.MarkAllStatefulSetsAsReady(t.Context(), search.Namespace, fakeClient))

	// Pass 2: StatefulSet ready, returns OK
	result = helper.reconcile(t.Context(), zap.S())
	assert.True(t, result.IsOK())

	// Verify headless Service
	svcNsName := search.SearchServiceNamespacedNameForCluster(0)
	svc, err := fakeClient.GetService(t.Context(), svcNsName)
	require.NoError(t, err)

	assert.Equal(t, "test-search-search-0-svc", svc.Name)
	assert.Equal(t, "test-ns", svc.Namespace)
	assert.Equal(t, corev1.ClusterIPNone, svc.Spec.ClusterIP)
	assert.False(t, svc.Spec.PublishNotReadyAddresses)
	assert.Equal(t, "test-search-search-0-svc", svc.Spec.Selector["app"])
	assert.Equal(t, "test-search-search-0-svc", svc.Labels["app"])
	assert.Empty(t, svc.Labels["shard"])

	portMap := make(map[string]int32)
	for _, p := range svc.Spec.Ports {
		portMap[p.Name] = p.Port
	}
	assert.Equal(t, int32(27028), portMap["mongot-grpc"])
	assert.Equal(t, int32(8080), portMap["healthcheck"])

	// Verify StatefulSet
	stsNsName := search.StatefulSetNamespacedNameForCluster(0)
	sts, err := fakeClient.GetStatefulSet(t.Context(), stsNsName)
	require.NoError(t, err)

	assert.Equal(t, "test-search-search-0", sts.Name)
	assert.Equal(t, "test-ns", sts.Namespace)
	assert.Equal(t, "test-search-search-0-svc", sts.Labels["app"])
	assert.Empty(t, sts.Labels["shard"])
	assert.Empty(t, sts.OwnerReferences)
	assert.Equal(t, search.Name, sts.Labels[khandler.MongoDBSearchOwnerNameLabel])
	assert.Equal(t, search.Namespace, sts.Labels[khandler.MongoDBSearchOwnerNamespaceLabel])

	// Verify ConfigMap
	cmNsName := search.MongotConfigConfigMapNameForCluster(0)
	cm, err := fakeClient.GetConfigMap(t.Context(), cmNsName)
	require.NoError(t, err)

	assert.Equal(t, "test-search-search-0-config", cm.Name)
	assert.Contains(t, cm.Data, MongotConfigFilename)
}

type fakeExternalSource struct {
	hosts []string
}

func (f *fakeExternalSource) HostSeeds(_ string) ([]string, error) {
	return f.hosts, nil
}

func (f *fakeExternalSource) KeyfileSecretName() string {
	return ""
}

func (f *fakeExternalSource) TLSConfig() *TLSSourceConfig {
	return nil
}

func (f *fakeExternalSource) Validate() error {
	return nil
}

func (f *fakeExternalSource) ResourceType() mdbv1.ResourceType {
	return mdbv1.ReplicaSet
}

func TestBuildReplicaSetPlan_PerClusterUnitsForMC(t *testing.T) {
	mdb := newTestMongoDBSearch("mdb-search", "ns")
	mdb.Spec.Clusters = []searchv1.ClusterSpec{
		{Name: "cluster-a", Index: ptr.To(int32(0)), Replicas: ptr.To(int32(2))},
		// Pin the second cluster to 7 (!= its array position 1) so the assertions below
		// fail if the index ever comes from the loop position instead of the CRD pin.
		{Name: "cluster-b", Index: ptr.To(int32(7)), Replicas: ptr.To(int32(2))},
	}
	mdb.Spec.Source = &searchv1.MongoDBSource{
		ExternalMongoDBSource: &searchv1.ExternalMongoDBSource{
			HostAndPorts: []string{"a.example:27017", "b.example:27017"},
		},
	}

	r := &MongoDBSearchReconcileHelper{
		mdbSearch: mdb,
		state:     NewSearchDeploymentState(),
	}
	source := &fakeExternalSource{hosts: mdb.Spec.Source.ExternalMongoDBSource.HostAndPorts}

	plan, err := r.buildReplicaSetPlan(source)
	require.NoError(t, err)
	require.Len(t, plan.units, 2, "expected one unit per cluster")

	assert.Equal(t, "mdb-search-search-0", plan.units[0].stsName.Name)
	assert.Equal(t, "mdb-search-search-0-proxy-svc", plan.units[0].proxySvc.Name)
	assert.Equal(t, "cluster-a", plan.units[0].clusterName)
	assert.Equal(t, 0, plan.units[0].clusterIndex)

	assert.Equal(t, "mdb-search-search-7", plan.units[1].stsName.Name)
	assert.Equal(t, "mdb-search-search-7-proxy-svc", plan.units[1].proxySvc.Name)
	assert.Equal(t, "cluster-b", plan.units[1].clusterName)
	assert.Equal(t, 7, plan.units[1].clusterIndex)
}

func TestReplicationReaderTagSetsMod(t *testing.T) {
	secondaryPreferred := ptr.To("secondaryPreferred")
	tests := []struct {
		name     string
		selector *searchv1.SyncSourceSelector
		want     *mongot.ConfigReplicationReader
	}{
		{
			name:     "nil selector leaves base default untouched",
			selector: nil,
			want:     &mongot.ConfigReplicationReader{ReadPreference: secondaryPreferred},
		},
		{
			name:     "empty matchTagSets leaves base default untouched",
			selector: &searchv1.SyncSourceSelector{MatchTagSets: []map[string]string{}},
			want:     &mongot.ConfigReplicationReader{ReadPreference: secondaryPreferred},
		},
		{
			name:     "single tag set",
			selector: &searchv1.SyncSourceSelector{MatchTagSets: []map[string]string{{"region": "us-east"}}},
			want: &mongot.ConfigReplicationReader{
				ReadPreference: secondaryPreferred,
				TagSets:        [][]mongot.ConfigTag{{{Name: "region", Value: "us-east"}}},
			},
		},
		{
			name:     "multiple tags sorted by key",
			selector: &searchv1.SyncSourceSelector{MatchTagSets: []map[string]string{{"zone": "z1", "region": "us-east"}}},
			want: &mongot.ConfigReplicationReader{
				ReadPreference: secondaryPreferred,
				TagSets:        [][]mongot.ConfigTag{{{Name: "region", Value: "us-east"}, {Name: "zone", Value: "z1"}}},
			},
		},
		{
			name: "ordered tag sets with empty fallback set",
			selector: &searchv1.SyncSourceSelector{MatchTagSets: []map[string]string{
				{"zone": "z1", "region": "us-east"},
				{"region": "eu-west"},
				{},
			}},
			want: &mongot.ConfigReplicationReader{
				ReadPreference: secondaryPreferred,
				TagSets: [][]mongot.ConfigTag{
					{{Name: "region", Value: "us-east"}, {Name: "zone", Value: "z1"}},
					{{Name: "region", Value: "eu-west"}},
					{},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Seed the ReplicationReader default that baseMongotConfig always applies
			// first in the real Apply chain; the mod only populates its tagSets.
			cfg := mongot.Config{}
			cfg.SyncSource.ReplicationReader = &mongot.ConfigReplicationReader{ReadPreference: secondaryPreferred}
			replicationReaderTagSetsMod(tt.selector)(&cfg)
			assert.Equal(t, tt.want, cfg.SyncSource.ReplicationReader)
		})
	}
}

// Each cluster's mongot config carries that cluster's own matchTagSets; a cluster
// without a selector keeps the base config's match-any default (no tagSets).
func TestBuildReplicaSetPlan_PerClusterMatchTagSets(t *testing.T) {
	mdb := newTestMongoDBSearch("mdb-search", "ns")
	mdb.Spec.Clusters = []searchv1.ClusterSpec{
		{Name: "cluster-a", Index: ptr.To(int32(0)), Replicas: ptr.To(int32(1)), SyncSourceSelector: &searchv1.SyncSourceSelector{MatchTagSets: []map[string]string{{"region": "us-east"}}}},
		{Name: "cluster-b", Index: ptr.To(int32(1)), Replicas: ptr.To(int32(1))},
	}
	mdb.Spec.Source = &searchv1.MongoDBSource{
		ExternalMongoDBSource: &searchv1.ExternalMongoDBSource{HostAndPorts: []string{"a.example:27017"}},
	}

	r := &MongoDBSearchReconcileHelper{
		mdbSearch: mdb,
		state:     NewSearchDeploymentState(),
	}
	source := &fakeExternalSource{hosts: mdb.Spec.Source.ExternalMongoDBSource.HostAndPorts}

	plan, err := r.buildReplicaSetPlan(source)
	require.NoError(t, err)
	require.Len(t, plan.units, 2)

	cfgA := mongot.Config{}
	plan.units[0].mongotConfigFn(&cfgA)
	assert.Equal(t, &mongot.ConfigReplicationReader{
		ReadPreference: ptr.To("secondaryPreferred"),
		TagSets:        [][]mongot.ConfigTag{{{Name: "region", Value: "us-east"}}},
	}, cfgA.SyncSource.ReplicationReader)

	cfgB := mongot.Config{}
	plan.units[1].mongotConfigFn(&cfgB)
	assert.Equal(t, &mongot.ConfigReplicationReader{
		ReadPreference: ptr.To("secondaryPreferred"),
	}, cfgB.SyncSource.ReplicationReader)
}

func TestBuildReplicaSetPlan_SingleClusterUsesIndexZeroNames(t *testing.T) {
	mdb := newTestMongoDBSearch("mdb-search", "ns")
	mdb.Spec.Source = &searchv1.MongoDBSource{
		ExternalMongoDBSource: &searchv1.ExternalMongoDBSource{
			HostAndPorts: []string{"a.example:27017"},
		},
	}
	r := &MongoDBSearchReconcileHelper{mdbSearch: mdb, state: NewSearchDeploymentState()}
	source := &fakeExternalSource{hosts: mdb.Spec.Source.ExternalMongoDBSource.HostAndPorts}

	plan, err := r.buildReplicaSetPlan(source)
	require.NoError(t, err)
	require.Len(t, plan.units, 1)
	assert.Equal(t, "mdb-search-search-0", plan.units[0].stsName.Name)
	assert.Equal(t, "mdb-search-search-0-svc", plan.units[0].headlessSvc.Name)
	assert.Equal(t, "mdb-search-search-0-proxy-svc", plan.units[0].proxySvc.Name)
	assert.Equal(t, "mdb-search-search-0-config", plan.units[0].configMapName.Name)
}

// Each unit's resources must land on the member-cluster client matched by
// clusterName, never on the central client.
func TestReconcilePlan_UsesPerClusterClient(t *testing.T) {
	mdb := newTestMongoDBSearch("mdb-search", "ns")
	mdb.Spec.Clusters = []searchv1.ClusterSpec{
		{Name: "cluster-a", Index: ptr.To(int32(0)), Replicas: ptr.To(int32(2))},
		{Name: "cluster-b", Index: ptr.To(int32(1)), Replicas: ptr.To(int32(2))},
	}
	mdb.Spec.Source = &searchv1.MongoDBSource{
		ExternalMongoDBSource: &searchv1.ExternalMongoDBSource{
			HostAndPorts: []string{"a.example:27017"},
		},
	}

	centralClient := kubernetesClient.NewClient(mock.NewEmptyFakeClientBuilder().Build())
	clusterAClient := kubernetesClient.NewClient(mock.NewEmptyFakeClientBuilder().Build())
	clusterBClient := kubernetesClient.NewClient(mock.NewEmptyFakeClientBuilder().Build())

	memberClients := map[string]kubernetesClient.Client{
		"cluster-a": clusterAClient,
		"cluster-b": clusterBClient,
	}

	source := &fakeExternalSource{hosts: mdb.Spec.Source.ExternalMongoDBSource.HostAndPorts}

	r := &MongoDBSearchReconcileHelper{
		mdbSearch:            mdb,
		client:               centralClient,
		memberClusterClients: memberClients,
		state:                NewSearchDeploymentState(),
		operatorSearchConfig: newTestOperatorSearchConfig(),
		db:                   source,
	}

	plan, err := r.buildReplicaSetPlan(source)
	require.NoError(t, err)
	require.Len(t, plan.units, 2)

	for _, unit := range plan.units {
		_, _, err := r.applyReconcileUnit(t.Context(), zap.S(), plan, unit, reconcileUnitMods{})
		require.NoError(t, err)
	}

	stsA := &appsv1.StatefulSet{}
	require.NoError(t, clusterAClient.Get(t.Context(),
		types.NamespacedName{Name: "mdb-search-search-0", Namespace: "ns"}, stsA))

	stsB := &appsv1.StatefulSet{}
	require.NoError(t, clusterBClient.Get(t.Context(),
		types.NamespacedName{Name: "mdb-search-search-1", Namespace: "ns"}, stsB))

	// Also assert on the ConfigMap to catch ensureMongotConfig regressing to r.client.
	cmB := &corev1.ConfigMap{}
	require.NoError(t, clusterBClient.Get(t.Context(),
		types.NamespacedName{Name: "mdb-search-search-1-config", Namespace: "ns"}, cmB))

	stsCentral := &appsv1.StatefulSet{}
	err = centralClient.Get(t.Context(),
		types.NamespacedName{Name: "mdb-search-search-0", Namespace: "ns"}, stsCentral)
	assert.True(t, apierrors.IsNotFound(err), "central client must NOT have cluster-a STS")
	err = centralClient.Get(t.Context(),
		types.NamespacedName{Name: "mdb-search-search-1", Namespace: "ns"}, stsCentral)
	assert.True(t, apierrors.IsNotFound(err), "central client must NOT have cluster-b STS")

	err = clusterBClient.Get(t.Context(),
		types.NamespacedName{Name: "mdb-search-search-0", Namespace: "ns"}, &appsv1.StatefulSet{})
	assert.True(t, apierrors.IsNotFound(err), "cluster B client must NOT have cluster A STS")
}

// Sharded MC: buildShardedPlan emits one unit per (cluster, shard) plus one
// cluster-level proxy Service per cluster.
func TestBuildShardedPlan_PerClusterShardUnitsForMC(t *testing.T) {
	search := newTestMongoDBSearch("mdb-search", "ns")
	// MC requires managed LB; sharded + managed-LB requires TLS. ExternalHostname
	// must start with {shardName}. so the operator can derive the cluster-level
	// form, and must be distinct per cluster.
	search.Spec.Clusters = []searchv1.ClusterSpec{
		{Name: "cluster-a", Index: ptr.To(int32(0)), LoadBalancer: &searchv1.LoadBalancerConfig{Managed: &searchv1.ManagedLBConfig{
			ExternalHostname: "{shardName}.mdb-search-search-0-proxy-svc.ns.svc.cluster.local",
			RouterHostname:   "mdb-search-search-0-proxy-svc.ns.svc.cluster.local",
		}}},
		// Pin the second cluster to 7 (!= its array position 1) so the per-(cluster,shard)
		// assertions below fail if the index ever comes from the loop position.
		{Name: "cluster-b", Index: ptr.To(int32(7)), LoadBalancer: &searchv1.LoadBalancerConfig{Managed: &searchv1.ManagedLBConfig{
			ExternalHostname: "{shardName}.mdb-search-search-7-proxy-svc.ns.svc.cluster.local",
			RouterHostname:   "mdb-search-search-7-proxy-svc.ns.svc.cluster.local",
		}}},
	}

	shardedSource := &mockShardedSource{
		shardNames: []string{"sh-0", "sh-1"},
		hostSeeds: map[string][]string{
			"sh-0": {"sh-0-0.svc:27017"},
			"sh-1": {"sh-1-0.svc:27017"},
		},
	}

	r := &MongoDBSearchReconcileHelper{
		mdbSearch: search,
		db:        shardedSource,
		state:     NewSearchDeploymentState(),
	}

	plan, err := r.buildShardedPlan(shardedSource)
	require.NoError(t, err)

	require.Len(t, plan.units, 4, "expected one unit per (cluster, shard) pair")
	require.Len(t, plan.clusterLevelResources, 2, "expected one cluster-level resource per cluster")

	// (cluster-a, sh-0), (cluster-a, sh-1), (cluster-b, sh-0), (cluster-b, sh-1)
	expected := []struct {
		clusterName  string
		clusterIndex int
		shard        string
		stsName      string
	}{
		{"cluster-a", 0, "sh-0", "mdb-search-search-0-sh-0"},
		{"cluster-a", 0, "sh-1", "mdb-search-search-0-sh-1"},
		{"cluster-b", 7, "sh-0", "mdb-search-search-7-sh-0"},
		{"cluster-b", 7, "sh-1", "mdb-search-search-7-sh-1"},
	}
	for i, e := range expected {
		u := plan.units[i]
		assert.Equal(t, e.clusterName, u.clusterName, "unit %d clusterName", i)
		assert.Equal(t, e.clusterIndex, u.clusterIndex, "unit %d clusterIndex", i)
		assert.Equal(t, e.stsName, u.stsName.Name, "unit %d stsName", i)
		assert.Equal(t, e.shard, u.additionalSvcLabels[shardLabelKey])
	}

	assert.Equal(t, "cluster-a", plan.clusterLevelResources[0].clusterName)
	assert.Equal(t, 0, plan.clusterLevelResources[0].clusterIndex)
	assert.Equal(t, "mdb-search-search-0-proxy-svc", plan.clusterLevelResources[0].svcName.Name)
	assert.Equal(t, "cluster-b", plan.clusterLevelResources[1].clusterName)
	assert.Equal(t, 7, plan.clusterLevelResources[1].clusterIndex)
	assert.Equal(t, "mdb-search-search-7-proxy-svc", plan.clusterLevelResources[1].svcName.Name)
}

// A cluster's matchTagSets threads onto every shard unit of that cluster; a cluster
// without a selector keeps the base config's match-any default (no tagSets).
func TestBuildShardedPlan_PerClusterMatchTagSets(t *testing.T) {
	search := newTestMongoDBSearch("mdb-search", "ns")
	search.Spec.Clusters = []searchv1.ClusterSpec{
		{Name: "cluster-a", Index: ptr.To(int32(0)), SyncSourceSelector: &searchv1.SyncSourceSelector{MatchTagSets: []map[string]string{{"region": "us-east"}}}, LoadBalancer: &searchv1.LoadBalancerConfig{Managed: &searchv1.ManagedLBConfig{
			ExternalHostname: "{shardName}.mdb-search-search-0-proxy-svc.ns.svc.cluster.local",
			RouterHostname:   "mdb-search-search-0-proxy-svc.ns.svc.cluster.local",
		}}},
		{Name: "cluster-b", Index: ptr.To(int32(1)), LoadBalancer: &searchv1.LoadBalancerConfig{Managed: &searchv1.ManagedLBConfig{
			ExternalHostname: "{shardName}.mdb-search-search-1-proxy-svc.ns.svc.cluster.local",
			RouterHostname:   "mdb-search-search-1-proxy-svc.ns.svc.cluster.local",
		}}},
	}

	shardedSource := &mockShardedSource{
		shardNames: []string{"sh-0", "sh-1"},
		hostSeeds: map[string][]string{
			"sh-0": {"sh-0-0.svc:27017"},
			"sh-1": {"sh-1-0.svc:27017"},
		},
	}

	r := &MongoDBSearchReconcileHelper{
		mdbSearch: search,
		db:        shardedSource,
		state:     NewSearchDeploymentState(),
	}

	plan, err := r.buildShardedPlan(shardedSource)
	require.NoError(t, err)
	require.Len(t, plan.units, 4)

	taggedRR := &mongot.ConfigReplicationReader{
		ReadPreference: ptr.To("secondaryPreferred"),
		TagSets:        [][]mongot.ConfigTag{{{Name: "region", Value: "us-east"}}},
	}
	bareRR := &mongot.ConfigReplicationReader{ReadPreference: ptr.To("secondaryPreferred")}

	// units 0,1 = cluster-a (selector applies to both its shards); units 2,3 = cluster-b (no selector).
	want := []*mongot.ConfigReplicationReader{taggedRR, taggedRR, bareRR, bareRR}
	for i, w := range want {
		cfg := mongot.Config{}
		plan.units[i].mongotConfigFn(&cfg)
		assert.Equal(t, w, cfg.SyncSource.ReplicationReader, "unit %d (%s/%s) replicationReader", i, plan.units[i].clusterName, plan.units[i].additionalSvcLabels[shardLabelKey])
	}
}

// Sharded MC: per-(cluster, shard) STS + ConfigMap land on the matching
// member-cluster client, and the cluster-level proxy Service lands once per
// cluster. Central client receives nothing.
func TestReconcileShardedMC_FanOutUsesPerClusterClient(t *testing.T) {
	search := newTestMongoDBSearch("mdb-search", "ns")
	// MC requires managed LB; sharded + managed-LB requires TLS. ExternalHostname
	// must start with {shardName}. so the operator can derive the cluster-level
	// form, and must be distinct per cluster.
	search.Spec.Clusters = []searchv1.ClusterSpec{
		{Name: "cluster-a", Index: ptr.To(int32(0)), LoadBalancer: &searchv1.LoadBalancerConfig{Managed: &searchv1.ManagedLBConfig{
			ExternalHostname: "{shardName}.mdb-search-search-0-proxy-svc.ns.svc.cluster.local",
			RouterHostname:   "mdb-search-search-0-proxy-svc.ns.svc.cluster.local",
		}}},
		{Name: "cluster-b", Index: ptr.To(int32(1)), LoadBalancer: &searchv1.LoadBalancerConfig{Managed: &searchv1.ManagedLBConfig{
			ExternalHostname: "{shardName}.mdb-search-search-1-proxy-svc.ns.svc.cluster.local",
			RouterHostname:   "mdb-search-search-1-proxy-svc.ns.svc.cluster.local",
		}}},
	}

	shardedSource := &mockShardedSource{
		shardNames: []string{"sh-0", "sh-1"},
		hostSeeds: map[string][]string{
			"sh-0": {"sh-0-0.svc:27017"},
			"sh-1": {"sh-1-0.svc:27017"},
		},
	}

	centralClient := kubernetesClient.NewClient(mock.NewEmptyFakeClientBuilder().Build())
	clusterAClient := kubernetesClient.NewClient(mock.NewEmptyFakeClientBuilder().Build())
	clusterBClient := kubernetesClient.NewClient(mock.NewEmptyFakeClientBuilder().Build())

	r := &MongoDBSearchReconcileHelper{
		mdbSearch: search,
		db:        shardedSource,
		client:    centralClient,
		memberClusterClients: map[string]kubernetesClient.Client{
			"cluster-a": clusterAClient,
			"cluster-b": clusterBClient,
		},
		state:                NewSearchDeploymentState(),
		operatorSearchConfig: newTestOperatorSearchConfig(),
	}

	plan, err := r.buildShardedPlan(shardedSource)
	require.NoError(t, err)

	for _, unit := range plan.units {
		_, _, err := r.applyReconcileUnit(t.Context(), zap.S(), plan, unit, reconcileUnitMods{})
		require.NoError(t, err)
	}
	// Mirror reconcile()'s cluster-level proxy Service pass.
	for _, res := range plan.clusterLevelResources {
		clusterClient, err := r.clientForCluster(res.clusterName)
		require.NoError(t, err)
		require.NoError(t, r.ensureSearchService(t.Context(), zap.S(), clusterClient, res.svcName, buildClusterLevelProxyService(r.mdbSearch, res)))
	}

	// Per-(cluster, shard) STS + ConfigMap + per-shard proxy Service on the right client.
	cases := []struct {
		c        kubernetesClient.Client
		stsName  string
		cmName   string
		proxySvc string
	}{
		{clusterAClient, "mdb-search-search-0-sh-0", "mdb-search-search-0-sh-0-config", "mdb-search-search-0-sh-0-proxy-svc"},
		{clusterAClient, "mdb-search-search-0-sh-1", "mdb-search-search-0-sh-1-config", "mdb-search-search-0-sh-1-proxy-svc"},
		{clusterBClient, "mdb-search-search-1-sh-0", "mdb-search-search-1-sh-0-config", "mdb-search-search-1-sh-0-proxy-svc"},
		{clusterBClient, "mdb-search-search-1-sh-1", "mdb-search-search-1-sh-1-config", "mdb-search-search-1-sh-1-proxy-svc"},
	}
	for _, tc := range cases {
		require.NoError(t, tc.c.Get(t.Context(),
			types.NamespacedName{Name: tc.stsName, Namespace: "ns"}, &appsv1.StatefulSet{}),
			"STS %s missing on expected member client", tc.stsName)
		require.NoError(t, tc.c.Get(t.Context(),
			types.NamespacedName{Name: tc.cmName, Namespace: "ns"}, &corev1.ConfigMap{}),
			"ConfigMap %s missing on expected member client", tc.cmName)
		require.NoError(t, tc.c.Get(t.Context(),
			types.NamespacedName{Name: tc.proxySvc, Namespace: "ns"}, &corev1.Service{}),
			"per-shard proxy Service %s missing on expected member client", tc.proxySvc)
		err := centralClient.Get(t.Context(),
			types.NamespacedName{Name: tc.proxySvc, Namespace: "ns"}, &corev1.Service{})
		assert.True(t, apierrors.IsNotFound(err), "central client must NOT have per-shard proxy %s", tc.proxySvc)
	}

	// Central client must not have any per-shard STS.
	for _, name := range []string{
		"mdb-search-search-0-sh-0", "mdb-search-search-0-sh-1",
		"mdb-search-search-1-sh-0", "mdb-search-search-1-sh-1",
	} {
		err := centralClient.Get(t.Context(),
			types.NamespacedName{Name: name, Namespace: "ns"}, &appsv1.StatefulSet{})
		assert.True(t, apierrors.IsNotFound(err), "central client must NOT have %s", name)
	}

	// Cluster-level proxy Service: one per cluster, on the right client.
	for _, res := range plan.clusterLevelResources {
		c := clusterAClient
		if res.clusterName == "cluster-b" {
			c = clusterBClient
		}
		svc := &corev1.Service{}
		require.NoError(t, c.Get(t.Context(), res.svcName, svc),
			"cluster-level proxy svc %s missing on %s", res.svcName.Name, res.clusterName)
		// Service must be created via the per-cluster client, not central.
		err := centralClient.Get(t.Context(), res.svcName, &corev1.Service{})
		assert.True(t, apierrors.IsNotFound(err),
			"central client must NOT have cluster-level proxy svc %s", res.svcName.Name)
	}
	for _, res := range plan.clusterLevelResources {
		c := clusterAClient
		if res.clusterName == "cluster-a" {
			c = clusterBClient
		}
		err := c.Get(t.Context(), res.svcName, &corev1.Service{})
		assert.True(t, apierrors.IsNotFound(err),
			"cross-cluster leak: %s should not exist on %s", res.svcName.Name,
			map[bool]string{true: "cluster-b", false: "cluster-a"}[c == clusterBClient])
	}
}

// TestReconcileShardedMC_AllUnitsAppliedBeforeReadinessCheck guards against a
// regression where the apply-and-check loop short-circuits on the first
// not-ready StatefulSet and skips creating resources for subsequent
// (cluster, shard) units. After one reconcile, every (cluster, shard)
// StatefulSet must exist on the correct member-cluster client even though
// none of them can become Ready under a fake client.
func TestReconcileShardedMC_AllUnitsAppliedBeforeReadinessCheck(t *testing.T) {
	search := newTestMongoDBSearch("mdb-search", "ns")
	// MC requires managed LB; sharded + managed-LB requires TLS. ExternalHostname
	// must start with {shardName}. so the operator can derive the cluster-level
	// form, and must be distinct per cluster.
	search.Spec.Clusters = []searchv1.ClusterSpec{
		{Name: "cluster-a", Index: ptr.To(int32(0)), LoadBalancer: &searchv1.LoadBalancerConfig{Managed: &searchv1.ManagedLBConfig{
			ExternalHostname: "{shardName}.mdb-search-search-0-proxy-svc.ns.svc.cluster.local",
			RouterHostname:   "mdb-search-search-0-proxy-svc.ns.svc.cluster.local",
		}}},
		{Name: "cluster-b", Index: ptr.To(int32(1)), LoadBalancer: &searchv1.LoadBalancerConfig{Managed: &searchv1.ManagedLBConfig{
			ExternalHostname: "{shardName}.mdb-search-search-1-proxy-svc.ns.svc.cluster.local",
			RouterHostname:   "mdb-search-search-1-proxy-svc.ns.svc.cluster.local",
		}}},
	}
	search.Spec.Source = &searchv1.MongoDBSource{
		ExternalMongoDBSource: &searchv1.ExternalMongoDBSource{
			ShardedCluster: &searchv1.ExternalShardedClusterConfig{
				Router: searchv1.ExternalRouterConfig{Hosts: []string{"mongos.example:27017"}},
				Shards: []searchv1.ExternalShardConfig{
					{ShardName: "sh-0", Hosts: []string{"sh-0-a.example:27017"}},
					{ShardName: "sh-1", Hosts: []string{"sh-1-a.example:27017"}},
					{ShardName: "sh-2", Hosts: []string{"sh-2-a.example:27017"}},
				},
			},
		},
	}
	search.Spec.Security = searchv1.Security{
		TLS: &searchv1.TLS{CertsSecretPrefix: "certs"},
	}

	shardedSource := &mockShardedSource{
		shardNames: []string{"sh-0", "sh-1", "sh-2"},
		hostSeeds: map[string][]string{
			"sh-0": {"sh-0-a.example:27017"},
			"sh-1": {"sh-1-a.example:27017"},
			"sh-2": {"sh-2-a.example:27017"},
		},
	}

	// Pre-create per-(cluster, shard) TLS secrets on each member cluster so the
	// preflight TLS-presence check passes; otherwise reconcile would return
	// Pending before ever entering the apply loop and the regression we're
	// guarding against (apply-vs-check order) wouldn't be exercised.
	tlsSecretsForCluster := func(clusterIndex int) []client.Object {
		out := make([]client.Object, 0, 3)
		for _, shard := range []string{"sh-0", "sh-1", "sh-2"} {
			out = append(out, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("certs-mdb-search-search-%d-%s-cert", clusterIndex, shard),
					Namespace: "ns",
				},
				Data: map[string][]byte{
					"tls.crt": []byte("dummy-cert"),
					"tls.key": []byte("dummy-key"),
				},
			})
		}
		return out
	}

	centralClient := kubernetesClient.NewClient(mock.NewEmptyFakeClientBuilder().WithObjects(search).Build())
	clusterAClient := kubernetesClient.NewClient(mock.NewEmptyFakeClientBuilder().WithObjects(tlsSecretsForCluster(0)...).Build())
	clusterBClient := kubernetesClient.NewClient(mock.NewEmptyFakeClientBuilder().WithObjects(tlsSecretsForCluster(1)...).Build())

	r := &MongoDBSearchReconcileHelper{
		mdbSearch: search,
		db:        shardedSource,
		client:    centralClient,
		memberClusterClients: map[string]kubernetesClient.Client{
			"cluster-a": clusterAClient,
			"cluster-b": clusterBClient,
		},
		state:                NewSearchDeploymentState(),
		operatorSearchConfig: newTestOperatorSearchConfig(),
	}

	st := r.reconcile(t.Context(), zap.S())

	// Fake clients never mark a StatefulSet Ready, so reconcile must report
	// non-OK. The point of this test is the side effect: every (cluster, shard)
	// STS must have been applied BEFORE the readiness check fired.
	require.False(t, st.IsOK(), "expected non-OK status with fake STS, got %v", st)

	expectedSTS := []struct {
		c    kubernetesClient.Client
		name string
	}{
		{clusterAClient, "mdb-search-search-0-sh-0"},
		{clusterAClient, "mdb-search-search-0-sh-1"},
		{clusterAClient, "mdb-search-search-0-sh-2"},
		{clusterBClient, "mdb-search-search-1-sh-0"},
		{clusterBClient, "mdb-search-search-1-sh-1"},
		{clusterBClient, "mdb-search-search-1-sh-2"},
	}
	for _, exp := range expectedSTS {
		err := exp.c.Get(t.Context(), types.NamespacedName{Name: exp.name, Namespace: "ns"}, &appsv1.StatefulSet{})
		require.NoError(t, err, "STS %s must have been created before the readiness short-circuit fired", exp.name)
	}

	// Cluster-level proxy Services deferred until LB is Ready.
	for _, exp := range []struct {
		c    kubernetesClient.Client
		name string
	}{
		{clusterAClient, "mdb-search-search-0-proxy-svc"},
		{clusterBClient, "mdb-search-search-1-proxy-svc"},
	} {
		err := exp.c.Get(t.Context(), types.NamespacedName{Name: exp.name, Namespace: "ns"}, &corev1.Service{})
		require.True(t, apierrors.IsNotFound(err),
			"cluster-level proxy Service %s must NOT be created while LB is not ready", exp.name)
	}
}

// mcShardedFixture bundles the canonical 2-cluster × 3-shard MC sharded setup
// (managed LB, TLS, pre-staged per-(cluster, shard) TLS secrets) used by the
// multi-test scenarios below.
type mcShardedFixture struct {
	search  *searchv1.MongoDBSearch
	source  *mockShardedSource
	central kubernetesClient.Client
	members map[string]kubernetesClient.Client
}

func (f *mcShardedFixture) clusterIndex(name string) int {
	for _, c := range f.search.Spec.Clusters {
		if c.Name == name {
			return int(ptr.Deref(c.Index, 0))
		}
	}
	return 0
}

func newMCShardedFixture(t *testing.T) *mcShardedFixture {
	t.Helper()
	search := newTestMongoDBSearch("mdb-search", "ns")
	// MC requires managed LB; sharded + managed-LB requires TLS. ExternalHostname
	// must start with {shardName}. so the operator can derive the cluster-level
	// form, and must be distinct per cluster.
	search.Spec.Clusters = []searchv1.ClusterSpec{
		{Name: "cluster-a", Index: ptr.To(int32(0)), LoadBalancer: &searchv1.LoadBalancerConfig{Managed: &searchv1.ManagedLBConfig{
			ExternalHostname: "{shardName}.mdb-search-search-0-proxy-svc.ns.svc.cluster.local",
			RouterHostname:   "mdb-search-search-0-proxy-svc.ns.svc.cluster.local",
		}}},
		{Name: "cluster-b", Index: ptr.To(int32(1)), LoadBalancer: &searchv1.LoadBalancerConfig{Managed: &searchv1.ManagedLBConfig{
			ExternalHostname: "{shardName}.mdb-search-search-1-proxy-svc.ns.svc.cluster.local",
			RouterHostname:   "mdb-search-search-1-proxy-svc.ns.svc.cluster.local",
		}}},
	}
	search.Spec.Source = &searchv1.MongoDBSource{
		ExternalMongoDBSource: &searchv1.ExternalMongoDBSource{
			ShardedCluster: &searchv1.ExternalShardedClusterConfig{
				Router: searchv1.ExternalRouterConfig{Hosts: []string{"mongos.example:27017"}},
				Shards: []searchv1.ExternalShardConfig{
					{ShardName: "sh-0", Hosts: []string{"sh-0-a.example:27017"}},
					{ShardName: "sh-1", Hosts: []string{"sh-1-a.example:27017"}},
					{ShardName: "sh-2", Hosts: []string{"sh-2-a.example:27017"}},
				},
			},
		},
	}
	search.Spec.Security = searchv1.Security{
		TLS: &searchv1.TLS{CertsSecretPrefix: "certs"},
	}

	source := &mockShardedSource{
		shardNames: []string{"sh-0", "sh-1", "sh-2"},
		hostSeeds: map[string][]string{
			"sh-0": {"sh-0-a.example:27017"},
			"sh-1": {"sh-1-a.example:27017"},
			"sh-2": {"sh-2-a.example:27017"},
		},
	}

	tlsSecretsForCluster := func(clusterIndex int) []client.Object {
		out := make([]client.Object, 0, 3)
		for _, shard := range []string{"sh-0", "sh-1", "sh-2"} {
			out = append(out, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("certs-mdb-search-search-%d-%s-cert", clusterIndex, shard),
					Namespace: "ns",
				},
				Data: map[string][]byte{"tls.crt": []byte("dummy-cert"), "tls.key": []byte("dummy-key")},
			})
		}
		return out
	}

	return &mcShardedFixture{
		search:  search,
		source:  source,
		central: kubernetesClient.NewClient(mock.NewEmptyFakeClientBuilder().WithObjects(search).Build()),
		members: map[string]kubernetesClient.Client{
			"cluster-a": kubernetesClient.NewClient(mock.NewEmptyFakeClientBuilder().WithObjects(tlsSecretsForCluster(0)...).Build()),
			"cluster-b": kubernetesClient.NewClient(mock.NewEmptyFakeClientBuilder().WithObjects(tlsSecretsForCluster(1)...).Build()),
		},
	}
}

func (f *mcShardedFixture) newHelper() *MongoDBSearchReconcileHelper {
	return &MongoDBSearchReconcileHelper{
		mdbSearch:            f.search,
		db:                   f.source,
		client:               f.central,
		memberClusterClients: f.members,
		state:                NewSearchDeploymentState(),
		operatorSearchConfig: newTestOperatorSearchConfig(),
	}
}

// TestReconcileShardedMC_MultiPass walks the full lifecycle:
//
//	pass 1: applies all (cluster, shard) units, returns Pending (STS not ready)
//	then mark all STSs ready
//	pass 2: returns Pending (LB not ready — Envoy controller hasn't run)
//	then mark LB status ready (simulating Envoy controller's effect)
//	pass 3: returns OK
//
// Across the three passes, the resource counts must stay stable (idempotent).
func TestReconcileShardedMC_MultiPass(t *testing.T) {
	fx := newMCShardedFixture(t)
	helper := fx.newHelper()

	countResources := func(c kubernetesClient.Client) (sts, cm, svc int) {
		var stsList appsv1.StatefulSetList
		require.NoError(t, c.List(t.Context(), &stsList, client.InNamespace("ns")))
		var cmList corev1.ConfigMapList
		require.NoError(t, c.List(t.Context(), &cmList, client.InNamespace("ns")))
		var svcList corev1.ServiceList
		require.NoError(t, c.List(t.Context(), &svcList, client.InNamespace("ns")))
		return len(stsList.Items), len(cmList.Items), len(svcList.Items)
	}

	// Pass 1: applies everything, STSs not yet Ready.
	st := helper.reconcile(t.Context(), zap.S())
	require.False(t, st.IsOK(), "pass 1 must be Pending (STSs not ready)")
	stsA, cmA, svcA := countResources(fx.members["cluster-a"])
	stsB, cmB, svcB := countResources(fx.members["cluster-b"])
	require.Equal(t, 3, stsA, "pass 1: cluster-a should have 3 mongot STSs (one per shard)")
	require.Equal(t, 3, stsB, "pass 1: cluster-b should have 3 mongot STSs")
	require.Equal(t, 3, cmA, "pass 1: cluster-a should have 3 mongot ConfigMaps")
	require.Equal(t, 3, cmB)
	// 3 per-shard headless + 3 per-shard proxy = 6 per cluster (cluster-level proxy deferred until LB Ready).
	require.Equal(t, 6, svcA, "pass 1: cluster-a should have 6 Services")
	require.Equal(t, 6, svcB)

	// Mark all STSs ready (across all 3 clients — central is empty for this path
	// but include it for consistency).
	require.NoError(t, mock.MarkAllStatefulSetsAsReady(t.Context(), "ns",
		fx.central, fx.members["cluster-a"], fx.members["cluster-b"]))

	// Pass 2: STSs ready, but Status.LoadBalancer not set, so IsLoadBalancerReady=false.
	st = helper.reconcile(t.Context(), zap.S())
	require.False(t, st.IsOK(), "pass 2 must still be Pending (LB not ready)")
	require.Contains(t, MessageFromStatus(st), "load balancer", "pass 2 Pending must cite the LB, not STS readiness")

	stsA2, cmA2, svcA2 := countResources(fx.members["cluster-a"])
	stsB2, cmB2, svcB2 := countResources(fx.members["cluster-b"])
	require.Equal(t, stsA, stsA2, "pass 2: no duplicate STSs created on cluster-a")
	require.Equal(t, stsB, stsB2, "pass 2: no duplicate STSs created on cluster-b")
	require.Equal(t, cmA, cmA2)
	require.Equal(t, cmB, cmB2)
	require.Equal(t, svcA, svcA2)
	require.Equal(t, svcB, svcB2)

	// Simulate the Envoy controller marking the LB Running.
	fx.search.Status.LoadBalancer = &searchv1.LoadBalancerStatus{Phase: status.PhaseRunning}

	// Pass 3: everything ready → OK.
	st = helper.reconcile(t.Context(), zap.S())
	require.True(t, st.IsOK(), "pass 3 must be OK, got: %s", MessageFromStatus(st))

	stsA3, cmA3, svcA3 := countResources(fx.members["cluster-a"])
	stsB3, cmB3, svcB3 := countResources(fx.members["cluster-b"])
	require.Equal(t, stsA, stsA3)
	require.Equal(t, stsB, stsB3)
	require.Equal(t, cmA, cmA3)
	require.Equal(t, cmB, cmB3)
	require.Equal(t, svcA+1, svcA3, "pass 3: cluster-level proxy Service now created")
	require.Equal(t, svcB+1, svcB3)
}

// clusterStatusesFromStatus extracts the per-cluster status list carried in a
// workflow.Status's options (reconcile() returns options rather than mutating the
// CR; the CR write happens later in Reconcile() via UpdateStatus).
func clusterStatusesFromStatus(t *testing.T, st workflow.Status) []searchv1.ClusterStatus {
	t.Helper()
	opt, ok := status.GetOption(st.StatusOptions(), searchv1.MongoDBSearchClusterStatusesOption{})
	if !ok {
		return nil
	}
	return opt.(searchv1.MongoDBSearchClusterStatusesOption).Statuses
}

// clusterStatusByIndex finds the ClusterStatus for a given clusterIndex.
func clusterStatusByIndex(t *testing.T, statuses []searchv1.ClusterStatus, idx int) searchv1.ClusterStatus {
	t.Helper()
	for _, cs := range statuses {
		if cs.Index == idx {
			return cs
		}
	}
	t.Fatalf("no ClusterStatus with clusterIndex %d in %+v", idx, statuses)
	return searchv1.ClusterStatus{}
}

func readyDeploymentStatus(desired int32) appsv1.DeploymentStatus {
	return appsv1.DeploymentStatus{
		ReadyReplicas:      desired,
		UpdatedReplicas:    desired,
		Replicas:           desired,
		ObservedGeneration: 0,
	}
}

// markEnvoyDeploymentReady creates the managed Envoy Deployment for a cluster in a
// fully-rolled-out ready state, simulating what the envoy controller would have written.
func markEnvoyDeploymentReady(t *testing.T, c kubernetesClient.Client, search *searchv1.MongoDBSearch, clusterIndex int) {
	t.Helper()
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      search.LoadBalancerDeploymentNameForCluster(clusterIndex),
			Namespace: search.Namespace,
		},
		Spec:   appsv1.DeploymentSpec{Replicas: ptr.To(int32(1))},
		Status: readyDeploymentStatus(1),
	}
	require.NoError(t, c.Create(t.Context(), dep))
}

// markMetricsForwarderDeploymentReady creates the metrics-forwarder Deployment for a
// cluster in a fully-rolled-out ready state, simulating what the metrics-forwarder
// controller would have written.
func markMetricsForwarderDeploymentReady(t *testing.T, c kubernetesClient.Client, search *searchv1.MongoDBSearch, clusterIndex int) {
	t.Helper()
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      search.MetricsForwarderDeploymentNameForCluster(clusterIndex),
			Namespace: search.Namespace,
		},
		Spec:   appsv1.DeploymentSpec{Replicas: ptr.To(int32(1))},
		Status: readyDeploymentStatus(1),
	}
	require.NoError(t, c.Create(t.Context(), dep))
}

// TestReconcileShardedMC_PerClusterStatus verifies the per-cluster status surface:
// every cluster appears (not just failing ones), shards roll up into a single
// per-cluster Search phase via worst-of, and the managed-LB phase is read from the
// Envoy Deployment independently of the top-level phase gating.
func TestReconcileShardedMC_PerClusterStatus(t *testing.T) {
	fx := newMCShardedFixture(t)
	helper := fx.newHelper()

	// Pass 1: STSs just applied, none ready, no Envoy Deployments yet.
	st := helper.reconcile(t.Context(), zap.S())
	require.False(t, st.IsOK())
	statuses := clusterStatusesFromStatus(t, st)
	require.Len(t, statuses, 2, "both clusters must be listed, not just the first failing one")

	for _, idx := range []int{0, 1} {
		cs := clusterStatusByIndex(t, statuses, idx)
		require.Equal(t, status.PhasePending, cs.Search, "cluster %d search should be Pending (STSs not ready)", idx)
		require.NotEmpty(t, cs.SearchMessage, "cluster %d should carry a search message while not ready", idx)
		// Envoy Deployment absent → LoadBalancer reported Pending, informational only.
		require.Equal(t, status.PhasePending, cs.LoadBalancer, "cluster %d LB should be Pending (deployment not created yet)", idx)
	}
	require.Equal(t, "cluster-a", clusterStatusByIndex(t, statuses, 0).Name)
	require.Equal(t, "cluster-b", clusterStatusByIndex(t, statuses, 1).Name)

	// Make all mongot STSs ready and create ready Envoy Deployments in both members.
	require.NoError(t, mock.MarkAllStatefulSetsAsReady(t.Context(), "ns",
		fx.central, fx.members["cluster-a"], fx.members["cluster-b"]))
	markEnvoyDeploymentReady(t, fx.members["cluster-a"], fx.search, 0)
	markEnvoyDeploymentReady(t, fx.members["cluster-b"], fx.search, 1)
	fx.search.Status.LoadBalancer = &searchv1.LoadBalancerStatus{Phase: status.PhaseRunning}

	// Pass 2: everything ready → both clusters Running on both halves.
	st = helper.reconcile(t.Context(), zap.S())
	require.True(t, st.IsOK(), "expected OK, got: %s", MessageFromStatus(st))
	statuses = clusterStatusesFromStatus(t, st)
	require.Len(t, statuses, 2)
	for _, idx := range []int{0, 1} {
		cs := clusterStatusByIndex(t, statuses, idx)
		require.Equal(t, status.PhaseRunning, cs.Search, "cluster %d search should be Running", idx)
		require.Empty(t, cs.SearchMessage, "cluster %d search message should clear when Running", idx)
		require.Equal(t, status.PhaseRunning, cs.LoadBalancer, "cluster %d LB should be Running", idx)
		require.Empty(t, cs.LoadBalancerMessage)
	}
}

// TestReconcileShardedMC_PerClusterMetricsForwarderStatus verifies that
// when the forwarder is enabled, each cluster reports its own metricsForwarder phase
// (read from that cluster's forwarder Deployment), it transitions Pending->Running independently.
func TestReconcileShardedMC_PerClusterMetricsForwarderStatus(t *testing.T) {
	fx := newMCShardedFixture(t)
	// Force the metrics forwarder on (fixture uses an external source, so Auto would
	// leave it disabled without an OpsManager block).
	fx.search.Spec.Observability.MetricsForwarder.Mode = searchv1.MetricsForwarderModeEnabled
	helper := fx.newHelper()

	// Pass 1: mongot STSs + Envoy ready, but no metrics-forwarder Deployments yet.
	_ = helper.reconcile(t.Context(), zap.S())
	require.NoError(t, mock.MarkAllStatefulSetsAsReady(t.Context(), "ns",
		fx.central, fx.members["cluster-a"], fx.members["cluster-b"]))
	markEnvoyDeploymentReady(t, fx.members["cluster-a"], fx.search, 0)
	markEnvoyDeploymentReady(t, fx.members["cluster-b"], fx.search, 1)
	fx.search.Status.LoadBalancer = &searchv1.LoadBalancerStatus{Phase: status.PhaseRunning}

	st := helper.reconcile(t.Context(), zap.S())
	// Metrics forwarder is informational: even though its Deployments are absent
	// (Pending), the top-level phase must be OK (search + LB ready).
	require.True(t, st.IsOK(), "metrics forwarder must not gate top-level phase; got: %s", MessageFromStatus(st))
	statuses := clusterStatusesFromStatus(t, st)
	require.Len(t, statuses, 2)
	for _, idx := range []int{0, 1} {
		cs := clusterStatusByIndex(t, statuses, idx)
		require.Equal(t, status.PhaseRunning, cs.Search, "cluster %d search Running", idx)
		require.Equal(t, status.PhasePending, cs.MetricsForwarder,
			"cluster %d metricsForwarder Pending (deployment not created yet)", idx)
		require.NotEmpty(t, cs.MetricsForwarderMessage, "cluster %d should carry a metrics-forwarder message while not ready", idx)
	}

	// Create ready metrics-forwarder Deployments in both members.
	markMetricsForwarderDeploymentReady(t, fx.members["cluster-a"], fx.search, 0)
	markMetricsForwarderDeploymentReady(t, fx.members["cluster-b"], fx.search, 1)

	// Pass 2: every per-cluster half Running.
	st = helper.reconcile(t.Context(), zap.S())
	require.True(t, st.IsOK(), "expected OK, got: %s", MessageFromStatus(st))
	statuses = clusterStatusesFromStatus(t, st)
	require.Len(t, statuses, 2)
	for _, idx := range []int{0, 1} {
		cs := clusterStatusByIndex(t, statuses, idx)
		require.Equal(t, status.PhaseRunning, cs.MetricsForwarder, "cluster %d metricsForwarder Running", idx)
		require.Empty(t, cs.MetricsForwarderMessage, "cluster %d metrics-forwarder message clears when Running", idx)
	}
}

// TestReconcileShardedMC_PerClusterMetricsForwarderDisabled verifies that when the
// metrics forwarder is disabled, the per-cluster metricsForwarder phase is left empty
// (omitted), mirroring how loadBalancer is empty when no managed LB is configured.
func TestReconcileShardedMC_PerClusterMetricsForwarderDisabled(t *testing.T) {
	fx := newMCShardedFixture(t)
	fx.search.Spec.Observability.MetricsForwarder.Mode = searchv1.MetricsForwarderModeDisabled
	helper := fx.newHelper()

	_ = helper.reconcile(t.Context(), zap.S())
	require.NoError(t, mock.MarkAllStatefulSetsAsReady(t.Context(), "ns",
		fx.central, fx.members["cluster-a"], fx.members["cluster-b"]))
	markEnvoyDeploymentReady(t, fx.members["cluster-a"], fx.search, 0)
	markEnvoyDeploymentReady(t, fx.members["cluster-b"], fx.search, 1)
	fx.search.Status.LoadBalancer = &searchv1.LoadBalancerStatus{Phase: status.PhaseRunning}

	st := helper.reconcile(t.Context(), zap.S())
	require.True(t, st.IsOK(), "expected OK, got: %s", MessageFromStatus(st))
	statuses := clusterStatusesFromStatus(t, st)
	require.Len(t, statuses, 2)
	for _, idx := range []int{0, 1} {
		cs := clusterStatusByIndex(t, statuses, idx)
		require.Empty(t, cs.MetricsForwarder, "cluster %d metricsForwarder must be empty when disabled", idx)
		require.Empty(t, cs.MetricsForwarderMessage, "cluster %d metrics-forwarder message must be empty when disabled", idx)
	}
}

func TestReconcileShardedMC_PerClusterLoadBalancerMidRollout(t *testing.T) {
	fx := newMCShardedFixture(t)
	helper := fx.newHelper()

	_ = helper.reconcile(t.Context(), zap.S())
	require.NoError(t, mock.MarkAllStatefulSetsAsReady(t.Context(), "ns",
		fx.central, fx.members["cluster-a"], fx.members["cluster-b"]))

	// cluster-a: Envoy fully rolled out (ready). cluster-b: mid-rollout — enough ready
	// pods to satisfy a naive check, but they are the old generation.
	markEnvoyDeploymentReady(t, fx.members["cluster-a"], fx.search, 0)
	depB := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:       fx.search.LoadBalancerDeploymentNameForCluster(1),
			Namespace:  fx.search.Namespace,
			Generation: 2, // spec advanced to gen 2
		},
		Spec: appsv1.DeploymentSpec{Replicas: ptr.To(int32(1))},
		Status: appsv1.DeploymentStatus{
			ReadyReplicas:      1, // 1 ready pod, but it is the OLD ReplicaSet:
			UpdatedReplicas:    0, // no pods on the new spec yet
			Replicas:           1,
			ObservedGeneration: 1, // controller has not observed gen 2 yet
		},
	}
	require.NoError(t, fx.members["cluster-b"].Create(t.Context(), depB))

	st := helper.reconcile(t.Context(), zap.S())
	statuses := clusterStatusesFromStatus(t, st)
	require.Len(t, statuses, 2)

	csA := clusterStatusByIndex(t, statuses, 0)
	require.Equal(t, status.PhaseRunning, csA.LoadBalancer, "cluster 0 Envoy fully rolled out → Running")

	csB := clusterStatusByIndex(t, statuses, 1)
	require.Equal(t, status.PhasePending, csB.LoadBalancer,
		"cluster 1 Envoy mid-rollout (ready pods are old generation) → must be Pending, not Running")
	require.NotEmpty(t, csB.LoadBalancerMessage, "mid-rollout should carry a message explaining why")
}

// TestReconcileShardedMC_PerClusterStatusWorstOfShards verifies that when one shard
// in a cluster is ready and another is not, the cluster's rolled-up Search phase is
// the worst of the two (Pending), proving shard roll-up rather than first-loss-wins.
func TestReconcileShardedMC_PerClusterStatusWorstOfShards(t *testing.T) {
	fx := newMCShardedFixture(t)
	helper := fx.newHelper()

	// Apply everything.
	_ = helper.reconcile(t.Context(), zap.S())

	// Mark ALL of cluster-a ready, but only mark cluster-b's central client ready
	// (cluster-b's member STSs stay not-ready).
	require.NoError(t, mock.MarkAllStatefulSetsAsReady(t.Context(), "ns", fx.members["cluster-a"]))
	markEnvoyDeploymentReady(t, fx.members["cluster-a"], fx.search, 0)
	markEnvoyDeploymentReady(t, fx.members["cluster-b"], fx.search, 1)

	st := helper.reconcile(t.Context(), zap.S())
	require.False(t, st.IsOK(), "must be Pending while cluster-b shards are not ready")

	statuses := clusterStatusesFromStatus(t, st)
	require.Len(t, statuses, 2)
	require.Equal(t, status.PhaseRunning, clusterStatusByIndex(t, statuses, 0).Search, "cluster-a all shards ready")
	require.Equal(t, status.PhasePending, clusterStatusByIndex(t, statuses, 1).Search, "cluster-b has not-ready shards → worst-of Pending")
}

// TestReconcileShardedMC_MissingMemberClusterClient verifies that when a
// member cluster named in spec.clusters has no entry in memberClusterClients
// (e.g. an operator that joined the cluster mid-flight but hasn't been
// reconfigured yet), reconcile surfaces a clear error naming the missing
// cluster instead of silently writing to the central client.
func TestReconcileShardedMC_MissingMemberClusterClient(t *testing.T) {
	fx := newMCShardedFixture(t)
	// Drop cluster-b so the helper has no client for it.
	delete(fx.members, "cluster-b")

	helper := fx.newHelper()

	st := helper.reconcile(t.Context(), zap.S())
	require.False(t, st.IsOK(), "must not be OK when a referenced member cluster has no client")
	msg := MessageFromStatus(st)
	require.Contains(t, msg, "cluster-b", "error must name the missing cluster, got: %s", msg)

	// And critically: no cluster-b resources leaked to the central client.
	var stsList appsv1.StatefulSetList
	require.NoError(t, fx.central.List(t.Context(), &stsList, client.InNamespace("ns")))
	for _, sts := range stsList.Items {
		require.NotContains(t, sts.Name, "search-1-", "cluster-b STS %q must NOT have been created on the central client", sts.Name)
	}
}

// cleanupStaleShardResources must reach into member clusters in MC sharded
// mode — the per-shard proxy Services live on the member clients, not central.
func TestCleanupStaleShardResources_MCFanOut(t *testing.T) {
	search := newTestMongoDBSearch("mdb-search", "ns", func(s *searchv1.MongoDBSearch) {
		s.UID = "search-uid"
		s.Spec.Security = searchv1.Security{TLS: &searchv1.TLS{CertsSecretPrefix: "certs"}}
		s.Spec.Clusters = []searchv1.ClusterSpec{
			{Name: "cluster-a", Index: ptr.To(int32(0)), LoadBalancer: &searchv1.LoadBalancerConfig{Managed: &searchv1.ManagedLBConfig{}}},
			{Name: "cluster-b", Index: ptr.To(int32(1)), LoadBalancer: &searchv1.LoadBalancerConfig{Managed: &searchv1.ManagedLBConfig{}}},
		}
	})

	// memberProxySvc builds a proxy Service with the search-owner labels the MC
	// cleanup path looks for. owned=false drops the labels — must be left alone.
	memberProxySvc := func(name string, owned bool) *corev1.Service {
		labels := map[string]string{"component": proxyServiceComponent}
		if owned {
			labels[khandler.MongoDBSearchOwnerNameLabel] = search.Name
			labels[khandler.MongoDBSearchOwnerNamespaceLabel] = search.Namespace
		}
		return &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns", Labels: labels},
			Spec:       corev1.ServiceSpec{ClusterIP: "10.0.0.1", Ports: []corev1.ServicePort{{Port: 27028}}},
		}
	}

	// owned=false drops the owner labels, so the sweep must leave these alone.
	withMemberOwner := func(labels map[string]string, owned bool) map[string]string {
		if owned {
			labels[khandler.MongoDBSearchOwnerNameLabel] = search.Name
			labels[khandler.MongoDBSearchOwnerNamespaceLabel] = search.Namespace
		}
		return labels
	}
	memberSTS := func(idx int, shard string, owned bool) *appsv1.StatefulSet {
		return &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{
			Name: search.MongotStatefulSetForClusterShard(idx, shard).Name, Namespace: "ns", Labels: withMemberOwner(map[string]string{}, owned),
		}}
	}
	memberMongotSvc := func(idx int, shard string, owned bool) *corev1.Service {
		return &corev1.Service{ObjectMeta: metav1.ObjectMeta{
			Name: search.MongotServiceForClusterShard(idx, shard).Name, Namespace: "ns", Labels: withMemberOwner(map[string]string{"component": mongotComponent}, owned),
		}, Spec: corev1.ServiceSpec{ClusterIP: "10.0.0.3", Ports: []corev1.ServicePort{{Port: 27027}}}}
	}
	memberCM := func(idx int, shard string, owned bool) *corev1.ConfigMap {
		return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
			Name: search.MongotConfigMapForClusterShard(idx, shard).Name, Namespace: "ns", Labels: withMemberOwner(map[string]string{"component": mongotComponent}, owned),
		}}
	}
	memberTLSSecret := func(idx int, shard string, owned bool) *corev1.Secret {
		return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Name: search.TLSOperatorSecretForClusterShard(idx, shard).Name, Namespace: "ns", Labels: withMemberOwner(map[string]string{"component": mongotComponent}, owned),
		}}
	}

	// cluster-a (idx 0): keep shard-0 + cluster-level; sh-stale should be deleted.
	clusterA := kubernetesClient.NewClient(mock.NewEmptyFakeClientBuilder().WithObjects(
		memberProxySvc("mdb-search-search-0-sh-0-proxy-svc", true),
		memberProxySvc("mdb-search-search-0-sh-stale-proxy-svc", true),
		memberProxySvc("mdb-search-search-0-proxy-svc", true),
		memberProxySvc("foreign-svc", false), // not search-owned
		memberSTS(0, "sh-0", true), memberSTS(0, "sh-stale", true),
		memberMongotSvc(0, "sh-0", true), memberMongotSvc(0, "sh-stale", true), memberMongotSvc(0, "sh-foreign", false),
		memberCM(0, "sh-0", true), memberCM(0, "sh-stale", true),
		memberTLSSecret(0, "sh-0", true), memberTLSSecret(0, "sh-stale", true), memberTLSSecret(0, "sh-foreign", false),
	).Build())
	// cluster-b (idx 1): same pattern, plus an unrelated owned-by-other-CR svc
	// to guard the label-name check.
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
		state:                NewSearchDeploymentState(),
	}

	require.NoError(t, r.cleanupStaleShardResources(t.Context(), zap.S(), []string{"sh-0"}))

	// Per-cluster expectations: active + cluster-level kept, stale deleted, foreign untouched.
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

		err := tc.c.Get(t.Context(), types.NamespacedName{Name: active, Namespace: "ns"}, &corev1.Service{})
		require.NoError(t, err, "%s: active per-shard proxy Service must survive cleanup", tc.cluster)
		err = tc.c.Get(t.Context(), types.NamespacedName{Name: clusterLevel, Namespace: "ns"}, &corev1.Service{})
		require.NoError(t, err, "%s: cluster-level proxy Service must survive cleanup", tc.cluster)
		err = tc.c.Get(t.Context(), types.NamespacedName{Name: stale, Namespace: "ns"}, &corev1.Service{})
		require.True(t, apierrors.IsNotFound(err), "%s: stale per-shard proxy Service must be deleted, got err=%v", tc.cluster, err)
		err = tc.c.Get(t.Context(), types.NamespacedName{Name: tc.foreign, Namespace: "ns"}, &corev1.Service{})
		require.NoError(t, err, "%s: foreign Service %q must be untouched", tc.cluster, tc.foreign)
	}

	present := func(name string, obj client.Object, what string) {
		require.NoError(t, clusterA.Get(t.Context(), types.NamespacedName{Name: name, Namespace: "ns"}, obj), "cluster-a: %s must survive cleanup", what)
	}
	gone := func(name string, obj client.Object, what string) {
		err := clusterA.Get(t.Context(), types.NamespacedName{Name: name, Namespace: "ns"}, obj)
		require.True(t, apierrors.IsNotFound(err), "cluster-a: %s must be deleted, got err=%v", what, err)
	}
	present(search.MongotStatefulSetForClusterShard(0, "sh-0").Name, &appsv1.StatefulSet{}, "active mongot StatefulSet")
	gone(search.MongotStatefulSetForClusterShard(0, "sh-stale").Name, &appsv1.StatefulSet{}, "stale mongot StatefulSet")
	present(search.MongotServiceForClusterShard(0, "sh-0").Name, &corev1.Service{}, "active headless Service")
	gone(search.MongotServiceForClusterShard(0, "sh-stale").Name, &corev1.Service{}, "stale headless Service")
	present(search.MongotServiceForClusterShard(0, "sh-foreign").Name, &corev1.Service{}, "unowned headless Service")
	present(search.MongotConfigMapForClusterShard(0, "sh-0").Name, &corev1.ConfigMap{}, "active mongot ConfigMap")
	gone(search.MongotConfigMapForClusterShard(0, "sh-stale").Name, &corev1.ConfigMap{}, "stale mongot ConfigMap")
	present(search.TLSOperatorSecretForClusterShard(0, "sh-0").Name, &corev1.Secret{}, "active shard TLS operator Secret")
	gone(search.TLSOperatorSecretForClusterShard(0, "sh-stale").Name, &corev1.Secret{}, "stale shard TLS operator Secret")
	present(search.TLSOperatorSecretForClusterShard(0, "sh-foreign").Name, &corev1.Secret{}, "unowned shard TLS operator Secret")
}

// Empty GetShardNames() yields an empty work list. The reconciler must not fail
// noisily on the empty-shard-names case; the preflight step is a no-op and reconcile
// should reach an OK terminal state with no per-shard resources created.
// (Production-shaped sources never return empty
// — this is a defensive boundary check.)
func TestReconcileSharded_EmptyShardNames(t *testing.T) {
	search := newTestMongoDBSearch("mdb-search", "ns", func(s *searchv1.MongoDBSearch) {
		s.Spec.Source = &searchv1.MongoDBSource{
			ExternalMongoDBSource: &searchv1.ExternalMongoDBSource{
				ShardedCluster: &searchv1.ExternalShardedClusterConfig{
					Router: searchv1.ExternalRouterConfig{Hosts: []string{"mongos.example:27017"}},
					Shards: []searchv1.ExternalShardConfig{
						{ShardName: "ignored", Hosts: []string{"ignored:27017"}},
					},
				},
			},
		}
	})

	// emptyShardedSource implements SearchSourceShardedDeployment but reports
	// zero shards — the empty boundary case.
	src := &mockShardedSource{shardNames: nil}
	central := kubernetesClient.NewClient(mock.NewEmptyFakeClientBuilder().WithObjects(search).Build())

	r := &MongoDBSearchReconcileHelper{
		mdbSearch:            search,
		db:                   src,
		client:               central,
		operatorSearchConfig: newTestOperatorSearchConfig(),
		state:                NewSearchDeploymentState(),
	}

	plan, err := r.buildShardedPlan(src)
	require.NoError(t, err)
	require.Empty(t, plan.units, "no shards → no reconcile units")
	require.Empty(t, plan.clusterLevelResources, "no shards → no cluster-level proxy Services")

	// Reconcile must converge to OK; no STSs / proxy svcs created.
	st := r.reconcile(t.Context(), zap.S())
	require.True(t, st.IsOK(), "empty-shards reconcile must be OK, got: %s", MessageFromStatus(st))

	var stsList appsv1.StatefulSetList
	require.NoError(t, central.List(t.Context(), &stsList, client.InNamespace("ns")))
	require.Empty(t, stsList.Items, "no STSs must be created when shardNames is empty")

	var svcList corev1.ServiceList
	require.NoError(t, central.List(t.Context(), &svcList, client.InNamespace("ns")))
	for _, svc := range svcList.Items {
		require.NotContains(t, svc.Name, "-proxy-svc", "no proxy Services must be created when shardNames is empty, got %q", svc.Name)
	}
}

// The Search controller patches /status and the Envoy controller patches
// /status/loadBalancer — different JSON-patch paths, so they must converge to
// a consistent CR even when reconciles interleave. Tested deterministically by
// alternating reconciler calls and asserting the
// final CR has both phases set as expected, neither having clobbered the other.
func TestReconcileShardedMC_InterleavedStatusConverges(t *testing.T) {
	// The full controller path is exercised in
	// TestMongoDBSearchReconcile_MCSharded_CrossControllerLabelInvariant; here
	// we focus on what the helper-level reconcile writes to /status when LB
	// status is being mutated under it.
	fx := newMCShardedFixture(t)
	helper := fx.newHelper()

	// Pass 1: search applies units, returns Pending (STS not ready).
	st := helper.reconcile(t.Context(), zap.S())
	require.False(t, st.IsOK())

	// Simulate Envoy controller writing /status/loadBalancer = Running BEFORE
	// search marks STSs ready — i.e. interleaved order.
	got := &searchv1.MongoDBSearch{}
	require.NoError(t, fx.central.Get(t.Context(), fx.search.NamespacedName(), got))
	got.Status.LoadBalancer = &searchv1.LoadBalancerStatus{Phase: status.PhaseRunning}
	require.NoError(t, fx.central.Status().Update(t.Context(), got))

	// Pass 2 with STSs still not ready: search reconcile must NOT clobber
	// the LB substatus when patching /status.
	st = helper.reconcile(t.Context(), zap.S())
	require.False(t, st.IsOK(), "pass 2 must still be Pending (STSs not ready)")
	require.NoError(t, fx.central.Get(t.Context(), fx.search.NamespacedName(), got))
	require.NotNil(t, got.Status.LoadBalancer, "LB sub-status must survive search /status patch")
	require.Equal(t, status.PhaseRunning, got.Status.LoadBalancer.Phase,
		"LB sub-status must remain Running after search patch")

	// Now mark STSs ready under the helper.
	require.NoError(t, mock.MarkAllStatefulSetsAsReady(t.Context(), "ns",
		fx.central, fx.members["cluster-a"], fx.members["cluster-b"]))
	// Also have to mutate the in-memory fx.search to mirror the centrally-stored
	// LB substatus, since the helper reads Status.LoadBalancer off the in-memory
	// CR (not a re-fetch) when deciding IsLoadBalancerReady.
	fx.search.Status.LoadBalancer = &searchv1.LoadBalancerStatus{Phase: status.PhaseRunning}

	st = helper.reconcile(t.Context(), zap.S())
	require.True(t, st.IsOK(), "final reconcile must reach OK, got: %s", MessageFromStatus(st))

	// Final CR has both pieces of status intact.
	require.NoError(t, fx.central.Get(t.Context(), fx.search.NamespacedName(), got))
	require.NotNil(t, got.Status.LoadBalancer, "LB sub-status still present")
	require.Equal(t, status.PhaseRunning, got.Status.LoadBalancer.Phase)
}

// Routing-readiness switch lifecycle: shards stay pending until their mongot STS
// first meets the threshold, the switch survives STS delete/recreate (one-way),
// and stale switch entries are pruned.
func TestReconcileSharded_RoutingSwitchOneWay(t *testing.T) {
	search := newTestMongoDBSearch("mdb-search", "ns", func(s *searchv1.MongoDBSearch) {
		s.Spec.Source = &searchv1.MongoDBSource{
			ExternalMongoDBSource: &searchv1.ExternalMongoDBSource{
				ShardedCluster: &searchv1.ExternalShardedClusterConfig{
					Router: searchv1.ExternalRouterConfig{Hosts: []string{"mongos.example:27017"}},
					Shards: []searchv1.ExternalShardConfig{
						{ShardName: "sh-0", Hosts: []string{"sh-0-a.example:27017"}},
						{ShardName: "sh-1", Hosts: []string{"sh-1-a.example:27017"}},
					},
				},
			},
		}
		s.Spec.Clusters[0].LoadBalancer = &searchv1.LoadBalancerConfig{
			Managed: &searchv1.ManagedLBConfig{
				ExternalHostname: "{shardName}.mdb-search-search-0-proxy-svc.ns.svc.cluster.local",
				RouterHostname:   "mdb-search-search-0-proxy-svc.ns.svc.cluster.local",
			},
		}
		s.Spec.Security = searchv1.Security{TLS: &searchv1.TLS{CertsSecretPrefix: "certs"}}
	})

	source := &mockShardedSource{
		shardNames: []string{"sh-0", "sh-1"},
		hostSeeds: map[string][]string{
			"sh-0": {"sh-0-a.example:27017"},
			"sh-1": {"sh-1-a.example:27017"},
		},
	}

	objects := []client.Object{search}
	for _, shard := range source.shardNames {
		objects = append(objects, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("certs-mdb-search-search-0-%s-cert", shard),
				Namespace: "ns",
			},
			Data: map[string][]byte{"tls.crt": []byte("dummy-cert"), "tls.key": []byte("dummy-key")},
		})
	}
	fakeClient := newTestFakeClient(objects...)

	// Pre-existing switch entry for a shard that no longer exists — must be pruned.
	state := NewSearchDeploymentState()
	state.RoutingReadyMongotGroups = []string{"sh-removed"}
	helper := NewMongoDBSearchReconcileHelper(fakeClient, search, source, newTestOperatorSearchConfig(), nil, state)
	switchedOn := func(shard string) bool { return slices.Contains(helper.state.RoutingReadyMongotGroups, shard) }

	// Pass 1: STSs created, none routing-ready.
	helper.Reconcile(t.Context(), zap.S())
	assert.False(t, switchedOn("sh-0"))
	assert.False(t, switchedOn("sh-removed"), "switch entry for a removed shard must be pruned")

	// sh-0 reaches the routing-readiness threshold (sh-1 stays unready).
	stsName := search.MongotStatefulSetForClusterShard(0, "sh-0")
	sts, err := fakeClient.GetStatefulSet(t.Context(), stsName)
	require.NoError(t, err)
	sts.Status.ReadyReplicas = 1
	require.NoError(t, fakeClient.Status().Update(t.Context(), &sts))

	// Pass 2: sh-0's switch flips on even though sh-1 is still unready.
	helper.Reconcile(t.Context(), zap.S())
	assert.True(t, switchedOn("sh-0"))
	assert.False(t, switchedOn("sh-1"))

	// STS recreate: pass 3 recreates sh-0's STS with 0 ready replicas. The switch
	// is one-way, so sh-0 must NOT re-enter the pending set.
	require.NoError(t, fakeClient.Delete(t.Context(), &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: stsName.Name, Namespace: stsName.Namespace},
	}))
	helper.Reconcile(t.Context(), zap.S())
	recreated, err := fakeClient.GetStatefulSet(t.Context(), stsName)
	require.NoError(t, err)
	require.Equal(t, int32(0), recreated.Status.ReadyReplicas, "recreated STS must start unready")
	assert.True(t, switchedOn("sh-0"), "switch must survive STS delete/recreate")
}

// One unit's switch error must not starve the remaining units: errors
// are aggregated across ALL units and surfaced after the loop.
func TestReconcileSharded_SwitchErrorsAggregatedAcrossUnits(t *testing.T) {
	search := newTestMongoDBSearch("mdb-search", "ns", func(s *searchv1.MongoDBSearch) {
		s.Spec.Source = &searchv1.MongoDBSource{
			ExternalMongoDBSource: &searchv1.ExternalMongoDBSource{
				ShardedCluster: &searchv1.ExternalShardedClusterConfig{
					Router: searchv1.ExternalRouterConfig{Hosts: []string{"mongos.example:27017"}},
					Shards: []searchv1.ExternalShardConfig{
						{ShardName: "sh-0", Hosts: []string{"sh-0-a.example:27017"}},
						{ShardName: "sh-1", Hosts: []string{"sh-1-a.example:27017"}},
					},
				},
			},
		}
		s.Spec.Clusters[0].LoadBalancer = &searchv1.LoadBalancerConfig{
			Managed: &searchv1.ManagedLBConfig{
				ExternalHostname: "{shardName}.mdb-search-search-0-proxy-svc.ns.svc.cluster.local",
				RouterHostname:   "mdb-search-search-0-proxy-svc.ns.svc.cluster.local",
			},
		}
		s.Spec.Security = searchv1.Security{TLS: &searchv1.TLS{CertsSecretPrefix: "certs"}}
	})

	source := &mockShardedSource{
		shardNames: []string{"sh-0", "sh-1"},
		hostSeeds: map[string][]string{
			"sh-0": {"sh-0-a.example:27017"},
			"sh-1": {"sh-1-a.example:27017"},
		},
	}

	objects := []client.Object{search}
	for _, shard := range source.shardNames {
		objects = append(objects, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("certs-mdb-search-search-0-%s-cert", shard),
				Namespace: "ns",
			},
			Data: map[string][]byte{"tls.crt": []byte("dummy-cert"), "tls.key": []byte("dummy-key")},
		})
	}
	// Fail any state-CM write that would flip sh-0's switch; sh-1's writes pass through.
	injectedErr := fmt.Errorf("injected switch write failure for sh-0")
	flipsSh0 := func(obj client.Object) bool {
		cm, ok := obj.(*corev1.ConfigMap)
		if !ok || cm.Name != SearchStateCMName(search) {
			return false
		}
		var st SearchDeploymentState
		if json.Unmarshal([]byte(cm.Data[searchStateKey]), &st) != nil {
			return false
		}
		return slices.Contains(st.RoutingReadyMongotGroups, "sh-0")
	}
	base := mock.NewEmptyFakeClientBuilder().WithObjects(objects...).Build()
	fakeClient := kubernetesClient.NewClient(interceptor.NewClient(base, interceptor.Funcs{
		Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if flipsSh0(obj) {
				return injectedErr
			}
			return cl.Create(ctx, obj, opts...)
		},
		Update: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if flipsSh0(obj) {
				return injectedErr
			}
			return cl.Update(ctx, obj, opts...)
		},
	}))
	helper := NewMongoDBSearchReconcileHelper(fakeClient, search, source, newTestOperatorSearchConfig(), nil, nil)
	switchedOn := func(shard string) bool { return slices.Contains(helper.state.RoutingReadyMongotGroups, shard) }

	// Both shards meet the threshold; sh-0's switch write fails.
	helper.reconcile(t.Context(), zap.S())
	require.NoError(t, mock.MarkAllStatefulSetsAsReady(t.Context(), "ns", fakeClient))
	st := helper.reconcile(t.Context(), zap.S())

	require.False(t, st.IsOK())
	assert.Contains(t, MessageFromStatus(st), "injected switch write failure for sh-0")
	assert.True(t, switchedOn("sh-1"), "sh-1's switch must still flip despite sh-0's error")
	assert.False(t, switchedOn("sh-0"))
}

func TestReconcileSharded_StatefulSetTemplateStableAcrossReconciles(t *testing.T) {
	// With spec.clusters[].statefulSet set, the override merge sorts volumes by
	// name; applied mid-pipeline it produced a different volume order on the
	// create vs update paths, so the first reconcile after STS creation saw a
	// spurious template diff and rolled every mongot pod.
	search := newTestMongoDBSearch("test-search", "test-ns", func(s *searchv1.MongoDBSearch) {
		s.Spec.Clusters[0].StatefulSetConfiguration = &v1.StatefulSetConfiguration{
			SpecWrapper: v1.StatefulSetSpecWrapper{
				Spec: appsv1.StatefulSetSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							InitContainers: []corev1.Container{
								{Name: "startup-delay", Image: "busybox", Command: []string{"sh", "-c", "sleep 1"}},
							},
						},
					},
				},
			},
		}
	})

	shardedSource := &mockShardedSource{
		shardNames: []string{"my-cluster-0"},
		hostSeeds: map[string][]string{
			"my-cluster-0": {"my-cluster-0-0.my-cluster-sh.test-ns.svc.cluster.local:27017"},
		},
	}
	fakeClient := newTestFakeClient(search)
	helper := NewMongoDBSearchReconcileHelper(fakeClient, search, shardedSource, newTestOperatorSearchConfig(), nil, nil)

	helper.reconcile(t.Context(), zap.S())
	stsNsName := search.MongotStatefulSetForClusterShard(0, "my-cluster-0")
	created, err := fakeClient.GetStatefulSet(t.Context(), stsNsName)
	require.NoError(t, err)
	require.NoError(t, mock.MarkAllStatefulSetsAsReady(t.Context(), search.Namespace, fakeClient))

	helper.reconcile(t.Context(), zap.S())
	updated, err := fakeClient.GetStatefulSet(t.Context(), stsNsName)
	require.NoError(t, err)

	assert.Equal(t, created.Spec.Template, updated.Spec.Template,
		"second reconcile must not change the pod template (a diff rolls every mongot pod)")
	require.Len(t, updated.Spec.Template.Spec.InitContainers, 1)
	assert.Equal(t, "startup-delay", updated.Spec.Template.Spec.InitContainers[0].Name)
}

// TestReconcileShardedMC_ShardOverrideReplicas drives the full reconcile with
// shardOverrides through to the per-(cluster, shard) StatefulSets, then moves
// the overrides and asserts the STSs converge to the new spec.
func TestReconcileShardedMC_ShardOverrideReplicas(t *testing.T) {
	fx := newMCShardedFixture(t)
	fx.search.Spec.Clusters = []searchv1.ClusterSpec{
		{
			Name:         "cluster-a",
			Index:        ptr.To(int32(0)),
			Replicas:     ptr.To(int32(1)),
			LoadBalancer: fx.search.Spec.Clusters[0].LoadBalancer,
			ShardOverrides: []searchv1.ShardOverride{
				// One entry, two shards: both must pick up the override.
				{ShardNames: []string{"sh-0", "sh-1"}, Replicas: ptr.To(int32(2))},
			},
		},
		{
			Name:         "cluster-b",
			Index:        ptr.To(int32(1)),
			Replicas:     ptr.To(int32(3)),
			LoadBalancer: fx.search.Spec.Clusters[1].LoadBalancer,
			ShardOverrides: []searchv1.ShardOverride{
				// Explicit 0 takes this shard's mongot offline.
				{ShardNames: []string{"sh-2"}, Replicas: ptr.To(int32(0))},
			},
		},
	}
	helper := fx.newHelper()

	assertShardReplicas := func(expected map[string]map[string]int32) {
		t.Helper()
		for clusterName, shards := range expected {
			clusterIdx := fx.clusterIndex(clusterName)
			for shardName, replicas := range shards {
				stsName := fx.search.MongotStatefulSetForClusterShard(clusterIdx, shardName)
				sts := &appsv1.StatefulSet{}
				require.NoError(t, fx.members[clusterName].Get(t.Context(), stsName, sts))
				require.NotNil(t, sts.Spec.Replicas, "cluster %s shard %s", clusterName, shardName)
				assert.Equal(t, replicas, *sts.Spec.Replicas, "cluster %s shard %s", clusterName, shardName)
			}
		}
	}

	st := helper.reconcile(t.Context(), zap.S())
	require.False(t, st.IsOK(), "fake STSs are never ready; reconcile reports Pending")

	assertShardReplicas(map[string]map[string]int32{
		"cluster-a": {"sh-0": 2, "sh-1": 2, "sh-2": 1}, // sh-2 keeps the cluster default
		"cluster-b": {"sh-0": 3, "sh-1": 3, "sh-2": 0}, // cluster-a's override must not leak here
	})

	// Spec change: drop cluster-a's override, move cluster-b's to a different
	// shard. The next pass re-resolves every cell from the new spec.
	fx.search.Spec.Clusters[0].ShardOverrides = nil
	fx.search.Spec.Clusters[1].ShardOverrides = []searchv1.ShardOverride{
		{ShardNames: []string{"sh-0"}, Replicas: ptr.To(int32(5))},
	}

	st = helper.reconcile(t.Context(), zap.S())
	require.False(t, st.IsOK())

	assertShardReplicas(map[string]map[string]int32{
		"cluster-a": {"sh-0": 1, "sh-1": 1, "sh-2": 1}, // back to the cluster default
		"cluster-b": {"sh-0": 5, "sh-1": 3, "sh-2": 3}, // sh-2 back to default after the override moved
	})
}

func newMCReplicaSetHelper(members map[string]kubernetesClient.Client, central kubernetesClient.Client) *MongoDBSearchReconcileHelper {
	mdb := newTestMongoDBSearch("mdb-search", "ns")
	mdb.Spec.Clusters = []searchv1.ClusterSpec{
		{Name: "cluster-a", Index: ptr.To(int32(0)), Replicas: ptr.To(int32(1)), LoadBalancer: &searchv1.LoadBalancerConfig{Managed: &searchv1.ManagedLBConfig{
			ExternalHostname: "mdb-search-search-0-proxy-svc.ns.svc.cluster.local",
		}}},
		{Name: "cluster-b", Index: ptr.To(int32(1)), Replicas: ptr.To(int32(1)), LoadBalancer: &searchv1.LoadBalancerConfig{Managed: &searchv1.ManagedLBConfig{
			ExternalHostname: "mdb-search-search-1-proxy-svc.ns.svc.cluster.local",
		}}},
	}
	mdb.Spec.Source = &searchv1.MongoDBSource{
		ExternalMongoDBSource: &searchv1.ExternalMongoDBSource{
			HostAndPorts: []string{"a.example:27017"},
		},
	}
	source := &fakeExternalSource{hosts: mdb.Spec.Source.ExternalMongoDBSource.HostAndPorts}
	return &MongoDBSearchReconcileHelper{
		mdbSearch:            mdb,
		db:                   source,
		client:               central,
		memberClusterClients: members,
		state:                NewSearchDeploymentState(),
		operatorSearchConfig: newTestOperatorSearchConfig(),
	}
}

// A cluster referenced in spec.clusters but missing from the operator's member
// clients fails the reconcile, naming the unmanaged cluster, without blocking the
// well-configured clusters from reconciling. The missing cluster is the FIRST
// unit on purpose: under fail-fast the later cluster would never reconcile, so
// this test catches a regression to that behavior.
func TestReconcileRSMC_MissingClientFailsReconcile(t *testing.T) {
	central := kubernetesClient.NewClient(mock.NewEmptyFakeClientBuilder().Build())
	clusterB := kubernetesClient.NewClient(mock.NewEmptyFakeClientBuilder().Build())
	helper := newMCReplicaSetHelper(map[string]kubernetesClient.Client{"cluster-b": clusterB}, central)

	st := helper.reconcile(t.Context(), zap.S())
	require.False(t, st.IsOK())
	assert.Contains(t, MessageFromStatus(st), "no Kubernetes client registered",
		"a spec cluster with no configured client must fail the reconcile")

	// cluster-b still reconciled despite cluster-a (the first unit) having no client.
	require.NoError(t, clusterB.Get(t.Context(),
		types.NamespacedName{Name: "mdb-search-search-1", Namespace: "ns"}, &appsv1.StatefulSet{}))

	// cluster-a's unit was not leaked onto the central client.
	err := central.Get(t.Context(),
		types.NamespacedName{Name: "mdb-search-search-0", Namespace: "ns"}, &appsv1.StatefulSet{})
	assert.True(t, apierrors.IsNotFound(err), "cluster-a STS must NOT be created on the central client")
}

// A failing member cluster must not block the units on the other clusters: the
// error is aggregated and returned once, after all units ran.
func TestReconcileRSMC_FailingClusterDoesNotBlockOthers(t *testing.T) {
	injectedErr := fmt.Errorf("injected cluster-a apply failure")
	baseA := mock.NewEmptyFakeClientBuilder().Build()
	clusterA := kubernetesClient.NewClient(interceptor.NewClient(baseA, interceptor.Funcs{
		Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			return injectedErr
		},
	}))
	clusterB := kubernetesClient.NewClient(mock.NewEmptyFakeClientBuilder().Build())
	central := kubernetesClient.NewClient(mock.NewEmptyFakeClientBuilder().Build())
	helper := newMCReplicaSetHelper(map[string]kubernetesClient.Client{"cluster-a": clusterA, "cluster-b": clusterB}, central)

	st := helper.reconcile(t.Context(), zap.S())
	require.False(t, st.IsOK())
	assert.Contains(t, MessageFromStatus(st), "injected cluster-a apply failure")

	// cluster-b's unit was still applied despite cluster-a failing first.
	require.NoError(t, clusterB.Get(t.Context(),
		types.NamespacedName{Name: "mdb-search-search-1", Namespace: "ns"}, &appsv1.StatefulSet{}))
}
