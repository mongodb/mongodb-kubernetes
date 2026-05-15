package operator

// Tests for the F6 inline-gating distributed-multi-cluster operator PoC.
// These tests deliberately do NOT spin up a real Raft cluster — they stub
// the DistributedCoordinator directly so we can assert gate-point behaviour
// in isolation. The headline integration test (real raft over TCP) lives in
// mongodbshardedcluster_controller_multi_distributed_test.go.

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

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

// fakeCoordinator implements coordination.DistributedCoordinator backed by
// simple in-memory state. Tests construct it inline and inject via
// SetCoordinator.
type fakeCoordinator struct {
	mu sync.Mutex

	clusterName string
	leader      bool

	// scope-keyed state: scope := component+"/"+cluster.
	leaseHolder  map[string]string // scope -> cluster (the holder)
	ready        map[string]bool   // scope -> Ready
	acByCR       map[coordination.CRKey]int64

	// Recordings.
	progressReports []scopeProgress
	readyMarks      []scopeProgress
	releases        []scopeProgress
	acquires        []scopeProgress
	acAnnouncements []int64

	// resources is a per-resource (refKey) → cluster → hash table reflecting
	// the cluster's last ReportResource call.
	resources map[string]map[string]string
	// resourcesAgreedFn lets tests override WaitForResourcesAgreed. If nil,
	// the fake always returns ResourcesAgreed.
	resourcesAgreedFn func(crKey coordination.CRKey, refs []coordination.ResourceRef) (coordination.ResourceAgreement, string)
}

type scopeProgress struct {
	CRKey     coordination.CRKey
	Component string
	Cluster   string
	Progress  coordination.ProgressSnapshot
	Ready     bool
}

func newFakeCoordinator(clusterName string, leader bool) *fakeCoordinator {
	return &fakeCoordinator{
		clusterName: clusterName,
		leader:      leader,
		leaseHolder: map[string]string{},
		ready:       map[string]bool{},
		acByCR:      map[coordination.CRKey]int64{},
	}
}

func scopeKey(component, cluster string) string { return component + "/" + cluster }

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

func (f *fakeCoordinator) ClusterIndex(name string) (int, bool) { return -1, false }

func (f *fakeCoordinator) AcquireOrRespect(crKey coordination.CRKey, component, cluster string) coordination.LeaseResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acquires = append(f.acquires, scopeProgress{CRKey: crKey, Component: component, Cluster: cluster})
	if f.ready[scopeKey(component, cluster)] {
		return coordination.LeaseOtherClusterDone
	}
	holder, has := f.leaseHolder[scopeKey(component, cluster)]
	if has && holder == cluster {
		return coordination.LeaseHeld
	}
	if has {
		return coordination.LeaseWaitForLease
	}
	// Auto-grant to caller.
	f.leaseHolder[scopeKey(component, cluster)] = cluster
	return coordination.LeaseHeld
}

func (f *fakeCoordinator) setHolder(component, cluster, holder string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if holder == "" {
		delete(f.leaseHolder, scopeKey(component, cluster))
		return
	}
	f.leaseHolder[scopeKey(component, cluster)] = holder
}

func (f *fakeCoordinator) setReady(component, cluster string, ready bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ready[scopeKey(component, cluster)] = ready
}

func (f *fakeCoordinator) IsComponentReady(crKey coordination.CRKey, component, cluster string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ready[scopeKey(component, cluster)]
}

func (f *fakeCoordinator) ReportProgress(crKey coordination.CRKey, component, cluster string, p coordination.ProgressSnapshot) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.progressReports = append(f.progressReports, scopeProgress{CRKey: crKey, Component: component, Cluster: cluster, Progress: p})
	return nil
}

func (f *fakeCoordinator) MarkReady(crKey coordination.CRKey, component, cluster string, p coordination.ProgressSnapshot) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.readyMarks = append(f.readyMarks, scopeProgress{CRKey: crKey, Component: component, Cluster: cluster, Progress: p, Ready: true})
	f.ready[scopeKey(component, cluster)] = true
	return nil
}

func (f *fakeCoordinator) ReleaseLease(crKey coordination.CRKey, component, cluster string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releases = append(f.releases, scopeProgress{CRKey: crKey, Component: component, Cluster: cluster})
	delete(f.leaseHolder, scopeKey(component, cluster))
	return nil
}

func (f *fakeCoordinator) AcVersion(crKey coordination.CRKey) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.acByCR[crKey]
}

func (f *fakeCoordinator) AnnounceAcPublished(crKey coordination.CRKey, version int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acAnnouncements = append(f.acAnnouncements, version)
	if version > f.acByCR[crKey] {
		f.acByCR[crKey] = version
	}
	return nil
}

func (f *fakeCoordinator) LastContact(cluster string) time.Duration { return 0 }

// resourceReports / resourcesAgreedFn — F12a interface methods. Tests that
// don't care about resource agreement leave resourcesAgreedFn nil, in which
// case WaitForResourcesAgreed always returns ResourcesAgreed.
func (f *fakeCoordinator) ReportResource(crKey coordination.CRKey, ref coordination.ResourceRef, contentHash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.resources == nil {
		f.resources = map[string]map[string]string{}
	}
	key := ref.String()
	if f.resources[key] == nil {
		f.resources[key] = map[string]string{}
	}
	f.resources[key][f.clusterName] = contentHash
	return nil
}

