package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"testing"

	"github.com/ghodss/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"
	"github.com/mongodb/mongodb-kubernetes/api/v1/status"
	userv1 "github.com/mongodb/mongodb-kubernetes/api/v1/user"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/controllers/searchcontroller"
	mdbcv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1/common"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/mongot"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/util/constants"
	khandler "github.com/mongodb/mongodb-kubernetes/pkg/handler"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

func newMongoDBCommunity(name, namespace string) *mdbcv1.MongoDBCommunity {
	return &mdbcv1.MongoDBCommunity{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: mdbcv1.MongoDBCommunitySpec{
			Type:    mdbcv1.ReplicaSet,
			Members: 1,
			Version: "8.2.0",
		},
	}
}

func newMongoDBSearch(name, namespace, mdbcName string) *searchv1.MongoDBSearch {
	return &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: searchv1.MongoDBSearchSpec{
			Source: &searchv1.MongoDBSource{
				MongoDBResourceRef: &userv1.MongoDBResourceRef{Name: mdbcName},
			},
		},
	}
}

func newSearchReconcilerWithOperatorConfig(
	mdbc *mdbcv1.MongoDBCommunity,
	operatorConfig searchcontroller.OperatorSearchConfig,
	searches ...*searchv1.MongoDBSearch,
) (*MongoDBSearchReconciler, client.Client) {
	builder := mock.NewEmptyFakeClientBuilder()

	if mdbc != nil {
		keyfileSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      mdbc.GetAgentKeyfileSecretNamespacedName().Name,
				Namespace: mdbc.Namespace,
			},
			StringData: map[string]string{
				constants.AgentKeyfileKey: "keyfile",
			},
		}
		builder.WithObjects(mdbc, keyfileSecret)
	}

	for _, search := range searches {
		if search != nil {
			builder.WithObjects(search)
		}
	}

	fakeClient := builder.Build()

	return newMongoDBSearchReconciler(fakeClient, operatorConfig, map[string]client.Client{}, ""), fakeClient
}

func newSearchReconciler(
	mdbc *mdbcv1.MongoDBCommunity,
	searches ...*searchv1.MongoDBSearch,
) (*MongoDBSearchReconciler, client.Client) {
	return newSearchReconcilerWithOperatorConfig(mdbc, searchcontroller.OperatorSearchConfig{}, searches...)
}

func buildExpectedMongotConfig(search *searchv1.MongoDBSearch, mdbc *mdbcv1.MongoDBCommunity) mongot.Config {
	var hostAndPorts []string
	for i := range mdbc.Spec.Members {
		hostAndPorts = append(hostAndPorts, fmt.Sprintf("%s-%d.%s.%s.svc.cluster.local:%d", mdbc.Name, i, mdbc.Name+"-svc", search.Namespace, 27017))
	}

	logLevel := "INFO"
	if search.Spec.LogLevel != "" {
		logLevel = string(search.Spec.LogLevel)
	}

	var wireprotoServer *mongot.ConfigWireproto
	if search.IsWireprotoEnabled() {
		wireprotoServer = &mongot.ConfigWireproto{
			Address: fmt.Sprintf("0.0.0.0:%d", search.GetMongotWireprotoPort()),
			Authentication: &mongot.ConfigAuthentication{
				Mode:    "keyfile",
				KeyFile: searchcontroller.TempKeyfilePath,
			},
			TLS: &mongot.ConfigWireprotoTLS{Mode: mongot.ConfigTLSModeDisabled},
		}
	}

	return mongot.Config{
		SyncSource: mongot.ConfigSyncSource{
			ReplicaSet: mongot.ConfigReplicaSet{
				HostAndPort:    hostAndPorts,
				Username:       searchv1.MongotDefaultSyncSourceUsername,
				PasswordFile:   searchcontroller.TempSourceUserPasswordPath,
				TLS:            ptr.To(false),
				ReadPreference: ptr.To("secondaryPreferred"),
				AuthSource:     ptr.To("admin"),
			},
		},
		Storage: mongot.ConfigStorage{
			DataPath: searchcontroller.MongotDataPath,
		},
		Server: mongot.ConfigServer{
			Grpc: &mongot.ConfigGrpc{
				Address: fmt.Sprintf("0.0.0.0:%d", search.GetMongotGrpcPort()),
				TLS:     &mongot.ConfigGrpcTLS{Mode: mongot.ConfigTLSModeDisabled},
			},
			Wireproto: wireprotoServer,
		},
		HealthCheck: mongot.ConfigHealthCheck{
			Address: fmt.Sprintf("0.0.0.0:%d", search.GetMongotHealthCheckPort()),
		},
		Logging: mongot.ConfigLogging{
			Verbosity: logLevel,
			LogPath:   nil,
		},
	}
}

func TestMongoDBSearchReconcile_NotFound(t *testing.T) {
	ctx := context.Background()
	reconciler, _ := newSearchReconciler(nil, nil)

	res, err := reconciler.Reconcile(
		ctx,
		reconcile.Request{NamespacedName: types.NamespacedName{Name: "missing", Namespace: "test"}},
	)

	assert.Error(t, err)
	assert.True(t, apiErrors.IsNotFound(err))
	assert.Equal(t, reconcile.Result{}, res)
}

func TestMongoDBSearchReconcile_MissingSource(t *testing.T) {
	ctx := context.Background()
	search := newMongoDBSearch("search", mock.TestNamespace, "source")
	reconciler, _ := newSearchReconciler(nil, search)

	res, err := reconciler.Reconcile(
		ctx,
		reconcile.Request{NamespacedName: types.NamespacedName{Name: search.Name, Namespace: search.Namespace}},
	)

	assert.NoError(t, err)
	assert.True(t, res.RequeueAfter > 0)
}

