package operator

import (
	"context"
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
	mdbcv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1" //nolint:depguard
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
			Clusters: []searchv1.ClusterSpec{{}},
			Source: &searchv1.MongoDBSource{
				MongoDBResourceRef: &userv1.MongoDBResourceRef{Name: mdbcName},
			},
			// immitate apiserver defaulting for .observability and .observability.prometheus
			Observability: searchv1.ObservabilityConfig{
				Prometheus: searchv1.Prometheus{
					Mode: searchv1.PrometheusModeEnabled,
					Port: int(searchv1.MongotDefaultPrometheusPort),
				},
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

	cfg := mongot.Config{
		SyncSource: mongot.ConfigSyncSource{
			ReplicaSet: mongot.ConfigReplicaSet{
				HostAndPort: hostAndPorts,
				ScramAuth: &mongot.ConfigScramAuth{
					Username:     searchv1.MongotDefaultSyncSourceUsername,
					PasswordFile: searchcontroller.TempSourceUserPasswordPath,
					TLS: &mongot.ScramAuthTLS{
						Enabled: false,
					},
					AuthSource: ptr.To("admin"),
				},
			},
			ReplicationReader: &mongot.ConfigReplicationReader{
				ReadPreference: ptr.To("secondaryPreferred"),
			},
		},
		Storage: mongot.ConfigStorage{
			DataPath: searchcontroller.MongotDataPath,
		},
		Server: mongot.ConfigServer{
			Name: searchcontroller.ServerNamePlaceholder,
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
		// OverloadRetrySignal defaults to true (see featureFlagsMongotMod) unless the CR disables it.
		FeatureFlags: &mongot.ConfigFeatureFlags{
			OverloadRetrySignal: ptr.To(true),
		},
	}

	if prometheus := search.Spec.Observability.Prometheus; prometheus.IsEnabled() {
		cfg.Metrics = mongot.ConfigMetrics{
			Enabled: true,
			Address: fmt.Sprintf("0.0.0.0:%d", prometheus.GetPort()),
		}
	}

	return cfg
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
				SearchVersion: "1.70.1",
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
			expectedPortNames := []string{"mongot-grpc", "healthcheck", "prometheus"}
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
	search1.Spec.Version = "1.70.1"
	search2 := newMongoDBSearch("search2", mock.TestNamespace, "mdb")
	search2.Spec.Version = "1.70.1"
	mdbc := newMongoDBCommunity("mdb", mock.TestNamespace)
	reconciler, c := newSearchReconciler(mdbc, search1, search2)

	checkSearchReconcileFailed(ctx, t, reconciler, c, search1, "multiple MongoDBSearch")
}

func TestMongoDBSearchReconcile_MultiClusterBlocked(t *testing.T) {
	ctx := context.Background()
	// workflow.Invalid capitalizes the first char, so match on the stable substring.
	const blockMsg = "MongoDBSearch is not supported yet"

	twoClusters := func(s *searchv1.MongoDBSearch) {
		s.Spec.Clusters = []searchv1.ClusterSpec{
			pinnedCluster("us-east", 0),
			pinnedCluster("us-west", 1),
		}
	}

	// A single operator (operatorClusterName == "") blocks >1 clusters. The block
	// defaults on when the enable flag is unset and stays on when explicitly "false"; an
	// empty value is not a valid bool, so it falls back to the default-on behavior too.
	for _, flag := range []struct{ name, value string }{
		{"flag empty (default on)", ""},
		{"flag false", "false"},
	} {
		t.Run(flag.name, func(t *testing.T) {
			t.Setenv(util.SearchEnableMultiClusterEnv, flag.value)
			search := newMongoDBSearch("search", mock.TestNamespace, "mdb")
			twoClusters(search)
			mdbc := newMongoDBCommunity("mdb", mock.TestNamespace)
			reconciler, c := newSearchReconciler(mdbc, search)
			checkSearchReconcileFailed(ctx, t, reconciler, c, search, blockMsg)
		})
	}

	// A single unnamed cluster is never blocked, regardless of the flag.
	t.Run("single cluster is allowed", func(t *testing.T) {
		search := newMongoDBSearch("search", mock.TestNamespace, "mdb")
		mdbc := newMongoDBCommunity("mdb", mock.TestNamespace)
		reconciler, c := newSearchReconciler(mdbc, search)

		updated := reconcileAndLoad(ctx, t, reconciler, c, search)
		assert.NotContains(t, updated.Status.Message, blockMsg, "single-cluster spec must not be blocked")
	})

	// A per-cluster operator (operatorClusterName set) narrows to its own entry, so >1
	// clusters is allowed even with the block on.
	t.Run("per-cluster operator is allowed", func(t *testing.T) {
		search := newMongoDBSearch("search", mock.TestNamespace, "mdb")
		twoClusters(search)
		reconciler, c := newSearchReconcilerWithMembers(t, nil, nil, "us-east", search)

		updated := reconcileAndLoad(ctx, t, reconciler, c, search)
		assert.NotContains(t, updated.Status.Message, blockMsg, "per-cluster operator must not be blocked")
	})

	// With the block disabled, a single operator with >1 clusters is not blocked either.
	t.Run("flag true skips the block", func(t *testing.T) {
		enableSearchMCReconcile(t)
		search := newMongoDBSearch("search", mock.TestNamespace, "mdb")
		twoClusters(search)
		mdbc := newMongoDBCommunity("mdb", mock.TestNamespace)
		reconciler, c := newSearchReconciler(mdbc, search)

		updated := reconcileAndLoad(ctx, t, reconciler, c, search)
		assert.NotContains(t, updated.Status.Message, blockMsg, "block must be skipped when flag is false")
	})
}

// reconcileAndLoad runs a single reconcile pass and returns the reloaded MongoDBSearch.
func reconcileAndLoad(
	ctx context.Context,
	t *testing.T,
	reconciler *MongoDBSearchReconciler,
	c client.Client,
	search *searchv1.MongoDBSearch,
) *searchv1.MongoDBSearch {
	_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: search.Name, Namespace: search.Namespace}})
	require.NoError(t, err)
	updated := &searchv1.MongoDBSearch{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: search.Name, Namespace: search.Namespace}, updated))
	return updated
}

