package operator

// Tests for the distributed-multi-cluster operator PoC gate points
// (chunks C4-C6 in docs/dev/distributed-multicluster-poc-implementation-plan.md).
//
// These tests deliberately do NOT spin up a real Raft cluster — they stub
// DistributedCoordinator directly so we can assert gate-point behaviour in
// isolation. The end-to-end three-in-process integration test lives in
// mongodbshardedcluster_controller_multi_distributed_test.go (C7).

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/coordination"
	"github.com/mongodb/mongodb-kubernetes/pkg/test"
)

// fakeCoordinator is a DistributedCoordinator implementation backed by simple
// in-memory state. Tests construct it inline and inject via SetCoordinator.
type fakeCoordinator struct {
	mu sync.Mutex

	clusterName  string
	leader       bool
	activeLease  *coordination.LeaseInfo
	acGeneration int
	perCluster   map[string]coordination.ClusterStatusReport

	// Recordings — tests assert on these.
	leaseCompletes      []coordination.LeaseInfo
	statusReports       []coordination.ClusterStatusReport
	acPublishedProposed []int
}

func newFakeCoordinator(clusterName string, leader bool) *fakeCoordinator {
	return &fakeCoordinator{
		clusterName: clusterName,
		leader:      leader,
		perCluster:  map[string]coordination.ClusterStatusReport{},
	}
}

func (f *fakeCoordinator) MyClusterName() string { return f.clusterName }

func (f *fakeCoordinator) IsLeader() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.leader
}

func (f *fakeCoordinator) setLeader(b bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.leader = b
}

func (f *fakeCoordinator) HasLeaseFor(component, clusterName string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.activeLease != nil && f.activeLease.Component == component && f.activeLease.ClusterName == clusterName
}

func (f *fakeCoordinator) setLease(component, clusterName string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.activeLease = &coordination.LeaseInfo{Component: component, ClusterName: clusterName}
}

func (f *fakeCoordinator) ProposeLeaseComplete(component, clusterName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.leaseCompletes = append(f.leaseCompletes, coordination.LeaseInfo{Component: component, ClusterName: clusterName})
	if f.activeLease != nil && f.activeLease.Component == component && f.activeLease.ClusterName == clusterName {
		f.activeLease = nil
	}
	return nil
}

func (f *fakeCoordinator) ProposeStatusReport(r coordination.ClusterStatusReport) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statusReports = append(f.statusReports, r)
	f.perCluster[r.ClusterName] = r
	return nil
}

func (f *fakeCoordinator) ProposeACPublished(generation int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acPublishedProposed = append(f.acPublishedProposed, generation)
	if generation > f.acGeneration {
		f.acGeneration = generation
	}
	return nil
}

func (f *fakeCoordinator) GetActiveLease() *coordination.LeaseInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.activeLease == nil {
		return nil
	}
	l := *f.activeLease
	return &l
}

func (f *fakeCoordinator) GetPerClusterStatus() map[string]coordination.ClusterStatusReport {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]coordination.ClusterStatusReport, len(f.perCluster))
	for k, v := range f.perCluster {
		out[k] = v
	}
	return out
}

func (f *fakeCoordinator) GetACGeneration() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.acGeneration
}

var _ coordination.DistributedCoordinator = (*fakeCoordinator)(nil)

