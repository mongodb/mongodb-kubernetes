package operator

import (
	"context"
	"fmt"
	"strconv"
	"testing"

	"github.com/ghodss/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"
	"github.com/mongodb/mongodb-kubernetes/api/v1/status"
	userv1 "github.com/mongodb/mongodb-kubernetes/api/v1/user"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/controllers/searchcontroller"
	mdbcv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1/common"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/automationconfig"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/mongot"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/util/constants"
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

// newMongoDB builds an enterprise MongoDB CR wired to the default mock OM project
// and credentials so that the search controller can establish an OM connection when needed.
func newMongoDB(name, namespace string) *mdbv1.MongoDB {
	mdb := mdbv1.NewReplicaSetBuilder().
		SetName(name).
		SetNamespace(namespace).
		SetVersion("8.2.0").
		Build()
	mdb.Spec.OpsManagerConfig = &mdbv1.PrivateCloudConfig{
		ConfigMapRef: mdbv1.ConfigMapRef{Name: mock.TestProjectConfigMapName},
	}
	mdb.Spec.Credentials = mock.TestCredentialsSecretName
	return mdb
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
	ctx context.Context,
	operatorConfig searchcontroller.OperatorSearchConfig,
	interceptors *interceptor.Funcs,
	objects ...client.Object,
) (*MongoDBSearchReconciler, client.WithWatch, *om.CachedOMConnectionFactory) {
	builder := mock.NewEmptyFakeClientBuilder()
	builder.WithIndex(&searchv1.MongoDBSearch{}, searchcontroller.MongoDBSearchIndexFieldName, mdbcSearchIndexBuilder)

	if interceptors != nil {
		builder.WithInterceptorFuncs(*interceptors)
	}

	// for any MongoDBCommunity objects, create a corresponding keyfile secret
	for _, obj := range objects {
		if mdbc, ok := obj.(*mdbcv1.MongoDBCommunity); ok {
			keyfileSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      mdbc.GetAgentKeyfileSecretNamespacedName().Name,
					Namespace: mdbc.Namespace,
				},
				StringData: map[string]string{
					constants.AgentKeyfileKey: "keyfile",
				},
			}
			builder.WithObjects(keyfileSecret)
		}
	}

	for _, obj := range objects {
		if obj != nil {
			builder.WithObjects(obj)
		}
	}

	// add mock Ops Manager ConfigMap and Secret
	builder.WithObjects(mock.GetDefaultResources()...)
	connectionFactory := om.NewCachedOMConnectionFactory(om.NewEmptyMockedOmConnection)
	fakeClient := builder.Build()

	return newMongoDBSearchReconciler(ctx, fakeClient, operatorConfig, connectionFactory.GetConnectionFunc), fakeClient, connectionFactory
}

func newSearchReconciler(
	ctx context.Context,
	objects ...client.Object,
) (*MongoDBSearchReconciler, client.Client, *om.CachedOMConnectionFactory) {
	return newSearchReconcilerWithOperatorConfig(ctx, searchcontroller.OperatorSearchConfig{}, nil, objects...)
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
	reconciler, _, _ := newSearchReconciler(ctx, nil, nil)

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
	reconciler, _, _ := newSearchReconciler(ctx, nil, search)

	res, err := reconciler.Reconcile(
		ctx,
		reconcile.Request{NamespacedName: types.NamespacedName{Name: search.Name, Namespace: search.Namespace}},
	)

	assert.Error(t, err)
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
			reconciler, c, _ := newSearchReconcilerWithOperatorConfig(ctx, operatorConfig, nil, mdbc, search)

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
	reconciler, c, _ := newSearchReconciler(ctx, mdbc, search)

	checkSearchReconcileFailed(ctx, t, reconciler, c, search, "MongoDB version")
}

func TestMongoDBSearchReconcile_MultipleSearchResources(t *testing.T) {
	ctx := context.Background()
	search1 := newMongoDBSearch("search1", mock.TestNamespace, "mdb")
	search2 := newMongoDBSearch("search2", mock.TestNamespace, "mdb")
	mdbc := newMongoDBCommunity("mdb", mock.TestNamespace)
	reconciler, c, _ := newSearchReconciler(ctx, mdbc, search1, search2)

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
			reconciler, c, _ := newSearchReconcilerWithOperatorConfig(ctx, operatorConfig, nil, mdbc, search)

			checkSearchReconcileFailed(ctx, t, reconciler, c, search, expectedMsg)
		})
	}
}

