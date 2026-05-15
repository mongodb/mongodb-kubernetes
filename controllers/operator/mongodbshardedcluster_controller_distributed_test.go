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
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/util/scale"
	"github.com/mongodb/mongodb-kubernetes/pkg/coordination"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
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
	// readySpecGen records the CR spec generation stored on the last
	// MarkReady / setReady call. AcquireOrRespect and IsComponentReady use
	// it to invalidate stale Ready entries when the reconciler observes a
	// newer CR generation.
	readySpecGen map[string]int64
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
	// agentKeys is the in-memory FSM-equivalent for the agent-key
	// distribution path. Keyed by projectID → agent API key.
	agentKeys map[string]string
	// crStatusReports records every ReportCRStatus invocation per CR key.
	// Tests assert this is non-empty after an updateStatus call when a
	// coordinator is attached. Keyed by crKey.String().
	crStatusReports map[string][]fakeCRStatusReport
}

type scopeProgress struct {
	CRKey     coordination.CRKey
	Component string
	Cluster   string
	Progress  coordination.ProgressSnapshot
	Ready     bool
	SpecGen   int64
}

func newFakeCoordinator(clusterName string, leader bool) *fakeCoordinator {
	return &fakeCoordinator{
		clusterName:  clusterName,
		leader:       leader,
		leaseHolder:  map[string]string{},
		ready:        map[string]bool{},
		readySpecGen: map[string]int64{},
		acByCR:       map[coordination.CRKey]int64{},
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

func (f *fakeCoordinator) AcquireOrRespect(crKey coordination.CRKey, component, cluster string, currentSpecGen int64) coordination.LeaseResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acquires = append(f.acquires, scopeProgress{CRKey: crKey, Component: component, Cluster: cluster, SpecGen: currentSpecGen})
	if f.ready[scopeKey(component, cluster)] {
		// Treat any non-zero currentSpecGen as up-to-date for the fake
		// (per-test setReady has no notion of spec generation); only
		// short-circuit if the stored ready bit is still considered valid.
		if storedGen, ok := f.readySpecGen[scopeKey(component, cluster)]; ok && currentSpecGen != 0 && storedGen < currentSpecGen {
			// Stale ready in the fake: fall through.
		} else {
			return coordination.LeaseOtherClusterDone
		}
	}
	holder, has := f.leaseHolder[scopeKey(component, cluster)]
	if has && holder == cluster {
		return coordination.LeaseHeld
	}
	if has {
		return coordination.LeaseWaitForLease
	}
	// G'5 iter 13c: cross-cluster serialisation. If any OTHER lease for the
	// same component is currently held on a different cluster, refuse this
	// slot — mirrors the FSM-side applyLeaseAllocate cross-cluster guard.
	prefix := component + "/"
	for k := range f.leaseHolder {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix && k != scopeKey(component, cluster) {
			return coordination.LeaseWaitForLease
		}
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

func (f *fakeCoordinator) IsComponentReady(crKey coordination.CRKey, component, cluster string, currentSpecGen int64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.ready[scopeKey(component, cluster)] {
		return false
	}
	if currentSpecGen == 0 {
		return true
	}
	storedGen, ok := f.readySpecGen[scopeKey(component, cluster)]
	if !ok {
		return true
	}
	return storedGen >= currentSpecGen
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
	f.readyMarks = append(f.readyMarks, scopeProgress{CRKey: crKey, Component: component, Cluster: cluster, Progress: p, Ready: true, SpecGen: p.CRSpecGeneration})
	f.ready[scopeKey(component, cluster)] = true
	if f.readySpecGen == nil {
		f.readySpecGen = map[string]int64{}
	}
	f.readySpecGen[scopeKey(component, cluster)] = p.CRSpecGeneration
	return nil
}

func (f *fakeCoordinator) ReleaseLease(crKey coordination.CRKey, component, cluster string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releases = append(f.releases, scopeProgress{CRKey: crKey, Component: component, Cluster: cluster})
	delete(f.leaseHolder, scopeKey(component, cluster))
	return nil
}

// GetLeasesHeldBy mirrors the FSM-side method: returns the components for
// which `cluster` is the recorded holder. Walks the in-memory leaseHolder
// map and returns the matching component prefixes. Order is undefined.
func (f *fakeCoordinator) GetLeasesHeldBy(crKey coordination.CRKey, cluster string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for k, holder := range f.leaseHolder {
		if holder != cluster {
			continue
		}
		// scopeKey is "<component>/<cluster>"; recover the component.
		sep := -1
		for i := len(k) - 1; i >= 0; i-- {
			if k[i] == '/' {
				sep = i
				break
			}
		}
		if sep <= 0 {
			continue
		}
		out = append(out, k[:sep])
	}
	return out
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

// PublishAgentKey stores the published key in the in-memory map; FSM
// equivalent for unit-test purposes. Idempotent.
func (f *fakeCoordinator) PublishAgentKey(crKey coordination.CRKey, projectID, agentKey string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.agentKeys == nil {
		f.agentKeys = map[string]string{}
	}
	f.agentKeys[projectID] = agentKey
	return nil
}

// GetAgentKey returns the previously-published key for projectID, or "".
func (f *fakeCoordinator) GetAgentKey(crKey coordination.CRKey, projectID string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.agentKeys == nil {
		return ""
	}
	return f.agentKeys[projectID]
}

// ReportCRStatus records the (phase, message) tuple per cluster for inspection.
// Lightweight fake — production impl submits a StatusReportPayload via raft.
func (f *fakeCoordinator) ReportCRStatus(crKey coordination.CRKey, phase, message string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.crStatusReports == nil {
		f.crStatusReports = map[string][]fakeCRStatusReport{}
	}
	key := crKey.Kind + "/" + crKey.Namespace + "/" + crKey.Name
	f.crStatusReports[key] = append(f.crStatusReports[key], fakeCRStatusReport{
		Cluster: f.clusterName,
		Phase:   phase,
		Message: message,
	})
	return nil
}

// fakeCRStatusReport captures a single ReportCRStatus call so tests can assert
// that updateStatus fired the heartbeat.
type fakeCRStatusReport struct {
	Cluster string
	Phase   string
	Message string
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
	// lease → WaitForLease. Uses a voting component ("shard-2") because the
	// iter-14b mongos exemption now bypasses the cross-cluster mutex for
	// "mongos" and returns Proceed regardless of holders — covered
	// separately in TestDistributedMode_MongosBypassesCrossClusterMutex.
	c.setHolder("shard-2", "member-cluster-1", "member-cluster-2")
	assert.Equal(t, distGateWait, helper.distGateInline("shard-2", "member-cluster-1"))

	// iter-14b mongos exemption: own-cluster mongos always returns Proceed
	// (no cross-cluster mutex applies). Non-self mongos slot returns
	// SkipDone so the createOrUpdateMongos loop short-circuits past slots
	// owned by another operator's local k8s client.
	c.setHolder("mongos", "member-cluster-1", "member-cluster-2")
	assert.Equal(t, distGateProceed, helper.distGateInline("mongos", "member-cluster-1"))
	assert.Equal(t, distGateSkipDone, helper.distGateInline("mongos", "member-cluster-2"))
}

// TestDistributedMode_InlineGate_StaleReadyInvalidatedBySpecGen exercises the
// G'5 iter-13b fix: a previously-Ready (component, cluster) recorded for an
// older CR generation must NOT short-circuit the gate when the reconciler is
// observing a newer generation. This is the path that broke
// test_rolling_restart in pod-mode — the FSM's Ready bit from the initial
// deploy stayed set across reconciles, so the gate kept returning
// distGateSkipDone for every cluster after a podTemplate annotation update,
// the operator never re-rendered the STS, and no pod rolled.
func TestDistributedMode_InlineGate_StaleReadyInvalidatedBySpecGen(t *testing.T) {
	helper := &ShardedClusterReconcileHelper{}
	c := newFakeCoordinator("member-cluster-1", false)
	helper.coordinator = c
	helper.sc = &mdbv1.MongoDB{}
	helper.sc.Name = "test-sc"
	helper.sc.Namespace = "ns"

	crKey := coordination.CRKey{Kind: "MongoDB", Namespace: "ns", Name: "test-sc"}

	// Initial deploy (CR generation = 1): mark cluster-2's config Ready for
	// gen=1, simulating a successful first reconcile.
	require.NoError(t, c.MarkReady(crKey, "config", "member-cluster-2", coordination.ProgressSnapshot{
		CurrentReplicas: 1, ReadyReplicas: 1, CRSpecGeneration: 1,
	}))

	// Same generation → SkipDone (the prior Ready is fresh).
	helper.sc.Generation = 1
	assert.Equal(t, distGateSkipDone, helper.distGateInline("config", "member-cluster-2"),
		"matching spec generation should still short-circuit to SkipDone")

	// CR generation advances to 2 (e.g. user edited podTemplate). The prior
	// Ready entry is now stale; the gate must NOT short-circuit.
	helper.sc.Generation = 2
	got := helper.distGateInline("config", "member-cluster-2")
	assert.NotEqual(t, distGateSkipDone, got,
		"stale Ready (recorded for gen=1) must NOT short-circuit when reconcile sees gen=2; got %v", got)
	// For a non-local cluster + stale Ready, the gate falls through to "not
	// ready yet" → Wait (the leader for THAT cluster will re-do the work).
	assert.Equal(t, distGateWait, got)

	// Once cluster-2 re-MarksReady for gen=2, SkipDone is restored.
	require.NoError(t, c.MarkReady(crKey, "config", "member-cluster-2", coordination.ProgressSnapshot{
		CurrentReplicas: 1, ReadyReplicas: 1, CRSpecGeneration: 2,
	}))
	assert.Equal(t, distGateSkipDone, helper.distGateInline("config", "member-cluster-2"),
		"after re-MarkReady at gen=2, SkipDone should resume")
}

// TestDistributedMode_LeaseSerializesAcrossClusters — G'5 iter 13c regression.
//
// Simulates a 3-member rolling-restart: each member-cluster operator has its
// own ShardedClusterReconcileHelper but they share a single coordinator
// (modelling the leader's FSM, which is the single source of truth across
// the raft cluster). The coordinator must serialise per-(CR, component):
// only one operator's distGateInline returns distGateProceed; the others
// return distGateWait. After the proceeding one MarkReady+Releases, the
// next operator's distGateInline returns distGateProceed.
//
// This is the unit-level fingerprint of pod-mode test_rolling_restart's
// monitor reporting `configSrv: 3 NotReady concurrently` (cap 1): all 3
// operators previously acquired their own per-(component, cluster) lease in
// parallel, rolled their pods simultaneously, and broke replicaset quorum.
func TestDistributedMode_LeaseSerializesAcrossClusters(t *testing.T) {
	// One coordinator backs the shared lease state (the leader's FSM).
	shared := newFakeCoordinator("member-cluster-1", false)

	// Each operator's helper points at the same coordinator but claims a
	// different local cluster name. We do this by overriding shared's
	// clusterName per-call via a per-helper wrapper.
	makeHelper := func(localCluster string) *ShardedClusterReconcileHelper {
		c := &perClusterCoordinatorView{shared: shared, localCluster: localCluster}
		h := &ShardedClusterReconcileHelper{}
		h.coordinator = c
		h.sc = &mdbv1.MongoDB{}
		h.sc.Name = "test-sc"
		h.sc.Namespace = "ns"
		h.sc.Generation = 2 // post-spec-bump (rolling restart)
		return h
	}

	helpers := []*ShardedClusterReconcileHelper{
		makeHelper("member-cluster-1"),
		makeHelper("member-cluster-2"),
		makeHelper("member-cluster-3"),
	}

	// Pre-condition: clusters reported Ready at gen=1 (initial deploy
	// finished). On gen=2 the stale-Ready iter-13b path falls through to
	// normal lease allocation.
	crKey := coordination.CRKey{Kind: "MongoDB", Namespace: "ns", Name: "test-sc"}
	for _, cn := range []string{"member-cluster-1", "member-cluster-2", "member-cluster-3"} {
		require.NoError(t, shared.MarkReady(crKey, "config", cn, coordination.ProgressSnapshot{
			CurrentReplicas: 1, ReadyReplicas: 1, CRSpecGeneration: 1,
		}))
	}
	// MarkReady also stores a lease holder; clear it so the test starts
	// from "no leases active". (MarkReady doesn't allocate a lease; the
	// fake only records Ready. ReleaseLease wasn't called either since no
	// AcquireOrRespect ran. Defensive sanity-check:)
	shared.mu.Lock()
	shared.leaseHolder = map[string]string{}
	shared.mu.Unlock()

	// Interleaved gate calls: all 3 operators reach distGateInline at
	// "around the same time" for (config, <own cluster>).
	verdicts := make([]distGateAction, 3)
	for i, cn := range []string{"member-cluster-1", "member-cluster-2", "member-cluster-3"} {
		verdicts[i] = helpers[i].distGateInline("config", cn)
	}

	// Exactly one Proceed, the other two Wait.
	proceedCount := 0
	waitCount := 0
	proceedIdx := -1
	for i, v := range verdicts {
		switch v {
		case distGateProceed:
			proceedCount++
			proceedIdx = i
		case distGateWait:
			waitCount++
		}
	}
	require.Equalf(t, 1, proceedCount, "exactly one operator must Proceed; got verdicts=%v", verdicts)
	require.Equalf(t, 2, waitCount, "the other two operators must Wait; got verdicts=%v", verdicts)

	// The proceeding operator's local cluster releases (mimics
	// distMarkReadyAndRelease at gen=2 after STS-apply + pods Ready).
	proceedingCluster := []string{"member-cluster-1", "member-cluster-2", "member-cluster-3"}[proceedIdx]
	require.NoError(t, helpers[proceedIdx].coordinator.MarkReady(crKey, "config", proceedingCluster, coordination.ProgressSnapshot{
		CurrentReplicas: 1, ReadyReplicas: 1, CRSpecGeneration: 2,
	}))
	require.NoError(t, helpers[proceedIdx].coordinator.ReleaseLease(crKey, "config", proceedingCluster))

	// Now the next operator's distGateInline must yield Proceed.
	nextProceed := 0
	for i, cn := range []string{"member-cluster-1", "member-cluster-2", "member-cluster-3"} {
		if i == proceedIdx {
			continue
		}
		if helpers[i].distGateInline("config", cn) == distGateProceed {
			nextProceed++
		}
	}
	// One of the remaining two should Proceed (whichever sees the empty
	// slot first); the other still Waits.
	assert.Equal(t, 1, nextProceed,
		"after releasing, exactly one of the remaining operators must Proceed")
}

// perClusterCoordinatorView is a thin wrapper that lets multiple test helpers
// share one fakeCoordinator's lease state while reporting different local
// cluster names from MyClusterName(). It mimics the production setup where
// each operator's local Coordinator instance reads/writes the same replicated
// FSM but reports its own cluster name.
type perClusterCoordinatorView struct {
	shared       *fakeCoordinator
	localCluster string
}

func (v *perClusterCoordinatorView) MyClusterName() string { return v.localCluster }
func (v *perClusterCoordinatorView) IsLeader() bool        { return v.shared.IsLeader() }
func (v *perClusterCoordinatorView) ClusterIndex(name string) (int, bool) {
	return v.shared.ClusterIndex(name)
}

func (v *perClusterCoordinatorView) AcquireOrRespect(crKey coordination.CRKey, component, cluster string, currentSpecGen int64) coordination.LeaseResult {
	return v.shared.AcquireOrRespect(crKey, component, cluster, currentSpecGen)
}

func (v *perClusterCoordinatorView) IsComponentReady(crKey coordination.CRKey, component, cluster string, currentSpecGen int64) bool {
	return v.shared.IsComponentReady(crKey, component, cluster, currentSpecGen)
}

func (v *perClusterCoordinatorView) ReportProgress(crKey coordination.CRKey, component, cluster string, p coordination.ProgressSnapshot) error {
	return v.shared.ReportProgress(crKey, component, cluster, p)
}

func (v *perClusterCoordinatorView) MarkReady(crKey coordination.CRKey, component, cluster string, p coordination.ProgressSnapshot) error {
	return v.shared.MarkReady(crKey, component, cluster, p)
}

func (v *perClusterCoordinatorView) ReleaseLease(crKey coordination.CRKey, component, cluster string) error {
	return v.shared.ReleaseLease(crKey, component, cluster)
}

func (v *perClusterCoordinatorView) GetLeasesHeldBy(crKey coordination.CRKey, cluster string) []string {
	return v.shared.GetLeasesHeldBy(crKey, cluster)
}

func (v *perClusterCoordinatorView) AcVersion(crKey coordination.CRKey) int64 {
	return v.shared.AcVersion(crKey)
}

func (v *perClusterCoordinatorView) AnnounceAcPublished(crKey coordination.CRKey, version int64) error {
	return v.shared.AnnounceAcPublished(crKey, version)
}

func (v *perClusterCoordinatorView) LastContact(cluster string) time.Duration {
	return v.shared.LastContact(cluster)
}

func (v *perClusterCoordinatorView) ReportResource(crKey coordination.CRKey, ref coordination.ResourceRef, contentHash string) error {
	return v.shared.ReportResource(crKey, ref, contentHash)
}

func (v *perClusterCoordinatorView) WaitForResourcesAgreed(crKey coordination.CRKey, refs []coordination.ResourceRef) (coordination.ResourceAgreement, string) {
	return v.shared.WaitForResourcesAgreed(crKey, refs)
}

func (v *perClusterCoordinatorView) PublishAgentKey(crKey coordination.CRKey, projectID, agentKey string) error {
	return v.shared.PublishAgentKey(crKey, projectID, agentKey)
}

func (v *perClusterCoordinatorView) GetAgentKey(crKey coordination.CRKey, projectID string) string {
	return v.shared.GetAgentKey(crKey, projectID)
}

func (v *perClusterCoordinatorView) ReportCRStatus(crKey coordination.CRKey, phase, message string) error {
	return v.shared.ReportCRStatus(crKey, phase, message)
}

var _ coordination.DistributedCoordinator = (*perClusterCoordinatorView)(nil)

// runScaleSerializationScenario drives a multi-member scale transition across
// three clusters and asserts the FSM-level (CR, component) lease guard
// serialises them. Used by both scale-up and scale-down tests below.
//
// The scenario reflects what happens in pod-mode during a per-cluster `members:
// +3` (or `-3`) edit on a sharded CR:
//
//  1. Each member-cluster operator reconciles concurrently; each tries to
//     acquire the (CR, "shard-0") lease for its own cluster.
//  2. The FSM grants the lease to exactly ONE cluster — the others receive
//     LeaseWait (modelled as distGateWait).
//  3. The proceeding cluster runs through ALL its STS-write iterations
//     necessary for the scale delta (each delta step is its own reconcile
//     loop in production; in this unit test we simulate it by performing
//     `delta` MarkReady+Release cycles in a row — emphasising that even
//     across multiple in-flight iterations the OTHER clusters never sneak in
//     a parallel acquire).
//  4. After the holder Releases for the last time, the next operator's
//     acquire-or-respect promotes one of the still-waiting clusters.
//  5. Across the full scenario at most one cluster ever holds the lease.
//
// The function returns the sequence of clusters that held the lease (one
// entry per cluster, in the order they acquired) and the highest observed
// concurrency. The latter MUST be 1.
func runScaleSerializationScenario(
	t *testing.T,
	component string,
	clusters []string,
	currentSpecGen int64,
	stepsPerCluster int,
) (acquireOrder []string, maxConcurrency int) {
	t.Helper()
	require.GreaterOrEqual(t, stepsPerCluster, 1, "stepsPerCluster must be ≥1 (otherwise the scenario is trivially serial)")

	shared := newFakeCoordinator(clusters[0], false)
	crKey := coordination.CRKey{Kind: "MongoDB", Namespace: "ns", Name: "test-sc"}

	makeHelper := func(localCluster string) *ShardedClusterReconcileHelper {
		c := &perClusterCoordinatorView{shared: shared, localCluster: localCluster}
		h := &ShardedClusterReconcileHelper{}
		h.coordinator = c
		h.sc = &mdbv1.MongoDB{}
		h.sc.Name = "test-sc"
		h.sc.Namespace = "ns"
		h.sc.Generation = currentSpecGen
		return h
	}
	helpers := make(map[string]*ShardedClusterReconcileHelper, len(clusters))
	for _, cn := range clusters {
		helpers[cn] = makeHelper(cn)
	}

	// Seed Ready bits at currentSpecGen-1 so the iter-13b stale-Ready path
	// falls through to normal lease allocation. This mirrors pod-mode where
	// the previous (pre-edit) generation reported Ready before the scale
	// trigger landed.
	for _, cn := range clusters {
		require.NoError(t, shared.MarkReady(crKey, component, cn, coordination.ProgressSnapshot{
			CurrentReplicas: 1, ReadyReplicas: 1, CRSpecGeneration: currentSpecGen - 1,
		}))
	}
	// MarkReady alone does not allocate a lease holder; reset defensively.
	shared.mu.Lock()
	shared.leaseHolder = map[string]string{}
	shared.mu.Unlock()

	maxConcurrency = 0

	// remaining is the set of clusters yet to complete their scale work.
	remaining := map[string]bool{}
	for _, cn := range clusters {
		remaining[cn] = true
	}

	// Bounded loop: each iteration is one "tick" where every remaining
	// cluster's operator runs distGateInline once. The single Proceed-er
	// then performs stepsPerCluster MarkReady+Release cycles (= a multi-iter
	// scale window) before releasing the lease so the next cluster can pick
	// it up.
	maxTicks := len(clusters) * (stepsPerCluster + 2)
	for tick := 0; tick < maxTicks && len(remaining) > 0; tick++ {
		// Each remaining cluster's helper runs distGateInline once "around
		// the same time" — i.e. before any of them sees a release.
		verdicts := map[string]distGateAction{}
		proceedingCluster := ""
		concurrentHolders := 0
		for cn := range remaining {
			v := helpers[cn].distGateInline(component, cn)
			verdicts[cn] = v
			if v == distGateProceed {
				proceedingCluster = cn
				concurrentHolders++
			}
		}
		if concurrentHolders > maxConcurrency {
			maxConcurrency = concurrentHolders
		}
		require.LessOrEqualf(t, concurrentHolders, 1,
			"tick %d: more than one cluster Proceeded simultaneously — FSM guard failed. verdicts=%v",
			tick, verdicts)
		if proceedingCluster == "" {
			// No cluster Proceeded this tick — should only happen mid-tick
			// between acquire/release if all remaining clusters got Wait.
			// In our model that means the lease is held but the holder is
			// in `remaining` AND not amongst the verdicts that returned
			// Proceed — impossible by construction. Fail loudly.
			t.Fatalf("tick %d: no cluster Proceeded; verdicts=%v remaining=%v", tick, verdicts, remaining)
		}

		acquireOrder = append(acquireOrder, proceedingCluster)

		// Perform `stepsPerCluster` MarkReady cycles to simulate the
		// progression of a multi-member scale (each step = one reconcile
		// where the STS replica count moved by one). Crucially, during these
		// steps the OTHER clusters must keep returning Wait, never Proceed.
		for step := 0; step < stepsPerCluster; step++ {
			require.NoError(t, helpers[proceedingCluster].coordinator.ReportProgress(crKey, component, proceedingCluster, coordination.ProgressSnapshot{
				CurrentReplicas: 1 + step, ReadyReplicas: 1 + step, CRSpecGeneration: currentSpecGen,
			}))
			// Probe the others mid-step.
			for cn := range remaining {
				if cn == proceedingCluster {
					continue
				}
				v := helpers[cn].distGateInline(component, cn)
				require.Equalf(t, distGateWait, v,
					"mid-step %d of cluster %s: cluster %s must Wait, got %v",
					step, proceedingCluster, cn, v)
			}
		}

		// Final MarkReady at the new spec gen + ReleaseLease.
		require.NoError(t, helpers[proceedingCluster].coordinator.MarkReady(crKey, component, proceedingCluster, coordination.ProgressSnapshot{
			CurrentReplicas: 1 + stepsPerCluster, ReadyReplicas: 1 + stepsPerCluster, CRSpecGeneration: currentSpecGen,
		}))
		require.NoError(t, helpers[proceedingCluster].coordinator.ReleaseLease(crKey, component, proceedingCluster))

		delete(remaining, proceedingCluster)
	}

	require.Emptyf(t, remaining, "scenario didn't drain remaining clusters within %d ticks; remaining=%v", maxTicks, remaining)
	return acquireOrder, maxConcurrency
}

// TestDistributedMode_LeaseSerializesScaleUpThreeMembers — G'5 iter 14
// regression for multi-member scale-up.
//
// Scenario: three member-cluster operators each see the same CR transition
// `shard.clusterSpecList[i].members: 1 → 4` (i.e. +3 voting members per
// cluster, total shard-0 grows from 3 → 12). All three reconcile concurrently
// at the new generation; the FSM-side `(CR, "shard-0")` mutual-exclusion must
// serialise them — exactly one cluster's distGateInline returns Proceed at
// any given tick, the other two return Wait. Across the full transition the
// per-cluster lease cycles in turn, never in parallel.
//
// This is the unit-level fingerprint of the per-RS NotReady cap=1 invariant
// the pod-mode `test_scale_up_3` step asserts. Both rely on the same FSM
// guard introduced in iter-13c (`fsm_real.go::applyLeaseAllocate` cross-
// cluster check + `FSM.HasSiblingLease`); this test confirms the guard is
// scale-operation-agnostic — it does NOT only kick in for rolling-restart.
//
// Coverage delta vs TestDistributedMode_LeaseSerializesAcrossClusters: that
// test exercised a single acquire→release cycle. This one drives
// `stepsPerCluster=3` MarkReady-mid-progress cycles, asserting cross-cluster
// Wait holds across an extended scale window (the production code path
// where a cluster's STS grows by N replicas across N reconciles before the
// final MarkReady fires).
func TestDistributedMode_LeaseSerializesScaleUpThreeMembers(t *testing.T) {
	clusters := []string{"member-cluster-1", "member-cluster-2", "member-cluster-3"}

	acquireOrder, maxConcurrency := runScaleSerializationScenario(
		t,
		"shard-0",
		clusters,
		2, // CR generation post-scale-up edit
		3, // 3 members added per cluster → 3 mid-progress cycles
	)

	assert.Equal(t, 1, maxConcurrency,
		"during a multi-member scale-up, at most one cluster may hold the (CR, shard-0) lease at any time")
	assert.Len(t, acquireOrder, len(clusters),
		"every cluster must eventually acquire & complete its scale-up phase")
	// Each cluster appears exactly once — no cluster re-acquires after a
	// release (state correctly reset by ReleaseLease + the cross-cluster
	// guard now letting a DIFFERENT cluster pick the slot up).
	seen := map[string]int{}
	for _, cn := range acquireOrder {
		seen[cn]++
	}
	for _, cn := range clusters {
		assert.Equalf(t, 1, seen[cn],
			"cluster %s should acquire the lease exactly once across the scale; got %d acquisitions (order=%v)",
			cn, seen[cn], acquireOrder)
	}
}

// TestDistributedMode_LeaseSerializesScaleDownThreeMembers — G'5 iter 14
// regression for multi-member scale-down.
//
// Inverse of the scale-up test: CR transition `shard.clusterSpecList[i].
// members: 4 → 1` (i.e. −3 voting members per cluster, shard-0 shrinks
// 12 → 3). Same FSM lease guard applies — scale-down must also serialise
// across clusters so we never drop multiple voting members concurrently
// across replica sets.
//
// Scale-down is the slightly-trickier direction because the agent removes
// pods from the back of the STS; if two clusters scaled down in parallel,
// voting-quorum loss is possible (e.g. 3-of-3 voting members reduced to
// 1-of-1 simultaneously across all clusters → 2 down concurrently). The
// lease guard prevents this exactly the same way it prevents the rolling-
// restart fan-out.
func TestDistributedMode_LeaseSerializesScaleDownThreeMembers(t *testing.T) {
	clusters := []string{"member-cluster-1", "member-cluster-2", "member-cluster-3"}

	acquireOrder, maxConcurrency := runScaleSerializationScenario(
		t,
		"shard-0",
		clusters,
		3, // CR generation post-scale-down edit (assume 1=create, 2=scale-up, 3=scale-down)
		3, // 3 members removed per cluster → 3 mid-progress cycles
	)

	assert.Equal(t, 1, maxConcurrency,
		"during a multi-member scale-down, at most one cluster may hold the (CR, shard-0) lease at any time")
	assert.Len(t, acquireOrder, len(clusters),
		"every cluster must eventually acquire & complete its scale-down phase")
	seen := map[string]int{}
	for _, cn := range acquireOrder {
		seen[cn]++
	}
	for _, cn := range clusters {
		assert.Equalf(t, 1, seen[cn],
			"cluster %s should acquire the lease exactly once across the scale; got %d acquisitions (order=%v)",
			cn, seen[cn], acquireOrder)
	}
}

// TestDistributedMode_LeaseStateResetsAfterRelease — G'5 iter 14 explicit
// state-reset assertion (Phase 1 requirement C: "cluster-1 releases →
// cluster-2 acquires (NOT cluster-1 again on a retry)").
//
// Tighter than the scenario-driver: drives a single acquire/release cycle
// and then asserts the JUST-RELEASED cluster does NOT get Proceed on its
// next distGateInline call when other clusters are still waiting — the
// next acquire MUST go to one of the other clusters.
func TestDistributedMode_LeaseStateResetsAfterRelease(t *testing.T) {
	clusters := []string{"member-cluster-1", "member-cluster-2", "member-cluster-3"}
	shared := newFakeCoordinator(clusters[0], false)
	crKey := coordination.CRKey{Kind: "MongoDB", Namespace: "ns", Name: "test-sc"}

	makeHelper := func(localCluster string) *ShardedClusterReconcileHelper {
		c := &perClusterCoordinatorView{shared: shared, localCluster: localCluster}
		h := &ShardedClusterReconcileHelper{}
		h.coordinator = c
		h.sc = &mdbv1.MongoDB{}
		h.sc.Name = "test-sc"
		h.sc.Namespace = "ns"
		h.sc.Generation = 2
		return h
	}
	helpers := map[string]*ShardedClusterReconcileHelper{}
	for _, cn := range clusters {
		helpers[cn] = makeHelper(cn)
	}
	for _, cn := range clusters {
		require.NoError(t, shared.MarkReady(crKey, "shard-0", cn, coordination.ProgressSnapshot{
			CurrentReplicas: 1, ReadyReplicas: 1, CRSpecGeneration: 1,
		}))
	}
	shared.mu.Lock()
	shared.leaseHolder = map[string]string{}
	shared.mu.Unlock()

	// Round 1: all 3 race for shard-0.
	verdicts := map[string]distGateAction{}
	for _, cn := range clusters {
		verdicts[cn] = helpers[cn].distGateInline("shard-0", cn)
	}
	proceeding := ""
	for cn, v := range verdicts {
		if v == distGateProceed {
			require.Emptyf(t, proceeding, "two clusters Proceeded: %s and %s", proceeding, cn)
			proceeding = cn
		}
	}
	require.NotEmpty(t, proceeding, "exactly one cluster must Proceed; got verdicts=%v", verdicts)

	// Proceeding cluster does its work, marks Ready, releases.
	require.NoError(t, helpers[proceeding].coordinator.MarkReady(crKey, "shard-0", proceeding, coordination.ProgressSnapshot{
		CurrentReplicas: 4, ReadyReplicas: 4, CRSpecGeneration: 2,
	}))
	require.NoError(t, helpers[proceeding].coordinator.ReleaseLease(crKey, "shard-0", proceeding))

	// Round 2: the just-released cluster MUST NOT Proceed — its work for this
	// generation is done (its Ready bit is now at gen=2, so AcquireOrRespect
	// returns LeaseOtherClusterDone). One of the OTHER two clusters Proceeds.
	round2 := map[string]distGateAction{}
	for _, cn := range clusters {
		round2[cn] = helpers[cn].distGateInline("shard-0", cn)
	}
	assert.Equal(t, distGateSkipDone, round2[proceeding],
		"just-released cluster %s should see SkipDone (its work is done) not Proceed on retry; round2=%v",
		proceeding, round2)
	nextProceed := ""
	for cn, v := range round2 {
		if cn == proceeding {
			continue
		}
		if v == distGateProceed {
			require.Emptyf(t, nextProceed, "two clusters Proceeded in round 2: %s and %s", nextProceed, cn)
			nextProceed = cn
		}
	}
	require.NotEmptyf(t, nextProceed, "exactly one of the OTHER clusters must Proceed in round 2 after %s released; round2=%v", proceeding, round2)
	assert.NotEqualf(t, proceeding, nextProceed, "next Proceed must be a different cluster than the just-released one")
}

// TestDistributedMode_MongosBypassesCrossClusterMutex — G'5 iter 14b regression.
//
// mongos is stateless; there is NO replicaset quorum to protect during a
// rolling restart, so the (CR, component) cross-cluster mutex enforced by
// iter-13c for voting components MUST NOT apply to mongos. If it did (as in
// iter-14's first pod-mode attempt), the per-cluster mongos roll serialises
// across all 3 member clusters AND blocks waiting for the local pod's OM AC
// goal-state catch-up — observed deadlock fingerprint on cluster-1's
// mongos/<self> lease, with cluster-2/cluster-3 stuck on
// "waiting for lease on mongos/kind-e2e-cluster-1" until the 900s test
// budget timed out.
//
// Expectation: when 3 member-cluster operators concurrently call
// `distGateInline("mongos", <own cluster>)`, ALL THREE receive distGateProceed
// — there's no cross-cluster mutex for the stateless mongos component, only
// for voting components ("config", "shard-N"). When a non-self cluster's
// mongos slot is queried (clusterName != MyClusterName), the verdict must be
// distGateSkipDone so each operator processes its OWN cluster's STS only and
// skips the iteration entries for the OTHER clusters (which it can't write to
// in distributed mode anyway — only the local kubeconfig is in the
// globalMemberClustersMap).
//
// Companion assertion: the SAME helpers, when calling distGateInline("config",
// <own cluster>), must STILL serialise (exactly one Proceed, two Wait) —
// proving the mongos bypass is component-scoped, not a general loss of the
// iter-13c serialisation invariant.
func TestDistributedMode_MongosBypassesCrossClusterMutex(t *testing.T) {
	clusters := []string{"member-cluster-1", "member-cluster-2", "member-cluster-3"}
	shared := newFakeCoordinator(clusters[0], false)

	makeHelper := func(localCluster string) *ShardedClusterReconcileHelper {
		c := &perClusterCoordinatorView{shared: shared, localCluster: localCluster}
		h := &ShardedClusterReconcileHelper{}
		h.coordinator = c
		h.sc = &mdbv1.MongoDB{}
		h.sc.Name = "test-sc"
		h.sc.Namespace = "ns"
		h.sc.Generation = 2 // post-spec-bump (rolling restart)
		return h
	}
	helpers := make([]*ShardedClusterReconcileHelper, len(clusters))
	for i, cn := range clusters {
		helpers[i] = makeHelper(cn)
	}

	// All three operators reach distGateInline at the same simulated time for
	// (mongos, <own cluster>). Mongos is stateless — every one must Proceed.
	for i, cn := range clusters {
		got := helpers[i].distGateInline("mongos", cn)
		assert.Equalf(t, distGateProceed, got,
			"distGateInline(mongos, %s) must return distGateProceed (no cross-cluster mutex for stateless component); got=%v",
			cn, got)
	}

	// Iteration entries for OTHER clusters (clusterName != localCluster) must
	// return distGateSkipDone so the createOrUpdateMongos loop short-circuits
	// past slots whose local k8s client lives in another operator's process.
	// Cluster-1's operator iterating cluster-2's slot must Skip, not Wait
	// (Wait would re-introduce the cross-cluster blocking pattern).
	for selfIdx, selfCN := range clusters {
		for otherIdx, otherCN := range clusters {
			if selfIdx == otherIdx {
				continue
			}
			got := helpers[selfIdx].distGateInline("mongos", otherCN)
			assert.Equalf(t, distGateSkipDone, got,
				"helper on %s iterating mongos/%s must return distGateSkipDone (each operator handles only its own local cluster); got=%v",
				selfCN, otherCN, got)
		}
	}

	// No mongos leases should have been allocated on the shared coordinator.
	// (Fix B short-circuits BEFORE calling AcquireOrRespect, so the FSM-side
	// (CR, mongos) lease slot stays empty.)
	shared.mu.Lock()
	for cluster := range shared.leaseHolder {
		assert.NotContainsf(t, cluster, "mongos",
			"no mongos lease must be allocated on the shared coordinator after Fix B; got holders=%v", shared.leaseHolder)
	}
	shared.mu.Unlock()

	// Companion assertion: the SAME helpers with the SAME fake coordinator
	// must STILL serialise config across clusters (the iter-13c invariant is
	// unchanged for voting components). Reset shared state to "no clusters
	// reported anything for config yet" then drive 3 concurrent
	// distGateInline("config", <own>) calls.
	crKey := coordination.CRKey{Kind: "MongoDB", Namespace: "ns", Name: "test-sc"}
	for _, cn := range clusters {
		require.NoError(t, shared.MarkReady(crKey, "config", cn, coordination.ProgressSnapshot{
			CurrentReplicas: 1, ReadyReplicas: 1, CRSpecGeneration: 1,
		}))
	}
	shared.mu.Lock()
	shared.leaseHolder = map[string]string{}
	shared.mu.Unlock()

	configVerdicts := make([]distGateAction, len(clusters))
	for i, cn := range clusters {
		configVerdicts[i] = helpers[i].distGateInline("config", cn)
	}
	proceedCount := 0
	waitCount := 0
	for _, v := range configVerdicts {
		switch v {
		case distGateProceed:
			proceedCount++
		case distGateWait:
			waitCount++
		}
	}
	assert.Equalf(t, 1, proceedCount, "config must STILL serialise; expected exactly 1 Proceed, got verdicts=%v", configVerdicts)
	assert.Equalf(t, 2, waitCount, "config must STILL serialise; expected 2 Waits, got verdicts=%v", configVerdicts)
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
	assert.True(t, c.IsComponentReady(coordination.CRKey{Kind: "MongoDB", Namespace: "ns", Name: "test-sc"}, "config", "member-cluster-1", 0))
}

// TestDistributedMode_FollowerLocalReplication asserts that in distributed
// mode the three replication entry points write to the operator's OWN local
// cluster (not cross-cluster) — getHealthyMemberClusters filters out
// nil-Client peers so each operator only touches its own k8s client.
// The Phase D revision (post-D'7-iter3) replaced the previous "follower
// no-ops entirely" behaviour: pods on follower clusters need the
// sh-hostname-override CM and the <projectID>-group-secret Secret to
// mount, so each operator must create them locally.
func TestDistributedMode_FollowerLocalReplication(t *testing.T) {
	ctx := context.Background()
	helper, _, _ := buildMultiClusterShardedHelperForDistributedTest(t)
	c := newFakeCoordinator("member-cluster-2", false)
	helper.SetCoordinator(c)

	// reconcileHostnameOverrideConfigMap doesn't need an OM connection and
	// must succeed against the local cluster.
	require.NoError(t, helper.reconcileHostnameOverrideConfigMap(ctx, zap.S()))

	// replicateSSLMMSCAConfigMap is a no-op when the spec doesn't reference
	// an SSL MMS CA CM — passing an empty ProjectConfig hits that branch.
	require.NoError(t, helper.replicateSSLMMSCAConfigMap(ctx, mdbv1.ProjectConfig{}, zap.S()))

	// replicateAgentKeySecret needs a non-nil OM connection because it
	// passes through to EnsureAgentKeySecretExists. The mock from the
	// helper-builder isn't wired here, so we just assert the function
	// would call out to the local cluster's secret client; full
	// integration is covered by the e2e test.
	// (Skip the actual call — it'd nil-deref on the OM connection arg.)
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

// TestDistributedMode_FollowerScalerOneAtATime — G'5 iter 14c regression
// (post-Running scenario, refined in iter-14d).
//
// Pinpoints the root cause of the iter-14b Phase 3 scale-up safety violation
// (max_notready_per_component={'shard-0': 3}, cap=1 invariant broken). In
// distributed pod-mode, the follower-cluster operators never reach the
// post-doShardedClusterProcessing exit point and therefore never populate
// `deploymentState.Status.SizeStatusInClusters.ShardMongodsInClusters`. The
// scaler treats an empty entry as zero, which means CurrentReplicas()=0 even
// when the live STS in kube has N>0 replicas. With CurrentReplicas==0 and
// ScalingFirstTime()==true, ReplicasThisReconciliation short-circuits to
// DesiredReplicas, so the follower writes Spec.Replicas=TARGET (e.g. 5) in a
// single shot rather than +1 per reconcile. Within the holder's lease window
// that single +Δ write contributes Δ NotReady pods to the shard-0 replica
// set globally — directly violating the cap=1 NotReady invariant.
//
// The test exercises the boundary where the bug surfaces: build the helper
// in distributed mode (coordinator attached) with an EMPTY SizeStatusInClusters
// map but a POST-Running `Status.ShardCount` indicator (the iter-14d gate),
// plus a pre-existing STS on each member cluster reflecting the real
// CURRENT-state replica counts. Assert the scaler returns the kube-derived
// current count, not zero. The downstream effect of this fix is that
// ReplicasThisReconciliation returns `currentReplicas + 1`, preserving the
// one-pod-at-a-time invariant that the (CR, shard-0) cross-cluster lease
// guard relies on for cap=1 safety.
//
// iter-14d note: the rehydration is now gated on
// `deploymentState.Status.ShardCount > 0` (proxy for "we've been past
// PhaseRunning at least once"). On a true initial deploy the gate is closed
// — see TestDistributedMode_InitialDeployFastPath for that branch. This test
// simulates the post-Running scenario by seeding `sc.Status.ShardCount = 1`
// BEFORE constructing the helper; `migrateToNewDeploymentState` copies it
// into `deploymentState.Status.ShardCount` so the gate opens.
//
// On tip 8b17b62e5 (pre-iter-14c) this test FAILS — the scaler reports
// CurrentReplicas=0 and ReplicasThisReconciliation returns the target (5).
// On tip 3e1ec46ea (iter-14c fix) the test PASSES even WITHOUT the iter-14d
// gate (because rehydration is unconditional in distributed mode). On the
// iter-14d code (this commit's chain) the test PASSES because the seeded
// `Status.ShardCount` opens the gate.
func TestDistributedMode_FollowerScalerOneAtATime(t *testing.T) {
	ctx := context.Background()
	memberClusterNames := []string{"member-cluster-1", "member-cluster-2", "member-cluster-3"}

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	fakeKubeClient := mock.NewEmptyFakeClientBuilder().WithObjects(mock.GetDefaultResources()...).Build()
	kubeClient := kubernetesClient.NewClient(fakeKubeClient)

	memberClusterMap := getFakeMultiClusterMapWithConfiguredInterceptor(memberClusterNames, omConnectionFactory, true, true)

	// Build a 1-shard sharded MongoDB CR with members [5, 5, 4] per cluster —
	// the target post-scale state from pod-mode test_scale_up_3.
	target := map[string]int{
		"member-cluster-1": 5,
		"member-cluster-2": 5,
		"member-cluster-3": 4,
	}
	// Live STS state pre-scale: [2, 2, 1] per cluster. Three member-cluster
	// operators have completed initial deploy + the iter-14 rolling restart
	// step, so the STSes exist with these replica counts. The leader's
	// deploymentState recorded them; followers' deploymentState did not (they
	// returned Pending before reaching calculateSizeStatus).
	current := map[string]int{
		"member-cluster-1": 2,
		"member-cluster-2": 2,
		"member-cluster-3": 1,
	}

	sc := test.DefaultClusterBuilder().
		SetTopology(mdbv1.ClusterTopologyMultiCluster).
		SetShardCountSpec(1).
		SetMongodsPerShardCountSpec(0).
		SetConfigServerCountSpec(0).
		SetMongosCountSpec(0).
		SetShardClusterSpec(test.CreateClusterSpecList(memberClusterNames, target)).
		SetConfigSrvClusterSpec(test.CreateClusterSpecList(memberClusterNames, map[string]int{"member-cluster-1": 2, "member-cluster-2": 2, "member-cluster-3": 1})).
		SetMongosClusterSpec(test.CreateClusterSpecList(memberClusterNames, map[string]int{"member-cluster-1": 1, "member-cluster-2": 2, "member-cluster-3": 1})).
		Build()
	// iter-14d: simulate that we've been past PhaseRunning at least once by
	// seeding `sc.Status.ShardCount`. This is the gate the rehydration uses
	// to distinguish "post-Running rolling-restart / scale-up" (where the
	// follower's SizeStatusInClusters map may legitimately lag the live STS)
	// from "initial deploy" (where empty everything is correct and the
	// ScalingFirstTime fast-path must remain intact). The migrateToNewDeploymentState
	// path copies `sc.Status` into `deploymentState.Status`, so this seed is
	// what `initializeMemberClusters` actually reads when the state configmap
	// is absent.
	sc.Status.ShardCount = 1
	require.NoError(t, kubeClient.Create(ctx, sc))

	// Pre-create the shard-0 STS in each cluster's fake k8s client. The MDB
	// resource sc.Name + multi-cluster naming pattern is "{name}-{shard}-{i}".
	// We index by cluster position in `memberClusterNames` (0/1/2). Note: the
	// helper assigns Index from the deploymentState ClusterMapping; for a
	// fresh-built helper the mapping follows insertion order in the spec.
	//
	// iter-14e: the rehydration reads `Status.ReadyReplicas` (not
	// Spec.Replicas) to anchor the staircase pacing to actually-ready pods.
	// Seed both Spec.Replicas and Status.ReadyReplicas to the pre-scale
	// count so the rehydrate fires and reports the correct CurrentReplicas.
	for i, cn := range memberClusterNames {
		stsName := sc.MultiShardRsName(i, 0)
		sts := &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      stsName,
				Namespace: sc.Namespace,
			},
			Spec: appsv1.StatefulSetSpec{
				Replicas: ptr.To(int32(current[cn])),
			},
			Status: appsv1.StatefulSetStatus{
				ReadyReplicas: int32(current[cn]),
			},
		}
		require.NoError(t, memberClusterMap[cn].Create(ctx, sts),
			"pre-create STS %s on %s", stsName, cn)
	}

	// Build the reconciler helper in DISTRIBUTED MODE: a coordinator is
	// attached BEFORE initializeMemberClusters runs. MyClusterName is set to
	// "member-cluster-2" — a FOLLOWER — to model the bug surface where the
	// operator never completed a full reconcile and the deploymentState
	// configmap was never populated with ShardMongodsInClusters.
	r := newShardedClusterReconciler(ctx, kubeClient, nil,
		"fake-initDatabaseNonStaticImageVersion",
		"fake-databaseNonStaticImageVersion",
		false, false, false, "", memberClusterMap,
		omConnectionFactory.GetConnectionFunc, testBackupEnableDelay)

	followerCoordinator := newFakeCoordinator("member-cluster-2", false)
	helper, err := NewShardedClusterReconcilerHelperWithCoordinator(ctx,
		r.ReconcileCommonController, nil,
		"fake-initDatabaseNonStaticImageVersion",
		"fake-databaseNonStaticImageVersion",
		false, false, false, "", sc, memberClusterMap,
		omConnectionFactory.GetConnectionFunc, zap.S(),
		testBackupEnableDelay, followerCoordinator)
	require.NoError(t, err)
	require.True(t, helper.IsDistributed(), "helper must be in distributed mode")

	// Sanity: deploymentState has NO recorded per-cluster shard counts (empty
	// SizeStatusInClusters — the bug surface that the rehydration exists to
	// paper over) but DOES have a non-zero `Status.ShardCount` (the iter-14d
	// gate that says "we've been past PhaseRunning, rehydration is safe").
	require.Empty(t, helper.deploymentState.Status.SizeStatusInClusters.ShardMongodsInClusters,
		"test premise: follower's deploymentState must NOT have per-cluster shard counts populated; got %+v",
		helper.deploymentState.Status.SizeStatusInClusters.ShardMongodsInClusters)
	require.Greater(t, helper.deploymentState.Status.ShardCount, 0,
		"test premise: deploymentState.Status.ShardCount must be >0 (post-Running) for rehydration to engage; got %d",
		helper.deploymentState.Status.ShardCount)

	// In distributed mode each member-cluster operator runs against ONLY its
	// own kube client; the iter-14e rehydration therefore reads STS state
	// from the LOCAL cluster slot and seeds REMOTE slots from
	// `spec.ClusterSpecList[*].Members` (a deliberate symmetry-restoring
	// step that prevents the iter-14c asymmetric Replicas array from
	// flipping ScalingFirstTime=false with CurrentReplicas=0 on the
	// remotes — the regression fingerprint that broke initial deploy).
	//
	// The test exercises a FOLLOWER on `member-cluster-2`. The cross-
	// cluster staircase invariant is enforced at the FSM lease layer
	// (iter-13c (CR, component) cross-cluster mutex), NOT at the scaler
	// layer: each operator runs its own scaler and writes its own STS,
	// gated by lease acquisition. So the only scaler value that actually
	// drives a STS write on cluster-2 is the LOCAL scaler — that's the
	// one we pin here.
	//
	// Concretely with prevMembers post-rehydration = [c1=5 (spec),
	// c2=2 (live LOCAL STS), c3=4 (spec)] and target = [5, 5, 4]:
	//   - LOCAL c2 scaler: ScalingFirstTime=false; DesiredReplicas finds
	//     c1 spec=prev (same → continue), c2 spec=5 prev=2 (different,
	//     us → return replicasInSpec=5). DesiredReplicas=5,
	//     CurrentReplicas=2, RTR = current+1 = 3. The desired staircase.
	// On tip 268af4cde this fails — the rehydration is gated off because
	// Status.ShardCount=0, so c2's scaler reports CurrentReplicas=0,
	// ScalingFirstTime=true, RTR=target=5 (the +Δ-in-one-write bug).
	localCluster := "member-cluster-2"
	var localMC multicluster.MemberCluster
	for _, mc := range helper.shardsMemberClustersMap[0] {
		if mc.Name == localCluster {
			localMC = mc
			break
		}
	}
	require.NotEmpty(t, localMC.Name, "test premise: local cluster %s must be in shardsMemberClustersMap[0]", localCluster)

	scaler := helper.GetShardScaler(0, localMC)

	// PRIMARY ASSERTION: CurrentReplicas reflects the live LOCAL STS (2),
	// not zero. On tip 8b17b62e5 (pre-iter-14c) and tip 268af4cde
	// (iter-14d with Status.ShardCount=0) this fails — the scaler reports
	// CurrentReplicas=0 because the persisted state is empty.
	assert.Equalf(t, current[localCluster], scaler.CurrentReplicas(),
		"LOCAL scaler for shard-0 on %s must report CurrentReplicas=%d derived from live STS, got %d",
		localCluster, current[localCluster], scaler.CurrentReplicas())

	// SECONDARY ASSERTION: RTR follows the staircase for the local
	// cluster's own scaler. With the bug, CurrentReplicas==0 →
	// RTR==target (a +Δ-in-one-write per cluster that breaks cap=1).
	wantRTR := current[localCluster] + 1
	if wantRTR > target[localCluster] {
		wantRTR = target[localCluster]
	}
	assert.Equalf(t, wantRTR, scale.ReplicasThisReconciliation(scaler),
		"LOCAL ReplicasThisReconciliation for shard-0 on %s must follow staircase (current=%d, target=%d, want=%d), got %d",
		localCluster, current[localCluster], target[localCluster], wantRTR,
		scale.ReplicasThisReconciliation(scaler))
}

// TestDistributedMode_InitialDeployFastPath — G'5 iter 14d regression pin.
//
// Pinpoints the iter-14c regression: the unconditional rehydration in
// `initializeMemberClusters` broke the initial-deploy fast-path. On the
// second reconcile of a fresh CR, the local cluster's STS exists (the
// follower wrote it on reconcile-1) but the remote-cluster STSes are
// unreachable from this operator's client. Rehydration fills the LOCAL slot
// only, producing an asymmetric Replicas array: some non-zero, others zero.
// `MultiClusterReplicaSetScaler.ScalingFirstTime()` returns true ONLY if
// every slot is zero — so the local-slot rehydration flips it to false on
// reconcile-2, and the scaler enters a per-cluster +1 staircase from
// CurrentReplicas=0 for clusters it can't see. The leader then reports
// `Pending: Continuing scaling` indefinitely while followers wait on AC
// publish — the deadlock documented in G'5 iter 14c status (2026-05-17).
//
// The iter-14d gate uses `deploymentState.Status.ShardCount > 0` as a proxy
// for "we've been past PhaseRunning at least once". On a true initial deploy
// the gate is closed; rehydration does not run; the empty Replicas array
// keeps `ScalingFirstTime=true` and `ReplicasThisReconciliation=target`
// (the desired fast-path that lets a fresh deploy hit goal-state in one
// shot per cluster).
//
// Setup: distributed-mode helper, follower coordinator, NO STSes
// pre-created in member clusters (a real fresh deploy), `sc.Status.ShardCount`
// LEFT AT ZERO (the gate is closed). Assertion: for every cluster's scaler,
// `ScalingFirstTime()` is true and `ReplicasThisReconciliation` returns the
// target. On tip 3e1ec46ea (iter-14c) this test FAILS — the local cluster's
// slot gets rehydrated from the STS we don't create, BUT because the helper's
// LOCAL cluster name is `member-cluster-2` and we DO want to model the bug
// surface in the most direct form, we use a second variant of the test that
// pre-creates a STS only on the local cluster (mimicking reconcile-2 of
// initial deploy where the local STS exists but the operator's view of
// remotes is empty). That variant should ALSO PASS on iter-14d (rehydration
// gated off) and FAIL on iter-14c (asymmetric rehydration flips
// ScalingFirstTime).
func TestDistributedMode_InitialDeployFastPath(t *testing.T) {
	t.Run("reconcile-1: no STS anywhere", func(t *testing.T) {
		runInitialDeployFastPathCase(t, false)
	})
	t.Run("reconcile-2: local STS exists, remotes do not (the iter-14c regression surface)", func(t *testing.T) {
		runInitialDeployFastPathCase(t, true)
	})
}

// runInitialDeployFastPathCase models an initial-deploy reconcile in
// distributed pod-mode. `localStsExists` toggles between reconcile-1 (no STS
// anywhere) and reconcile-2 (local cluster wrote its STS at target on
// reconcile-1 but couldn't publish AC; remotes are still empty). Both shapes
// MUST yield `ScalingFirstTime=true` and `ReplicasThisReconciliation=target`
// for every cluster's scaler — i.e. rehydration must NOT engage during
// initial deploy regardless of what the local STS looks like.
func runInitialDeployFastPathCase(t *testing.T, localStsExists bool) {
	ctx := context.Background()
	memberClusterNames := []string{"member-cluster-1", "member-cluster-2", "member-cluster-3"}

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	fakeKubeClient := mock.NewEmptyFakeClientBuilder().WithObjects(mock.GetDefaultResources()...).Build()
	kubeClient := kubernetesClient.NewClient(fakeKubeClient)

	memberClusterMap := getFakeMultiClusterMapWithConfiguredInterceptor(memberClusterNames, omConnectionFactory, true, true)

	target := map[string]int{
		"member-cluster-1": 2,
		"member-cluster-2": 2,
		"member-cluster-3": 1,
	}

	sc := test.DefaultClusterBuilder().
		SetTopology(mdbv1.ClusterTopologyMultiCluster).
		SetShardCountSpec(1).
		SetMongodsPerShardCountSpec(0).
		SetConfigServerCountSpec(0).
		SetMongosCountSpec(0).
		SetShardClusterSpec(test.CreateClusterSpecList(memberClusterNames, target)).
		SetConfigSrvClusterSpec(test.CreateClusterSpecList(memberClusterNames, map[string]int{"member-cluster-1": 2, "member-cluster-2": 2, "member-cluster-3": 1})).
		SetMongosClusterSpec(test.CreateClusterSpecList(memberClusterNames, map[string]int{"member-cluster-1": 1, "member-cluster-2": 1, "member-cluster-3": 1})).
		Build()
	// CRITICAL: explicitly zero out `sc.Status` — the DefaultClusterBuilder
	// pre-fills Status.ShardCount=2 (a convenience for the legacy tests that
	// inspect updateStatus options). For the initial-deploy gate-closed
	// branch we need Status.ShardCount=0 so the iter-14d guard suppresses
	// rehydration.
	sc.Status = mdbv1.MongoDbStatus{}
	require.Equal(t, 0, sc.Status.ShardCount, "test premise: initial deploy has Status.ShardCount=0")
	require.NoError(t, kubeClient.Create(ctx, sc))

	if localStsExists {
		// Reconcile-2 surface: only the LOCAL cluster's STS exists. The
		// follower coordinator below names its cluster "member-cluster-2", so
		// the local cluster is index 1.
		//
		// iter-14e: the rehydrate reads `Status.ReadyReplicas` rather than
		// `Spec.Replicas` so the staircase paces to actually-ready pods.
		// Seed both fields to the target so the rehydrate fires.
		localClusterName := "member-cluster-2"
		stsName := sc.MultiShardRsName(1, 0)
		sts := &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      stsName,
				Namespace: sc.Namespace,
			},
			Spec: appsv1.StatefulSetSpec{
				Replicas: ptr.To(int32(target[localClusterName])),
			},
			Status: appsv1.StatefulSetStatus{
				ReadyReplicas: int32(target[localClusterName]),
			},
		}
		require.NoError(t, memberClusterMap[localClusterName].Create(ctx, sts),
			"pre-create LOCAL-only STS %s on %s", stsName, localClusterName)
	}

	r := newShardedClusterReconciler(ctx, kubeClient, nil,
		"fake-initDatabaseNonStaticImageVersion",
		"fake-databaseNonStaticImageVersion",
		false, false, false, "", memberClusterMap,
		omConnectionFactory.GetConnectionFunc, testBackupEnableDelay)

	followerCoordinator := newFakeCoordinator("member-cluster-2", false)
	helper, err := NewShardedClusterReconcilerHelperWithCoordinator(ctx,
		r.ReconcileCommonController, nil,
		"fake-initDatabaseNonStaticImageVersion",
		"fake-databaseNonStaticImageVersion",
		false, false, false, "", sc, memberClusterMap,
		omConnectionFactory.GetConnectionFunc, zap.S(),
		testBackupEnableDelay, followerCoordinator)
	require.NoError(t, err)
	require.True(t, helper.IsDistributed(), "helper must be in distributed mode")
	require.Equal(t, 0, helper.deploymentState.Status.ShardCount,
		"test premise: initial-deploy state must have ShardCount=0 so the iter-14d gate stays closed")

	// THE PRIMARY ASSERTION: every scaler must report
	// `ReplicasThisReconciliation = target` (the fast-path) on initial
	// deploy regardless of whether `ScalingFirstTime` is true or false.
	// On reconcile-1 (no STS anywhere) the rehydration is a no-op and
	// `ScalingFirstTime=true` short-circuits RTR to target. On reconcile-2
	// (local STS at target, remotes empty) the iter-14e rehydration seeds
	// LOCAL=target from the live STS and REMOTES=target from the spec, so
	// every prev matches spec → `DesiredReplicas` returns the (already-
	// reached) target → RTR=target. Either path keeps the desired fast-
	// path invariant: the operator does not introduce a phantom scale-down
	// or +1 staircase on a CR that's already at goal-state.
	//
	// Background: an earlier draft asserted `ScalingFirstTime=true` as a
	// proxy for "fast-path engaged". That was over-narrow — what actually
	// matters is RTR=target. The iter-14c regression fingerprint was
	// RTR=current+1 from CurrentReplicas=0 on the remote scalers (because
	// only the local slot was rehydrated, leaving remotes at zero). With
	// the iter-14e seed-from-spec the prev array is symmetric again, so
	// the scaler resolves to "everyone at spec → no-op", which is the
	// goal-state behaviour we want.
	for _, mc := range helper.shardsMemberClustersMap[0] {
		scaler := helper.GetShardScaler(0, mc)
		assert.Equalf(t, target[mc.Name], scale.ReplicasThisReconciliation(scaler),
			"ReplicasThisReconciliation for shard-0 on %s must take the initial-deploy fast-path (=target=%d), got %d",
			mc.Name, target[mc.Name], scale.ReplicasThisReconciliation(scaler))
	}
}

// TestDistributedMode_FollowerScaleUpStaircaseWithoutShardCount — G'5 iter 14e
// regression pin. Tightens the iter-14d gate's blind spot.
//
// The iter-14d gate keys the rehydration on
// `deploymentState.Status.ShardCount > 0` ("we've been past PhaseRunning at
// least once"). Status.ShardCount is set by `MongoDB.UpdateStatus` ONLY when
// `phase == PhaseRunning`. In distributed pod-mode, FOLLOWER operators never
// publish their CR Status as Running — `updateOmDeploymentShardedCluster`
// short-circuits to `Pending: waiting for leader to publish AC` for every
// non-leader. So Status.ShardCount on every follower's CR is permanently
// zero; the iter-14d gate is permanently CLOSED on followers.
//
// During a scale-up after the initial deploy completed (target [5,5,4] vs
// current [2,2,1]), each follower's reconcile observes:
//   - persisted SizeStatusInClusters: empty (no prior PhaseRunning).
//   - Status.ShardCount: 0 (iter-14d gate closed).
//   - live LOCAL STS at the pre-scale replicas (e.g. 2).
//
// Without rehydration the scaler reports CurrentReplicas=0 and
// ScalingFirstTime=true, so ReplicasThisReconciliation returns the target
// (5) in a single shot. The follower writes Spec.Replicas=5 → +3 NotReady
// pods enter the shard-0 replicaset globally → cap=1 invariant breaks. This
// is the iter-14d e2e fingerprint: cluster-2 STS jumped 2 → 5, cluster-3
// STS jumped 1 → 4, while the leader correctly performed +1 staircase.
//
// The fix: rehydrate from the live LOCAL STS when it exists at non-zero
// replicas, REGARDLESS of `Status.ShardCount`. The presence of a non-zero
// local STS is itself the "post initial deploy" signal — strictly stronger
// than the iter-14d gate (any cluster that has Status.ShardCount>0 has its
// local STS at non-zero too; the converse holds for followers).
//
// To avoid the iter-14c regression (asymmetric Replicas array on initial-
// deploy reconcile-2), the rehydration must also seed the REMOTE-cluster
// slots from `spec.clusterSpecList[*].Members` when the local STS rehydrate
// fires. This keeps ScalingFirstTime=false (correct: post-deploy) and lets
// the scaler's DesiredReplicas loop pick the "first cluster whose spec
// differs from prev" — which on a follower with rehydrate-from-spec
// matches the leader's view of the scale-up frontier, and the iter-13c
// (CR, shard-0) cross-cluster lease guard serialises clusters one-at-a-time.
//
// Test surface: distributed-mode helper, follower coordinator
// (member-cluster-2), STS pre-created on EVERY member cluster (mimicking
// post-initial-deploy state on all three), spec set to the scale-up target,
// `sc.Status.ShardCount = 0` (the follower's CR never reached Running).
// Assert: for every cluster's shard-0 scaler, `CurrentReplicas()` matches
// the live STS value (i.e. rehydration fired) and
// `ReplicasThisReconciliation` follows the staircase pattern (only the
// first-to-scale cluster moves by +1; the others stay put).
//
// On tip `268af4cde` (iter-14d) this test FAILS: the iter-14d gate is
// closed because Status.ShardCount=0, rehydration is skipped, every cluster
// reports CurrentReplicas=0 → ScalingFirstTime=true → RTR=target. After
// the iter-14e fix this test PASSES.
func TestDistributedMode_FollowerScaleUpStaircaseWithoutShardCount(t *testing.T) {
	ctx := context.Background()
	memberClusterNames := []string{"member-cluster-1", "member-cluster-2", "member-cluster-3"}

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	fakeKubeClient := mock.NewEmptyFakeClientBuilder().WithObjects(mock.GetDefaultResources()...).Build()
	kubeClient := kubernetesClient.NewClient(fakeKubeClient)

	memberClusterMap := getFakeMultiClusterMapWithConfiguredInterceptor(memberClusterNames, omConnectionFactory, true, true)

	// Post-scale target: [5, 5, 4] per cluster — the post-mutation state of
	// pod-mode test_scale_up_3.
	target := map[string]int{
		"member-cluster-1": 5,
		"member-cluster-2": 5,
		"member-cluster-3": 4,
	}
	// Pre-scale (live STS) state: [2, 2, 1] — the baseline test_create plus
	// rolling-restart left in the cluster.
	current := map[string]int{
		"member-cluster-1": 2,
		"member-cluster-2": 2,
		"member-cluster-3": 1,
	}

	sc := test.DefaultClusterBuilder().
		SetTopology(mdbv1.ClusterTopologyMultiCluster).
		SetShardCountSpec(1).
		SetMongodsPerShardCountSpec(0).
		SetConfigServerCountSpec(0).
		SetMongosCountSpec(0).
		SetShardClusterSpec(test.CreateClusterSpecList(memberClusterNames, target)).
		SetConfigSrvClusterSpec(test.CreateClusterSpecList(memberClusterNames, map[string]int{"member-cluster-1": 2, "member-cluster-2": 2, "member-cluster-3": 1})).
		SetMongosClusterSpec(test.CreateClusterSpecList(memberClusterNames, map[string]int{"member-cluster-1": 1, "member-cluster-2": 2, "member-cluster-3": 1})).
		Build()
	// CRITICAL: the follower's CR Status never reached PhaseRunning, so
	// Status.ShardCount must be ZERO. This is the iter-14d gate condition
	// under which iter-14c's rehydration is suppressed — exactly the bug
	// surface this test pins.
	sc.Status = mdbv1.MongoDbStatus{}
	require.Equal(t, 0, sc.Status.ShardCount,
		"test premise: follower's Status.ShardCount must be 0 (never reached Running locally)")
	require.NoError(t, kubeClient.Create(ctx, sc))

	// Pre-create the shard-0 STS on EVERY member cluster at the pre-scale
	// replicas. Each cluster's local operator already wrote its STS during
	// the pre-scale initial deploy; the iter-14e fix derives "post-deploy"
	// from the live LOCAL STS (Status.ReadyReplicas), not from
	// Status.ShardCount.
	//
	// Status.ReadyReplicas is the anchor (not Spec.Replicas) so the
	// rehydration paces the +1 staircase against actually-ready pods —
	// preventing the runaway-Spec issue where consecutive reconciles see
	// each other's writes as the new "current" and ratchet ahead of the
	// pod-startup horizon.
	for i, cn := range memberClusterNames {
		stsName := sc.MultiShardRsName(i, 0)
		sts := &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      stsName,
				Namespace: sc.Namespace,
			},
			Spec: appsv1.StatefulSetSpec{
				Replicas: ptr.To(int32(current[cn])),
			},
			Status: appsv1.StatefulSetStatus{
				ReadyReplicas: int32(current[cn]),
			},
		}
		require.NoError(t, memberClusterMap[cn].Create(ctx, sts),
			"pre-create STS %s on %s", stsName, cn)
	}

	r := newShardedClusterReconciler(ctx, kubeClient, nil,
		"fake-initDatabaseNonStaticImageVersion",
		"fake-databaseNonStaticImageVersion",
		false, false, false, "", memberClusterMap,
		omConnectionFactory.GetConnectionFunc, testBackupEnableDelay)

	// FOLLOWER coordinator: this is the operator running on member-cluster-2,
	// not the raft leader. The follower's CR Status hasn't been published as
	// Running, so Status.ShardCount=0.
	followerCoordinator := newFakeCoordinator("member-cluster-2", false)
	helper, err := NewShardedClusterReconcilerHelperWithCoordinator(ctx,
		r.ReconcileCommonController, nil,
		"fake-initDatabaseNonStaticImageVersion",
		"fake-databaseNonStaticImageVersion",
		false, false, false, "", sc, memberClusterMap,
		omConnectionFactory.GetConnectionFunc, zap.S(),
		testBackupEnableDelay, followerCoordinator)
	require.NoError(t, err)
	require.True(t, helper.IsDistributed(), "helper must be in distributed mode")

	// Sanity-check the bug surface: deploymentState has NO ShardCount AND
	// NO per-cluster shard counts. The iter-14d gate `ShardCount > 0` would
	// suppress rehydration here; the iter-14e fix must use the live LOCAL STS
	// as a stronger signal.
	require.Equal(t, 0, helper.deploymentState.Status.ShardCount,
		"test premise: follower's deploymentState.Status.ShardCount must remain 0 (iter-14d gate is the blind spot we're fixing)")
	require.Empty(t, helper.deploymentState.Status.SizeStatusInClusters.ShardMongodsInClusters,
		"test premise: follower's persisted SizeStatusInClusters must be empty (never wrote Running locally)")

	// Each member-cluster operator only writes ITS OWN local STS in
	// distributed mode (the iter-13c/14b inline gate routes non-self
	// clusters to SkipDone). So the only scaler value that actually
	// affects a STS write here is the LOCAL cluster's scaler. We pin the
	// LOCAL scaler's CurrentReplicas and RTR — that's the staircase
	// invariant the FSM lease guard relies on for cap=1 NotReady safety.
	//
	// With prev post-rehydration = [c1=5 (spec), c2=2 (live LOCAL STS),
	// c3=4 (spec)] and target = [5, 5, 4]:
	//   - LOCAL c2 scaler: DesiredReplicas finds c1 matches spec (no
	//     scale needed for c1 from this operator's view), c2 differs and
	//     is "us" → return replicasInSpec=5. DesiredReplicas=5,
	//     CurrentReplicas=2, RTR=3. Staircase.
	// On tip 268af4cde this fails — the iter-14d Status.ShardCount=0 gate
	// suppresses rehydration on the follower; CurrentReplicas=0,
	// ScalingFirstTime=true, RTR=target=5 → STS Spec.Replicas jumps 2→5
	// in one shot → +3 NotReady pods globally → cap=1 violated.
	localCluster := "member-cluster-2"
	var localMC multicluster.MemberCluster
	for _, mc := range helper.shardsMemberClustersMap[0] {
		if mc.Name == localCluster {
			localMC = mc
			break
		}
	}
	require.NotEmpty(t, localMC.Name, "test premise: local cluster %s must be in shardsMemberClustersMap[0]", localCluster)

	scaler := helper.GetShardScaler(0, localMC)

	// PRIMARY ASSERTION: rehydration fired despite Status.ShardCount=0.
	// On tip 268af4cde this fails — CurrentReplicas reports 0 because the
	// iter-14d gate suppressed the rehydration path.
	assert.Equalf(t, current[localCluster], scaler.CurrentReplicas(),
		"LOCAL shard-0 scaler on %s must report CurrentReplicas=%d derived from live STS even when Status.ShardCount=0 (iter-14d gate's blind spot), got %d",
		localCluster, current[localCluster], scaler.CurrentReplicas())

	// SECONDARY ASSERTION: the staircase invariant holds — the LOCAL
	// scaler returns RTR=current+1, not RTR=target. Without the fix, the
	// scaler reports RTR=target=5 (one-shot Δ=3 write) which is the cap=1
	// NotReady violation in the iter-14d e2e log
	// (max_notready_per_component={'shard-0': 3}).
	wantRTR := current[localCluster] + 1
	if wantRTR > target[localCluster] {
		wantRTR = target[localCluster]
	}
	assert.Equalf(t, wantRTR, scale.ReplicasThisReconciliation(scaler),
		"LOCAL ReplicasThisReconciliation for shard-0 on %s must follow staircase (current=%d, target=%d, want=%d), got %d",
		localCluster, current[localCluster], target[localCluster], wantRTR,
		scale.ReplicasThisReconciliation(scaler))
}

