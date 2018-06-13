package operator

import (
	"testing"

	"reflect"

	"github.com/10gen/ops-manager-kubernetes/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func init() {
	logger, _ := zap.NewDevelopment()
	zap.ReplaceGlobals(logger)
}

func TestPrepareScaleDownShardedCluster(t *testing.T) {
	old := DefaultClusterBuilder().SetConfigServerCount(3).SetMongodsPerShardCount(4).Build()
	new := DefaultClusterBuilder().SetConfigServerCount(2).SetMongodsPerShardCount(3).Build()
	newState := createStateFromResource(new)

	oldDeployment := createDeploymentFromResource(old)
	mockedOmConnection := om.NewMockedOmConnection(oldDeployment)
	prepareScaleDownShardedCluster(mockedOmConnection, newState, old, new, zap.S())

	// expected change of state: rs members are marked unvoted
	expectedDeployment := createDeploymentFromResource(old)
	firstConfig := new.ConfigRsName() + "-2"
	firstShard := new.ShardRsName(0) + "-3"
	secondShard := new.ShardRsName(1) + "-3"

	expectedDeployment.MarkRsMembersUnvoted(new.ConfigRsName(), []string{firstConfig})
	expectedDeployment.MarkRsMembersUnvoted(new.ShardRsName(0), []string{firstShard})
	expectedDeployment.MarkRsMembersUnvoted(new.ShardRsName(1), []string{secondShard})

	mockedOmConnection.CheckNumberOfRequests(t, 1)
	mockedOmConnection.CheckDeployment(t, expectedDeployment)
	// we don't remove hosts from monitoring at this stage
	mockedOmConnection.CheckMonitoredHosts(t, []string{})
}

// TestPrepareScaleDownShardedCluster_OnlyMongos checks that if only mongos processes are scaled down - then no preliminary
// actions are done
func TestPrepareScaleDownShardedCluster_OnlyMongos(t *testing.T) {
	old := DefaultClusterBuilder().SetMongosCount(4).Build()
	new := DefaultClusterBuilder().SetMongosCount(2).Build()

	newState := createStateFromResource(new)

	oldDeployment := createDeploymentFromResource(old)
	mockedOmConnection := om.NewMockedOmConnection(oldDeployment)
	prepareScaleDownShardedCluster(mockedOmConnection, newState, old, new, zap.S())

	mockedOmConnection.CheckNumberOfRequests(t, 0)
	mockedOmConnection.CheckDeployment(t, createDeploymentFromResource(old))
	mockedOmConnection.CheckMonitoredHosts(t, []string{})
}

func TestUpdateOmDeploymentShardedCluster_HostsRemovedFromMonitoring(t *testing.T) {
	old := DefaultClusterBuilder().SetMongosCount(3).SetConfigServerCount(4).Build()
	new := DefaultClusterBuilder().SetMongosCount(1).SetConfigServerCount(3).Build()

	newState := createStateFromResource(new)

	mockOm := om.NewMockedOmConnection(createDeploymentFromResource(old))
	updateOmDeploymentShardedCluster(mockOm, old, new, newState, zap.S())

	mockOm.CheckOrderOfOperations(t, reflect.ValueOf(mockOm.ReadUpdateDeployment), reflect.ValueOf(mockOm.RemoveHost))

	// expected change of state: no unvoting - just monitoring deleted
	firstConfig := new.ConfigRsName() + "-3"
	firstMongos := new.MongosRsName() + "-1"
	secondMongos := new.MongosRsName() + "-2"

	mockOm.CheckMonitoredHosts(t, []string{
		firstConfig + ".slaney-cs.mongodb.svc.cluster.local",
		firstMongos + ".slaney-svc.mongodb.svc.cluster.local",
		secondMongos + ".slaney-svc.mongodb.svc.cluster.local",
	})
}

func createDeploymentFromResource(sh *v1.MongoDbShardedCluster) om.Deployment {
	state := createStateFromResource(sh)
	mongosProcesses := createProcesses(state.mongosSetHelper.BuildStatefulSet(), sh.Spec.ClusterName, sh.Spec.Version, om.ProcessTypeMongos)
	configRs := buildReplicaSetFromStatefulSet(state.configSrvSetHelper.BuildStatefulSet(), sh.Spec.ClusterName, sh.Spec.Version)
	shards := make([]om.ReplicaSetWithProcesses, len(state.shardsSetsHelpers))
	for i, s := range state.shardsSetsHelpers {
		shards[i] = buildReplicaSetFromStatefulSet(s.BuildStatefulSet(), sh.Spec.ClusterName, sh.Spec.Version)
	}

	d := om.NewDeployment()
	d.MergeShardedCluster(sh.Name, mongosProcesses, configRs, shards)
	return d
}

func createStateFromResource(sh *v1.MongoDbShardedCluster) KubeState {
	return KubeState{
		mongosSetHelper:    defaultSetHelper().SetName(sh.MongosRsName()).SetService(sh.MongosServiceName()).SetReplicas(sh.Spec.MongosCount),
		configSrvSetHelper: defaultSetHelper().SetName(sh.ConfigRsName()).SetService(sh.ConfigSrvServiceName()).SetReplicas(sh.Spec.ConfigServerCount),
		shardsSetsHelpers: []*StatefulSetHelper{defaultSetHelper().SetName(sh.ShardRsName(0)).SetService(sh.ShardServiceName()).SetReplicas(sh.Spec.MongodsPerShardCount),
			defaultSetHelper().SetName(sh.ShardRsName(1)).SetService(sh.ShardServiceName()).SetReplicas(sh.Spec.MongodsPerShardCount)}}
}

type ClusterBuilder struct {
	*v1.MongoDbShardedCluster
}

func DefaultClusterBuilder() *ClusterBuilder {
	spec := &v1.MongoDbShardedClusterSpec{
		ShardCount:           2,
		MongodsPerShardCount: 3,
		ConfigServerCount:    3,
		MongosCount:          4,
		Version:              "3.6.4"}
	cluster := &v1.MongoDbShardedCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "slaney", Namespace: "mongodb"},
		Spec:       *spec}
	return &ClusterBuilder{cluster}
}

func (b *ClusterBuilder) SetName(name string) *ClusterBuilder {
	b.Name = name
	return b
}
func (b *ClusterBuilder) SetShardCount(count int) *ClusterBuilder {
	b.Spec.ShardCount = count
	return b
}
func (b *ClusterBuilder) SetMongodsPerShardCount(count int) *ClusterBuilder {
	b.Spec.MongodsPerShardCount = count
	return b
}
func (b *ClusterBuilder) SetConfigServerCount(count int) *ClusterBuilder {
	b.Spec.ConfigServerCount = count
	return b
}
func (b *ClusterBuilder) SetMongosCount(count int) *ClusterBuilder {
	b.Spec.MongosCount = count
	return b
}
func (b *ClusterBuilder) Build() *v1.MongoDbShardedCluster {
	return b.MongoDbShardedCluster
}
