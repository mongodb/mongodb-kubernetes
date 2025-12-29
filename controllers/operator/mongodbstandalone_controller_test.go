package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/construct"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/controlledfeature"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/watch"
	mcoConstruct "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/controllers/construct"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/dns"
	"github.com/mongodb/mongodb-kubernetes/pkg/images"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/versionutil"
)

func TestCreateOmProcess(t *testing.T) {
	const mongodbImage = "quay.io/mongodb/mongodb-enterprise-server"
	sts := construct.DatabaseStatefulSet(*DefaultReplicaSetBuilder().SetName("dublin").Build(), construct.StandaloneOptions(construct.GetPodEnvOptions()), zap.S())
	process := createProcess(mongodbImage, false, sts, util.AgentContainerName, DefaultStandaloneBuilder().Build())
	// Note, that for standalone the name of process is the name of statefulset - not the pod inside it.
	assert.Equal(t, "dublin", process.Name())
	assert.Equal(t, "dublin-0.dublin-svc.my-namespace.svc.cluster.local", process.HostName())
	assert.Equal(t, "4.0.0", process.Version())
}

func TestCreateOmProcesStatic(t *testing.T) {
	const mongodbImage = "quay.io/mongodb/mongodb-enterprise-server"
	t.Setenv(architectures.DefaultEnvArchitecture, string(architectures.Static))

	sts := construct.DatabaseStatefulSet(*DefaultReplicaSetBuilder().SetName("dublin").Build(), construct.StandaloneOptions(construct.GetPodEnvOptions()), zap.S())
	process := createProcess(mongodbImage, false, sts, util.AgentContainerName, DefaultStandaloneBuilder().Build())
	// Note, that for standalone the name of process is the name of statefulset - not the pod inside it.
	assert.Equal(t, "dublin", process.Name())
	assert.Equal(t, "dublin-0.dublin-svc.my-namespace.svc.cluster.local", process.HostName())
	assert.Equal(t, "4.0.0-ent", process.Version())
}

func TestOnAddStandalone(t *testing.T) {
	ctx := context.Background()
	st := DefaultStandaloneBuilder().SetVersion("4.1.0").SetService("mysvc").Build()
	st.Status.FeatureCompatibilityVersion = "4.1"

	reconciler, kubeClient, omConnectionFactory := defaultStandaloneReconciler(ctx, nil, "", "", om.NewEmptyMockedOmConnection, st)

	checkReconcileSuccessful(ctx, t, reconciler, st, kubeClient)

	omConn := omConnectionFactory.GetConnection()

	// seems we don't need very deep checks here as there should be smaller tests specially for those methods
	assert.Len(t, mock.GetMapForObject(kubeClient, &corev1.Service{}), 1)
	assert.Len(t, mock.GetMapForObject(kubeClient, &appsv1.StatefulSet{}), 1)
	assert.Equal(t, *mock.GetMapForObject(kubeClient, &appsv1.StatefulSet{})[st.ObjectKey()].(*appsv1.StatefulSet).Spec.Replicas, int32(1))
	assert.Len(t, mock.GetMapForObject(kubeClient, &corev1.Secret{}), 3)

	omConn.(*om.MockedOmConnection).CheckDeployment(t, createDeploymentFromStandalone(st), "auth", "tls")
	omConn.(*om.MockedOmConnection).CheckNumberOfUpdateRequests(t, 1)
}

func TestStandaloneClusterReconcileContainerImages(t *testing.T) {
	databaseRelatedImageEnv := fmt.Sprintf("RELATED_IMAGE_%s_1_0_0", util.NonStaticDatabaseEnterpriseImage)
	initDatabaseRelatedImageEnv := fmt.Sprintf("RELATED_IMAGE_%s_2_0_0", util.InitDatabaseImageUrlEnv)

	imageUrlsMock := images.ImageUrls{
		databaseRelatedImageEnv:     "quay.io/mongodb/mongodb-kubernetes-database:@sha256:MONGODB_DATABASE",
		initDatabaseRelatedImageEnv: "quay.io/mongodb/mongodb-kubernetes-init-database:@sha256:MONGODB_INIT_DATABASE",
	}

	ctx := context.Background()
	st := DefaultStandaloneBuilder().SetVersion("8.0.0").Build()
	reconciler, kubeClient, _ := defaultReplicaSetReconciler(ctx, imageUrlsMock, "2.0.0", "1.0.0", st)

	checkReconcileSuccessful(ctx, t, reconciler, st, kubeClient)

	sts := &appsv1.StatefulSet{}
	err := kubeClient.Get(ctx, kube.ObjectKey(st.Namespace, st.Name), sts)
	assert.NoError(t, err)

	require.Len(t, sts.Spec.Template.Spec.InitContainers, 1)
	require.Len(t, sts.Spec.Template.Spec.Containers, 1)

	assert.Equal(t, "quay.io/mongodb/mongodb-kubernetes-init-database:@sha256:MONGODB_INIT_DATABASE", sts.Spec.Template.Spec.InitContainers[0].Image)
	assert.Equal(t, "quay.io/mongodb/mongodb-kubernetes-database:@sha256:MONGODB_DATABASE", sts.Spec.Template.Spec.Containers[0].Image)
}