// TestGetSourceMongoDB_ExternalSource verifies that an explicitly-configured external source is
// returned immediately without looking up any MongoDB CR.
func TestGetSourceMongoDB_ExternalSource(t *testing.T) {
	ctx := context.Background()
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "search", Namespace: mock.TestNamespace},
		Spec: searchv1.MongoDBSearchSpec{
			Source: &searchv1.MongoDBSource{
				ExternalMongoDBSource: &searchv1.ExternalMongoDBSource{
					HostAndPorts: []string{"external-host:27017"},
				},
			},
		},
	}
	reconciler, fakeClient, _ := newSearchReconciler(ctx, search)
	log := zaptest.NewLogger(t).Sugar()

	source, err := reconciler.getSourceMongoDBForSearch(ctx, fakeClient, search, log)

	require.NoError(t, err)
	require.NotNil(t, source)
	assert.Equal(t, []string{"external-host:27017"}, source.HostSeeds())
}

// TestGetSourceMongoDB_NoResourceRef verifies that an error is returned when the search resource
// has no source configured and no matching MongoDB or MongoDBCommunity resource exists. When
// Spec.Source is nil, the controller falls back to implicit name matching (using the search's own
// name), and returns an error when neither CR can be found.
func TestGetSourceMongoDB_NoResourceRef(t *testing.T) {
	ctx := context.Background()
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "search", Namespace: mock.TestNamespace},
		Spec:       searchv1.MongoDBSearchSpec{},
	}
	reconciler, fakeClient, _ := newSearchReconciler(ctx, search)
	log := zaptest.NewLogger(t).Sugar()

	_, err := reconciler.getSourceMongoDBForSearch(ctx, fakeClient, search, log)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "No database resource named")
}

// TestGetSourceMongoDB_CommunityMongoDB_ImplicitName verifies that when the MongoDBSearch has no
// explicit mongodbResourceRef, the controller resolves the source by matching the Search resource's
// own name against a MongoDBCommunity with that name.
func TestGetSourceMongoDB_CommunityMongoDB_ImplicitName(t *testing.T) {
	ctx := context.Background()
	mdbc := newMongoDBCommunity("mdb", mock.TestNamespace)
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "mdb", Namespace: mock.TestNamespace},
		Spec:       searchv1.MongoDBSearchSpec{Source: &searchv1.MongoDBSource{}},
	}
	reconciler, fakeClient, _ := newSearchReconciler(ctx, mdbc, search)
	log := zaptest.NewLogger(t).Sugar()

	source, err := reconciler.getSourceMongoDBForSearch(ctx, fakeClient, search, log)

	require.NoError(t, err)
	assert.IsType(t, searchcontroller.CommunitySearchSource{}, source)
}

// TestGetSourceMongoDB_EnterpriseMongoDB_ImplicitName verifies that when the MongoDBSearch has no
// explicit mongodbResourceRef and an enterprise MongoDB CR with the same name exists, the search
// source resolves to an EnterpriseResourceSearchSource.
func TestGetSourceMongoDB_EnterpriseMongoDB_ImplicitName(t *testing.T) {
	ctx := context.Background()
	mdb := newMongoDB("mdb", mock.TestNamespace)
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "mdb", Namespace: mock.TestNamespace},
		Spec:       searchv1.MongoDBSearchSpec{Source: &searchv1.MongoDBSource{}},
	}
	reconciler, fakeClient, _ := newSearchReconciler(ctx, nil, mdb, search)
	log := zaptest.NewLogger(t).Sugar()

	source, err := reconciler.getSourceMongoDBForSearch(ctx, fakeClient, search, log)

	require.NoError(t, err)
	assert.IsType(t, searchcontroller.EnterpriseResourceSearchSource{}, source)
}

// TestGetSourceMongoDB_EnterpriseTakesPriorityOverCommunity_ImplicitName verifies that when both a
// MongoDB and a MongoDBCommunity with the same name exist, the enterprise MongoDB is preferred.
func TestGetSourceMongoDB_EnterpriseTakesPriorityOverCommunity_ImplicitName(t *testing.T) {
	ctx := context.Background()
	mdb := newMongoDB("mdb", mock.TestNamespace)
	mdbc := newMongoDBCommunity("mdb", mock.TestNamespace)
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "mdb", Namespace: mock.TestNamespace},
		Spec:       searchv1.MongoDBSearchSpec{Source: &searchv1.MongoDBSource{}},
	}
	reconciler, fakeClient, _ := newSearchReconciler(ctx, mdbc, mdb, search)
	log := zaptest.NewLogger(t).Sugar()

	source, err := reconciler.getSourceMongoDBForSearch(ctx, fakeClient, search, log)

	require.NoError(t, err)
	assert.IsType(t, searchcontroller.EnterpriseResourceSearchSource{}, source)
}

