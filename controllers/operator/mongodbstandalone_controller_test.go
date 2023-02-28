package operator

import (
	"reflect"
	"testing"

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
	process := createProcess(sts, util.DatabaseContainerName, DefaultStandaloneBuilder().Build())
	// Note, that for standalone the name of process is the name of statefulset - not the pod inside it.
	assert.Equal(t, "dublin", process.Name())
	assert.Equal(t, "dublin-0.dublin-svc.my-namespace.svc.cluster.local", process.HostName())
	assert.Equal(t, "4.0.0", process.Version())
}

func TestOnAddStandalone(t *testing.T) {
	st := DefaultStandaloneBuilder().SetVersion("4.1.0").SetService("mysvc").Build()

	reconciler, client := defaultStandaloneReconciler(st)

	checkReconcileSuccessful(t, reconciler, st, client)

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
	st := DefaultStandaloneBuilder().SetVersion("4.1.0").SetService("mysvc").Build()

	client := mock.NewClient().WithResource(st).WithStsReady(false).AddDefaultMdbConfigResources()
	manager := mock.NewManagerSpecificClient(client)

	reconciler := newStandaloneReconciler(manager, om.NewEmptyMockedOmConnection)

	checkReconcilePending(t, reconciler, st, "StatefulSet not ready", client, 3)
	client.WithStsReady(true)

	checkReconcileSuccessful(t, reconciler, st, client)
}

// TestAddDeleteStandalone checks that no state is left in OpsManager on removal of the standalone
func TestAddDeleteStandalone(t *testing.T) {
	// First we need to create a standalone
	st := DefaultStandaloneBuilder().SetVersion("4.0.0").Build()

	reconciler, client := defaultStandaloneReconciler(st)

	checkReconcileSuccessful(t, reconciler, st, client)

	// Now delete it
	assert.NoError(t, reconciler.OnDelete(st, zap.S()))

	omConn := om.CurrMockedConnection
	// Operator doesn't mutate K8s state, so we don't check its changes, only OM
	omConn.CheckResourcesDeleted(t)

	// Note, that 'omConn.ReadAutomationStatus' happened twice - because the connection emulates agents delay in reaching goal state
	omConn.CheckOrderOfOperations(t,
		reflect.ValueOf(omConn.ReadUpdateDeployment), reflect.ValueOf(omConn.ReadAutomationStatus),
		reflect.ValueOf(omConn.ReadAutomationStatus), reflect.ValueOf(omConn.GetHosts), reflect.ValueOf(omConn.RemoveHost))

}

func TestStandaloneAuthenticationOwnedByOpsManager(t *testing.T) {
	stBuilder := DefaultStandaloneBuilder()
	stBuilder.Spec.Security = nil
	st := stBuilder.Build()

	reconciler, client := defaultStandaloneReconciler(st)
	reconciler.omConnectionFactory = func(context *om.OMContext) om.Connection {
		context.Version = versionutil.OpsManagerVersion{
			VersionString: "5.0.0",
		}
		conn := om.NewEmptyMockedOmConnection(context)
		return conn
	}

	checkReconcileSuccessful(t, reconciler, st, client)

	mockedConn := om.CurrMockedConnection
	cf, _ := mockedConn.GetControlledFeature()

	assert.Len(t, cf.Policies, 2)
	assert.Equal(t, cf.ManagementSystem.Version, util.OperatorVersion)
	assert.Equal(t, cf.ManagementSystem.Name, util.OperatorName)
	assert.Equal(t, cf.Policies[0].PolicyType, controlledfeature.ExternallyManaged)
	assert.Len(t, cf.Policies[0].DisabledParams, 0)
}

