package operator

import (
	"testing"

	"time"

	"github.com/10gen/ops-manager-kubernetes/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/util"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func init() {
	logger, _ := zap.NewDevelopment()
	zap.ReplaceGlobals(logger)
}

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
	//  We need to "register" automation agents containers in OM soon after controller creates standalones
	// and waits for agents to be registered
	go registerAgents(t, controller, api, st)

	controller.onAddStandalone(st)

	omConn := controller.omConnection.(*om.MockedOmConnection)

	omConn.CheckDeployment(t, createDeploymentFromStandalone(st))
	omConn.CheckNumberOfUpdateRequests(t, 1)
}

func registerAgents(t *testing.T, controller *MongoDbController, kubeApi *MockedKubeApi, st *v1.MongoDbStandalone) {
	time.Sleep(300 * time.Millisecond)

	// At this stage we expect the code to be "waiting until agents are registered"
	zap.S().Info("Emulating agents are registered in OM")
	assert.NotNil(t, controller.omConnection)

	// todo more kube checks incapsulated into kube api
	assert.Len(t, kubeApi.services, 2)
	assert.Len(t, kubeApi.sets, 1)
	assert.Len(t, kubeApi.secrets, 2)

	// "registering" agents
	hostnames, _ := GetDnsNames(st.Name, st.ServiceName(), st.Namespace, st.Spec.ClusterName, 1)
	omConn := controller.omConnection.(*om.MockedOmConnection)
	omConn.SetHosts(hostnames)
}

type StandaloneBuilder struct {
	*v1.MongoDbStandalone
}

func DefaultStandaloneBuilder() *StandaloneBuilder {
	spec := &v1.MongoDbStandaloneSpec{
		Version:     "4.0.0",
		Persistent:  util.BooleanRef(false),
		Project:     ProjectConfigMapName,
		Credentials: CredentialsSecretName,
	}
	standalone := &v1.MongoDbStandalone{
		ObjectMeta: metav1.ObjectMeta{Name: "dublin", Namespace: Namespace},
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