func TestMongoDBSearchReconcile_InvalidSearchImageVersion(t *testing.T) {
	ctx := context.Background()
	expectedMsg := "MongoDBSearch version '1.47.0' is not supported. This operator requires MongoDBSearch version '1.70.1' or newer. The operator will ignore this resource: it will not reconcile or reconfigure the workload. Existing deployments will continue to run, but cannot be managed by the operator. To regain operator management, set a supported version and recreate the MongoDBSearch resource."

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
			if tc.statefulSetConfig != nil {
				search.Spec.Clusters = []searchv1.ClusterSpec{{StatefulSetConfiguration: tc.statefulSetConfig}}
			}

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

// pinnedCluster builds a spec.clusters[] entry with an explicit clusterIndex
// (required on every entry of a multi-cluster spec).
func pinnedCluster(name string, idx int32) searchv1.ClusterSpec {
	return searchv1.ClusterSpec{Name: name, Index: ptr.To(idx)}
}

// enableSearchMCReconcile turns off the default multi-cluster block for tests that
// exercise multi-cluster reconcile behavior.
func enableSearchMCReconcile(t *testing.T) {
	t.Setenv(util.SearchEnableMultiClusterEnv, "true")
}

func TestMongoDBSearchReconcile_Success_MultiCluster(t *testing.T) {
	enableSearchMCReconcile(t)
	ctx := context.Background()

	// MC GA requires external source (Q2); managed source + MC (Q3) is post-MVP.
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-search", Namespace: mock.TestNamespace},
		Spec: searchv1.MongoDBSearchSpec{
			Version: "1.70.1",
			Source: &searchv1.MongoDBSource{
				ExternalMongoDBSource: &searchv1.ExternalMongoDBSource{
					HostAndPorts: []string{"mdb-0.mdb.svc:27017", "mdb-1.mdb.svc:27017"},
				},
			},
		},
	}
	search.Spec.Clusters = []searchv1.ClusterSpec{
		{
			Name: "us-east", Index: ptr.To(int32(0)), Replicas: ptr.To(int32(1)),
			LoadBalancer: &searchv1.LoadBalancerConfig{Managed: &searchv1.ManagedLBConfig{ExternalHostname: "mongot-us-east.example.com"}},
		},
		{
			Name: "us-west", Index: ptr.To(int32(1)), Replicas: ptr.To(int32(1)),
			LoadBalancer: &searchv1.LoadBalancerConfig{Managed: &searchv1.ManagedLBConfig{ExternalHostname: "mongot-us-west.example.com"}},
		},
	}

	eastClient := mock.NewEmptyFakeClientBuilder().Build()
	westClient := mock.NewEmptyFakeClientBuilder().Build()
	memberClients := map[string]client.Client{
		"us-east": eastClient,
		"us-west": westClient,
	}

	reconciler, centralClient := newSearchReconcilerWithMembers(t, nil, memberClients, "", search)

	const maxPasses = 5
	got := driveSearchReconcileToRunning(ctx, t, reconciler, centralClient, search, maxPasses, eastClient, westClient)
	require.Equal(t, status.PhaseRunning, got.Status.Phase, "reconciler must reach Running within %d passes", maxPasses)

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
			assertSearchOwnerLabels(t, search, tc.clusterName, sts, headlessSvc, proxySvc, cm)
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
	operatorClusterName string,
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
	return newMongoDBSearchReconciler(centralClient, searchcontroller.OperatorSearchConfig{}, memberClients, operatorClusterName), centralClient
}