func TestMongoDBSearchReconcile_Success(t *testing.T) {
	tests := []struct {
		name          string
		withWireproto bool
	}{
		{name: "grpc only (default)", withWireproto: false},
		{name: "grpc + wireproto via annotation", withWireproto: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			search := newMongoDBSearch("search", mock.TestNamespace, "mdb")
			search.Spec.LogLevel = "WARN"
			search.Annotations = map[string]string{
				searchv1.ForceWireprotoAnnotation: strconv.FormatBool(tc.withWireproto),
			}

			mdbc := newMongoDBCommunity("mdb", mock.TestNamespace)
			operatorConfig := searchcontroller.OperatorSearchConfig{
				SearchRepo:    "testrepo",
				SearchName:    "mongot",
				SearchVersion: "1.48.0",
			}
			reconciler, c := newSearchReconcilerWithOperatorConfig(mdbc, operatorConfig, search)

			// BEFORE readiness: version should still be empty (controller sets Version only after StatefulSet ready)
			searchPending := &searchv1.MongoDBSearch{}
			assert.NoError(t, c.Get(ctx, types.NamespacedName{Name: search.Name, Namespace: search.Namespace}, searchPending))
			assert.Empty(t, searchPending.Status.Version, "Status.Version must be empty before StatefulSet is marked ready")

			checkSearchReconcileSuccessful(ctx, t, reconciler, c, search)

			svc := &corev1.Service{}
			err := c.Get(ctx, search.SearchServiceNamespacedName(), svc)
			assert.NoError(t, err)
			servicePortNames := []string{}
			for _, port := range svc.Spec.Ports {
				servicePortNames = append(servicePortNames, port.Name)
			}
			expectedPortNames := []string{"mongot-grpc", "healthcheck"}
			if tc.withWireproto {
				expectedPortNames = append(expectedPortNames, "mongot-wireproto")
			}
			assert.ElementsMatch(t, expectedPortNames, servicePortNames)

			cm := &corev1.ConfigMap{}
			err = c.Get(ctx, search.MongotConfigConfigMapNamespacedName(), cm)
			assert.NoError(t, err)
			expectedConfig := buildExpectedMongotConfig(search, mdbc)
			configYaml, err := yaml.Marshal(expectedConfig)
			assert.NoError(t, err)
			assert.Equal(t, string(configYaml), cm.Data[searchcontroller.MongotConfigFilename])

			updatedSearch := &searchv1.MongoDBSearch{}
			assert.NoError(t, c.Get(ctx, types.NamespacedName{Name: search.Name, Namespace: search.Namespace}, updatedSearch))
			assert.Equal(t, operatorConfig.SearchVersion, updatedSearch.Status.Version)

			sts := &appsv1.StatefulSet{}
			err = c.Get(ctx, search.StatefulSetNamespacedName(), sts)
			assert.NoError(t, err)
		})
	}
}

func checkSearchReconcileFailed(
	ctx context.Context,
	t *testing.T,
	reconciler *MongoDBSearchReconciler,
	c client.Client,
	search *searchv1.MongoDBSearch,
	expectedMsg string,
) {
	res, err := reconciler.Reconcile(
		ctx,
		reconcile.Request{NamespacedName: types.NamespacedName{Name: search.Name, Namespace: search.Namespace}},
	)
	assert.NoError(t, err)
	assert.Less(t, res.RequeueAfter, util.TWENTY_FOUR_HOURS)

	updated := &searchv1.MongoDBSearch{}
	assert.NoError(t, c.Get(ctx, types.NamespacedName{Name: search.Name, Namespace: search.Namespace}, updated))
	assert.Equal(t, status.PhaseFailed, updated.Status.Phase)
	assert.Contains(t, updated.Status.Message, expectedMsg)
}

// checkSearchReconcileSuccessful performs reconcile to check if it gets to a Running state.
// In case it's a first reconcile and still Pending it's retried with mocked sts simulated as ready.
func checkSearchReconcileSuccessful(
	ctx context.Context,
	t *testing.T,
	reconciler *MongoDBSearchReconciler,
	c client.Client,
	search *searchv1.MongoDBSearch,
) {
	namespacedName := types.NamespacedName{Name: search.Name, Namespace: search.Namespace}
	res, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
	require.NoError(t, err)
	mdbs := &searchv1.MongoDBSearch{}
	require.NoError(t, c.Get(ctx, namespacedName, mdbs))
	if mdbs.Status.Phase == status.PhasePending {
		// mark mocked search statefulset as ready to not return Pending this time
		require.NoError(t, mock.MarkAllStatefulSetsAsReady(ctx, search.Namespace, c))

		res, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
		require.NoError(t, err)
		mdbs = &searchv1.MongoDBSearch{}
		require.NoError(t, c.Get(ctx, namespacedName, mdbs))
	}

	require.Equal(t, util.TWENTY_FOUR_HOURS, res.RequeueAfter)
	require.Equal(t, status.PhaseRunning, mdbs.Status.Phase)
}

func TestMongoDBSearchReconcile_InvalidVersion(t *testing.T) {
	ctx := context.Background()
	search := newMongoDBSearch("search", mock.TestNamespace, "mdb")
	mdbc := newMongoDBCommunity("mdb", mock.TestNamespace)
	mdbc.Spec.Version = "6.0"
	reconciler, c := newSearchReconciler(mdbc, search)

	checkSearchReconcileFailed(ctx, t, reconciler, c, search, "MongoDB version")
}

func TestMongoDBSearchReconcile_MultipleSearchResources(t *testing.T) {
	ctx := context.Background()
	search1 := newMongoDBSearch("search1", mock.TestNamespace, "mdb")
	search2 := newMongoDBSearch("search2", mock.TestNamespace, "mdb")
	mdbc := newMongoDBCommunity("mdb", mock.TestNamespace)
	reconciler, c := newSearchReconciler(mdbc, search1, search2)

	checkSearchReconcileFailed(ctx, t, reconciler, c, search1, "multiple MongoDBSearch")
}