func TestStandaloneClusterReconcileContainerImagesWithStaticArchitecture(t *testing.T) {
	t.Setenv(architectures.DefaultEnvArchitecture, string(architectures.Static))

	databaseRelatedImageEnv := fmt.Sprintf("RELATED_IMAGE_%s_8_0_0_ubi9", mcoConstruct.MongodbImageEnv)

	imageUrlsMock := images.ImageUrls{
		architectures.MdbAgentImageRepo: "quay.io/mongodb/mongodb-agent",
		mcoConstruct.MongodbImageEnv:    "quay.io/mongodb/mongodb-enterprise-server",
		databaseRelatedImageEnv:         "quay.io/mongodb/mongodb-enterprise-server:@sha256:MONGODB_DATABASE",
	}

	ctx := context.Background()
	st := DefaultStandaloneBuilder().SetVersion("8.0.0").Build()
	reconciler, kubeClient, omConnectionFactory := defaultReplicaSetReconciler(ctx, imageUrlsMock, "", "", st)
	omConnectionFactory.SetPostCreateHook(func(connection om.Connection) {
		connection.(*om.MockedOmConnection).SetAgentVersion("12.0.30.7791-1", "")
	})

	checkReconcileSuccessful(ctx, t, reconciler, st, kubeClient)

	sts := &appsv1.StatefulSet{}
	err := kubeClient.Get(ctx, kube.ObjectKey(st.Namespace, st.Name), sts)
	assert.NoError(t, err)

	assert.Len(t, sts.Spec.Template.Spec.InitContainers, 0)
	require.Len(t, sts.Spec.Template.Spec.Containers, 3)

	// Version from OM
	VerifyStaticContainers(t, sts.Spec.Template.Spec.Containers)
}

// TestOnAddStandaloneWithDelay checks the reconciliation on standalone creation with some "delay" in getting
// StatefulSet ready. The first reconciliation gets to Pending while the second reconciliation suceeds
func TestOnAddStandaloneWithDelay(t *testing.T) {
	ctx := context.Background()
	st := DefaultStandaloneBuilder().SetVersion("4.1.0").SetService("mysvc").Build()

	markStsAsReady := false
	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	kubeClient := mock.NewEmptyFakeClientBuilder().WithObjects(st).WithObjects(mock.GetDefaultResources()...).Build()
	kubeClient = interceptor.NewClient(kubeClient, interceptor.Funcs{
		Get: func(ctx context.Context, client client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			// we reference markAsStsReady from outside scope, which is modified to false below
			return mock.GetFakeClientInterceptorGetFunc(omConnectionFactory, markStsAsReady, markStsAsReady)(ctx, client, key, obj, opts...)
		},
	})

	reconciler := newStandaloneReconciler(ctx, kubeClient, nil, "fake-initDatabaseNonStaticImageVersion", "fake-databaseNonStaticImageVersion", false, false, false, "", omConnectionFactory.GetConnectionFunc)

	checkReconcilePending(ctx, t, reconciler, st, "StatefulSet not ready", kubeClient, 3)
	// this affects Get interceptor func, blocking automatically marking sts as ready
	markStsAsReady = true

	checkReconcileSuccessful(ctx, t, reconciler, st, kubeClient)
}

// TestAddDeleteStandalone checks that no state is left in OpsManager on removal of the standalone
func TestAddDeleteStandalone(t *testing.T) {
	ctx := context.Background()
	// First we need to create a standalone
	st := DefaultStandaloneBuilder().SetVersion("4.0.0").Build()

	reconciler, kubeClient, omConnectionFactory := defaultStandaloneReconciler(ctx, nil, "", "", om.NewEmptyMockedOmConnection, st)

	checkReconcileSuccessful(ctx, t, reconciler, st, kubeClient)

	// Now delete it
	assert.NoError(t, reconciler.OnDelete(ctx, st, zap.S()))

	mockedConn := omConnectionFactory.GetConnection().(*om.MockedOmConnection)
	// Operator doesn't mutate K8s state, so we don't check its changes, only OM
	mockedConn.CheckResourcesDeleted(t)

	// Note, that 'omConn.ReadAutomationStatus' happened twice - because the connection emulates agents delay in reaching goal state
	mockedConn.CheckOrderOfOperations(t,
		reflect.ValueOf(mockedConn.ReadUpdateDeployment), reflect.ValueOf(mockedConn.ReadAutomationStatus),
		reflect.ValueOf(mockedConn.ReadAutomationStatus), reflect.ValueOf(mockedConn.GetHosts), reflect.ValueOf(mockedConn.RemoveHost))
}

