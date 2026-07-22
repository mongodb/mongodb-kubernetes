package operator

import (
	"context"
	"fmt"
	"maps"
	"strconv"
	"testing"

	"github.com/ghodss/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
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
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/watch"
	"github.com/mongodb/mongodb-kubernetes/controllers/searchcontroller"
	mdbcv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1" //nolint:depguard
	khandler "github.com/mongodb/mongodb-kubernetes/pkg/handler"
	"github.com/mongodb/mongodb-kubernetes/pkg/mongot"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/constants"
)

func observeControllerLogs(t *testing.T) *observer.ObservedLogs {
	t.Helper()
	core, logs := observer.New(zap.InfoLevel)
	previous := zap.L()
	zap.ReplaceGlobals(zap.New(core))
	t.Cleanup(func() { zap.ReplaceGlobals(previous) })
	return logs
}

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

func TestMongoDBSearchOnDelete_RemovesDependentWatches(t *testing.T) {
	ctx := context.Background()
	searchKey := types.NamespacedName{Name: "missing-watch-cleanup", Namespace: mock.TestNamespace}
	otherSearchKey := types.NamespacedName{Name: "other-search", Namespace: mock.TestNamespace}
	reconciler, _ := newSearchReconciler(nil, nil)

	sharedSecret := types.NamespacedName{Name: "shared-secret", Namespace: mock.TestNamespace}
	reconciler.watch.AddWatchedResourceIfNotAdded(sharedSecret.Name, sharedSecret.Namespace, watch.Secret, searchKey)
	reconciler.watch.AddWatchedResourceIfNotAdded(sharedSecret.Name, sharedSecret.Namespace, watch.Secret, otherSearchKey)
	reconciler.watch.AddWatchedResourceIfNotAdded("owned-config", mock.TestNamespace, watch.ConfigMap, searchKey)

	deletedSearch := &searchv1.MongoDBSearch{ObjectMeta: metav1.ObjectMeta{
		Name: searchKey.Name, Namespace: searchKey.Namespace, UID: "deleted-search-uid",
	}}
	require.NoError(t, reconciler.OnDelete(ctx, deletedSearch, zap.S()))

	for watchedObj, dependents := range reconciler.watch.GetWatchedResources() {
		assert.NotContains(t, dependents, searchKey, "deleted search must be removed from watcher key %s", watchedObj)
	}
	assert.Contains(t, reconciler.watch.GetWatchedResources()[watch.Object{ResourceType: watch.Secret, Resource: sharedSecret}], otherSearchKey)
}

func TestOperatorClusterNotInSearchSpec(t *testing.T) {
	for _, tc := range []struct {
		name                string
		operatorClusterName string
		clusters            []searchv1.ClusterSpec
		want                bool
	}{
		{name: "central operator", clusters: nil, want: false},
		{name: "projected cluster retained", operatorClusterName: "cluster-a", clusters: []searchv1.ClusterSpec{{Name: "cluster-a"}}, want: false},
		{name: "projected cluster removed with another retained", operatorClusterName: "cluster-a", clusters: []searchv1.ClusterSpec{{Name: "cluster-b"}}, want: true},
		{name: "empty topology remains a validation error", operatorClusterName: "cluster-a", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			search := &searchv1.MongoDBSearch{Spec: searchv1.MongoDBSearchSpec{Clusters: tc.clusters}}
			assert.Equal(t, tc.want, operatorClusterNotInSearchSpec(search, tc.operatorClusterName))
		})
	}
}

