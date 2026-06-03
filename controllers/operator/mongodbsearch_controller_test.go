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

	v1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	searchv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/search"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	userv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/user"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/controllers/searchcontroller"
	mdbcv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1"
	//nolint:depguard
	khandler "github.com/mongodb/mongodb-kubernetes/pkg/handler"
	"github.com/mongodb/mongodb-kubernetes/pkg/mongot"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/constants"
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

	return newMongoDBSearchReconciler(fakeClient, operatorConfig, map[string]client.Client{}), fakeClient
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
		FeatureFlags: &mongot.ConfigFeatureFlags{
			OverloadRetrySignal: ptr.To(true),
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

func TestMongoDBSearchReconcile_DisableReconciliationAnnotation_SkipsReconcile(t *testing.T) {
	ctx := context.Background()

	// CR is annotated disabled but has a missing source — without the
	// short-circuit, Reconcile would set Status.Phase=Failed. The
	// short-circuit must return Result{} + nil without touching status.
	search := newMongoDBSearch("search", mock.TestNamespace, "missing-source")
	search.Annotations = map[string]string{
		searchv1.DisableReconciliationAnnotation: "true",
	}
	reconciler, c := newSearchReconciler(nil, search)

	res, err := reconciler.Reconcile(
		ctx,
		reconcile.Request{NamespacedName: types.NamespacedName{Name: search.Name, Namespace: search.Namespace}},
	)
	assert.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, res)

	// Status untouched — would be Failed if reconcile had proceeded
	// (MissingSource path sets PhaseFailed).
	updated := &searchv1.MongoDBSearch{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: search.Name, Namespace: search.Namespace}, updated))
	assert.Empty(t, updated.Status.Phase)
	assert.Empty(t, updated.Status.Message)

	// No StatefulSet was created either.
	sts := &appsv1.StatefulSet{}
	err = c.Get(ctx, search.StatefulSetNamespacedNameForCluster(0), sts)
	assert.True(t, apiErrors.IsNotFound(err), "no StatefulSet should be created when reconciliation is disabled, got err=%v", err)
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
			err := c.Get(ctx, search.SearchServiceNamespacedNameForCluster(0), svc)
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
			err = c.Get(ctx, search.MongotConfigConfigMapNameForCluster(0), cm)
			assert.NoError(t, err)
			expectedConfig := buildExpectedMongotConfig(search, mdbc)
			configYaml, err := yaml.Marshal(expectedConfig)
			assert.NoError(t, err)
			assert.Equal(t, string(configYaml), cm.Data[searchcontroller.MongotConfigFilename])

			updatedSearch := &searchv1.MongoDBSearch{}
			assert.NoError(t, c.Get(ctx, types.NamespacedName{Name: search.Name, Namespace: search.Namespace}, updatedSearch))
			assert.Equal(t, operatorConfig.SearchVersion, updatedSearch.Status.Version)

			sts := &appsv1.StatefulSet{}
			err = c.Get(ctx, search.StatefulSetNamespacedNameForCluster(0), sts)
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
		statefulSetConfig *v1.StatefulSetConfiguration
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
			statefulSetConfig: &v1.StatefulSetConfiguration{
				SpecWrapper: v1.StatefulSetSpecWrapper{
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

	r := newMongoDBSearchReconciler(central, searchcontroller.OperatorSearchConfig{}, members)

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

	r := newMongoDBSearchReconciler(central, searchcontroller.OperatorSearchConfig{}, members)

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

// TODO: collapse StateConfigMap reconcile-loop tests (this one +
// _NoOpOnStableMapping, _OperatorRestart, _ReAddCluster) into a single
// table-driven test.
func TestMongoDBSearchControllerReconcile_StateConfigMap(t *testing.T) {
	ctx := context.Background()

	withClusters := func(names ...string) func(*searchv1.MongoDBSearch) {
		return func(s *searchv1.MongoDBSearch) {
			entries := make([]searchv1.ClusterSpec, 0, len(names))
			for _, n := range names {
				entries = append(entries, searchv1.ClusterSpec{ClusterName: n})
			}
			s.Spec.Clusters = &entries
		}
	}

	search := newMongoDBSearch("mysearch", mock.TestNamespace, "mdb")
	withClusters("us-east", "us-west")(search)
	mdbc := newMongoDBCommunity("mdb", mock.TestNamespace)
	reconciler, c := newSearchReconciler(mdbc, search)

	// First reconcile — state ConfigMap must be created.
	_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: search.Name, Namespace: search.Namespace}})
	require.NoError(t, err)

	stateCM := &corev1.ConfigMap{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "mysearch-state", Namespace: mock.TestNamespace}, stateCM))

	var gotState SearchDeploymentState
	require.NoError(t, decodeStateJSON(stateCM, &gotState))
	assert.Equal(t, map[string]int{"us-east": 0, "us-west": 1}, gotState.ClusterMapping)

	// Owner reference must point to the MongoDBSearch.
	latestSearch := &searchv1.MongoDBSearch{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: search.Name, Namespace: search.Namespace}, latestSearch))
	require.Len(t, stateCM.OwnerReferences, 1)
	assert.Equal(t, latestSearch.UID, stateCM.OwnerReferences[0].UID)

	// Second reconcile — add a third cluster.
	withClusters("us-east", "us-west", "eu-central")(latestSearch)
	require.NoError(t, c.Update(ctx, latestSearch))

	_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: search.Name, Namespace: search.Namespace}})
	require.NoError(t, err)

	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "mysearch-state", Namespace: mock.TestNamespace}, stateCM))
	require.NoError(t, decodeStateJSON(stateCM, &gotState))
	assert.Equal(t, map[string]int{"us-east": 0, "us-west": 1, "eu-central": 2}, gotState.ClusterMapping)

	// Third reconcile — remove us-west; mapping must retain it.
	latestSearch = &searchv1.MongoDBSearch{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: search.Name, Namespace: search.Namespace}, latestSearch))
	withClusters("us-east", "eu-central")(latestSearch)
	require.NoError(t, c.Update(ctx, latestSearch))

	_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: search.Name, Namespace: search.Namespace}})
	require.NoError(t, err)

	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "mysearch-state", Namespace: mock.TestNamespace}, stateCM))
	require.NoError(t, decodeStateJSON(stateCM, &gotState))
	assert.Equal(t, map[string]int{"us-east": 0, "us-west": 1, "eu-central": 2}, gotState.ClusterMapping, "removed cluster must be retained")
}