// driveSearchReconcileToRunning loops Reconcile up to maxPasses, seeding LoadBalancer
// status + marking STSes ready (on c plus readyOn) each pass: the per-unit STS
// readiness gate exits Pending until every unit's STS is ready, so one pass can't
// reach Running. Returns once Status.Phase==Running.
func driveSearchReconcileToRunning(
	ctx context.Context,
	t *testing.T,
	reconciler *MongoDBSearchReconciler,
	c client.Client,
	search *searchv1.MongoDBSearch,
	maxPasses int,
	readyOn ...client.Client,
) *searchv1.MongoDBSearch {
	t.Helper()
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: search.Name, Namespace: search.Namespace}}
	readyClients := append([]client.Client{c}, readyOn...)
	got := &searchv1.MongoDBSearch{}
	for i := 0; i < maxPasses; i++ {
		_, err := reconciler.Reconcile(ctx, req)
		require.NoError(t, err)
		require.NoError(t, c.Get(ctx, req.NamespacedName, got))
		if got.Status.LoadBalancer == nil {
			// The Envoy controller would normally drive LB status; seed it so
			// IsLoadBalancerReady() returns true once STSes are caught up.
			got.Status.LoadBalancer = &searchv1.LoadBalancerStatus{Phase: status.PhaseRunning}
			require.NoError(t, c.Status().Update(ctx, got))
		}
		require.NoError(t, mock.MarkAllStatefulSetsAsReady(ctx, search.Namespace, readyClients...))
		if got.Status.Phase == status.PhaseRunning {
			break
		}
	}
	return got
}

// assertSearchOwnerLabels verifies the cross-cluster enqueue labels (owner name/
// namespace + cluster name) on every operator-created object. Watch routing
// (EnqueueMemberClusterObjectToSearch) and label-based GC depend on these, so a
// path that creates resources without them passes existence checks but breaks
// re-enqueue in e2e only.
func assertSearchOwnerLabels(t *testing.T, search *searchv1.MongoDBSearch, clusterName string, objs ...client.Object) {
	t.Helper()
	for _, obj := range objs {
		labels := obj.GetLabels()
		assert.Equal(t, search.Name, labels[khandler.MongoDBSearchOwnerNameLabel], "owner-name label on %T %s", obj, obj.GetName())
		assert.Equal(t, search.Namespace, labels[khandler.MongoDBSearchOwnerNamespaceLabel], "owner-namespace label on %T %s", obj, obj.GetName())
		assert.Equal(t, clusterName, labels[khandler.MongoDBSearchClusterNameLabel], "cluster-name label on %T %s", obj, obj.GetName())
	}
}