func TestMongoDBSearchOnDelete_SweepsOwnedResourcesOnMemberClusters(t *testing.T) {
	ctx := context.Background()
	searchKey := types.NamespacedName{Name: "missing-search", Namespace: mock.TestNamespace}
	deletedSearch := &searchv1.MongoDBSearch{ObjectMeta: metav1.ObjectMeta{
		Name: searchKey.Name, Namespace: searchKey.Namespace, UID: "deleted-search-uid",
	}}
	ownerLabels := khandler.SearchManagedLabels(deletedSearch, "", "", "")
	foreignLabels := maps.Clone(ownerLabels)
	foreignLabels[khandler.MongoDBSearchOwnerNameLabel] = "another-search"

	type clusterFixture struct {
		ownedSts *appsv1.StatefulSet
		ownedDep *appsv1.Deployment
		foreign  *corev1.Service
	}
	newFixture := func(prefix string) clusterFixture {
		return clusterFixture{
			ownedSts: &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: prefix + "-owned-sts", Namespace: searchKey.Namespace, Labels: maps.Clone(ownerLabels)}},
			ownedDep: &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: prefix + "-owned-deployment", Namespace: searchKey.Namespace, Labels: maps.Clone(ownerLabels)}},
			foreign:  &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: prefix + "-foreign", Namespace: searchKey.Namespace, Labels: foreignLabels}},
		}
	}
	deleteErr := fmt.Errorf("injected StatefulSet sweep failure")
	newClusterClient := func(fx clusterFixture, funcs interceptor.Funcs) client.Client {
		return mock.NewEmptyFakeClientBuilder().
			WithObjects(fx.ownedSts, fx.ownedDep, fx.foreign).
			WithInterceptorFuncs(funcs).
			Build()
	}
	failStsSweep := interceptor.Funcs{
		DeleteAllOf: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteAllOfOption) error {
			if _, ok := obj.(*appsv1.StatefulSet); ok {
				return deleteErr
			}
			return c.DeleteAllOf(ctx, obj, opts...)
		},
	}
	fixtureA, fixtureB, centralFixture := newFixture("member-a"), newFixture("member-b"), newFixture("central")
	memberA := newClusterClient(fixtureA, failStsSweep)
	memberB := newClusterClient(fixtureB, interceptor.Funcs{})
	centralClient := newClusterClient(centralFixture, interceptor.Funcs{})
	reconciler := newMongoDBSearchReconciler(centralClient, searchcontroller.OperatorSearchConfig{}, map[string]client.Client{"member-a": memberA, "member-b": memberB}, "")
	logs := observeControllerLogs(t)

	require.NoError(t, reconciler.OnDelete(ctx, deletedSearch, zap.S()),
		"member-cluster sweep failures are warnings, not OnDelete errors")

	assert.Positive(t, logs.FilterMessageSnippet(deleteErr.Error()).Len(), "expected a warning mentioning the injected sweep failure")
	// The failed StatefulSet sweep on member-a blocks neither the other kinds nor member-b.
	require.NoError(t, memberA.Get(ctx, client.ObjectKeyFromObject(fixtureA.ownedSts), &appsv1.StatefulSet{}))
	assert.True(t, apiErrors.IsNotFound(memberA.Get(ctx, client.ObjectKeyFromObject(fixtureA.ownedDep), &appsv1.Deployment{})))
	assert.True(t, apiErrors.IsNotFound(memberB.Get(ctx, client.ObjectKeyFromObject(fixtureB.ownedSts), &appsv1.StatefulSet{})))
	assert.True(t, apiErrors.IsNotFound(memberB.Get(ctx, client.ObjectKeyFromObject(fixtureB.ownedDep), &appsv1.Deployment{})))
	for _, member := range []struct {
		name    string
		client  client.Client
		fixture clusterFixture
	}{
		{name: "member-a", client: memberA, fixture: fixtureA},
		{name: "member-b", client: memberB, fixture: fixtureB},
	} {
		require.NoError(t, member.client.Get(ctx, client.ObjectKeyFromObject(member.fixture.foreign), &corev1.Service{}),
			"[%s] another Search's resources are never selected", member.name)
	}
	// Central-cluster resources carry controller owner references and are left
	// to native garbage collection — the sweep never touches the central cluster.
	require.NoError(t, centralClient.Get(ctx, client.ObjectKeyFromObject(centralFixture.ownedSts), &appsv1.StatefulSet{}))
	require.NoError(t, centralClient.Get(ctx, client.ObjectKeyFromObject(centralFixture.ownedDep), &appsv1.Deployment{}))
}