// TestGetSourceMongoDB_EnterpriseMongoDB_WithExternalMembers verifies that when the enterprise
// MongoDB has external members, the OM deployment is consulted for the process hostnames from the
// automation config, and those hostnames appear in HostSeeds().
func TestGetSourceMongoDB_EnterpriseMongoDB_WithExternalMembers(t *testing.T) {
	ctx := context.Background()
	log := zaptest.NewLogger(t).Sugar()

	// One Kubernetes member and three external members, to verify that external members are included in the search source configuration.
	mdb := newMongoDB("mdb", mock.TestNamespace)
	mdb.Spec.Members = 1
	mdb.Spec.ExternalMembers = []string{"mdb-ext-0", "mdb-ext-1", "mdb-ext-2"}

	// Build a deployment with three processes in the "mdb" replica set,
	// imitating an existing MongoDB deployment outside of Kubernetes.
	spec := mdbv1.NewReplicaSetBuilder().SetVersion("8.2.0").Build().Spec
	mongodConfig := mdbv1.NewAdditionalMongodConfig("net.port", util.MongoDbDefaultPort)
	processes := []om.Process{
		om.NewMongodProcess("mdb-ext-0", "mdb-ext-0.external.com", "fake-image", false, mongodConfig, &spec, "", nil, ""),
		om.NewMongodProcess("mdb-ext-1", "mdb-ext-1.external.com", "fake-image", false, mongodConfig, &spec, "", nil, ""),
		om.NewMongodProcess("mdb-ext-2", "mdb-ext-2.external.com", "fake-image", false, mongodConfig, &spec, "", nil, ""),
	}
	options := make([]automationconfig.MemberOptions, len(processes))
	rs := om.NewReplicaSetWithProcesses(om.NewReplicaSet("mdb", "", "8.2.0"), processes, options)

	search := newMongoDBSearch("search", mock.TestNamespace, "mdb")

	reconciler, fakeClient, omConnectionFactory := newSearchReconciler(ctx, mdb, search)
	omConnectionFactory.SetPostCreateHook(func(conn om.Connection) {
		conn.ReadUpdateDeployment(func(d om.Deployment) error {
			d.MergeReplicaSet(rs, nil, nil, nil, log)
			return nil
		}, log)
	})

	source, err := reconciler.getSourceMongoDBForSearch(ctx, fakeClient, search, log)

	require.NoError(t, err)
	enterpriseSource, ok := source.(searchcontroller.EnterpriseResourceSearchSource)
	require.True(t, ok, "expected EnterpriseResourceSearchSource but got %T", source)
	assert.ElementsMatch(t, []string{
		// External members resolved via the OM automation config process hostname map.
		"mdb-ext-0.external.com:27017",
		"mdb-ext-1.external.com:27017",
		"mdb-ext-2.external.com:27017",
		// The single internal Kubernetes member.
		"mdb-0.mdb-svc.my-namespace.svc.cluster.local:27017",
	}, enterpriseSource.HostSeeds())
}

// TestGetSourceMongoDB_NeitherFound verifies that an error is returned when neither a MongoDB nor a
// MongoDBCommunity with the referenced name exists.
func TestGetSourceMongoDB_NeitherFound(t *testing.T) {
	ctx := context.Background()
	search := newMongoDBSearch("search", mock.TestNamespace, "nonexistent")
	reconciler, fakeClient, _ := newSearchReconciler(ctx, nil, search)
	log := zaptest.NewLogger(t).Sugar()

	_, err := reconciler.getSourceMongoDBForSearch(ctx, fakeClient, search, log)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "No database resource named")
}

// TestGetSourceMongoDB_EnterpriseGetError verifies that a non-404 error from the kube API while
// getting the enterprise MongoDB CR is propagated back.
func TestGetSourceMongoDB_EnterpriseGetError(t *testing.T) {
	ctx := context.Background()
	log := zaptest.NewLogger(t).Sugar()
	search := newMongoDBSearch("search", mock.TestNamespace, "mdb")

	interceptors := interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*mdbv1.MongoDB); ok {
				return fmt.Errorf("internal server error")
			}
			return c.Get(ctx, key, obj, opts...)
		},
	}

	reconciler, client, _ := newSearchReconcilerWithOperatorConfig(ctx, searchcontroller.OperatorSearchConfig{}, &interceptors, search)
	_, err := reconciler.getSourceMongoDBForSearch(ctx, client, search, log)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "error getting MongoDB")
}

// TestGetSourceMongoDB_CommunityGetError verifies that when the enterprise MongoDB CR is not found
// but a non-404 error occurs while getting the MongoDBCommunity CR, that error is propagated.
func TestGetSourceMongoDB_CommunityGetError(t *testing.T) {
	ctx := context.Background()
	log := zaptest.NewLogger(t).Sugar()
	search := newMongoDBSearch("search", mock.TestNamespace, "mdb")

	interceptors := interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*mdbcv1.MongoDBCommunity); ok {
				return fmt.Errorf("internal server error")
			}
			return c.Get(ctx, key, obj, opts...)
		},
	}

	reconciler, client, _ := newSearchReconcilerWithOperatorConfig(ctx, searchcontroller.OperatorSearchConfig{}, &interceptors, search)

	_, err := reconciler.getSourceMongoDBForSearch(ctx, client, search, log)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "error getting MongoDBCommunity")
}