func newOperatorPerClusterMongoDBSearch(name, namespace string) *searchv1.MongoDBSearch {
	clusters := []searchv1.ClusterSpec{
		{
			Name: "cluster-a", Index: ptr.To(int32(0)), Replicas: ptr.To(int32(1)),
			LoadBalancer: &searchv1.LoadBalancerConfig{Managed: &searchv1.ManagedLBConfig{ExternalHostname: "mongot-cluster-a.example.com"}},
		},
		{
			Name: "cluster-b", Index: ptr.To(int32(1)), Replicas: ptr.To(int32(1)),
			LoadBalancer: &searchv1.LoadBalancerConfig{Managed: &searchv1.ManagedLBConfig{ExternalHostname: "mongot-cluster-b.example.com"}},
		},
	}
	return &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: searchv1.MongoDBSearchSpec{
			Version: "1.70.1",
			Source: &searchv1.MongoDBSource{
				ExternalMongoDBSource: &searchv1.ExternalMongoDBSource{
					HostAndPorts: []string{"mdb-0.mdb.svc:27017", "mdb-1.mdb.svc:27017"},
				},
			},
			Clusters: clusters,
		},
	}
}

// operatorPerClusterProjectionCases drives the RS and sharded projected-reconcile tests;
// the non-zero pin guards that naming follows the pinned index, not array position.
var operatorPerClusterProjectionCases = []struct {
	name        string
	opCluster   string
	pinClusterB *int32 // override spec.clusters[1].clusterIndex when non-nil
	wantIdx     int
	wrongIdx    int
}{
	{name: "default_pins_cluster_a_index_0", opCluster: "cluster-a", pinClusterB: nil, wantIdx: 0, wrongIdx: 1},
	{name: "cluster_b_pinned_to_index_7", opCluster: "cluster-b", pinClusterB: ptr.To(int32(7)), wantIdx: 7, wrongIdx: 0},
}

func TestReconcile_OperatorPerCluster_ProjectedReconcilesLocalOnly(t *testing.T) {
	enableSearchMCReconcile(t)
	for _, tc := range operatorPerClusterProjectionCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			search := newOperatorPerClusterMongoDBSearch("mdb-search", mock.TestNamespace)
			if tc.pinClusterB != nil {
				search.Spec.Clusters[1].Index = tc.pinClusterB
			}

			reconciler, c := newSearchReconcilerWithMembers(t, nil, nil, tc.opCluster, search)

			got := driveSearchReconcileToRunning(ctx, t, reconciler, c, search, 5)

			// Local cluster's resources must exist at the PINNED index on the operator's client.
			sts := &appsv1.StatefulSet{}
			require.NoError(t, c.Get(ctx, search.StatefulSetNamespacedNameForCluster(tc.wantIdx), sts),
				"STS at pinned index %d must exist", tc.wantIdx)
			cm := &corev1.ConfigMap{}
			require.NoError(t, c.Get(ctx, search.MongotConfigConfigMapNameForCluster(tc.wantIdx), cm),
				"mongot ConfigMap at pinned index %d must exist", tc.wantIdx)
			proxy := &corev1.Service{}
			require.NoError(t, c.Get(ctx, search.ProxyServiceNamespacedNameForCluster(tc.wantIdx), proxy),
				"proxy Service at pinned index %d must exist", tc.wantIdx)
			headless := &corev1.Service{}
			require.NoError(t, c.Get(ctx, search.SearchServiceNamespacedNameForCluster(tc.wantIdx), headless),
				"headless search Service at pinned index %d must exist", tc.wantIdx)

			assertSearchOwnerLabels(t, search, tc.opCluster, sts, cm, proxy, headless)

			// The other cluster's index MUST be untouched — this operator never wrote it.
			err := c.Get(ctx, search.StatefulSetNamespacedNameForCluster(tc.wrongIdx), &appsv1.StatefulSet{})
			assert.True(t, apiErrors.IsNotFound(err), "STS at index %d must NOT exist; got err=%v", tc.wrongIdx, err)
			err = c.Get(ctx, search.MongotConfigConfigMapNameForCluster(tc.wrongIdx), &corev1.ConfigMap{})
			assert.True(t, apiErrors.IsNotFound(err), "mongot ConfigMap at index %d must NOT exist; got err=%v", tc.wrongIdx, err)
			err = c.Get(ctx, search.ProxyServiceNamespacedNameForCluster(tc.wrongIdx), &corev1.Service{})
			assert.True(t, apiErrors.IsNotFound(err), "proxy Service at index %d must NOT exist; got err=%v", tc.wrongIdx, err)
			err = c.Get(ctx, search.SearchServiceNamespacedNameForCluster(tc.wrongIdx), &corev1.Service{})
			assert.True(t, apiErrors.IsNotFound(err), "headless search Service at index %d must NOT exist; got err=%v", tc.wrongIdx, err)

			require.Equal(t, status.PhaseRunning, got.Status.Phase,
				"projected reconcile must reach Running; got %q (msg=%q)", got.Status.Phase, got.Status.Message)
		})
	}
}

