package operator

import (
	"context"
	"reflect"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/architectures"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/watch"
	"k8s.io/apimachinery/pkg/types"

	"github.com/10gen/ops-manager-kubernetes/pkg/dns"
	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/versionutil"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/controlledfeature"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/mock"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestCreateOmProcess(t *testing.T) {
	sts := construct.DatabaseStatefulSet(*DefaultReplicaSetBuilder().SetName("dublin").Build(), construct.StandaloneOptions(construct.GetPodEnvOptions()), nil)
	process := createProcess(sts, util.AgentContainerName, DefaultStandaloneBuilder().Build())
	// Note, that for standalone the name of process is the name of statefulset - not the pod inside it.
	assert.Equal(t, "dublin", process.Name())
	assert.Equal(t, "dublin-0.dublin-svc.my-namespace.svc.cluster.local", process.HostName())
	assert.Equal(t, "4.0.0", process.Version())
}

func TestCreateOmProcesStatic(t *testing.T) {
	t.Setenv("MONGODB_IMAGE", "quay.io/mongodb/mongodb-enterprise-server")
	t.Setenv(architectures.DefaultEnvArchitecture, string(architectures.Static))

	sts := construct.DatabaseStatefulSet(*DefaultReplicaSetBuilder().SetName("dublin").Build(), construct.StandaloneOptions(construct.GetPodEnvOptions()), nil)
	process := createProcess(sts, util.AgentContainerName, DefaultStandaloneBuilder().Build())
	// Note, that for standalone the name of process is the name of statefulset - not the pod inside it.
	assert.Equal(t, "dublin", process.Name())
	assert.Equal(t, "dublin-0.dublin-svc.my-namespace.svc.cluster.local", process.HostName())
	assert.Equal(t, "4.0.0-ent", process.Version())
}

func TestOnAddStandalone(t *testing.T) {
	ctx := context.Background()
	st := DefaultStandaloneBuilder().SetVersion("4.1.0").SetService("mysvc").Build()

	reconciler, client := defaultStandaloneReconciler(ctx, st)

	checkReconcileSuccessful(ctx, t, reconciler, st, client)

	omConn := om.CurrMockedConnection

	// seems we don't need very deep checks here as there should be smaller tests specially for those methods
	assert.Len(t, client.GetMapForObject(&corev1.Service{}), 1)
	assert.Len(t, client.GetMapForObject(&appsv1.StatefulSet{}), 1)
	assert.Equal(t, *client.GetMapForObject(&appsv1.StatefulSet{})[st.ObjectKey()].(*appsv1.StatefulSet).Spec.Replicas, int32(1))
	assert.Len(t, client.GetMapForObject(&corev1.Secret{}), 2)

	omConn.CheckDeployment(t, createDeploymentFromStandalone(st), "auth", "tls")
	omConn.CheckNumberOfUpdateRequests(t, 1)
}

// TestOnAddStandaloneWithDelay checks the reconciliation on standalone creation with some "delay" in getting
// StatefulSet ready. The first reconciliation gets to Pending while the second reconciliation suceeds
func TestOnAddStandaloneWithDelay(t *testing.T) {
	ctx := context.Background()
	st := DefaultStandaloneBuilder().SetVersion("4.1.0").SetService("mysvc").Build()

	client := mock.NewClient().WithResource(ctx, st).WithStsReady(false).AddDefaultMdbConfigResources(ctx)
	manager := mock.NewManagerSpecificClient(client)

	reconciler := newStandaloneReconciler(ctx, manager, om.NewEmptyMockedOmConnection)

	checkReconcilePending(ctx, t, reconciler, st, "StatefulSet not ready", client, 3)
	client.WithStsReady(true)

	checkReconcileSuccessful(ctx, t, reconciler, st, client)
}

// TestAddDeleteStandalone checks that no state is left in OpsManager on removal of the standalone
func TestAddDeleteStandalone(t *testing.T) {
	ctx := context.Background()
	// First we need to create a standalone
	st := DefaultStandaloneBuilder().SetVersion("4.0.0").Build()

	reconciler, client := defaultStandaloneReconciler(ctx, st)

	checkReconcileSuccessful(ctx, t, reconciler, st, client)

	// Now delete it
	assert.NoError(t, reconciler.OnDelete(ctx, st, zap.S()))

	omConn := om.CurrMockedConnection
	// Operator doesn't mutate K8s state, so we don't check its changes, only OM
	omConn.CheckResourcesDeleted(t)

	// Note, that 'omConn.ReadAutomationStatus' happened twice - because the connection emulates agents delay in reaching goal state
	omConn.CheckOrderOfOperations(t,
		reflect.ValueOf(omConn.ReadUpdateDeployment), reflect.ValueOf(omConn.ReadAutomationStatus),
		reflect.ValueOf(omConn.ReadAutomationStatus), reflect.ValueOf(omConn.GetHosts), reflect.ValueOf(omConn.RemoveHost))
}