func TestMongoDBSearchReconcile_InvalidSearchImageVersion(t *testing.T) {
	ctx := context.Background()
	expectedMsg := "MongoDBSearch version 1.47.0 is not supported because of breaking changes. The operator will ignore this resource: it will not reconcile or reconfigure the workload. Existing deployments will continue to run, but cannot be managed by the operator. To regain operator management, you must delete and recreate the MongoDBSearch resource."

	tests := []struct {
		name              string
		specVersion       string
		operatorVersion   string
		statefulSetConfig *common.StatefulSetConfiguration
	}{
		{
			name:        "unsupported version in Spec.Version",
			specVersion: "1.47.0",
		},
		{
			name:            "unsupported version in operator config",
			operatorVersion: "1.47.0",
		},
		{
			name: "unsupported version in StatefulSetConfiguration",
			statefulSetConfig: &common.StatefulSetConfiguration{
				SpecWrapper: common.StatefulSetSpecWrapper{
					Spec: appsv1.StatefulSetSpec{
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{
									{
										Name:  searchcontroller.MongotContainerName,
										Image: "testrepo/mongot:1.47.0",
									},
								},
							},
						},
					},
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			search := newMongoDBSearch("search", mock.TestNamespace, "mdb")
			mdbc := newMongoDBCommunity("mdb", mock.TestNamespace)

			search.Spec.Version = tc.specVersion
			search.Spec.StatefulSetConfiguration = tc.statefulSetConfig

			operatorConfig := searchcontroller.OperatorSearchConfig{
				SearchVersion: tc.operatorVersion,
			}
			reconciler, _ := newSearchReconcilerWithOperatorConfig(mdbc, operatorConfig, search)

			checkSearchReconcileFailed(ctx, t, reconciler, reconciler.kubeClient, search, expectedMsg)
		})
	}
}

func newFakeClientForTest(t *testing.T) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	return fake.NewClientBuilder().WithScheme(scheme).Build()
}

func TestNewMongoDBSearchReconciler_SingleCluster(t *testing.T) {
	central := newFakeClientForTest(t)
	members := map[string]client.Client{} // empty -> single-cluster install

	r := newMongoDBSearchReconciler(central, searchcontroller.OperatorSearchConfig{}, members, "")

	assert.NotNil(t, r.kubeClient, "central kubeClient must be set")
	assert.Empty(t, r.memberClusterClientsMap, "members map must be empty in single-cluster mode")
}

func TestNewMongoDBSearchReconciler_MultiCluster(t *testing.T) {
	central := newFakeClientForTest(t)
	east := newFakeClientForTest(t)
	west := newFakeClientForTest(t)
	members := map[string]client.Client{
		"us-east-k8s": east,
		"eu-west-k8s": west,
	}

	r := newMongoDBSearchReconciler(central, searchcontroller.OperatorSearchConfig{}, members, "")

	assert.Len(t, r.memberClusterClientsMap, 2)
	assert.NotNil(t, r.memberClusterClientsMap["us-east-k8s"])
	assert.NotNil(t, r.memberClusterClientsMap["eu-west-k8s"])
}

func TestMongoDBSearchReconcile_MissingSecret_Requeues(t *testing.T) {
	ctx := context.Background()
	search := newMongoDBSearch("search", mock.TestNamespace, "mdb")
	mdbc := newMongoDBCommunity("mdb", mock.TestNamespace)

	reconciler, _ := newSearchReconciler(mdbc, search)

	res, err := reconciler.Reconcile(
		ctx,
		reconcile.Request{NamespacedName: types.NamespacedName{Name: search.Name, Namespace: search.Namespace}},
	)

	require.NoError(t, err, "missing secret must surface as RequeueAfter, not an error")
	require.True(t, res.RequeueAfter > 0, "must requeue when a customer-replicated secret is missing")
}

// setSearchClusters overwrites s.Spec.Clusters with one entry per name.
func setSearchClusters(s *searchv1.MongoDBSearch, names ...string) {
	entries := make([]searchv1.ClusterSpec, 0, len(names))
	for _, n := range names {
		entries = append(entries, searchv1.ClusterSpec{ClusterName: n})
	}
	s.Spec.Clusters = &entries
}

// setupStateCMTest builds a MongoDBSearch with the given clusters and a fresh reconciler+client.
func setupStateCMTest(t *testing.T, clusterNames ...string) (*MongoDBSearchReconciler, client.Client, *searchv1.MongoDBSearch) {
	t.Helper()
	search := newMongoDBSearch("mysearch", mock.TestNamespace, "mdb")
	setSearchClusters(search, clusterNames...)
	mdbc := newMongoDBCommunity("mdb", mock.TestNamespace)
	reconciler, c := newSearchReconciler(mdbc, search)
	return reconciler, c, search
}

// reconcileAndGetState reconciles once and returns the resulting state ConfigMap + decoded state.
func reconcileAndGetState(t *testing.T, ctx context.Context, r *MongoDBSearchReconciler, c client.Client, name string) (*corev1.ConfigMap, SearchDeploymentState) {
	t.Helper()
	_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: mock.TestNamespace}})
	require.NoError(t, err)
	stateCM := &corev1.ConfigMap{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: name + "-state", Namespace: mock.TestNamespace}, stateCM))
	var state SearchDeploymentState
	require.NoError(t, decodeStateJSON(stateCM, &state))
	return stateCM, state
}