func TestReconcile_OperatorPerCluster_NoMatchSilentNoOp(t *testing.T) {
	enableSearchMCReconcile(t)
	ctx := context.Background()
	search := newOperatorPerClusterMongoDBSearch("mdb-search", mock.TestNamespace)

	// operatorClusterName="cluster-c" — NOT in spec.clusters[].
	reconciler, c := newSearchReconcilerWithMembers(t, nil, nil, "cluster-c", search)

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
		c.Get(ctx, types.NamespacedName{Name: search.Name + "-search-state", Namespace: search.Namespace}, stateCM)),
		"state ConfigMap must not be created in no-match path")

	// Status must NOT have been mutated — Phase remains the zero value.
	updated := &searchv1.MongoDBSearch{}
	require.NoError(t, c.Get(ctx, req.NamespacedName, updated))
	assert.Equal(t, status.Phase(""), updated.Status.Phase, "no-match reconcile must not touch status")
}

// Customer pin is authoritative: re-pinning renders at the new index. The
// old-index resources leak — accepted MVP scope, no cleanup.
func TestReconcile_OperatorPerCluster_RePinUpdatesNames(t *testing.T) {
	enableSearchMCReconcile(t)
	ctx := context.Background()
	search := newOperatorPerClusterMongoDBSearch("mdb-search", mock.TestNamespace)

	reconciler, c := newSearchReconcilerWithMembers(t, nil, nil, "cluster-a", search)
	driveSearchReconcileToRunning(ctx, t, reconciler, c, search, 5)
	require.NoError(t, c.Get(ctx, search.StatefulSetNamespacedNameForCluster(0), &appsv1.StatefulSet{}),
		"initial reconcile must render at the original pinned index 0")

	// Customer re-pins cluster-a from 0 to 2.
	got := &searchv1.MongoDBSearch{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: search.Name, Namespace: search.Namespace}, got))
	got.Spec.Clusters[0].Index = ptr.To(int32(2))
	require.NoError(t, c.Update(ctx, got))

	driveSearchReconcileToRunning(ctx, t, reconciler, c, search, 5)

	require.NoError(t, c.Get(ctx, search.StatefulSetNamespacedNameForCluster(2), &appsv1.StatefulSet{}),
		"STS must exist at the new pinned index 2")
	require.NoError(t, c.Get(ctx, search.MongotConfigConfigMapNameForCluster(2), &corev1.ConfigMap{}),
		"mongot ConfigMap must exist at the new pinned index 2")
}

func newOperatorPerClusterShardedMongoDBSearch(name, namespace string) *searchv1.MongoDBSearch {
	// Each cluster gets its own externalHostname (distinct per cluster, {shardName} per-shard) and a
	// distinct shard-agnostic routerHostname (required for external sharded + managed LB).
	clusters := []searchv1.ClusterSpec{
		{
			Name: "cluster-a", Index: ptr.To(int32(0)), Replicas: ptr.To(int32(1)),
			LoadBalancer: &searchv1.LoadBalancerConfig{Managed: &searchv1.ManagedLBConfig{ExternalHostname: "{shardName}.cluster-a.example.com", RouterHostname: "router.cluster-a.example.com"}},
		},
		{
			Name: "cluster-b", Index: ptr.To(int32(1)), Replicas: ptr.To(int32(1)),
			LoadBalancer: &searchv1.LoadBalancerConfig{Managed: &searchv1.ManagedLBConfig{ExternalHostname: "{shardName}.cluster-b.example.com", RouterHostname: "router.cluster-b.example.com"}},
		},
	}
	return &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: searchv1.MongoDBSearchSpec{
			Version: "1.70.1",
			Source: &searchv1.MongoDBSource{
				ExternalMongoDBSource: &searchv1.ExternalMongoDBSource{
					ShardedCluster: &searchv1.ExternalShardedClusterConfig{
						Router: searchv1.ExternalRouterConfig{Hosts: []string{"mongos.example:27017"}},
						Shards: []searchv1.ExternalShardConfig{
							{ShardName: "sh-0", Hosts: []string{"sh-0-a.example:27017"}},
							{ShardName: "sh-1", Hosts: []string{"sh-1-a.example:27017"}},
						},
					},
				},
			},
			Security: searchv1.Security{TLS: &searchv1.TLS{CertsSecretPrefix: "certs"}},
			Clusters: clusters,
		},
	}
}