func TestStandaloneAuthenticationOwnedByOpsManager(t *testing.T) {
	ctx := context.Background()
	stBuilder := DefaultStandaloneBuilder()
	stBuilder.Spec.Security = nil
	st := stBuilder.Build()

	reconciler, client := defaultStandaloneReconciler(ctx, st)
	reconciler.omConnectionFactory = func(context *om.OMContext) om.Connection {
		context.Version = versionutil.OpsManagerVersion{
			VersionString: "5.0.0",
		}
		conn := om.NewEmptyMockedOmConnection(context)
		return conn
	}

	checkReconcileSuccessful(ctx, t, reconciler, st, client)

	mockedConn := om.CurrMockedConnection
	cf, _ := mockedConn.GetControlledFeature()

	assert.Len(t, cf.Policies, 2)
	assert.Equal(t, cf.ManagementSystem.Version, util.OperatorVersion)
	assert.Equal(t, cf.ManagementSystem.Name, util.OperatorName)
	assert.Equal(t, cf.Policies[0].PolicyType, controlledfeature.ExternallyManaged)
	assert.Len(t, cf.Policies[0].DisabledParams, 0)
}

func TestStandaloneAuthenticationOwnedByOperator(t *testing.T) {
	ctx := context.Background()
	st := DefaultStandaloneBuilder().Build()

	reconciler, client := defaultStandaloneReconciler(ctx, st)
	reconciler.omConnectionFactory = func(context *om.OMContext) om.Connection {
		context.Version = versionutil.OpsManagerVersion{
			VersionString: "5.0.0",
		}
		conn := om.NewEmptyMockedOmConnection(context)
		return conn
	}

	checkReconcileSuccessful(ctx, t, reconciler, st, client)

	mockedConn := om.CurrMockedConnection
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

	reconciler, client := defaultStandaloneReconciler(ctx, st)

	checkReconcileSuccessful(ctx, t, reconciler, st, client)

	svc, err := client.GetService(ctx, kube.ObjectKey(st.Namespace, st.ServiceName()))
	assert.NoError(t, err)
	assert.Equal(t, int32(30000), svc.Spec.Ports[0].Port)
}

func TestStandaloneCustomPodSpecTemplate(t *testing.T) {
	ctx := context.Background()
	st := DefaultStandaloneBuilder().SetPodSpecTemplate(corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"first": "val"}},
	}).Build()

	reconciler, client := defaultStandaloneReconciler(ctx, st)

	checkReconcileSuccessful(ctx, t, reconciler, st, client)

	statefulSet, err := client.GetStatefulSet(ctx, mock.ObjectKeyFromApiObject(st))
	assert.NoError(t, err)

	expectedLabels := map[string]string{
		"app": "dublin-svc", "controller": "mongodb-enterprise-operator",
		"first": "val", "pod-anti-affinity": "dublin",
	}
	assert.Equal(t, expectedLabels, statefulSet.Spec.Template.Labels)
}

// TestStandalone_ConfigMapAndSecretWatched
func TestStandalone_ConfigMapAndSecretWatched(t *testing.T) {
	ctx := context.Background()
	s := DefaultStandaloneBuilder().Build()

	reconciler, client := defaultStandaloneReconciler(ctx, s)

	checkReconcileSuccessful(ctx, t, reconciler, s, client)

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
	reconcilerFactory := func(s *mdbv1.MongoDB) (reconcile.Reconciler, *mock.MockedClient) {
		// Call the original defaultReplicaSetReconciler, which returns a *ReconcileMongoDbReplicaSet that implements reconcile.Reconciler
		reconciler, mockClient := defaultStandaloneReconciler(ctx, s)
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

	overridenResource := DefaultStandaloneBuilder().SetPodSpecTemplate(podTemplate).Build()
	overridenResources := testReconciliationResources{
		Resource:          overridenResource,
		ReconcilerFactory: reconcilerFactory,
	}

	agentVersionMappingTest(ctx, t, defaultResources, overridenResources)
}

// defaultStandaloneReconciler is the standalone reconciler used in unit test. It "adds" necessary
// additional K8s objects (st, connection config map and secrets) necessary for reconciliation,
// so it's possible to call 'reconcileAppDB()' on it right away
func defaultStandaloneReconciler(ctx context.Context, rs *mdbv1.MongoDB) (*ReconcileMongoDbStandalone, *mock.MockedClient) {
	manager := mock.NewManager(ctx, rs)
	manager.Client.AddDefaultMdbConfigResources(ctx)

	return newStandaloneReconciler(ctx, manager, om.NewEmptyMockedOmConnection), manager.Client
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
	return b.MongoDB.DeepCopy()
}

func createDeploymentFromStandalone(st *mdbv1.MongoDB) om.Deployment {
	d := om.NewDeployment()
	sts := construct.DatabaseStatefulSet(*st, construct.StandaloneOptions(construct.GetPodEnvOptions()), nil)
	hostnames, _ := dns.GetDnsForStatefulSet(sts, st.Spec.GetClusterDomain(), nil)
	process := om.NewMongodProcess(st.Name, hostnames[0], st.Spec.AdditionalMongodConfig, st.GetSpec(), "", nil)

	lastConfig, err := st.GetLastAdditionalMongodConfigByType(mdbv1.StandaloneConfig)
	if err != nil {
		panic(err)
	}

	d.MergeStandalone(process, st.Spec.AdditionalMongodConfig.ToMap(), lastConfig.ToMap(), nil)
	d.AddMonitoringAndBackup(zap.S(), st.Spec.GetSecurity().IsTLSEnabled(), util.CAFilePathInContainer)
	return d
}