// decodeStateJSON parses the "state" key of a state ConfigMap into dst.
func decodeStateJSON(cm *corev1.ConfigMap, dst interface{}) error {
	raw, ok := cm.Data["state"]
	if !ok {
		return fmt.Errorf("state key missing from ConfigMap %s", cm.Name)
	}
	return json.Unmarshal([]byte(raw), dst)
}

// TestMongoDBSearchControllerReconcile_StateConfigMap_NoOpOnStableMapping verifies that a second reconcile
// with an unchanged cluster list does not bump the state ConfigMap's resourceVersion.
func TestMongoDBSearchControllerReconcile_StateConfigMap_NoOpOnStableMapping(t *testing.T) {
	ctx := context.Background()
	search := newMongoDBSearch("mysearch", mock.TestNamespace, "mdb")
	clusters := []searchv1.ClusterSpec{{ClusterName: "us-east"}, {ClusterName: "us-west"}}
	search.Spec.Clusters = &clusters
	mdbc := newMongoDBCommunity("mdb", mock.TestNamespace)
	reconciler, c := newSearchReconciler(mdbc, search)

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: search.Name, Namespace: search.Namespace}}
	_, err := reconciler.Reconcile(ctx, req)
	require.NoError(t, err)

	stateCM := &corev1.ConfigMap{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "mysearch-state", Namespace: mock.TestNamespace}, stateCM))
	rvAfterFirst := stateCM.ResourceVersion

	// Second reconcile with identical spec — mapping is stable; state CM must not be rewritten.
	_, err = reconciler.Reconcile(ctx, req)
	require.NoError(t, err)

	stateCM2 := &corev1.ConfigMap{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "mysearch-state", Namespace: mock.TestNamespace}, stateCM2))
	assert.Equal(t, rvAfterFirst, stateCM2.ResourceVersion, "state CM must not be rewritten when mapping is unchanged")
}

