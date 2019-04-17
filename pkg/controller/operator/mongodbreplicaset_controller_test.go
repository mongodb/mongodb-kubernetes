package operator

import (
	"context"
	"os"
	"reflect"
	"testing"

	v1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type ReplicaSetBuilder struct {
	*v1.MongoDB
}

func TestReplicaSetEventMethodsHandlePanic(t *testing.T) {
	// nullifying env variable will result in panic exception raised
	_ = os.Setenv(util.AutomationAgentImageUrl, "")
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

	assert.Len(t, client.services, 1)
	assert.Len(t, client.sets, 1)
	assert.Equal(t, *client.getSet(rs.ObjectKey()).Spec.Replicas, int32(3))
	assert.Len(t, client.secrets, 2)

	connection := om.CurrMockedConnection
	connection.CheckDeployment(t, createDeploymentFromReplicaSet(rs))
	connection.CheckNumberOfUpdateRequests(t, 1)
}

// TestScaleUpReplicaSet verifies scaling up for replica set. Statefulset and OM Deployment must be changed accordingly
func TestScaleUpReplicaSet(t *testing.T) {
	rs := DefaultReplicaSetBuilder().SetMembers(3).Build()

	manager := newMockedManager(rs)
	client := manager.client

	reconciler := newReplicaSetReconciler(manager, om.NewEmptyMockedOmConnection)

	checkReconcileSuccessful(t, reconciler, rs, client)
	set := &appsv1.StatefulSet{}
	_ = client.Get(context.TODO(), objectKeyFromApiObject(rs), set)

	// Now scale up to 5 nodes
	rs = DefaultReplicaSetBuilder().SetMembers(5).Build()
	_ = client.Update(context.TODO(), rs)

	checkReconcileSuccessful(t, reconciler, rs, client)

	updatedSet := &appsv1.StatefulSet{}
	_ = client.Get(context.TODO(), objectKeyFromApiObject(rs), updatedSet)

	// Statefulset is expected to be the same - only number of replicas changed
	set.Spec.Replicas = util.Int32Ref(int32(5))
	assert.Equal(t, set.Spec, updatedSet.Spec)

	connection := om.CurrMockedConnection
	connection.CheckDeployment(t, createDeploymentFromReplicaSet(rs))
	connection.CheckNumberOfUpdateRequests(t, 2)
}

// TestAddDeleteReplicaSet checks that no state is left in OpsManager on removal of the replicaset
func TestAddDeleteReplicaSet(t *testing.T) {
	// First we need to create a replicaset
	st := DefaultReplicaSetBuilder().Build()

	kubeManager := newMockedManager(st)
	reconciler := newReplicaSetReconciler(kubeManager, om.NewEmptyMockedOmConnectionWithDelay)

	checkReconcileSuccessful(t, reconciler, st, kubeManager.client)
	omConn := om.CurrMockedConnection
	omConn.CleanHistory()

	// Now delete it
	assert.NoError(t, reconciler.delete(st, zap.S()))

	// Operator doesn't mutate K8s state, so we don't check its changes, only OM
	omConn.CheckResourcesDeleted(t)

	omConn.CheckOrderOfOperations(t,
		reflect.ValueOf(omConn.ReadUpdateDeployment), reflect.ValueOf(omConn.ReadAutomationStatus),
		reflect.ValueOf(omConn.ReadBackupConfigs), reflect.ValueOf(omConn.GetHosts), reflect.ValueOf(omConn.RemoveHost))

}
func DefaultReplicaSetBuilder() *ReplicaSetBuilder {
	spec := v1.MongoDbSpec{
		Version:      "4.0.0",
		Persistent:   util.BooleanRef(false),
		Project:      TestProjectConfigMapName,
		Credentials:  TestCredentialsSecretName,
		ResourceType: v1.ReplicaSet,
		Members:      3,
	}
	rs := &v1.MongoDB{Spec: spec, ObjectMeta: metav1.ObjectMeta{Name: "temple", Namespace: TestNamespace}}
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
func (b *ReplicaSetBuilder) Build() *v1.MongoDB {
	b.Spec.ResourceType = v1.ReplicaSet
	b.InitDefaults()
	return b.MongoDB
}

func createDeploymentFromReplicaSet(rs *v1.MongoDB) om.Deployment {
	helper := createStatefulHelperFromReplicaSet(rs)

	d := om.NewDeployment()
	hostnames, _ := GetDnsForStatefulSet(helper.BuildStatefulSet(), rs.Spec.ClusterName)
	d.MergeReplicaSet(buildReplicaSetFromStatefulSet(helper.BuildStatefulSet(), rs.Spec.ClusterName, rs.Spec.Version), nil)
	d.AddMonitoringAndBackup(hostnames[0], zap.S())

	return d
}

func createStatefulHelperFromReplicaSet(sh *v1.MongoDB) *StatefulSetHelper {
	return defaultSetHelper().SetName(sh.Name).SetService(sh.ServiceName()).SetReplicas(sh.Spec.Members)
}