func TestMongoDBSearchDeploymentWatchesRouteLifecycleEvents(t *testing.T) {
	reconciler, _ := newSearchReconciler(nil)
	topologies := []struct {
		name    string
		watches []mongoDBSearchResourceWatch
	}{
		{name: "central", watches: centralMongoDBSearchResourceWatches(reconciler)},
		{name: "member", watches: memberMongoDBSearchResourceWatches(reconciler)},
	}
	searchKey := types.NamespacedName{Name: "search", Namespace: mock.TestNamespace}
	labeled := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
		Name:      "owned",
		Namespace: searchKey.Namespace,
		Labels: map[string]string{
			khandler.MongoDBSearchOwnerNameLabel:      searchKey.Name,
			khandler.MongoDBSearchOwnerNamespaceLabel: searchKey.Namespace,
		},
	}}
	plain := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: labeled.Name, Namespace: labeled.Namespace}}

	for _, topology := range topologies {
		t.Run(topology.name, func(t *testing.T) {
			var deploymentWatch *mongoDBSearchResourceWatch
			for i := range topology.watches {
				if _, ok := topology.watches[i].obj.(*appsv1.Deployment); ok {
					deploymentWatch = &topology.watches[i]
					break
				}
			}
			require.NotNil(t, deploymentWatch)
			require.Len(t, deploymentWatch.predicates, 1)
			p := deploymentWatch.predicates[0]

			tests := []struct {
				name string
				send func(workqueue.TypedRateLimitingInterface[reconcile.Request])
			}{
				{
					name: "create",
					send: func(q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
						e := event.TypedCreateEvent[client.Object]{Object: labeled}
						require.True(t, p.Create(event.CreateEvent{Object: labeled}))
						deploymentWatch.handler.Create(t.Context(), e, q)
					},
				},
				{
					// Removing the owner labels must still enqueue: the reconcile
					// sweep is what restores or finishes off the orphaned resource.
					name: "update label removal",
					send: func(q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
						e := event.TypedUpdateEvent[client.Object]{ObjectOld: labeled, ObjectNew: plain}
						require.True(t, p.Update(e))
						deploymentWatch.handler.Update(t.Context(), e, q)
					},
				},
			}
			for _, tc := range tests {
				t.Run(tc.name, func(t *testing.T) {
					q := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
					defer q.ShutDown()
					tc.send(q)
					require.Equal(t, 1, q.Len())
					req, shutdown := q.Get()
					require.False(t, shutdown)
					assert.Equal(t, searchKey, req.NamespacedName)
					q.Done(req)
				})
			}
		})
	}
}

func TestRegisterTLSResourceWatchesIncludesShardedMemberDependencies(t *testing.T) {
	reconciler, _ := newSearchReconciler(nil)
	search := newMongoDBSearch("search", mock.TestNamespace, "")
	search.Spec.Clusters = []searchv1.ClusterSpec{
		{Name: "cluster-a", Index: ptr.To(int32(0))},
		{Name: "cluster-b", Index: ptr.To(int32(3))},
	}
	search.Spec.Security.TLS = &searchv1.TLS{CertsSecretPrefix: "source-certs"}
	externalSource := &searchv1.ExternalMongoDBSource{
		ShardedCluster: &searchv1.ExternalShardedClusterConfig{
			Router: searchv1.ExternalRouterConfig{Hosts: []string{"mongos:27017"}},
			Shards: []searchv1.ExternalShardConfig{
				{ShardName: "shard-a", Hosts: []string{"shard-a:27017"}},
				{ShardName: "shard-b", Hosts: []string{"shard-b:27017"}},
			},
		},
		TLS: &searchv1.ExternalMongodTLS{CA: &corev1.LocalObjectReference{Name: "source-ca"}},
	}

	reconciler.registerTLSResourceWatches(search, searchcontroller.NewShardedExternalSearchSource(search.Namespace, externalSource))

	watched := reconciler.watch.GetWatchedResources()
	expected := []watch.Object{{
		ResourceType: watch.ConfigMap,
		Resource:     types.NamespacedName{Name: "source-ca", Namespace: search.Namespace},
	}}
	for _, cluster := range search.Spec.Clusters {
		for _, shardName := range []string{"shard-a", "shard-b"} {
			expected = append(expected, watch.Object{
				ResourceType: watch.Secret,
				Resource:     search.TLSSecretForClusterShard(cluster.ResolveIndex(), shardName),
			})
		}
	}
	for _, resource := range expected {
		assert.Contains(t, watched[resource], search.NamespacedName(), "missing dependency watch for %s", resource)
	}
}