func operatorPerClusterShardedTLSSecrets(search *searchv1.MongoDBSearch, clusterIndex int) []client.Object {
	shards := []string{"sh-0", "sh-1"}
	out := make([]client.Object, 0, len(shards))
	for _, shard := range shards {
		nsName := search.TLSSecretForClusterShard(clusterIndex, shard)
		out = append(out, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: nsName.Name, Namespace: nsName.Namespace},
			Data:       map[string][]byte{"tls.crt": []byte("dummy"), "tls.key": []byte("dummy")},
		})
	}
	return out
}

func TestReconcile_OperatorPerCluster_ShardedSource_ProjectedReconcilesLocalOnly(t *testing.T) {
	enableSearchMCReconcile(t)
	for _, tc := range operatorPerClusterProjectionCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			search := newOperatorPerClusterShardedMongoDBSearch("mdb-search", mock.TestNamespace)
			if tc.pinClusterB != nil {
				search.Spec.Clusters[1].Index = tc.pinClusterB
			}

			reconciler, c := newSearchReconcilerWithMembers(t, nil, nil, tc.opCluster, search)
			for _, obj := range operatorPerClusterShardedTLSSecrets(search, tc.wantIdx) {
				require.NoError(t, c.Create(ctx, obj))
			}

			got := driveSearchReconcileToRunning(ctx, t, reconciler, c, search, 5)

			for _, shard := range []string{"sh-0", "sh-1"} {
				wantName := fmt.Sprintf("mdb-search-search-%d-%s", tc.wantIdx, shard)
				sts := &appsv1.StatefulSet{}
				require.NoError(t, c.Get(ctx, search.MongotStatefulSetForClusterShard(tc.wantIdx, shard), sts),
					"STS for shard %s at pinned index %d must exist", shard, tc.wantIdx)
				assert.Equal(t, wantName, sts.Name, "STS name must use pinned ClusterIndex %d, not array position 0", tc.wantIdx)
				cm := &corev1.ConfigMap{}
				require.NoError(t, c.Get(ctx, search.MongotConfigMapForClusterShard(tc.wantIdx, shard), cm),
					"mongot ConfigMap for shard %s must exist", shard)
				svc := &corev1.Service{}
				require.NoError(t, c.Get(ctx, search.MongotServiceForClusterShard(tc.wantIdx, shard), svc),
					"headless Service for shard %s must exist", shard)

				assertSearchOwnerLabels(t, search, tc.opCluster, sts, cm, svc)
			}

			clusterLevelProxy := &corev1.Service{}
			require.NoError(t, c.Get(ctx, search.ProxyServiceNamespacedNameForCluster(tc.wantIdx), clusterLevelProxy),
				"cluster-level proxy Service at pinned index %d must exist", tc.wantIdx)
			assertSearchOwnerLabels(t, search, tc.opCluster, clusterLevelProxy)

			// The other operator's index MUST be untouched.
			for _, shard := range []string{"sh-0", "sh-1"} {
				wrongName := fmt.Sprintf("mdb-search-search-%d-%s", tc.wrongIdx, shard)
				sts := &appsv1.StatefulSet{}
				err := c.Get(ctx, types.NamespacedName{Name: wrongName, Namespace: search.Namespace}, sts)
				assert.True(t, apiErrors.IsNotFound(err), "STS %q must NOT exist; err=%v", wrongName, err)
				cm := &corev1.ConfigMap{}
				err = c.Get(ctx, search.MongotConfigMapForClusterShard(tc.wrongIdx, shard), cm)
				assert.True(t, apiErrors.IsNotFound(err), "mongot ConfigMap for shard %s at idx %d must NOT exist; err=%v", shard, tc.wrongIdx, err)
				svc := &corev1.Service{}
				err = c.Get(ctx, search.MongotServiceForClusterShard(tc.wrongIdx, shard), svc)
				assert.True(t, apiErrors.IsNotFound(err), "headless Service for shard %s at idx %d must NOT exist; err=%v", shard, tc.wrongIdx, err)
			}
			wrongProxy := &corev1.Service{}
			err := c.Get(ctx, search.ProxyServiceNamespacedNameForCluster(tc.wrongIdx), wrongProxy)
			assert.True(t, apiErrors.IsNotFound(err), "cluster-level proxy Service at idx %d must NOT exist; err=%v", tc.wrongIdx, err)

			require.Equal(t, status.PhaseRunning, got.Status.Phase,
				"projected sharded reconcile must reach Running; got %q (msg=%q)", got.Status.Phase, got.Status.Message)
		})
	}
}

