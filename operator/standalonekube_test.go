package operator

import (
	"testing"

	"time"

	"os"

	"github.com/10gen/ops-manager-kubernetes/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/util"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
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

	api := newMockedKubeApi()
	controller := NewMongoDbController(api, nil, om.NewEmptyMockedOmConnection)
	//  We need to "start" statefulset pods and  "register" automation agents containers in OM soon after controller
	// creates standalones and waits for agents to be registered
	go registerAgents(t, controller, api, st)

	controller.onAddStandalone(st)

	omConn := om.CurrMockedConnection

	omConn.CheckDeployment(t, createDeploymentFromStandalone(st))
	omConn.CheckNumberOfUpdateRequests(t, 1)
}

func TestStandaloneEventMethodsHandlePanic(t *testing.T) {
	// nullifying env variable will result in panic exception raised
	os.Setenv(AutomationAgentImageUrl, "")
	st := DefaultStandaloneBuilder().Build()

	NewMongoDbController(newMockedKubeApi(), nil, om.NewEmptyMockedOmConnection).onAddStandalone(st)
	NewMongoDbController(newMockedKubeApi(), nil, om.NewEmptyMockedOmConnection).onUpdateStandalone(st, st)

	// restoring
	InitDefaultEnvVariables()
}

func registerAgents(t *testing.T, controller *MongoDbController, kubeApi *MockedKubeApi, st *v1.MongoDbStandalone) {
	time.Sleep(200 * time.Millisecond)

	// At this stage we expect the code to be "waiting until statefulset is started"
	zap.S().Info("Emulating pods start and agents registered in OM")
	assert.NotNil(t, om.CurrMockedConnection)

	// seems we don't need very deep checks here as there should be smaller tests specially for those methods
	assert.Len(t, kubeApi.services, 2)
	assert.Len(t, kubeApi.sets, 1)
	assert.Len(t, kubeApi.secrets, 2)

	// making statefulset "ready"
	kubeApi.startStatefulsets()

	// "registering" agents
	hostnames, _ := GetDnsNames(st.Name, st.ServiceName(), st.Namespace, st.Spec.ClusterName, 1)
	om.CurrMockedConnection.SetHosts(hostnames)
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
		ObjectMeta: metav1.ObjectMeta{Name: "dublin", Namespace: TestNamespace},
		Spec:       *spec}
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