func TestMongoDBSearchReconcile_DeletionTimestampTakesPriorityOverDisableAnnotation(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name          string
		newReconciler func(t *testing.T, search *searchv1.MongoDBSearch) (reconcile.Reconciler, client.Client)
		wantLog       string
		wantAbsentLog string
	}{
		{
			name: "main controller",
			newReconciler: func(t *testing.T, search *searchv1.MongoDBSearch) (reconcile.Reconciler, client.Client) {
				return newSearchReconciler(nil, search)
			},
			wantLog:       "is deleting; skipping main-controller reconcile",
			wantAbsentLog: "reconciliation disabled",
		},
		{
			name: "envoy controller",
			newReconciler: func(t *testing.T, search *searchv1.MongoDBSearch) (reconcile.Reconciler, client.Client) {
				c := fake.NewClientBuilder().WithScheme(envoyTestScheme(t)).WithStatusSubresource(&searchv1.MongoDBSearch{}).WithObjects(search).Build()
				return newMongoDBSearchEnvoyReconciler(c, "envoy:latest", nil, ""), c
			},
			wantLog: "is deleting; skipping envoy reconcile",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := observeControllerLogs(t)
			now := metav1.Now()
			search := newMongoDBSearch("search", mock.TestNamespace, "missing-source")
			search.DeletionTimestamp = &now
			search.Finalizers = []string{"kubernetes"}
			search.Annotations = map[string]string{searchv1.DisableReconciliationAnnotation: "true"}
			reconciler, c := tc.newReconciler(t, search)

			res, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: search.Name, Namespace: search.Namespace}})
			require.NoError(t, err)
			assert.Equal(t, reconcile.Result{}, res)

			updated := &searchv1.MongoDBSearch{}
			require.NoError(t, c.Get(ctx, types.NamespacedName{Name: search.Name, Namespace: search.Namespace}, updated))
			assert.Empty(t, updated.Status.Phase)
			assert.Nil(t, updated.Status.LoadBalancer)
			assert.Equal(t, 1, logs.FilterMessageSnippet(tc.wantLog).Len())
			if tc.wantAbsentLog != "" {
				assert.Zero(t, logs.FilterMessageSnippet(tc.wantAbsentLog).Len())
			}
		})
	}
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

	// Annotation matching is case-sensitive: "True" does not disable, so the
	// reconcile proceeds and the missing source surfaces as a failed phase.
	updated.Annotations[searchv1.DisableReconciliationAnnotation] = "True"
	require.NoError(t, c.Update(ctx, updated))
	res, err = reconciler.Reconcile(
		ctx,
		reconcile.Request{NamespacedName: types.NamespacedName{Name: search.Name, Namespace: search.Namespace}},
	)
	assert.NoError(t, err)
	assert.True(t, res.RequeueAfter > 0)
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: search.Name, Namespace: search.Namespace}, updated))
	assert.Equal(t, status.PhaseFailed, updated.Status.Phase)
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

	t.Run("state write conflict requeues instead of failing", func(t *testing.T) {
		ctx := context.Background()
		search := newMongoDBSearch("search", mock.TestNamespace, "mdb")
		mdbc := newMongoDBCommunity("mdb", mock.TestNamespace)
		// A state CM without owner labels forces the pre-reconcile no-op state
		// mutation to attempt a metadata-repair update, which conflicts here.
		stateCM := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: searchcontroller.SearchStateCMName(search), Namespace: search.Namespace},
		}
		c := mock.NewEmptyFakeClientBuilder().
			WithObjects(search, mdbc, stateCM).
			WithInterceptorFuncs(interceptor.Funcs{
				Update: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					if obj.GetName() == stateCM.Name {
						return apiErrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, stateCM.Name, nil)
					}
					return cl.Update(ctx, obj, opts...)
				},
			}).
			Build()
		reconciler := newMongoDBSearchReconciler(c, searchcontroller.OperatorSearchConfig{}, map[string]client.Client{}, "")

		res, err := reconciler.Reconcile(ctx, requestFromObject(search))
		require.NoError(t, err)
		assert.True(t, res.Requeue, "conflict must requeue, not fail")

		updated := &searchv1.MongoDBSearch{}
		require.NoError(t, c.Get(ctx, types.NamespacedName{Name: search.Name, Namespace: search.Namespace}, updated))
		assert.Equal(t, status.PhasePending, updated.Status.Phase)
	})
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
	assert.True(t, r.clusterRouter.NamedClustersAreLocal)
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
	assert.False(t, r.clusterRouter.NamedClustersAreLocal)
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

