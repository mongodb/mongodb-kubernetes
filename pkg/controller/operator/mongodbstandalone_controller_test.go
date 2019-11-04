package operator

import (
	"reflect"
	"testing"

	"os"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestCreateOmProcess(t *testing.T) {
	process := createProcess(defaultSetHelper().BuildStatefulSet(), DefaultStandaloneBuilder().Build())
	// Note, that for standalone the name of process is the name of statefulset - not the pod inside it.
	assert.Equal(t, "dublin", process.Name())
	assert.Equal(t, "dublin-0.test-service.my-namespace.svc.cluster.local", process.HostName())
	assert.Equal(t, "4.0.0", process.Version())
}

func TestOnAddStandalone(t *testing.T) {
	st := DefaultStandaloneBuilder().SetVersion("4.1.0").SetService("mysvc").Build()

	client := newMockedClient(st)
	manager := newMockedManagerSpecificClient(client)

	reconciler := newStandaloneReconciler(manager, om.NewEmptyMockedOmConnection)

	checkReconcileSuccessful(t, reconciler, st, client)

	omConn := om.CurrMockedConnection

	// seems we don't need very deep checks here as there should be smaller tests specially for those methods
	assert.Len(t, client.services, 1)
	assert.Len(t, client.sets, 1)
	assert.Equal(t, *client.sets[st.ObjectKey()].(*appsv1.StatefulSet).Spec.Replicas, int32(1))
	assert.Len(t, client.secrets, 2)

	omConn.CheckDeployment(t, createDeploymentFromStandalone(st))
	omConn.CheckNumberOfUpdateRequests(t, 1)
}

// TestOnAddStandaloneWithDelay checks the reconciliation on standalone creation with a small delay.
// It emulates the kubernetes work on statefulset creation ('StsCreationDelayMillis') and makes sure
// the operator waits for this to finish.
func TestOnAddStandaloneWithDelay(t *testing.T) {
	st := DefaultStandaloneBuilder().SetVersion("4.1.0").SetService("mysvc").Build()

	client := newMockedClient(st)
	client.StsCreationDelayMillis = 200
	manager := newMockedManagerSpecificClient(client)

	reconciler := newStandaloneReconciler(manager, om.NewEmptyMockedOmConnection)

	checkReconcileSuccessful(t, reconciler, st, client)
}

// TestOnAddStandaloneWithDelayPending checks the reconciliation on standalone creation with a small delay
// if the Operator gives up before the StatefulSet gets ready.
// It emulates the kubernetes work on statefulset creation ('StsCreationDelayMillis') and makes sure
// the mongodb resource goes to Pending state
func TestOnAddStandaloneWithDelayPending(t *testing.T) {
	_ = os.Setenv(util.PodWaitRetriesEnv, "0")
	st := DefaultStandaloneBuilder().SetVersion("4.1.0").SetService("mysvc").Build()

	client := newMockedClient(st)
	client.StsCreationDelayMillis = 200
	manager := newMockedManagerSpecificClient(client)

	reconciler := newStandaloneReconciler(manager, om.NewEmptyMockedOmConnection)

	checkReconcilePending(t, reconciler, st, "MongoDB dublin resource is reconciling", client)

	InitDefaultEnvVariables()
}

// TestAddDeleteStandalone checks that no state is left in OpsManager on removal of the standalone
func TestAddDeleteStandalone(t *testing.T) {
	// First we need to create a standalone
	st := DefaultStandaloneBuilder().SetVersion("4.0.0").Build()

	kubeManager := newMockedManager(st)
	reconciler := newStandaloneReconciler(kubeManager, om.NewEmptyMockedOmConnectionWithDelay)

	checkReconcileSuccessful(t, reconciler, st, kubeManager.client)

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
	// nullifying env variable will result in panic exception raised
	os.Setenv(util.AutomationAgentImageUrl, "")
	st := DefaultStandaloneBuilder().Build()

	manager := newMockedManager(st)
	checkReconcileFailed(t, newStandaloneReconciler(manager, om.NewEmptyMockedOmConnection), st,
		"Failed to reconcile Mongodb Standalone: MONGODB_ENTERPRISE_DATABASE_IMAGE environment variable is not set!",
		manager.client)

	// restoring
	InitDefaultEnvVariables()
}

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
					Name: TestProjectConfigMapName,
				},
			},
			Credentials: TestCredentialsSecretName,
		},
		Security: &mdbv1.Security{
			Authentication: &mdbv1.Authentication{
				Modes: []string{},
			},
			TLSConfig: &mdbv1.TLSConfig{},
		},
		ResourceType: mdbv1.Standalone,
	}
	resource := &mdbv1.MongoDB{ObjectMeta: metav1.ObjectMeta{Name: "dublin", Namespace: TestNamespace}, Spec: spec}
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
func (b *StandaloneBuilder) Build() *mdbv1.MongoDB {
	b.Spec.ResourceType = mdbv1.Standalone
	b.InitDefaults()
	return b.MongoDB
}

func createDeploymentFromStandalone(st *mdbv1.MongoDB) om.Deployment {
	helper := createStatefulHelperFromStandalone(st)

	d := om.NewDeployment()
	hostnames, _ := GetDnsForStatefulSet(helper.BuildStatefulSet(), st.Spec.ClusterName)
	process := om.NewMongodProcess(st.Name, hostnames[0], st)
	d.MergeStandalone(process, nil)
	d.AddMonitoringAndBackup(hostnames[0], zap.S())
	return d
}

func createStatefulHelperFromStandalone(sh *mdbv1.MongoDB) *StatefulSetHelper {
	return defaultSetHelper().SetName(sh.Name).SetService(sh.ServiceName()).SetReplicas(1)
}