// decodeStateJSON parses the "state" key of a state ConfigMap into dst.
func decodeStateJSON(cm *corev1.ConfigMap, dst interface{}) error {
	raw, ok := cm.Data["state"]
	if !ok {
		return fmt.Errorf("state key missing from ConfigMap %s", cm.Name)
	}
	return json.Unmarshal([]byte(raw), dst)
}

// updateSearchClusters refetches the CR by name and overwrites its cluster list, then Updates.
func updateSearchClusters(t *testing.T, ctx context.Context, c client.Client, name string, clusters ...string) {
	t.Helper()
	latest := &searchv1.MongoDBSearch{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: name, Namespace: mock.TestNamespace}, latest))
	setSearchClusters(latest, clusters...)
	require.NoError(t, c.Update(ctx, latest))
}

// TestMongoDBSearchControllerReconcile_StateConfigMap covers all reconcile-loop
// behaviours of the {search-name}-state ConfigMap: initial creation, add/remove
// clusters, no-op writes, operator-restart preservation, re-add re-claims index.
func TestMongoDBSearchControllerReconcile_StateConfigMap(t *testing.T) {
	ctx := context.Background()

	t.Run("initial_add_retain", func(t *testing.T) {
		reconciler, c, search := setupStateCMTest(t, "us-east", "us-west")

		// First reconcile — state ConfigMap must be created with the initial mapping + owner ref.
		stateCM, state := reconcileAndGetState(t, ctx, reconciler, c, search.Name)
		assert.Equal(t, map[string]int{"us-east": 0, "us-west": 1}, state.ClusterMapping)
		latestSearch := &searchv1.MongoDBSearch{}
		require.NoError(t, c.Get(ctx, types.NamespacedName{Name: search.Name, Namespace: search.Namespace}, latestSearch))
		require.Len(t, stateCM.OwnerReferences, 1)
		assert.Equal(t, latestSearch.UID, stateCM.OwnerReferences[0].UID)

		// Add eu-central.
		updateSearchClusters(t, ctx, c, search.Name, "us-east", "us-west", "eu-central")
		_, state = reconcileAndGetState(t, ctx, reconciler, c, search.Name)
		assert.Equal(t, map[string]int{"us-east": 0, "us-west": 1, "eu-central": 2}, state.ClusterMapping)

		// Remove us-west — mapping must retain it.
		updateSearchClusters(t, ctx, c, search.Name, "us-east", "eu-central")
		_, state = reconcileAndGetState(t, ctx, reconciler, c, search.Name)
		assert.Equal(t, map[string]int{"us-east": 0, "us-west": 1, "eu-central": 2}, state.ClusterMapping, "removed cluster must be retained")
	})

	t.Run("no_op_on_stable_mapping", func(t *testing.T) {
		reconciler, c, search := setupStateCMTest(t, "us-east", "us-west")
		stateCM1, _ := reconcileAndGetState(t, ctx, reconciler, c, search.Name)
		stateCM2, _ := reconcileAndGetState(t, ctx, reconciler, c, search.Name)
		assert.Equal(t, stateCM1.ResourceVersion, stateCM2.ResourceVersion, "state CM must not be rewritten when mapping is unchanged")
	})

	t.Run("operator_restart_preserves_mapping", func(t *testing.T) {
		reconciler, c, search := setupStateCMTest(t, "us-east", "us-west")
		// Pre-seed a state CM as if a previous operator instance had written it.
		stateJSON, err := json.Marshal(SearchDeploymentState{
			CommonDeploymentState: CommonDeploymentState{ClusterMapping: map[string]int{"us-east": 0, "us-west": 1}},
		})
		require.NoError(t, err)
		require.NoError(t, c.Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "mysearch-state", Namespace: mock.TestNamespace},
			Data:       map[string]string{stateKey: string(stateJSON)},
		}))
		_, state := reconcileAndGetState(t, ctx, reconciler, c, search.Name)
		assert.Equal(t, map[string]int{"us-east": 0, "us-west": 1}, state.ClusterMapping, "existing mapping must be preserved across operator restart")
	})

	t.Run("re_add_cluster_reclaims_index", func(t *testing.T) {
		reconciler, c, search := setupStateCMTest(t, "us-east", "us-west", "eu-central")
		_, _ = reconcileAndGetState(t, ctx, reconciler, c, search.Name)

		// Remove us-west (index 1), then re-add — it must reclaim index 1.
		updateSearchClusters(t, ctx, c, search.Name, "us-east", "eu-central")
		_, _ = reconcileAndGetState(t, ctx, reconciler, c, search.Name)
		updateSearchClusters(t, ctx, c, search.Name, "us-east", "eu-central", "us-west")
		_, state := reconcileAndGetState(t, ctx, reconciler, c, search.Name)
		assert.Equal(t, map[string]int{"us-east": 0, "us-west": 1, "eu-central": 2}, state.ClusterMapping,
			"re-added cluster must reclaim its original index")
	})
}