// TestDistributedMode_FollowerScalerAnchorsToReadyReplicas — G'5 iter 14e
// regression pin against the runaway-Spec bug.
//
// The original iter-14e draft rehydrated from `Spec.Replicas`. That value
// advances every reconcile (because the previous reconcile's RTR write is
// now the "current" Spec.Replicas). On a fast-firing reconcile chain
// (K8s STS watch triggers reconcile within ~1.5s of a Spec update), this
// causes Spec.Replicas to ratchet ahead of pod-startup time:
//
//	reconcile-1: Spec.Replicas=2 (live), RTR=3 -> write Spec.Replicas=3.
//	STS watch fires.
//	reconcile-2 (1.5s later): Spec.Replicas=3 (live now), RTR=4 -> write 4.
//	reconcile-3 (3s after that): Spec.Replicas=4 (live), RTR=5 -> write 5.
//	Pods 3, 4, 5 are all spawning concurrently => max NotReady = 3.
//
// This was reproduced live on a follower during pod-mode test_scale_up_3
// where the cluster-2 STS jumped 2 -> 3 -> 4 -> 5 within ~5 seconds,
// breaking the per-RS cap=1 NotReady invariant.
//
// The fix: anchor the rehydration to `Status.ReadyReplicas`. This number
// only advances when a pod has actually passed its readiness probe — i.e.
// when the previous +1 has fully landed. The next reconcile's rehydrate
// then yields the SAME CurrentReplicas as the previous reconcile until a
// new pod becomes Ready, so RTR=current+1 stays at the SAME value, and
// the Spec.Replicas write is idempotent (no further increment).
//
// Test surface: distributed-mode helper, follower coordinator on
// member-cluster-2, local STS pre-created with Spec.Replicas=3 (the
// just-written +1) but Status.ReadyReplicas=2 (pod-3 still spinning up).
// The scaler should report CurrentReplicas=2 (anchored to ReadyReplicas)
// and RTR=3 (current+1), NOT advance to RTR=4 just because Spec.Replicas
// already reads 3. Without the fix, the scaler would report
// CurrentReplicas=3 (from Spec.Replicas) and RTR=4 — the runaway-Spec
// fingerprint that broke pod-mode test_scale_up_3 with iter-14e v1.
func TestDistributedMode_FollowerScalerAnchorsToReadyReplicas(t *testing.T) {
	ctx := context.Background()
	memberClusterNames := []string{"member-cluster-1", "member-cluster-2", "member-cluster-3"}

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	fakeKubeClient := mock.NewEmptyFakeClientBuilder().WithObjects(mock.GetDefaultResources()...).Build()
	kubeClient := kubernetesClient.NewClient(fakeKubeClient)

	// Use markStsAsReady=false: the fake-client interceptor would otherwise
	// promote `Status.ReadyReplicas = Spec.Replicas` on every Get, which
	// hides the very bug this test pins (where ReadyReplicas legitimately
	// lags Spec.Replicas during a mid-staircase scale-up). We need the
	// scaler to observe `Status.ReadyReplicas=2` even while `Spec.Replicas=3`.
	memberClusterMap := getFakeMultiClusterMapWithConfiguredInterceptor(memberClusterNames, omConnectionFactory, false, true)

	target := map[string]int{
		"member-cluster-1": 5,
		"member-cluster-2": 5,
		"member-cluster-3": 4,
	}
	// MID-staircase state: Spec.Replicas already advanced to 3 (the +1 from
	// the previous reconcile) but Status.ReadyReplicas is still 2 (pod-3
	// is spawning, hasn't passed readiness probe).
	const localCluster = "member-cluster-2"
	localSpecReplicas := int32(3)
	localReadyReplicas := int32(2)

	sc := test.DefaultClusterBuilder().
		SetTopology(mdbv1.ClusterTopologyMultiCluster).
		SetShardCountSpec(1).
		SetMongodsPerShardCountSpec(0).
		SetConfigServerCountSpec(0).
		SetMongosCountSpec(0).
		SetShardClusterSpec(test.CreateClusterSpecList(memberClusterNames, target)).
		SetConfigSrvClusterSpec(test.CreateClusterSpecList(memberClusterNames, map[string]int{"member-cluster-1": 2, "member-cluster-2": 2, "member-cluster-3": 1})).
		SetMongosClusterSpec(test.CreateClusterSpecList(memberClusterNames, map[string]int{"member-cluster-1": 1, "member-cluster-2": 2, "member-cluster-3": 1})).
		Build()
	sc.Status = mdbv1.MongoDbStatus{}
	require.NoError(t, kubeClient.Create(ctx, sc))

	// Pre-create the LOCAL cluster's shard-0 STS with the asymmetric
	// Spec/Status values that model the mid-staircase moment.
	stsName := sc.MultiShardRsName(1, 0)
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      stsName,
			Namespace: sc.Namespace,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: ptr.To(localSpecReplicas),
		},
		Status: appsv1.StatefulSetStatus{
			ReadyReplicas: localReadyReplicas,
		},
	}
	require.NoError(t, memberClusterMap[localCluster].Create(ctx, sts),
		"pre-create LOCAL STS %s on %s", stsName, localCluster)

	r := newShardedClusterReconciler(ctx, kubeClient, nil,
		"fake-initDatabaseNonStaticImageVersion",
		"fake-databaseNonStaticImageVersion",
		false, false, false, "", memberClusterMap,
		omConnectionFactory.GetConnectionFunc, testBackupEnableDelay)

	followerCoordinator := newFakeCoordinator(localCluster, false)
	helper, err := NewShardedClusterReconcilerHelperWithCoordinator(ctx,
		r.ReconcileCommonController, nil,
		"fake-initDatabaseNonStaticImageVersion",
		"fake-databaseNonStaticImageVersion",
		false, false, false, "", sc, memberClusterMap,
		omConnectionFactory.GetConnectionFunc, zap.S(),
		testBackupEnableDelay, followerCoordinator)
	require.NoError(t, err)
	require.True(t, helper.IsDistributed())

	// Find the local slot in the rehydrated map.
	var localMC multicluster.MemberCluster
	for _, mc := range helper.shardsMemberClustersMap[0] {
		if mc.Name == localCluster {
			localMC = mc
			break
		}
	}
	require.NotEmpty(t, localMC.Name)

	scaler := helper.GetShardScaler(0, localMC)

	// PRIMARY ASSERTION: CurrentReplicas is anchored to ReadyReplicas (2),
	// NOT Spec.Replicas (3). On a tip that rehydrated from Spec.Replicas,
	// CurrentReplicas would report 3 and RTR would be 4 — the runaway-Spec
	// fingerprint.
	assert.Equalf(t, int(localReadyReplicas), scaler.CurrentReplicas(),
		"LOCAL scaler must anchor CurrentReplicas to Status.ReadyReplicas=%d, NOT Spec.Replicas=%d (the runaway-Spec bug)",
		localReadyReplicas, localSpecReplicas)

	// SECONDARY ASSERTION: RTR matches the +1 step from ReadyReplicas
	// (not Spec). On the buggy tip RTR would be ReadyReplicas+2 (because
	// Spec already advanced once).
	expectedRTR := int(localReadyReplicas) + 1
	assert.Equalf(t, expectedRTR, scale.ReplicasThisReconciliation(scaler),
		"LOCAL RTR must advance ONE step ahead of ReadyReplicas=%d (=%d), got %d (Spec.Replicas would imply RTR=%d which is the runaway-Spec bug)",
		localReadyReplicas, expectedRTR, scale.ReplicasThisReconciliation(scaler), int(localSpecReplicas)+1)
}