func TestStandaloneAuthenticationOwnedByOperator(t *testing.T) {
	st := DefaultStandaloneBuilder().Build()

	reconciler, client := defaultStandaloneReconciler(st)
	reconciler.omConnectionFactory = func(context *om.OMContext) om.Connection {
		context.Version = versionutil.OpsManagerVersion{
			VersionString: "5.0.0",
		}
		conn := om.NewEmptyMockedOmConnection(context)
		return conn
	}

	checkReconcileSuccessful(t, reconciler, st, client)

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
	config := mdbv1.NewAdditionalMongodConfig("net.port", 30000)
	st := mdbv1.NewStandaloneBuilder().
		SetNamespace(mock.TestNamespace).
		SetAdditionalConfig(config).
		SetConnectionSpec(testConnectionSpec()).
		Build()

	reconciler, client := defaultStandaloneReconciler(st)

	checkReconcileSuccessful(t, reconciler, st, client)

	svc, err := client.GetService(kube.ObjectKey(st.Namespace, st.ServiceName()))
	assert.NoError(t, err)
	assert.Equal(t, int32(30000), svc.Spec.Ports[0].Port)
}

func TestStandaloneCustomPodSpecTemplate(t *testing.T) {
	st := DefaultStandaloneBuilder().SetPodSpecTemplate(corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"first": "val"}},
	}).Build()

	reconciler, client := defaultStandaloneReconciler(st)

	checkReconcileSuccessful(t, reconciler, st, client)

	statefulSet, err := client.GetStatefulSet(mock.ObjectKeyFromApiObject(st))
	assert.NoError(t, err)

	expectedLabels := map[string]string{"app": "dublin-svc", "controller": "mongodb-enterprise-operator",
		"first": "val", "pod-anti-affinity": "dublin"}
	assert.Equal(t, expectedLabels, statefulSet.Spec.Template.Labels)
}

// TestStandalone_ConfigMapAndSecretWatched
func TestStandalone_ConfigMapAndSecretWatched(t *testing.T) {
	s := DefaultStandaloneBuilder().Build()

	reconciler, client := defaultStandaloneReconciler(s)

	checkReconcileSuccessful(t, reconciler, s, client)

	expected := map[watch.Object][]types.NamespacedName{
		{ResourceType: watch.ConfigMap, Resource: kube.ObjectKey(mock.TestNamespace, mock.TestProjectConfigMapName)}: {kube.ObjectKey(mock.TestNamespace, s.Name)},
		{ResourceType: watch.Secret, Resource: kube.ObjectKey(mock.TestNamespace, s.Spec.Credentials)}:               {kube.ObjectKey(mock.TestNamespace, s.Name)},
	}

	assert.Equal(t, reconciler.WatchedResources, expected)
}

// defaultStandaloneReconciler is the standalone reconciler used in unit test. It "adds" necessary
// additional K8s objects (st, connection config map and secrets) necessary for reconciliation
// so it's possible to call 'reconcileAppDB()' on it right away
func defaultStandaloneReconciler(rs *mdbv1.MongoDB) (*ReconcileMongoDbStandalone, *mock.MockedClient) {
	manager := mock.NewManager(rs)
	manager.Client.AddDefaultMdbConfigResources()

	return newStandaloneReconciler(manager, om.NewEmptyMockedOmConnection), manager.Client
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
				OpsManagerConfig: &mdbv1.PrivateCloudConfig{
					ConfigMapRef: mdbv1.ConfigMapRef{
						Name: mock.TestProjectConfigMapName,
					},
				},
				Credentials: mock.TestCredentialsSecretName,
			},
			Security: &mdbv1.Security{
				Authentication: &mdbv1.Authentication{
					Modes: []string{},
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
	process := om.NewMongodProcess(0, st.Name, hostnames[0], st.Spec.AdditionalMongodConfig, st.GetSpec(), "")

	lastConfig, err := st.GetLastAdditionalMongodConfigByType(mdbv1.StandaloneConfig)
	if err != nil {
		panic(err)
	}

	d.MergeStandalone(process, st.Spec.AdditionalMongodConfig.ToMap(), lastConfig.ToMap(), nil)
	d.AddMonitoringAndBackup(zap.S(), st.Spec.GetSecurity().IsTLSEnabled(), util.CAFilePathInContainer)
	return d
}