func TestMongoDBSearchReconcile_Success_MultiCluster(t *testing.T) {
	ctx := context.Background()

	// MC GA requires external source (Q2); managed source + MC (Q3) is post-MVP.
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-search", Namespace: mock.TestNamespace},
		Spec: searchv1.MongoDBSearchSpec{
			Source: &searchv1.MongoDBSource{
				ExternalMongoDBSource: &searchv1.ExternalMongoDBSource{
					HostAndPorts: []string{"mdb-0.mdb.svc:27017", "mdb-1.mdb.svc:27017"},
				},
			},
			LoadBalancer: &searchv1.LoadBalancerConfig{Managed: &searchv1.ManagedLBConfig{ExternalHostname: "mongot-{clusterName}.example.com"}},
		},
	}
	clusters := []searchv1.ClusterSpec{
		{ClusterName: "us-east", Replicas: ptr.To(int32(1))},
		{ClusterName: "us-west", Replicas: ptr.To(int32(1))},
	}
	search.Spec.Clusters = &clusters

	eastClient := mock.NewEmptyFakeClientBuilder().Build()
	westClient := mock.NewEmptyFakeClientBuilder().Build()
	memberClients := map[string]client.Client{
		"us-east": eastClient,
		"us-west": westClient,
	}

	reconciler, centralClient := newSearchReconcilerWithMembers(t, nil, memberClients, search)

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: search.Name, Namespace: search.Namespace}}

	// First reconcile creates state CM, then fans out per-cluster resources.
	// The per-unit STS readiness gate makes the helper exit Pending as soon
	// as one unit's STS isn't ready, so several reconciles are needed for the
	// loop to walk every unit. Each iteration, mark STSes ready on all clients
	// so the next pass progresses to the next unit.
	got := &searchv1.MongoDBSearch{}
	const maxPasses = 5
	for i := 0; i < maxPasses; i++ {
		_, err := reconciler.Reconcile(ctx, req)
		require.NoError(t, err)
		require.NoError(t, centralClient.Get(ctx, req.NamespacedName, got))
		// The Envoy controller would normally drive LB status; we seed it so
		// IsLoadBalancerReady() returns true once STSes are caught up.
		if got.Status.LoadBalancer == nil {
			got.Status.LoadBalancer = &searchv1.LoadBalancerStatus{Phase: status.PhaseRunning}
			require.NoError(t, centralClient.Status().Update(ctx, got))
		}
		require.NoError(t, mock.MarkAllStatefulSetsAsReady(ctx, search.Namespace, centralClient, eastClient, westClient))
		if got.Status.Phase == status.PhaseRunning {
			break
		}
	}
	require.Equal(t, status.PhaseRunning, got.Status.Phase, "reconciler must reach Running within %d passes", maxPasses)

	// State CM lives on the central client and records the mapping the helper
	// fanned out over.
	stateCM := &corev1.ConfigMap{}
	require.NoError(t, centralClient.Get(ctx, types.NamespacedName{Name: "mdb-search-state", Namespace: mock.TestNamespace}, stateCM))
	var state SearchDeploymentState
	require.NoError(t, decodeStateJSON(stateCM, &state))
	require.Equal(t, map[string]int{"us-east": 0, "us-west": 1}, state.ClusterMapping)

	// Per-cluster resource fan-out: each cluster sees its own index-suffixed
	// STS / headless Service / proxy Service / mongot ConfigMap on its own client,
	// and the central client sees none of them.
	cases := []struct {
		name        string
		clusterName string
		clusterIdx  int
		mc          client.Client
		otherMC     client.Client
	}{
		{name: "us-east", clusterName: "us-east", clusterIdx: 0, mc: eastClient, otherMC: westClient},
		{name: "us-west", clusterName: "us-west", clusterIdx: 1, mc: westClient, otherMC: eastClient},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stsName := search.StatefulSetNamespacedNameForCluster(tc.clusterIdx)
			headlessName := search.SearchServiceNamespacedNameForCluster(tc.clusterIdx)
			proxyName := search.ProxyServiceNamespacedNameForCluster(tc.clusterIdx)
			cmName := search.MongotConfigConfigMapNameForCluster(tc.clusterIdx)

			// Lands on the right member client.
			sts := &appsv1.StatefulSet{}
			require.NoError(t, tc.mc.Get(ctx, stsName, sts), "STS must exist on owning member client")
			headlessSvc := &corev1.Service{}
			require.NoError(t, tc.mc.Get(ctx, headlessName, headlessSvc), "headless Service must exist on owning member client")
			proxySvc := &corev1.Service{}
			require.NoError(t, tc.mc.Get(ctx, proxyName, proxySvc), "proxy Service must exist on owning member client")
			cm := &corev1.ConfigMap{}
			require.NoError(t, tc.mc.Get(ctx, cmName, cm), "mongot ConfigMap must exist on owning member client")

			// Owner labels stamp cross-cluster identity — owner refs do not
			// carry between clusters, so labels are the link back to the CR.
			for _, obj := range []client.Object{sts, headlessSvc, proxySvc, cm} {
				labels := obj.GetLabels()
				assert.Equal(t, search.Name, labels[khandler.MongoDBSearchOwnerNameLabel], "owner-name label on %T", obj)
				assert.Equal(t, search.Namespace, labels[khandler.MongoDBSearchOwnerNamespaceLabel], "owner-namespace label on %T", obj)
				assert.Equal(t, tc.clusterName, labels[khandler.MongoDBSearchClusterNameLabel], "cluster-name label on %T", obj)
			}
		})
	}
}

// newSearchReconcilerWithMembers builds a MongoDBSearchReconciler that is pre-wired
// with the given member-cluster clients. The central fake client is built from
// mock.NewEmptyFakeClientBuilder() so all search-related types are registered.
func newSearchReconcilerWithMembers(
	t *testing.T,
	mdbc *mdbcv1.MongoDBCommunity,
	memberClients map[string]client.Client,
	searches ...*searchv1.MongoDBSearch,
) (*MongoDBSearchReconciler, client.Client) {
	t.Helper()
	builder := mock.NewEmptyFakeClientBuilder().WithStatusSubresource(&searchv1.MongoDBSearch{})

	if mdbc != nil {
		builder.WithObjects(mdbc)
	}
	for _, s := range searches {
		if s != nil {
			builder.WithObjects(s)
		}
	}
	centralClient := builder.Build()
	return newMongoDBSearchReconciler(centralClient, searchcontroller.OperatorSearchConfig{}, memberClients, ""), centralClient
}

