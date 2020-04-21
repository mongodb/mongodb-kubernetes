package operator

import (
	"os"
	"reflect"
	"testing"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/mock"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestCreateOmProcess(t *testing.T) {
	sts, _ := defaultSetHelper().SetName("dublin").BuildStatefulSet()
	process := createProcess(sts, DefaultStandaloneBuilder().Build())
	// Note, that for standalone the name of process is the name of statefulset - not the pod inside it.
	assert.Equal(t, "dublin", process.Name())
	assert.Equal(t, "dublin-0.test-service.my-namespace.svc.cluster.local", process.HostName())
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

	omConn.CheckDeployment(t, createDeploymentFromStandalone(st), "auth", "ssl")
	omConn.CheckNumberOfUpdateRequests(t, 1)
}

// TestOnAddStandaloneWithDelay checks the reconciliation on standalone creation with some "delay" in getting
// StatefulSet ready. The first reconciliation gets to Pending while the second reconciliation suceeds
func TestOnAddStandaloneWithDelay(t *testing.T) {
	st := DefaultStandaloneBuilder().SetVersion("4.1.0").SetService("mysvc").Build()

	client := mock.NewClient().WithResource(st).WithStsReady(false).AddDefaultMdbConfigResources()
	manager := mock.NewManagerSpecificClient(client)

	reconciler := newStandaloneReconciler(manager, om.NewEmptyMockedOmConnection)

	checkReconcilePending(t, reconciler, st, "MongoDB dublin resource is still starting", client)
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
	assert.NoError(t, reconciler.delete(st, zap.S()))

	omConn := om.CurrMockedConnection
	// Operator doesn't mutate K8s state, so we don't check its changes, only OM
	omConn.CheckResourcesDeleted(t)

	// Note, that 'omConn.ReadAutomationStatus' happened twice - because the connection emulates agents delay in reaching goal state
	omConn.CheckOrderOfOperations(t,
		reflect.ValueOf(omConn.ReadUpdateDeployment), reflect.ValueOf(omConn.ReadAutomationStatus),
		reflect.ValueOf(omConn.ReadAutomationStatus), reflect.ValueOf(omConn.GetHosts), reflect.ValueOf(omConn.RemoveHost))

}

func TestStandaloneEventMethodsHandlePanic(t *testing.T) {
	// restoring
	defer InitDefaultEnvVariables()

	// nullifying env variable will result in panic exception raised
	os.Setenv(util.AutomationAgentImage, "")
	st := DefaultStandaloneBuilder().Build()

	reconciler, client := defaultStandaloneReconciler(st)
	checkReconcileFailed(t,
		reconciler,
		st,
		true,
		"Failed to reconcile Mongodb Standalone: MONGODB_ENTERPRISE_DATABASE_IMAGE environment variable is not set!",
		client,
	)
}

func TestStandaloneCustomPodSpecTemplate(t *testing.T) {
	st := DefaultStandaloneBuilder().SetPodSpecTemplate(corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"first": "val"}},
	}).Build()

	reconciler, client := defaultStandaloneReconciler(st)

	checkReconcileSuccessful(t, reconciler, st, client)

	statefulSet := getStatefulSet(client, objectKeyFromApiObject(st))

	expectedLabels := map[string]string{"app": "dublin-svc", "controller": "mongodb-enterprise-operator",
		"first": "val", "pod-anti-affinity": "dublin"}
	assert.Equal(t, expectedLabels, statefulSet.Spec.Template.Labels)
}

// defaultStandaloneReconciler is the standalone reconciler used in unit test. It "adds" necessary
// additional K8s objects (st, connection config map and secrets) necessary for reconciliation
// so it's possible to call 'reconcile()' on it right away
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
		Version:    "4.0.0",
		Members:    1,
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
	b.Spec.PodSpec.PodTemplate = &spec
	return b
}

func (b *StandaloneBuilder) Build() *mdbv1.MongoDB {
	b.Spec.ResourceType = mdbv1.Standalone
	b.InitDefaults()
	return b.MongoDB.DeepCopy()
}

func createDeploymentFromStandalone(st *mdbv1.MongoDB) om.Deployment {
	helper := createStatefulHelperFromStandalone(st)

	d := om.NewDeployment()
	sts, _ := helper.BuildStatefulSet()
	hostnames, _ := util.GetDnsForStatefulSet(sts, st.Spec.GetClusterDomain())
	process := om.NewMongodProcess(st.Name, hostnames[0], st)
	d.MergeStandalone(process, nil)
	d.AddMonitoringAndBackup(hostnames[0], zap.S())
	return d
}

func createStatefulHelperFromStandalone(sh *mdbv1.MongoDB) *StatefulSetHelper {
	return defaultSetHelper().SetName(sh.Name).SetService(sh.ServiceName()).SetReplicas(1)
}