func TestMongoDBSearchReconcile_RegistersOperatorTLSSecretWatch(t *testing.T) {
	ctx := context.Background()
	search := newMongoDBSearch("search", mock.TestNamespace, "mdb")
	search.Spec.Security = searchv1.Security{TLS: &searchv1.TLS{CertsSecretPrefix: "certs"}}
	mdbc := newMongoDBCommunity("mdb", mock.TestNamespace)
	reconciler, c := newSearchReconciler(mdbc, search)

	sourceTLS := search.TLSSecretNamespacedName()
	require.NoError(t, c.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: sourceTLS.Name, Namespace: sourceTLS.Namespace},
		Data: map[string][]byte{
			"tls.crt": []byte("dummy"),
			"tls.key": []byte("dummy"),
		},
	}))

	_, err := reconciler.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{Name: search.Name, Namespace: search.Namespace},
	})
	require.NoError(t, err)

	operatorTLS := search.TLSOperatorSecretNamespacedName()
	watched := reconciler.watch.GetWatchedResources()
	assert.Contains(t, watched[watch.Object{ResourceType: watch.Secret, Resource: operatorTLS}], search.NamespacedName())
}

// pinnedCluster builds a spec.clusters[] entry with an explicit clusterIndex
// (required on every entry of a multi-cluster spec).
func pinnedCluster(name string, idx int32) searchv1.ClusterSpec {
	return searchv1.ClusterSpec{Name: name, Index: ptr.To(idx)}
}