func TestReconcile_OperatorPerCluster_ShardedSource_NoMatchSilentNoOp(t *testing.T) {
	enableSearchMCReconcile(t)
	ctx := context.Background()
	search := newOperatorPerClusterShardedMongoDBSearch("mdb-search", mock.TestNamespace)

	// operatorClusterName="cluster-c" — NOT in spec.clusters[].
	reconciler, c := newSearchReconcilerWithMembers(t, nil, nil, "cluster-c", search)

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: search.Name, Namespace: search.Namespace}}
	res, err := reconciler.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, res, "no-match sharded reconcile must return zero Result with no error")

	// No per-shard resources at any (cluster, shard) pair.
	for _, idx := range []int{0, 1, 2} {
		for _, shard := range []string{"sh-0", "sh-1"} {
			sts := &appsv1.StatefulSet{}
			assert.True(t, apiErrors.IsNotFound(c.Get(ctx, search.MongotStatefulSetForClusterShard(idx, shard), sts)),
				"STS at index=%d shard=%s must not exist", idx, shard)
			cm := &corev1.ConfigMap{}
			assert.True(t, apiErrors.IsNotFound(c.Get(ctx, search.MongotConfigMapForClusterShard(idx, shard), cm)),
				"mongot ConfigMap at index=%d shard=%s must not exist", idx, shard)
		}
	}

	stateCM := &corev1.ConfigMap{}
	assert.True(t, apiErrors.IsNotFound(
		c.Get(ctx, types.NamespacedName{Name: search.Name + "-search-state", Namespace: search.Namespace}, stateCM)),
		"state ConfigMap must not be created in sharded no-match path")

	// Status must NOT have been mutated — Phase remains the zero value.
	updated := &searchv1.MongoDBSearch{}
	require.NoError(t, c.Get(ctx, req.NamespacedName, updated))
	assert.Equal(t, status.Phase(""), updated.Status.Phase, "no-match reconcile must not touch status")
}