func TestStandaloneAuthenticationOwnedByOpsManager(t *testing.T) {
	ctx := context.Background()
	stBuilder := DefaultStandaloneBuilder()
	stBuilder.Spec.Security = nil
	st := stBuilder.Build()

	reconciler, kubeClient, omConnectionFactory := defaultStandaloneReconciler(ctx, nil, "", "", omConnectionFactoryFuncSettingVersion(), st)

	checkReconcileSuccessful(ctx, t, reconciler, st, kubeClient)

	cf, _ := omConnectionFactory.GetConnection().GetControlledFeature()

	assert.Len(t, cf.Policies, 2)
	assert.Equal(t, cf.ManagementSystem.Version, util.OperatorVersion)
	assert.Equal(t, cf.ManagementSystem.Name, util.OperatorName)
	assert.Equal(t, cf.Policies[0].PolicyType, controlledfeature.ExternallyManaged)
	assert.Len(t, cf.Policies[0].DisabledParams, 0)
}

func omConnectionFactoryFuncSettingVersion() func(context *om.OMContext) om.Connection {
	return func(context *om.OMContext) om.Connection {
		context.Version = versionutil.OpsManagerVersion{
			VersionString: "5.0.0",
		}
		conn := om.NewEmptyMockedOmConnection(context)
		return conn
	}
}

func TestStandaloneAuthenticationOwnedByOperator(t *testing.T) {
	ctx := context.Background()
	st := DefaultStandaloneBuilder().Build()

	reconciler, kubeClient, omConnectionFactory := defaultStandaloneReconciler(ctx, nil, "", "", omConnectionFactoryFuncSettingVersion(), st)

	checkReconcileSuccessful(ctx, t, reconciler, st, kubeClient)

	mockedConn := omConnectionFactory.GetConnection()
	cf, _ := mockedConn.GetControlledFeature()

	assert.Len(t, cf.Policies, 3)
	assert.Equal(t, cf.ManagementSystem.Version, util.OperatorVersion)
	assert.Equal(t, cf.ManagementSystem.Name, util.OperatorName)

	var policies []controlledfeature.PolicyType
	for _, p := range cf.Policies {
		policies = append(policies, p.PolicyType)
	}

	assert.Contains(t, policies, controlledfeature.ExternallyManaged)
	assert.Contains(t, policies, controlledfeature.DisableAuthenticationMechanisms)
}

func TestStandalonePortIsConfigurable_WithAdditionalMongoConfig(t *testing.T) {
	ctx := context.Background()
	config := mdbv1.NewAdditionalMongodConfig("net.port", 30000)
	st := mdbv1.NewStandaloneBuilder().
		SetNamespace(mock.TestNamespace).
		SetAdditionalConfig(config).
		SetConnectionSpec(testConnectionSpec()).
		Build()

	reconciler, kubeClient, _ := defaultStandaloneReconciler(ctx, nil, "", "", om.NewEmptyMockedOmConnection, st)

	checkReconcileSuccessful(ctx, t, reconciler, st, kubeClient)

	svc, err := kubeClient.GetService(ctx, kube.ObjectKey(st.Namespace, st.ServiceName()))
	assert.NoError(t, err)
	assert.Equal(t, int32(30000), svc.Spec.Ports[0].Port)
}

func TestStandaloneCustomPodSpecTemplate(t *testing.T) {
	ctx := context.Background()
	st := DefaultStandaloneBuilder().SetPodSpecTemplate(corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"first": "val"}},
	}).Build()

	reconciler, kubeClient, _ := defaultStandaloneReconciler(ctx, nil, "", "", om.NewEmptyMockedOmConnection, st)

	checkReconcileSuccessful(ctx, t, reconciler, st, kubeClient)

	statefulSet, err := kubeClient.GetStatefulSet(ctx, mock.ObjectKeyFromApiObject(st))
	assert.NoError(t, err)

	expectedLabels := map[string]string{
		"app": "dublin-svc", util.OperatorLabelName: util.OperatorLabelValue,
		"first": "val", "pod-anti-affinity": "dublin",
	}
	assert.Equal(t, expectedLabels, statefulSet.Spec.Template.Labels)
}