// newSimulatedMCSearchReconciler builds a MongoDBSearchReconciler in simulated
// multi-cluster mode: operatorClusterName is set and memberClusterClients is
// empty (each operator only talks to its own cluster via its central client).
func newSimulatedMCSearchReconciler(
	t *testing.T,
	operatorClusterName string,
	searches ...*searchv1.MongoDBSearch,
) (*MongoDBSearchReconciler, client.Client) {
	t.Helper()
	builder := mock.NewEmptyFakeClientBuilder().WithStatusSubresource(&searchv1.MongoDBSearch{})
	for _, s := range searches {
		if s != nil {
			builder.WithObjects(s)
		}
	}
	centralClient := builder.Build()
	return newMongoDBSearchReconciler(
		centralClient,
		searchcontroller.OperatorSearchConfig{},
		map[string]client.Client{},
		operatorClusterName,
	), centralClient
}

// driveSearchReconcileToRunning runs Reconcile up to maxPasses times, seeding
// LoadBalancer status and marking STSes ready on every pass so the per-unit
// readiness gate walks forward. Returns the final MongoDBSearch. Exits as soon
// as Status.Phase == Running. Used by the simulated-MC full-reconcile tests
// (the existing MC-success test inlines an equivalent loop).
func driveSearchReconcileToRunning(
	ctx context.Context,
	t *testing.T,
	reconciler *MongoDBSearchReconciler,
	c client.Client,
	search *searchv1.MongoDBSearch,
	maxPasses int,
) *searchv1.MongoDBSearch {
	t.Helper()
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: search.Name, Namespace: search.Namespace}}
	got := &searchv1.MongoDBSearch{}
	for i := 0; i < maxPasses; i++ {
		_, err := reconciler.Reconcile(ctx, req)
		require.NoError(t, err)
		require.NoError(t, c.Get(ctx, req.NamespacedName, got))
		if got.Status.LoadBalancer == nil {
			got.Status.LoadBalancer = &searchv1.LoadBalancerStatus{Phase: status.PhaseRunning}
			require.NoError(t, c.Status().Update(ctx, got))
		}
		require.NoError(t, mock.MarkAllStatefulSetsAsReady(ctx, search.Namespace, c))
		if got.Status.Phase == status.PhaseRunning {
			break
		}
	}
	return got
}

// newSimulatedMCMongoDBSearch builds a MongoDBSearch with an external RS source
// and a 2-entry spec.clusters where both entries have ClusterIndex set. Used as
// the baseline fixture for the simulated-MC full-reconcile tests below.
func newSimulatedMCMongoDBSearch(name, namespace string) *searchv1.MongoDBSearch {
	clusters := []searchv1.ClusterSpec{
		{ClusterName: "cluster-a", ClusterIndex: ptr.To(int32(0)), Replicas: ptr.To(int32(1))},
		{ClusterName: "cluster-b", ClusterIndex: ptr.To(int32(1)), Replicas: ptr.To(int32(1))},
	}
	return &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: searchv1.MongoDBSearchSpec{
			Source: &searchv1.MongoDBSource{
				ExternalMongoDBSource: &searchv1.ExternalMongoDBSource{
					HostAndPorts: []string{"mdb-0.mdb.svc:27017", "mdb-1.mdb.svc:27017"},
				},
			},
			LoadBalancer: &searchv1.LoadBalancerConfig{
				Managed: &searchv1.ManagedLBConfig{ExternalHostname: "mongot-{clusterName}.example.com"},
			},
			Clusters: &clusters,
		},
	}
}

// TestReconcile_SimulatedMC_ProjectedReconcilesLocalOnly verifies that an
// operator with operatorClusterName="cluster-a" only writes resources for its
// own projected entry; resources for cluster-b are NEVER created, and the
// {search-name}-state ConfigMap is NOT used (the simulated-MC path skips it).
func TestReconcile_SimulatedMC_ProjectedReconcilesLocalOnly(t *testing.T) {
	ctx := context.Background()
	search := newSimulatedMCMongoDBSearch("mdb-search", mock.TestNamespace)

	reconciler, c := newSimulatedMCSearchReconciler(t, "cluster-a", search)

	got := driveSearchReconcileToRunning(ctx, t, reconciler, c, search, 3)

	// cluster-a's resources (index 0) must exist on the central (= operator's) client.
	stsA := &appsv1.StatefulSet{}
	require.NoError(t, c.Get(ctx, search.StatefulSetNamespacedNameForCluster(0), stsA),
		"STS for cluster-a (index 0) must exist")
	cmA := &corev1.ConfigMap{}
	require.NoError(t, c.Get(ctx, search.MongotConfigConfigMapNameForCluster(0), cmA),
		"mongot ConfigMap for cluster-a must exist")
	proxyA := &corev1.Service{}
	require.NoError(t, c.Get(ctx, search.ProxyServiceNamespacedNameForCluster(0), proxyA),
		"proxy Service for cluster-a must exist")

	// cluster-b's resources (index 1) MUST NOT exist — this operator never wrote them.
	stsB := &appsv1.StatefulSet{}
	err := c.Get(ctx, search.StatefulSetNamespacedNameForCluster(1), stsB)
	assert.True(t, apiErrors.IsNotFound(err), "STS for cluster-b (index 1) must NOT exist; got err=%v", err)
	cmB := &corev1.ConfigMap{}
	err = c.Get(ctx, search.MongotConfigConfigMapNameForCluster(1), cmB)
	assert.True(t, apiErrors.IsNotFound(err), "mongot ConfigMap for cluster-b must NOT exist; got err=%v", err)
	proxyB := &corev1.Service{}
	err = c.Get(ctx, search.ProxyServiceNamespacedNameForCluster(1), proxyB)
	assert.True(t, apiErrors.IsNotFound(err), "proxy Service for cluster-b must NOT exist; got err=%v", err)

	// The {search-name}-state ConfigMap must NOT be created — projected path
	// synthesises the mapping in memory and skips the state CM entirely.
	stateCM := &corev1.ConfigMap{}
	err = c.Get(ctx, types.NamespacedName{Name: search.Name + "-state", Namespace: search.Namespace}, stateCM)
	assert.True(t, apiErrors.IsNotFound(err), "state ConfigMap must NOT be created in simulated-MC projected path; got err=%v", err)

	// Status phase must reflect a normal reconcile outcome (Running or Pending,
	// depending on STS-readiness timing) — anything else (Failed/Invalid) signals
	// a regression in the projection path.
	assert.Contains(t, []status.Phase{status.PhaseRunning, status.PhasePending}, got.Status.Phase,
		"projected reconcile should land on Running or Pending; got %q (msg=%q)", got.Status.Phase, got.Status.Message)
}

