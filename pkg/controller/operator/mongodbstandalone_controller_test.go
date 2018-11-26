package operator

import (
	"testing"

	"os"

	"github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
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
	client.StsCreationDelayMillis = 200
	reconciler := newStandaloneReconciler(newMockedManagerSpecificClient(client), om.NewEmptyMockedOmConnection)

	checkReconcileSuccessful(t, reconciler, st, client)

	omConn := om.CurrMockedConnection

	// seems we don't need very deep checks here as there should be smaller tests specially for those methods
	assert.Len(t, client.services, 2)
	assert.Len(t, client.sets, 1)
	assert.Equal(t, *client.sets[st.ObjectKey()].(*appsv1.StatefulSet).Spec.Replicas, int32(1))
	assert.Len(t, client.secrets, 2)

	omConn.CheckDeployment(t, createDeploymentFromStandalone(st))
	omConn.CheckNumberOfUpdateRequests(t, 1)
}

func TestStandaloneEventMethodsHandlePanic(t *testing.T) {
	// nullifying env variable will result in panic exception raised
	os.Setenv(util.AutomationAgentImageUrl, "")
	st := DefaultStandaloneBuilder().Build()

	manager := newMockedManager(st)
	checkReconcileFailed(t, newStandaloneReconciler(manager, om.NewEmptyMockedOmConnection), st, "Failed to reconcile Mongodb Standalone", manager.client)

	// restoring
	InitDefaultEnvVariables()
}

type StandaloneBuilder struct {
	*v1.MongoDbStandalone
}

func DefaultStandaloneBuilder() *StandaloneBuilder {
	spec := &v1.MongoDbStandaloneSpec{
		Version:     "4.0.0",
		Persistent:  util.BooleanRef(false),
		Project:     TestProjectConfigMapName,
		Credentials: TestCredentialsSecretName,
	}
	standalone := &v1.MongoDbStandalone{
		Meta: v1.Meta{ObjectMeta: metav1.ObjectMeta{Name: "dublin", Namespace: TestNamespace}},
		Spec: *spec}
	return &StandaloneBuilder{standalone}
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
func (b *StandaloneBuilder) Build() *v1.MongoDbStandalone {
	return b.MongoDbStandalone
}

func createDeploymentFromStandalone(st *v1.MongoDbStandalone) om.Deployment {
	helper := createStatefulHelperFromStandalone(st)

	d := om.NewDeployment()
	hostnames, _ := GetDnsForStatefulSet(helper.BuildStatefulSet(), st.Spec.ClusterName)
	d.MergeStandalone(om.NewMongodProcess(st.Name, hostnames[0], st.Spec.Version), nil)
	d.AddMonitoringAndBackup(hostnames[0], zap.S())
	return d
}

func createStatefulHelperFromStandalone(sh *v1.MongoDbStandalone) *StatefulSetHelper {
	return defaultSetHelper().SetName(sh.Name).SetService(sh.ServiceName()).SetReplicas(1)
}