func TestMongoDBSearchReconcile_Success_MultiCluster(t *testing.T) {
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
			assertSearchOwnerLabels(t, search, tc.clusterName, false, sts, headlessSvc, proxySvc, cm)
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
func assertSearchOwnerLabels(t *testing.T, search *searchv1.MongoDBSearch, clusterName string, sameCluster bool, objs ...client.Object) {
	t.Helper()
	for _, obj := range objs {
		labels := obj.GetLabels()
		assert.Equal(t, search.Name, labels[khandler.MongoDBSearchOwnerNameLabel], "owner-name label on %T %s", obj, obj.GetName())
		assert.Equal(t, search.Namespace, labels[khandler.MongoDBSearchOwnerNamespaceLabel], "owner-namespace label on %T %s", obj, obj.GetName())
		assert.Equal(t, clusterName, labels[khandler.MongoDBSearchClusterNameLabel], "cluster-name label on %T %s", obj, obj.GetName())
		if sameCluster {
			require.Len(t, obj.GetOwnerReferences(), 1, "same-cluster resource %T %s must retain the GC backstop", obj, obj.GetName())
			assert.Equal(t, search.UID, obj.GetOwnerReferences()[0].UID)
		} else {
			assert.Empty(t, obj.GetOwnerReferences(), "cross-cluster resource %T %s must remain label-only", obj, obj.GetName())
		}
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
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, UID: types.UID(name + "-uid")},
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

			assertSearchOwnerLabels(t, search, tc.opCluster, true, sts, cm, proxy, headless)

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

func TestReconcile_OperatorPerCluster_RemovedClusterCleansLocalResources(t *testing.T) {
	ctx := context.Background()
	search := newOperatorPerClusterMongoDBSearch("mdb-search", mock.TestNamespace)

	reconciler, c := newSearchReconcilerWithMembers(t, nil, nil, "cluster-c", search)
	legacyAuthLabels := khandler.SearchManagedLabels(search, "", "", "cluster-c")
	managed := []client.Object{
		&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "removed-mongot", Namespace: search.Namespace, UID: "removed-sts", Labels: khandler.SearchManagedLabels(search, "", searchMongotComponent, "cluster-c")}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "removed-headless", Namespace: search.Namespace, UID: "removed-headless", Labels: khandler.SearchManagedLabels(search, "", searchMongotComponent, "cluster-c")}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "removed-proxy", Namespace: search.Namespace, UID: "removed-proxy", Labels: khandler.SearchManagedLabels(search, "", searchProxyComponent, "cluster-c")}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "removed-config", Namespace: search.Namespace, UID: "removed-config", Labels: khandler.SearchManagedLabels(search, "", searchMongotComponent, "cluster-c")}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: searchcontroller.SearchStateCMName(search), Namespace: search.Namespace, UID: "removed-state", Labels: khandler.SearchManagedLabels(search, "", "", "")}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: search.X509OperatorManagedSecret().Name, Namespace: search.Namespace, UID: "legacy-x509", Labels: maps.Clone(legacyAuthLabels)}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: search.ScramClientCertOperatorManagedSecret().Name, Namespace: search.Namespace, UID: "legacy-scram", Labels: maps.Clone(legacyAuthLabels)}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "removed-secret", Namespace: search.Namespace, UID: "removed-secret", Labels: khandler.SearchManagedLabels(search, "", searchMongotComponent, "cluster-c")}},
	}
	for _, obj := range managed {
		require.NoError(t, c.Create(ctx, obj))
	}
	metricsConfig := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name:      "metrics-owned",
		Namespace: search.Namespace,
		UID:       "metrics-owned",
		Labels:    khandler.SearchManagedLabels(search, "", metricsForwarderLabelName, "cluster-c"),
	}}
	require.NoError(t, c.Create(ctx, metricsConfig))
	customerSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: "customer-source-secret", Namespace: search.Namespace, UID: "customer-source", Labels: maps.Clone(legacyAuthLabels),
		},
		Data: map[string][]byte{"value": []byte("customer")},
	}
	require.NoError(t, c.Create(ctx, customerSecret))

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: search.Name, Namespace: search.Namespace}}
	res, err := reconciler.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, res)

	for _, obj := range managed {
		err = c.Get(ctx, client.ObjectKeyFromObject(obj), obj)
		assert.True(t, apiErrors.IsNotFound(err), "%T %s must be deleted", obj, obj.GetName())
	}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(metricsConfig), &corev1.ConfigMap{}),
		"main cleanup must leave metrics resources to the metrics controller")
	preservedCustomer := &corev1.Secret{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(customerSecret), preservedCustomer))
	assert.Equal(t, customerSecret.UID, preservedCustomer.UID)
	assert.Equal(t, customerSecret.Data, preservedCustomer.Data)

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