// TestReconcile_SimulatedMC_NoMatchSilentNoOp verifies that an operator whose
// clusterName does not appear in spec.clusters[] silently returns Result{}, nil
// and never mutates status or creates any per-cluster resources.
func TestReconcile_SimulatedMC_NoMatchSilentNoOp(t *testing.T) {
	ctx := context.Background()
	search := newSimulatedMCMongoDBSearch("mdb-search", mock.TestNamespace)

	// operatorClusterName="cluster-c" — NOT in spec.clusters[].
	reconciler, c := newSimulatedMCSearchReconciler(t, "cluster-c", search)

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: search.Name, Namespace: search.Namespace}}
	res, err := reconciler.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, res, "no-match reconcile must return zero Result with no error")

	// No per-cluster resources created at any index.
	for _, idx := range []int{0, 1, 2} {
		sts := &appsv1.StatefulSet{}
		assert.True(t, apiErrors.IsNotFound(c.Get(ctx, search.StatefulSetNamespacedNameForCluster(idx), sts)),
			"STS at index %d must not exist", idx)
		cm := &corev1.ConfigMap{}
		assert.True(t, apiErrors.IsNotFound(c.Get(ctx, search.MongotConfigConfigMapNameForCluster(idx), cm)),
			"mongot ConfigMap at index %d must not exist", idx)
	}

	// State ConfigMap must not be created.
	stateCM := &corev1.ConfigMap{}
	assert.True(t, apiErrors.IsNotFound(
		c.Get(ctx, types.NamespacedName{Name: search.Name + "-state", Namespace: search.Namespace}, stateCM)),
		"state ConfigMap must not be created in no-match path")

	// Status must NOT have been mutated — Phase remains the zero value.
	updated := &searchv1.MongoDBSearch{}
	require.NoError(t, c.Get(ctx, req.NamespacedName, updated))
	assert.Equal(t, status.Phase(""), updated.Status.Phase, "no-match reconcile must not touch status")
}

// TestReconcile_SimulatedMC_ShardedSourceInvalid verifies that a sharded
// external source is rejected at projection time with a Failed/Invalid status.
func TestReconcile_SimulatedMC_ShardedSourceInvalid(t *testing.T) {
	ctx := context.Background()
	search := newSimulatedMCMongoDBSearch("mdb-search", mock.TestNamespace)
	// Replace the RS external source with a sharded one (v0 rejects this).
	search.Spec.Source = &searchv1.MongoDBSource{
		ExternalMongoDBSource: &searchv1.ExternalMongoDBSource{
			ShardedCluster: &searchv1.ExternalShardedClusterConfig{},
		},
	}

	reconciler, c := newSimulatedMCSearchReconciler(t, "cluster-a", search)

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: search.Name, Namespace: search.Namespace}}
	_, err := reconciler.Reconcile(ctx, req)
	require.NoError(t, err, "Invalid status returns no error; status carries the failure")

	updated := &searchv1.MongoDBSearch{}
	require.NoError(t, c.Get(ctx, req.NamespacedName, updated))
	assert.Equal(t, status.PhaseFailed, updated.Status.Phase)
	assert.Contains(t, updated.Status.Message, "replica-set source only")
}

// TestReconcile_SimulatedMC_MissingClusterIndexInvalid verifies that any
// spec.clusters[i] missing ClusterIndex (even the non-matching one) is rejected
// at projection time with a Failed/Invalid status.
func TestReconcile_SimulatedMC_MissingClusterIndexInvalid(t *testing.T) {
	ctx := context.Background()
	search := newSimulatedMCMongoDBSearch("mdb-search", mock.TestNamespace)
	// Drop ClusterIndex on the SECOND entry only — projection must still fail.
	clusters := []searchv1.ClusterSpec{
		{ClusterName: "cluster-a", ClusterIndex: ptr.To(int32(0)), Replicas: ptr.To(int32(1))},
		{ClusterName: "cluster-b", Replicas: ptr.To(int32(1))},
	}
	search.Spec.Clusters = &clusters

	reconciler, c := newSimulatedMCSearchReconciler(t, "cluster-a", search)

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: search.Name, Namespace: search.Namespace}}
	_, err := reconciler.Reconcile(ctx, req)
	require.NoError(t, err, "Invalid status returns no error; status carries the failure")

	updated := &searchv1.MongoDBSearch{}
	require.NoError(t, c.Get(ctx, req.NamespacedName, updated))
	assert.Equal(t, status.PhaseFailed, updated.Status.Phase)
	assert.Contains(t, updated.Status.Message, "clusterIndex to be set")
}