// buildMultiClusterShardedHelperForDistributedTest is a tiny factory for the
// gate-point tests: it constructs a three-cluster sharded MongoDB CR and the
// associated reconcile helper, leaving the caller to attach a coordinator.
func buildMultiClusterShardedHelperForDistributedTest(t *testing.T) (
	*ShardedClusterReconcileHelper,
	*mdbv1.MongoDB,
	*om.MockedOmConnection,
) {
	t.Helper()
	ctx := context.Background()
	memberClusterNames := []string{"member-cluster-1", "member-cluster-2", "member-cluster-3"}

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	conn := omConnectionFactory.GetConnectionFunc(&om.OMContext{GroupName: om.TestGroupName})
	mockOM := conn.(*om.MockedOmConnection)

	fakeKubeClient := mock.NewEmptyFakeClientBuilder().WithObjects(mock.GetDefaultResources()...).Build()
	kubeClient := kubernetesClient.NewClient(fakeKubeClient)

	memberClusterMap := getFakeMultiClusterMapWithoutInterceptor(memberClusterNames)

	sc := test.DefaultClusterBuilder().
		SetTopology(mdbv1.ClusterTopologyMultiCluster).
		SetShardCountSpec(2).
		SetMongodsPerShardCountSpec(0).
		SetConfigServerCountSpec(0).
		SetMongosCountSpec(0).
		SetShardClusterSpec(test.CreateClusterSpecList(memberClusterNames, map[string]int{"member-cluster-1": 1, "member-cluster-2": 1, "member-cluster-3": 1})).
		SetConfigSrvClusterSpec(test.CreateClusterSpecList(memberClusterNames, map[string]int{"member-cluster-1": 1, "member-cluster-2": 1, "member-cluster-3": 1})).
		SetMongosClusterSpec(test.CreateClusterSpecList(memberClusterNames, map[string]int{"member-cluster-1": 1})).
		Build()
	require.NoError(t, kubeClient.Create(ctx, sc))

	_, helper, err := newShardedClusterReconcilerForMultiCluster(ctx, false, sc, memberClusterMap, kubeClient, omConnectionFactory)
	require.NoError(t, err)
	return helper, sc, mockOM
}

// TestDistributedMode_FollowerSkipsAC verifies the gate at the top of
// updateOmDeploymentShardedCluster: a follower coordinator short-circuits
// without invoking the OM mock's AC-write/wait operations.
func TestDistributedMode_FollowerSkipsAC(t *testing.T) {
	ctx := context.Background()
	helper, sc, mockOM := buildMultiClusterShardedHelperForDistributedTest(t)

	follower := newFakeCoordinator("member-cluster-2", false)
	helper.SetCoordinator(follower)

	mockOM.CleanHistory()
	status := helper.updateOmDeploymentShardedCluster(ctx, mockOM, sc, deploymentOptions{}, false, zap.S())
	assert.False(t, status.IsOK(), "follower must return non-OK (Pending) status")

	// Critically: no AC-write/wait operations on the follower path.
	mockOM.CheckOperationsDidntHappen(t,
		reflect.ValueOf(mockOM.ReadUpdateDeployment),
		reflect.ValueOf(mockOM.ReadDeployment),
		reflect.ValueOf(mockOM.ReadAutomationConfig),
	)
	assert.Empty(t, follower.acPublishedProposed, "follower must not propose ac_published")
}

// TestDistributedMode_LeaderPassesACGate verifies the leader path: a leader
// coordinator does NOT short-circuit at the gate — it actually invokes the OM
// mock's AC operations.
func TestDistributedMode_LeaderPassesACGate(t *testing.T) {
	ctx := context.Background()
	helper, sc, mockOM := buildMultiClusterShardedHelperForDistributedTest(t)

	leader := newFakeCoordinator("member-cluster-1", true)
	helper.SetCoordinator(leader)

	mockOM.CleanHistory()
	// We don't drive a fully-successful AC publish from this test (that would
	// need real K8s state); we just check the leader entered the AC machinery.
	// updateOmDeploymentShardedCluster's body calls waitForAgentsToRegister
	// first which invokes ReadAutomationAgents on the mock — that's enough to
	// prove the leader did not short-circuit at the gate.
	_ = helper.updateOmDeploymentShardedCluster(ctx, mockOM, sc, deploymentOptions{}, false, zap.S())

	// At least one OM call happened — i.e. the gate did not short-circuit.
	mockOM.CheckOrderOfOperations(t, reflect.ValueOf(mockOM.ReadAutomationAgents))
}