// TestMongoDBSearchControllerReconcile_StateConfigMap_OperatorRestart simulates an operator restart by
// pre-seeding a state CM in the fake client before constructing the reconciler.  The reconcile must
// preserve the existing mapping rather than initialising a fresh one.
func TestMongoDBSearchControllerReconcile_StateConfigMap_OperatorRestart(t *testing.T) {
	ctx := context.Background()
	search := newMongoDBSearch("mysearch", mock.TestNamespace, "mdb")
	clusters := []searchv1.ClusterSpec{{ClusterName: "us-east"}, {ClusterName: "us-west"}}
	search.Spec.Clusters = &clusters
	mdbc := newMongoDBCommunity("mdb", mock.TestNamespace)

	// Build reconciler and client, then manually seed a state CM that already contains
	// a non-trivial mapping — as if a previous operator instance had written it.
	reconciler, c := newSearchReconciler(mdbc, search)

	existingState := SearchDeploymentState{
		CommonDeploymentState: CommonDeploymentState{
			ClusterMapping: map[string]int{"us-east": 0, "us-west": 1},
		},
	}
	stateJSON, err := json.Marshal(existingState)
	require.NoError(t, err)
	seedCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mysearch-state",
			Namespace: mock.TestNamespace,
		},
		Data: map[string]string{stateKey: string(stateJSON)},
	}
	require.NoError(t, c.Create(ctx, seedCM))

	_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: search.Name, Namespace: search.Namespace}})
	require.NoError(t, err)

	stateCM := &corev1.ConfigMap{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "mysearch-state", Namespace: mock.TestNamespace}, stateCM))
	var gotState SearchDeploymentState
	require.NoError(t, decodeStateJSON(stateCM, &gotState))
	assert.Equal(t, map[string]int{"us-east": 0, "us-west": 1}, gotState.ClusterMapping, "existing mapping must be preserved across operator restart")
}

// TestMongoDBSearchControllerReconcile_StateConfigMap_ReAddCluster verifies that a cluster that was previously
// removed and then re-added gets back its original index (monotonic, stable index assignment).
func TestMongoDBSearchControllerReconcile_StateConfigMap_ReAddCluster(t *testing.T) {
	ctx := context.Background()

	withClusters := func(s *searchv1.MongoDBSearch, names ...string) {
		entries := make([]searchv1.ClusterSpec, 0, len(names))
		for _, n := range names {
			entries = append(entries, searchv1.ClusterSpec{ClusterName: n})
		}
		s.Spec.Clusters = &entries
	}

	search := newMongoDBSearch("mysearch", mock.TestNamespace, "mdb")
	withClusters(search, "us-east", "us-west", "eu-central")
	mdbc := newMongoDBCommunity("mdb", mock.TestNamespace)
	reconciler, c := newSearchReconciler(mdbc, search)

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: search.Name, Namespace: search.Namespace}}

	// Initial reconcile: assign indices 0, 1, 2.
	_, err := reconciler.Reconcile(ctx, req)
	require.NoError(t, err)

	// Remove us-west (index 1).
	latestSearch := &searchv1.MongoDBSearch{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: search.Name, Namespace: search.Namespace}, latestSearch))
	withClusters(latestSearch, "us-east", "eu-central")
	require.NoError(t, c.Update(ctx, latestSearch))
	_, err = reconciler.Reconcile(ctx, req)
	require.NoError(t, err)

	// Re-add us-west — it must reclaim index 1.
	latestSearch = &searchv1.MongoDBSearch{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: search.Name, Namespace: search.Namespace}, latestSearch))
	withClusters(latestSearch, "us-east", "eu-central", "us-west")
	require.NoError(t, c.Update(ctx, latestSearch))
	_, err = reconciler.Reconcile(ctx, req)
	require.NoError(t, err)

	stateCM := &corev1.ConfigMap{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "mysearch-state", Namespace: mock.TestNamespace}, stateCM))
	var gotState SearchDeploymentState
	require.NoError(t, decodeStateJSON(stateCM, &gotState))
	assert.Equal(t, map[string]int{"us-east": 0, "us-west": 1, "eu-central": 2}, gotState.ClusterMapping,
		"re-added cluster must reclaim its original index")
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
	return newMongoDBSearchReconciler(centralClient, searchcontroller.OperatorSearchConfig{}, memberClients), centralClient
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
	envoyReconciler := newMongoDBSearchEnvoyReconciler(centralClient, "envoy:test", memberClients)

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
