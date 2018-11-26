package operator

import (
	"os"
	"testing"

	"go.uber.org/zap"

	"github.com/stretchr/testify/assert"

	"github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	appsv1 "k8s.io/api/apps/v1"
)

type ReplicaSetBuilder struct {
	*v1.MongoDbReplicaSet
}

func TestReplicaSetEventMethodsHandlePanic(t *testing.T) {
	// nullifying env variable will result in panic exception raised
	os.Setenv(util.AutomationAgentImageUrl, "")
	rs := DefaultReplicaSetBuilder().Build()

	manager := newMockedManager(rs)
	checkReconcileFailed(t, newReplicaSetReconciler(manager, om.NewEmptyMockedOmConnection), rs, "Failed to reconcile Mongodb Replica Set", manager.client)

	// restoring
	InitDefaultEnvVariables()
}

func TestOnAddReplicaSet(t *testing.T) {
	rs := DefaultReplicaSetBuilder().Build()

	manager := newMockedManager(rs)
	client := manager.client

	reconciler := newReplicaSetReconciler(manager, om.NewEmptyMockedOmConnection)

	checkReconcileSuccessful(t, reconciler, rs, client)

	assert.Len(t, client.services, 2)
	assert.Len(t, client.sets, 1)
	assert.Equal(t, *client.sets[rs.ObjectKey()].(*appsv1.StatefulSet).Spec.Replicas, int32(3))
	assert.Len(t, client.secrets, 2)

	connection := om.CurrMockedConnection
	connection.CheckDeployment(t, createDeploymentFromReplicaSet(rs))
	connection.CheckNumberOfUpdateRequests(t, 1)
}

func TestOnDeleteReplicaSet(t *testing.T) {
	// TODO
	/*st := DefaultReplicaSetBuilder().Build()

	controller := NewMongoDbController(newMockedKubeApi(), nil, om.NewEmptyMockedOmConnection)

	// create first
	controller.onAddReplicaSet(st)

	// "enabling" backup
	om.CurrMockedConnection.EnableBackup(st.Name, om.ReplicaSetType)

	// then delete
	controller.onDeleteReplicaSet(st)
	om.CurrMockedConnection.CheckResourcesDeleted(t, st.Name, true)*/
}

func DefaultReplicaSetBuilder() *ReplicaSetBuilder {
	spec := &v1.MongoDbReplicaSetSpec{
		Version:     "4.0.0",
		Persistent:  util.BooleanRef(false),
		Project:     TestProjectConfigMapName,
		Credentials: TestCredentialsSecretName,
		Members:     3,
	}
	rs := &v1.MongoDbReplicaSet{
		Meta: v1.Meta{ObjectMeta: metav1.ObjectMeta{Name: "temple", Namespace: TestNamespace}},
		Spec:       *spec}
	return &ReplicaSetBuilder{rs}
}

func (b *ReplicaSetBuilder) SetName(name string) *ReplicaSetBuilder {
	b.Name = name
	return b
}
func (b *ReplicaSetBuilder) SetVersion(version string) *ReplicaSetBuilder {
	b.Spec.Version = version
	return b
}
func (b *ReplicaSetBuilder) SetPersistent(p *bool) *ReplicaSetBuilder {
	b.Spec.Persistent = p
	return b
}
func (b *ReplicaSetBuilder) SetMembers(m int) *ReplicaSetBuilder {
	b.Spec.Members = m
	return b
}
func (b *ReplicaSetBuilder) Build() *v1.MongoDbReplicaSet {
	return b.MongoDbReplicaSet
}

func createDeploymentFromReplicaSet(rs *v1.MongoDbReplicaSet) om.Deployment {
	helper := createStatefulHelperFromReplicaSet(rs)

	d := om.NewDeployment()
	hostnames, _ := GetDnsForStatefulSet(helper.BuildStatefulSet(), rs.Spec.ClusterName)
	d.MergeReplicaSet(buildReplicaSetFromStatefulSet(helper.BuildStatefulSet(), rs.Spec.ClusterName, rs.Spec.Version), nil)
	d.AddMonitoringAndBackup(hostnames[0], zap.S())

	return d
}

func createStatefulHelperFromReplicaSet(sh *v1.MongoDbReplicaSet) *StatefulSetHelper {
	return defaultSetHelper().SetName(sh.Name).SetService(sh.ServiceName()).SetReplicas(sh.Spec.Members)
}