// ClusterIndex enforcement runs before LocalizeToCluster, so bad shapes surface as Failed.
func TestReconcile_OperatorPerCluster_ClusterIndexEnforcement(t *testing.T) {
	enableSearchMCReconcile(t)
	ctx := context.Background()

	t.Run("missing clusterIndex on a single entry is Invalid", func(t *testing.T) {
		search := newOperatorPerClusterMongoDBSearch("mdb-search", mock.TestNamespace)
		// Single-entry unpinned passes ValidateSpec (MC validators skip at len==1), so the
		// failure is attributable to the sim-MC gate requiring the pin even on one entry.
		search.Spec.Clusters = []searchv1.ClusterSpec{
			{Name: "cluster-a", Replicas: ptr.To(int32(1))},
		}

		reconciler, c := newSearchReconcilerWithMembers(t, nil, nil, "cluster-a", search)
		req := reconcile.Request{NamespacedName: types.NamespacedName{Name: search.Name, Namespace: search.Namespace}}
		_, err := reconciler.Reconcile(ctx, req)
		require.NoError(t, err, "workflow.Invalid returns nil error; assert via Status")

		got := &searchv1.MongoDBSearch{}
		require.NoError(t, c.Get(ctx, req.NamespacedName, got))
		require.Equal(t, status.PhaseFailed, got.Status.Phase,
			"missing clusterIndex must surface as Failed phase, got %q (msg=%q)", got.Status.Phase, got.Status.Message)
		// workflow.Invalid capitalizes the first char, so match on the stable substring.
		assert.Contains(t, got.Status.Message,
			"one operator per cluster requires index on every spec.clusters[] entry (missing on",
			"failure must come from ValidateOperatorPerClusterIndices")
	})

	t.Run("partial pin on a multi-entry spec is Invalid", func(t *testing.T) {
		search := newOperatorPerClusterMongoDBSearch("mdb-search", mock.TestNamespace)
		// Drop the pin on the second entry: the general ValidateSpec rule fires
		// before the sim-MC gate.
		search.Spec.Clusters[1].Index = nil

		reconciler, c := newSearchReconcilerWithMembers(t, nil, nil, "cluster-a", search)
		req := reconcile.Request{NamespacedName: types.NamespacedName{Name: search.Name, Namespace: search.Namespace}}
		_, err := reconciler.Reconcile(ctx, req)
		require.NoError(t, err, "workflow.Invalid returns nil error; assert via Status")

		got := &searchv1.MongoDBSearch{}
		require.NoError(t, c.Get(ctx, req.NamespacedName, got))
		require.Equal(t, status.PhaseFailed, got.Status.Phase,
			"partial pin must surface as Failed phase, got %q (msg=%q)", got.Status.Phase, got.Status.Message)
		// workflow.Invalid capitalizes the first char, so match on the stable substring.
		assert.Contains(t, got.Status.Message,
			"index is required when len(spec.clusters)",
			"failure must come from validateClustersClusterIndexRequired")
	})

	t.Run("empty clusters is Invalid", func(t *testing.T) {
		search := newOperatorPerClusterMongoDBSearch("mdb-search", mock.TestNamespace)
		// Empty clusters means there is no per-cluster loadBalancer either, so the
		// failure is attributable to the clusters check, not the LB validators.
		search.Spec.Clusters = []searchv1.ClusterSpec{}

		reconciler, c := newSearchReconcilerWithMembers(t, nil, nil, "cluster-a", search)
		req := reconcile.Request{NamespacedName: types.NamespacedName{Name: search.Name, Namespace: search.Namespace}}
		_, err := reconciler.Reconcile(ctx, req)
		require.NoError(t, err, "workflow.Invalid returns nil error; assert via Status")

		got := &searchv1.MongoDBSearch{}
		require.NoError(t, c.Get(ctx, req.NamespacedName, got))
		require.Equal(t, status.PhaseFailed, got.Status.Phase,
			"empty clusters must surface as Failed phase, got %q (msg=%q)", got.Status.Phase, got.Status.Message)
		// workflow.Invalid capitalizes the first letter, so match on the stable substring.
		assert.Contains(t, got.Status.Message, "pec.clusters must contain at least one entry",
			"failure must come from the pre-localize validation gate")
	})
}

// Cluster-level proxy Service selector must match the label Envoy Deployment stamps on its Pods.
func TestMongoDBSearchReconcile_MCSharded_CrossControllerLabelInvariant(t *testing.T) {
	enableSearchMCReconcile(t)
	ctx := context.Background()

	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mdb-search",
			Namespace: mock.TestNamespace,
		},
		Spec: searchv1.MongoDBSearchSpec{
			Version: "1.70.1",
			Clusters: []searchv1.ClusterSpec{
				{
					Name: "cluster-a", Index: ptr.To(int32(0)), Replicas: ptr.To(int32(1)),
					LoadBalancer: &searchv1.LoadBalancerConfig{
						Managed: &searchv1.ManagedLBConfig{
							ExternalHostname: "{shardName}.mdb-search-search-0-proxy-svc.example.com",
							RouterHostname:   "mdb-search-search-0-proxy-svc.example.com",
						},
					},
				},
				{
					Name: "cluster-b", Index: ptr.To(int32(1)), Replicas: ptr.To(int32(1)),
					LoadBalancer: &searchv1.LoadBalancerConfig{
						Managed: &searchv1.ManagedLBConfig{
							ExternalHostname: "{shardName}.mdb-search-search-1-proxy-svc.example.com",
							RouterHostname:   "mdb-search-search-1-proxy-svc.example.com",
						},
					},
				},
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

	searchReconciler, centralClient := newSearchReconcilerWithMembers(t, nil, memberClients, "", search)
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