// TestStandalone_ConfigMapAndSecretWatched
func TestStandalone_ConfigMapAndSecretWatched(t *testing.T) {
	ctx := context.Background()
	s := DefaultStandaloneBuilder().Build()

	reconciler, kubeClient, _ := defaultStandaloneReconciler(ctx, nil, "", "", om.NewEmptyMockedOmConnection, s)

	checkReconcileSuccessful(ctx, t, reconciler, s, kubeClient)

	expected := map[watch.Object][]types.NamespacedName{
		{ResourceType: watch.ConfigMap, Resource: kube.ObjectKey(mock.TestNamespace, mock.TestProjectConfigMapName)}: {kube.ObjectKey(mock.TestNamespace, s.Name)},
		{ResourceType: watch.Secret, Resource: kube.ObjectKey(mock.TestNamespace, s.Spec.Credentials)}:               {kube.ObjectKey(mock.TestNamespace, s.Name)},
	}

	actual := reconciler.resourceWatcher.GetWatchedResources()
	assert.Equal(t, expected, actual)
}

func TestStandaloneAgentVersionMapping(t *testing.T) {
	ctx := context.Background()
	defaultResource := DefaultStandaloneBuilder().Build()
	// Go couldn't infer correctly that *ReconcileMongoDbReplicaset implemented *reconciler.Reconciler interface
	// without this anonymous function
	reconcilerFactory := func(s *mdbv1.MongoDB) (reconcile.Reconciler, kubernetesClient.Client) {
		// Call the original defaultReplicaSetReconciler, which returns a *ReconcileMongoDbReplicaSet that implements reconcile.Reconciler
		reconciler, mockClient, _ := defaultStandaloneReconciler(ctx, nil, "", "", om.NewEmptyMockedOmConnection, s)
		// Return the reconciler as is, because it implements the reconcile.Reconciler interface
		return reconciler, mockClient
	}
	defaultResources := testReconciliationResources{
		Resource:          defaultResource,
		ReconcilerFactory: reconcilerFactory,
	}

	containers := []corev1.Container{{Name: util.AgentContainerName, Image: "foo"}}
	podTemplate := corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: containers,
		},
	}

	overriddenResource := DefaultStandaloneBuilder().SetPodSpecTemplate(podTemplate).Build()
	overriddenResources := testReconciliationResources{
		Resource:          overriddenResource,
		ReconcilerFactory: reconcilerFactory,
	}

	agentVersionMappingTest(ctx, t, defaultResources, overriddenResources)
}

func TestStandaloneRoleAnnotationIsSet(t *testing.T) {
	ctx := context.Background()

	role := mdbv1.MongoDBRole{
		Role: "embedded-role",
		Db:   "admin",
		Roles: []mdbv1.InheritedRole{{
			Db:   "admin",
			Role: "read",
		}},
	}

	st := DefaultStandaloneBuilder().SetRoles([]mdbv1.MongoDBRole{role}).Build()
	reconciler, client, omConnectionFactory := defaultStandaloneReconciler(ctx, nil, "", "", om.NewEmptyMockedOmConnection, st)

	checkReconcileSuccessful(ctx, t, reconciler, st, client)

	roleString, _ := json.Marshal([]string{"embedded-role@admin"})

	// Assert that the member ids are saved in the annotation
	assert.Equal(t, st.GetAnnotations()[util.LastConfiguredRoles], string(roleString))

	roles := omConnectionFactory.GetConnection().(*om.MockedOmConnection).GetRoles()
	assert.Len(t, roles, 1)

	st.GetSecurity().Roles = []mdbv1.MongoDBRole{}
	err := client.Update(ctx, st)
	assert.NoError(t, err)

	checkReconcileSuccessful(ctx, t, reconciler, st, client)

	// Assert that the roles annotation is updated and role is removed
	assert.Equal(t, st.GetAnnotations()[util.LastConfiguredRoles], "[]")

	roles = omConnectionFactory.GetConnection().(*om.MockedOmConnection).GetRoles()
	assert.Len(t, roles, 0)
}