func TestMongoDBSearchReconcile_HubRemovedClusterCleansManagedMemberResources(t *testing.T) {
	tests := []struct {
		name          string
		failStsDelete bool
	}{
		{name: "removed cluster's managed resources are deleted"},
		{name: "delete failure warns and does not fail the reconcile", failStsDelete: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			search := newOperatorPerClusterMongoDBSearch("mdb-search", mock.TestNamespace)
			search.Spec.Clusters = search.Spec.Clusters[:1]
			memberA := mock.NewEmptyFakeClientBuilder().Build()
			var memberB client.Client = mock.NewEmptyFakeClientBuilder().Build()
			labels := khandler.SearchManagedLabels(search, "", searchMongotComponent, "cluster-b")

			sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: search.StatefulSetNamespacedNameForCluster(1).Name, Namespace: search.Namespace, UID: "removed-sts", Labels: labels}}
			secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: search.StatefulSetNamespacedNameForCluster(1).Name + "-tls", Namespace: search.Namespace, UID: "removed-secret", Labels: maps.Clone(labels)}}
			require.NoError(t, memberB.Create(ctx, sts))
			require.NoError(t, memberB.Create(ctx, secret))
			injectedErr := fmt.Errorf("injected member delete failure")
			if tc.failStsDelete {
				memberB = interceptor.NewClient(memberB.(client.WithWatch), interceptor.Funcs{
					Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
						if _, ok := obj.(*appsv1.StatefulSet); ok {
							return injectedErr
						}
						return c.Delete(ctx, obj, opts...)
					},
				})
			}

			// The hub's own cluster is member-registered (hub-and-spoke): its member
			// sweep runs against the central cluster's storage, where the LIVE state
			// ConfigMap resides.
			centralClient := mock.NewEmptyFakeClientBuilder().WithStatusSubresource(&searchv1.MongoDBSearch{}).WithObjects(search).Build()
			stateCM := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
				Name:      searchcontroller.SearchStateCMName(search),
				Namespace: search.Namespace,
				UID:       "live-state-cm",
				Labels:    khandler.SearchManagedLabels(search, "", "", ""),
			}}
			require.NoError(t, centralClient.Create(ctx, stateCM))
			reconciler := newMongoDBSearchReconciler(centralClient, searchcontroller.OperatorSearchConfig{}, map[string]client.Client{
				"cluster-a":   memberA,
				"cluster-b":   memberB,
				"cluster-hub": centralClient,
			}, "")
			logs := observeControllerLogs(t)

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: search.Name, Namespace: search.Namespace}})
			require.NoError(t, err)

			// The removed-member sweep on the hub's own cluster must not touch the
			// central state ConfigMap.
			survivingStateCM := &corev1.ConfigMap{}
			require.NoError(t, centralClient.Get(ctx, client.ObjectKeyFromObject(stateCM), survivingStateCM),
				"the live central state ConfigMap must survive the removed-member sweep")
			assert.Equal(t, stateCM.UID, survivingStateCM.UID)

			assert.True(t, apiErrors.IsNotFound(memberB.Get(ctx, client.ObjectKeyFromObject(secret), &corev1.Secret{})),
				"one kind's delete failure must not block the other kinds")
			stsErr := memberB.Get(ctx, client.ObjectKeyFromObject(sts), &appsv1.StatefulSet{})
			if tc.failStsDelete {
				assert.NoError(t, stsErr, "failed delete leaves the StatefulSet behind for the next reconcile")
				assert.Positive(t, logs.FilterMessageSnippet(injectedErr.Error()).Len(), "expected a warning mentioning the injected delete failure")
			} else {
				assert.True(t, apiErrors.IsNotFound(stsErr))
				assert.Zero(t, logs.FilterMessageSnippet(injectedErr.Error()).Len())
			}
		})
	}
}

// Customer pin is authoritative: re-pinning renders at the new index. The
// old-index resources leak — accepted MVP scope, no cleanup.
func TestReconcile_OperatorPerCluster_RePinUpdatesNames(t *testing.T) {
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
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, UID: types.UID(name + "-uid")},
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