// Cluster-level proxy Service selector must match the label Envoy Deployment stamps on its Pods.
func TestMongoDBSearchReconcile_MCSharded_CrossControllerLabelInvariant(t *testing.T) {
	ctx := context.Background()

	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mdb-search",
			Namespace: mock.TestNamespace,
		},
		Spec: searchv1.MongoDBSearchSpec{
			Clusters: &[]searchv1.ClusterSpec{
				{ClusterName: "cluster-a", Replicas: ptr.To(int32(1))},
				{ClusterName: "cluster-b", Replicas: ptr.To(int32(1))},
			},
			Source: &searchv1.MongoDBSource{
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
			},
			LoadBalancer: &searchv1.LoadBalancerConfig{
				Managed: &searchv1.ManagedLBConfig{
					ExternalHostname: "{shardName}.mdb-search-search-{clusterIndex}-proxy-svc.example.com",
				},
			},
			Security: searchv1.Security{TLS: &searchv1.TLS{CertsSecretPrefix: "certs"}},
		},
	}

	tlsSecrets := func(clusterIndex int) []client.Object {
		out := make([]client.Object, 0, 3)
		for _, shard := range []string{"sh-0", "sh-1", "sh-2"} {
			out = append(out, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("certs-mdb-search-search-%d-%s-cert", clusterIndex, shard),
					Namespace: mock.TestNamespace,
				},
				Data: map[string][]byte{"tls.crt": []byte("dummy"), "tls.key": []byte("dummy")},
			})
		}
		return out
	}

	clusterA := mock.NewEmptyFakeClientBuilder().WithObjects(tlsSecrets(0)...).Build()
	clusterB := mock.NewEmptyFakeClientBuilder().WithObjects(tlsSecrets(1)...).Build()
	memberClients := map[string]client.Client{"cluster-a": clusterA, "cluster-b": clusterB}

	searchReconciler, centralClient := newSearchReconcilerWithMembers(t, nil, memberClients, search)
	envoyReconciler := newMongoDBSearchEnvoyReconciler(centralClient, "envoy:test", memberClients, "")

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: search.Name, Namespace: search.Namespace}}

	// Cooperative loop: each pass runs Search → Envoy, marks STSs+Deployments
	// ready in fake clients, and seeds the LB status if the Envoy reconciler
	// hasn't filled it in yet. Five passes is more than enough; assert on the
	// invariant after the search resource reaches Running.
	got := &searchv1.MongoDBSearch{}
	const maxPasses = 5
	for i := 0; i < maxPasses; i++ {
		_, err := searchReconciler.Reconcile(ctx, req)
		require.NoError(t, err)
		_, err = envoyReconciler.Reconcile(ctx, req)
		require.NoError(t, err)

		require.NoError(t, mock.MarkAllStatefulSetsAsReady(ctx, search.Namespace, centralClient, clusterA, clusterB))
		require.NoError(t, markAllDeploymentsAvailable(ctx, search.Namespace, clusterA, clusterB))

		require.NoError(t, centralClient.Get(ctx, req.NamespacedName, got))
		if got.Status.LoadBalancer == nil || got.Status.LoadBalancer.Phase != status.PhaseRunning {
			got.Status.LoadBalancer = &searchv1.LoadBalancerStatus{Phase: status.PhaseRunning}
			require.NoError(t, centralClient.Status().Update(ctx, got))
		}

		if got.Status.Phase == status.PhaseRunning {
			break
		}
	}
	require.Equal(t, status.PhaseRunning, got.Status.Phase,
		"search reconciler must reach Running within %d passes", maxPasses)

	// Invariant: per cluster, cluster-level proxy Service Selector["app"] must
	// match the Envoy Deployment podTemplate.metadata.labels["app"].
	for clusterName, idx := range map[string]int{"cluster-a": 0, "cluster-b": 1} {
		mc := memberClients[clusterName]

		svc := &corev1.Service{}
		require.NoError(t, mc.Get(ctx,
			search.ProxyServiceNamespacedNameForCluster(idx), svc),
			"cluster-level proxy Service missing on %s", clusterName)

		dep := &appsv1.Deployment{}
		require.NoError(t, mc.Get(ctx,
			types.NamespacedName{Name: search.LoadBalancerDeploymentNameForCluster(idx), Namespace: search.Namespace},
			dep), "Envoy Deployment missing on %s", clusterName)

		require.Equal(t,
			dep.Spec.Template.Labels["app"],
			svc.Spec.Selector["app"],
			"%s: cluster-level proxy Service Selector[app]=%q must match Envoy Deployment podTemplate label app=%q",
			clusterName, svc.Spec.Selector["app"], dep.Spec.Template.Labels["app"])

		// Be explicit that the selector value is the LB Deployment name (not the
		// per-shard fallback) — proves the Search reconciler observed
		// IsLoadBalancerReady()=true after Envoy ran.
		require.Equal(t,
			search.LoadBalancerDeploymentNameForCluster(idx),
			svc.Spec.Selector["app"],
			"%s: cluster-level proxy Selector must point at the LB Deployment label, got %q",
			clusterName, svc.Spec.Selector["app"])
	}
}

// markAllDeploymentsAvailable mirrors MarkAllStatefulSetsAsReady for the Envoy
// integration path. Sets ReadyReplicas=Spec.Replicas + the Available condition
// so any downstream readiness gate (search-side or test-side) reads it as up.
func markAllDeploymentsAvailable(ctx context.Context, namespace string, clients ...client.Client) error {
	for _, c := range clients {
		var deps appsv1.DeploymentList
		if err := c.List(ctx, &deps, client.InNamespace(namespace)); err != nil {
			return err
		}
		for i := range deps.Items {
			dep := deps.Items[i].DeepCopy()
			replicas := int32(1)
			if dep.Spec.Replicas != nil {
				replicas = *dep.Spec.Replicas
			}
			dep.Status.Replicas = replicas
			dep.Status.ReadyReplicas = replicas
			dep.Status.AvailableReplicas = replicas
			dep.Status.UpdatedReplicas = replicas
			dep.Status.ObservedGeneration = dep.Generation
			if err := c.Status().Patch(ctx, dep, client.MergeFrom(&deps.Items[i])); err != nil {
				return err
			}
		}
	}
	return nil
}