func (f *fakeCoordinator) WaitForResourcesAgreed(crKey coordination.CRKey, refs []coordination.ResourceRef) (coordination.ResourceAgreement, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.resourcesAgreedFn != nil {
		return f.resourcesAgreedFn(crKey, refs)
	}
	return coordination.ResourcesAgreed, ""
}

var _ coordination.DistributedCoordinator = (*fakeCoordinator)(nil)

// buildMultiClusterShardedHelperForDistributedTest is a tiny factory: it
// constructs a three-cluster sharded MongoDB CR and the associated reconcile
// helper, leaving the caller to attach a coordinator.
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

	memberClusterMap := getFakeMultiClusterMapWithConfiguredInterceptor(memberClusterNames, omConnectionFactory, true, true)

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
	assert.Empty(t, follower.acAnnouncements, "follower must not announce AC published")
}

// TestDistributedMode_InlineGate_Decisions exercises the F6 inline-gate
// decision matrix. The fakeCoordinator's AcquireOrRespect auto-grants the
// lease for the caller's cluster the first time it is called, so the gate
// returns Proceed in the simplest case.
func TestDistributedMode_InlineGate_Decisions(t *testing.T) {
	helper := &ShardedClusterReconcileHelper{}

	// Coordinator nil → always Proceed.
	helper.coordinator = nil
	assert.Equal(t, distGateProceed, helper.distGateInline("config", "anything"))

	// Coordinator set, cluster not ours, component not ready → Wait.
	c := newFakeCoordinator("member-cluster-1", false)
	helper.coordinator = c
	helper.sc = &mdbv1.MongoDB{}
	helper.sc.Name = "test-sc"
	helper.sc.Namespace = "ns"
	assert.Equal(t, distGateWait, helper.distGateInline("config", "member-cluster-2"))

	// Coordinator set, cluster not ours, component IS ready → SkipDone.
	c.setReady("config", "member-cluster-2", true)
	assert.Equal(t, distGateSkipDone, helper.distGateInline("config", "member-cluster-2"))

	// Coordinator set, cluster ours, AcquireOrRespect grants lease → Proceed.
	assert.Equal(t, distGateProceed, helper.distGateInline("shard-0", "member-cluster-1"))

	// Coordinator set, cluster ours, but another cluster already Ready → SkipDone.
	c.setReady("shard-1", "member-cluster-1", true)
	assert.Equal(t, distGateSkipDone, helper.distGateInline("shard-1", "member-cluster-1"))

	// Coordinator set, cluster ours, but someone else already holds the
	// lease → WaitForLease.
	c.setHolder("mongos", "member-cluster-1", "member-cluster-2")
	assert.Equal(t, distGateWait, helper.distGateInline("mongos", "member-cluster-1"))
}

// TestDistributedMode_MarkReadyAndRelease verifies that distMarkReadyAndRelease
// invokes MarkReady + ReleaseLease in order on the coordinator.
func TestDistributedMode_MarkReadyAndRelease(t *testing.T) {
	helper := &ShardedClusterReconcileHelper{}

	// Nil coordinator → no-op.
	helper.distMarkReadyAndRelease("config", "member-cluster-1", 1, 1, 1, zap.S())

	c := newFakeCoordinator("member-cluster-1", true)
	helper.coordinator = c
	helper.sc = &mdbv1.MongoDB{}
	helper.sc.Name = "test-sc"
	helper.sc.Namespace = "ns"
	helper.distMarkReadyAndRelease("config", "member-cluster-1", 1, 1, 1, zap.S())
	require.Len(t, c.readyMarks, 1)
	require.Len(t, c.releases, 1)
	assert.Equal(t, "config", c.readyMarks[0].Component)
	assert.True(t, c.readyMarks[0].Ready)
	assert.Equal(t, "config", c.releases[0].Component)
	assert.True(t, c.IsComponentReady(coordination.CRKey{Kind: "MongoDB", Namespace: "ns", Name: "test-sc"}, "config", "member-cluster-1"))
}

// TestDistributedMode_FollowerSkipsCrossClusterReplication asserts that the
// three cross-cluster Secret/CM replication entry points are decommissioned
// in distributed mode (no error, no work). PoC manually replicates these.
func TestDistributedMode_FollowerSkipsCrossClusterReplication(t *testing.T) {
	ctx := context.Background()
	helper, _, _ := buildMultiClusterShardedHelperForDistributedTest(t)
	c := newFakeCoordinator("member-cluster-2", false)
	helper.SetCoordinator(c)

	// All three returns nil without invoking peer-cluster clients.
	require.NoError(t, helper.replicateAgentKeySecret(ctx, nil, "ignored", zap.S()))
	require.NoError(t, helper.reconcileHostnameOverrideConfigMap(ctx, zap.S()))
	require.NoError(t, helper.replicateSSLMMSCAConfigMap(ctx, mdbv1.ProjectConfig{}, zap.S()))
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
	_ = helper.updateOmDeploymentShardedCluster(ctx, mockOM, sc, deploymentOptions{}, false, zap.S())

	// At least one OM call happened — i.e. the gate did not short-circuit.
	mockOM.CheckOrderOfOperations(t, reflect.ValueOf(mockOM.ReadAutomationAgents))
}