// defaultStandaloneReconciler is the standalone reconciler used in unit test. It "adds" necessary
// additional K8s objects (st, connection config map and secrets) necessary for reconciliation,
// so it's possible to call 'reconcileAppDB()' on it right away
func defaultStandaloneReconciler(ctx context.Context, imageUrls images.ImageUrls, initDatabaseNonStaticImageVersion, databaseNonStaticImageVersion string, omConnectionFactoryFunc om.ConnectionFactory, rs *mdbv1.MongoDB) (*ReconcileMongoDbStandalone, kubernetesClient.Client, *om.CachedOMConnectionFactory) {
	omConnectionFactory := om.NewCachedOMConnectionFactory(omConnectionFactoryFunc)
	kubeClient := mock.NewDefaultFakeClientWithOMConnectionFactory(omConnectionFactory, rs)
	return newStandaloneReconciler(ctx, kubeClient, imageUrls, initDatabaseNonStaticImageVersion, databaseNonStaticImageVersion, false, false, false, "", omConnectionFactory.GetConnectionFunc), kubeClient, omConnectionFactory
}

// TODO remove in favor of '/api/mongodbbuilder.go'
type StandaloneBuilder struct {
	*mdbv1.MongoDB
}

func DefaultStandaloneBuilder() *StandaloneBuilder {
	spec := mdbv1.MongoDbSpec{
		DbCommonSpec: mdbv1.DbCommonSpec{
			Version:    "4.0.0",
			Persistent: util.BooleanRef(true),
			ConnectionSpec: mdbv1.ConnectionSpec{
				SharedConnectionSpec: mdbv1.SharedConnectionSpec{
					OpsManagerConfig: &mdbv1.PrivateCloudConfig{
						ConfigMapRef: mdbv1.ConfigMapRef{
							Name: mock.TestProjectConfigMapName,
						},
					},
				}, Credentials: mock.TestCredentialsSecretName,
			},
			Security: &mdbv1.Security{
				Authentication: &mdbv1.Authentication{
					Modes: []mdbv1.AuthMode{},
				},
				TLSConfig: &mdbv1.TLSConfig{},
			},
			ResourceType: mdbv1.Standalone,
		},
		Members: 1,
	}
	resource := &mdbv1.MongoDB{ObjectMeta: metav1.ObjectMeta{Name: "dublin", Namespace: mock.TestNamespace}, Spec: spec}
	return &StandaloneBuilder{resource}
}

func (b *StandaloneBuilder) SetName(name string) *StandaloneBuilder {
	b.Name = name
	return b
}

func (b *StandaloneBuilder) SetVersion(version string) *StandaloneBuilder {
	b.Spec.Version = version
	return b
}

func (b *StandaloneBuilder) SetPersistent(p *bool) *StandaloneBuilder {
	b.Spec.Persistent = p
	return b
}

func (b *StandaloneBuilder) SetService(s string) *StandaloneBuilder {
	b.Spec.Service = s
	return b
}

func (b *StandaloneBuilder) SetRoles(roles []mdbv1.MongoDBRole) *StandaloneBuilder {
	if b.Spec.Security == nil {
		b.Spec.Security = &mdbv1.Security{}
	}
	b.Spec.Security.Roles = roles
	return b
}

func (b *StandaloneBuilder) SetPodSpecTemplate(spec corev1.PodTemplateSpec) *StandaloneBuilder {
	if b.Spec.PodSpec == nil {
		b.Spec.PodSpec = &mdbv1.MongoDbPodSpec{}
	}
	b.Spec.PodSpec.PodTemplateWrapper.PodTemplate = &spec
	return b
}

func (b *StandaloneBuilder) Build() *mdbv1.MongoDB {
	b.Spec.ResourceType = mdbv1.Standalone
	b.InitDefaults()
	return b.DeepCopy()
}

func createDeploymentFromStandalone(st *mdbv1.MongoDB) om.Deployment {
	d := om.NewDeployment()
	sts := construct.DatabaseStatefulSet(*st, construct.StandaloneOptions(construct.GetPodEnvOptions()), zap.S())
	hostnames, _ := dns.GetDnsForStatefulSet(sts, st.Spec.GetClusterDomain(), nil)
	process := om.NewMongodProcess(st.Name, hostnames[0], "fake-mongoDBImage", false, st.Spec.AdditionalMongodConfig, st.GetSpec(), "", nil, st.Status.FeatureCompatibilityVersion)

	lastConfig, err := st.GetLastAdditionalMongodConfigByType(mdbv1.StandaloneConfig)
	if err != nil {
		panic(err)
	}

	d.MergeStandalone(process, st.Spec.AdditionalMongodConfig.ToMap(), lastConfig.ToMap(), nil)
	d.ConfigureMonitoringAndBackup(zap.S(), st.Spec.GetSecurity().IsTLSEnabled(), util.CAFilePathInContainer)
	return d
}