func TestMongoDBSearchReconcile_ShardReductionSweepsStaleResourcesAndTLSSecret(t *testing.T) {
	ctx := t.Context()
	search := newOperatorPerClusterShardedMongoDBSearch("mdb-search", mock.TestNamespace)
	reconciler, c := newSearchReconcilerWithMembers(t, nil, nil, "cluster-a", search)
	sourceSecrets := operatorPerClusterShardedTLSSecrets(search, 0)
	for i, obj := range sourceSecrets {
		secret := obj.(*corev1.Secret)
		secret.UID = types.UID(fmt.Sprintf("source-secret-%d-uid", i))
		require.NoError(t, c.Create(ctx, secret))
	}

	got := driveSearchReconcileToRunning(ctx, t, reconciler, c, search, 5)
	require.Equal(t, status.PhaseRunning, got.Status.Phase, got.Status.Message)
	for _, shardName := range []string{"sh-0", "sh-1"} {
		require.NoError(t, c.Get(ctx, search.MongotStatefulSetForClusterShard(0, shardName), &appsv1.StatefulSet{}))
		require.NoError(t, c.Get(ctx, search.MongotServiceForClusterShard(0, shardName), &corev1.Service{}))
		require.NoError(t, c.Get(ctx, search.MongotConfigMapForClusterShard(0, shardName), &corev1.ConfigMap{}))
		require.NoError(t, c.Get(ctx, search.TLSOperatorSecretForClusterShard(0, shardName), &corev1.Secret{}))
	}

	liveSearch := &searchv1.MongoDBSearch{}
	require.NoError(t, c.Get(ctx, search.NamespacedName(), liveSearch))
	liveSearch.Spec.Source.ExternalMongoDBSource.ShardedCluster.Shards = liveSearch.Spec.Source.ExternalMongoDBSource.ShardedCluster.Shards[:1]
	require.NoError(t, c.Update(ctx, liveSearch))

	result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: search.NamespacedName()})

	require.NoError(t, err)
	assert.Positive(t, result.RequeueAfter)
	updatedSearch := &searchv1.MongoDBSearch{}
	require.NoError(t, c.Get(ctx, search.NamespacedName(), updatedSearch))
	assert.Equal(t, status.PhaseRunning, updatedSearch.Status.Phase, updatedSearch.Status.Message)
	for _, stale := range []struct {
		key types.NamespacedName
		obj client.Object
	}{
		{key: search.MongotStatefulSetForClusterShard(0, "sh-1"), obj: &appsv1.StatefulSet{}},
		{key: search.MongotServiceForClusterShard(0, "sh-1"), obj: &corev1.Service{}},
		{key: search.MongotConfigMapForClusterShard(0, "sh-1"), obj: &corev1.ConfigMap{}},
		{key: search.TLSOperatorSecretForClusterShard(0, "sh-1"), obj: &corev1.Secret{}},
	} {
		assert.True(t, apiErrors.IsNotFound(c.Get(ctx, stale.key, stale.obj)), "%T %s", stale.obj, stale.key)
	}
	require.NoError(t, c.Get(ctx, search.MongotStatefulSetForClusterShard(0, "sh-0"), &appsv1.StatefulSet{}))
	require.NoError(t, c.Get(ctx, search.MongotServiceForClusterShard(0, "sh-0"), &corev1.Service{}))
	require.NoError(t, c.Get(ctx, search.MongotConfigMapForClusterShard(0, "sh-0"), &corev1.ConfigMap{}))
	require.NoError(t, c.Get(ctx, search.TLSOperatorSecretForClusterShard(0, "sh-0"), &corev1.Secret{}))
	for _, obj := range sourceSecrets {
		want := obj.(*corev1.Secret)
		actual := &corev1.Secret{}
		require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(want), actual))
		assert.Equal(t, want.UID, actual.UID)
		assert.Equal(t, want.Data, actual.Data)
	}
}

func TestReconcile_OperatorPerCluster_ShardedSource_ProjectedReconcilesLocalOnly(t *testing.T) {
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

				assertSearchOwnerLabels(t, search, tc.opCluster, true, sts, cm, svc)
			}

			clusterLevelProxy := &corev1.Service{}
			require.NoError(t, c.Get(ctx, search.ProxyServiceNamespacedNameForCluster(tc.wantIdx), clusterLevelProxy),
				"cluster-level proxy Service at pinned index %d must exist", tc.wantIdx)
			assertSearchOwnerLabels(t, search, tc.opCluster, true, clusterLevelProxy)

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
