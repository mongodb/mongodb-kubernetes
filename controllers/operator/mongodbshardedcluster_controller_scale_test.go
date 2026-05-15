package operator

// F9 scale-up integration test, layered on the F8 harness.
//
// The F8 harness simulates STS work as a single "write once per
// (component, cluster)" event. Real scale-up would re-acquire the lease
// for the same (component, cluster) because the desired replica count is
// not yet met. In the simulated harness we model "scale-up" by FORCING the
// (shard-0, cluster-c) scope's Ready bit back to false mid-run, and
// asserting that:
//
//   1. A fresh lease for (shard-0, cluster-c) is allocated AFTER the
//      initial Ready / Release cycle (verifiable by counting FSM lease
//      transitions or per-scope AcquireOrRespect invocations).
//   2. Between each scale-up cycle the leader's AC version increments
//      (we simulate AC publish by having the leader's reconcile loop
//      call AnnounceAcPublished after each MarkReady on its own cluster).
//   3. Final state has every (component, cluster) Ready=true.

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/raft"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	clientpkg "sigs.k8s.io/controller-runtime/pkg/client"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/coordination"
	coordraft "github.com/mongodb/mongodb-kubernetes/pkg/coordination/raft"
	"github.com/mongodb/mongodb-kubernetes/pkg/test"
)

// scaleHarness extends the F8 ideas with mid-run mutation + AC announcement.
type scaleHarness struct {
	t                *testing.T
	clusters         []string
	sc               *mdbv1.MongoDB
	mockOM           *om.MockedOmConnection
	memberClusterMap map[string]clientpkg.Client

	tcpNodes     []*coordraft.TCPNode
	coordinators map[string]*coordraft.Coordinator
	helpers      map[string]*ShardedClusterReconcileHelper
	crKey        coordination.CRKey
	raftCRKey    coordraft.CRKey

	leader atomic.Value // string

	stsMu         sync.Mutex
	stsWriteOrder []string
	// allocations is the count of distinct lease allocations per
	// (component, cluster) scope, incremented once per stsWriteOrder entry
	// for that scope.
	allocations map[string]int

	stopReconcile chan struct{}
	wg            sync.WaitGroup
}

func newScaleHarness(t *testing.T, clusters []string) *scaleHarness {
	t.Helper()

	sortedClusters := append([]string(nil), clusters...)
	omFactory := om.NewDefaultCachedOMConnectionFactory()
	conn := omFactory.GetConnectionFunc(&om.OMContext{GroupName: om.TestGroupName})
	mockOM := conn.(*om.MockedOmConnection)

	fakeKube := mock.NewEmptyFakeClientBuilder().WithObjects(mock.GetDefaultResources()...).Build()
	kubeClient := kubernetesClient.NewClient(fakeKube)
	memberMap := getFakeMultiClusterMapWithConfiguredInterceptor(sortedClusters, omFactory, true, true)

	sc := test.DefaultClusterBuilder().
		SetTopology(mdbv1.ClusterTopologyMultiCluster).
		SetShardCountSpec(2).
		SetMongodsPerShardCountSpec(0).
		SetConfigServerCountSpec(0).
		SetMongosCountSpec(0).
		SetShardClusterSpec(test.CreateClusterSpecList(sortedClusters,
			map[string]int{sortedClusters[0]: 1, sortedClusters[1]: 1, sortedClusters[2]: 1})).
		SetConfigSrvClusterSpec(test.CreateClusterSpecList(sortedClusters,
			map[string]int{sortedClusters[0]: 1, sortedClusters[1]: 1, sortedClusters[2]: 1})).
		SetMongosClusterSpec(test.CreateClusterSpecList(sortedClusters,
			map[string]int{sortedClusters[0]: 1, sortedClusters[1]: 1, sortedClusters[2]: 1})).
		Build()
	require.NoError(t, kubeClient.Create(context.Background(), sc))

	crKey := coordination.CRKey{Kind: "MongoDB", Namespace: sc.Namespace, Name: sc.Name}
	raftCRKey := coordraft.CRKey{Kind: crKey.Kind, Namespace: crKey.Namespace, Name: crKey.Name}

	fsms := make([]*coordraft.FSM, len(sortedClusters))
	for i := range sortedClusters {
		fsms[i] = coordraft.NewFSM()
	}
	tcpNodes, err := coordraft.NewTCPRaftCluster(len(sortedClusters), fsms, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		for _, n := range tcpNodes {
			_ = n.Close()
		}
	})
	coordraft.WireAppChannelForwarding(tcpNodes)

	require.Eventually(t, func() bool {
		for _, n := range tcpNodes {
			if n.Manager.IsLeader() {
				return true
			}
		}
		return false
	}, 5*time.Second, 30*time.Millisecond, "no raft leader within 5s")

	h := &scaleHarness{
		t:                t,
		clusters:         sortedClusters,
		sc:               sc,
		mockOM:           mockOM,
		memberClusterMap: memberMap,
		tcpNodes:         tcpNodes,
		coordinators:     make(map[string]*coordraft.Coordinator, len(sortedClusters)),
		helpers:          make(map[string]*ShardedClusterReconcileHelper, len(sortedClusters)),
		crKey:            crKey,
		raftCRKey:        raftCRKey,
		allocations:      map[string]int{},
		stopReconcile:    make(chan struct{}),
	}

	peerMap := make(map[string]raft.ServerID, len(sortedClusters))
	for i, c := range sortedClusters {
		peerMap[c] = tcpNodes[i].ID
	}

	appAddrResolver := coordraft.BuildTestAppAddrResolver(tcpNodes)
	for i, c := range sortedClusters {
		coord := coordraft.NewCoordinator(c, tcpNodes[i].Manager, tcpNodes[i].FSM)
		coord.SetDefaultCR(raftCRKey)
		fw := coordraft.NewForwarder(tcpNodes[i].Manager, tcpNodes[i].StreamLayer)
		fw.ResolveAppAddr = appAddrResolver
		coord.SetForwarder(fw)
		coord.SetClusterPeerMap(peerMap)
		h.coordinators[c] = coord
		if tcpNodes[i].Manager.IsLeader() {
			h.leader.Store(c)
		}

		_, helper, err := newShardedClusterReconcilerForMultiCluster(context.Background(), false, sc, memberMap, kubeClient, omFactory)
		require.NoError(t, err)
		helper.SetCoordinator(coord)
		h.helpers[c] = helper
	}

	for _, c := range sortedClusters {
		h.wg.Add(1)
		go h.reconcileLoop(c)
	}

	t.Cleanup(func() {
		close(h.stopReconcile)
		h.wg.Wait()
	})

	return h
}

func (h *scaleHarness) reconcileLoop(cluster string) {
	defer h.wg.Done()
	helper := h.helpers[cluster]
	coord := h.coordinators[cluster]
	components := []string{"config", "shard-0", "shard-1", "mongos"}

	// observedAppliedIdx is the leader's last-seen FSM LastAppliedIndex. Any
	// increase between passes (i.e. some proposal was applied) triggers a
	// fresh AC publish — this mirrors the real reconciler's behaviour of
	// bumping AC after each STS-progress event, but is robust to passes
	// that observe only the final Ready state and miss the intermediate
	// Ready=false dip.
	observedAppliedIdx := uint64(0)

	for {
		select {
		case <-h.stopReconcile:
			return
		default:
		}
		pass := func() {
			for _, comp := range components {
				for _, c := range h.clusters {
					switch helper.distGateInline(comp, c) {
					case distGateProceed:
						h.stsMu.Lock()
						h.stsWriteOrder = append(h.stsWriteOrder, fmt.Sprintf("%s/%s", comp, c))
						h.allocations[fmt.Sprintf("%s/%s", comp, c)]++
						h.stsMu.Unlock()
						helper.distMarkReadyAndRelease(comp, c, 1, 1, 1, zap.S())
					case distGateSkipDone:
					case distGateWait:
						return
					}
				}
			}
		}
		pass()

		if coord.IsLeader() {
			fullState := coord.FSM().GetState()
			if fullState.LastAppliedIndex > observedAppliedIdx {
				observedAppliedIdx = fullState.LastAppliedIndex
				ver := coord.AcVersion(h.crKey) + 1
				_ = coord.AnnounceAcPublished(h.crKey, ver)
			}
		}

		time.Sleep(30 * time.Millisecond)
	}
}

// forceRescale flips Ready=false on (component, cluster) by directly
// applying a StatusReport that overrides the entry. Simulates "spec
// mutation requires a fresh STS write".
func (h *scaleHarness) forceRescale(component, cluster string) {
	leaderName := h.leader.Load().(string)
	coord := h.coordinators[leaderName]
	_ = coord.ReportProgress(h.crKey, component, cluster, coordination.ProgressSnapshot{
		CurrentReplicas: 4, // declared desired
		ReadyReplicas:   2, // not yet met
		ObservedGeneration: 2,
	})
}

func (h *scaleHarness) awaitReadyAll(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	components := []string{"config", "shard-0", "shard-1", "mongos"}
	leaderCoord := h.coordinators[h.leader.Load().(string)]

	for time.Now().Before(deadline) {
		state := leaderCoord.FSM().GetPerCR(h.raftCRKey)
		done := true
		for _, c := range h.clusters {
			cs, ok := state.PerClusterStatus[c]
			if !ok {
				done = false
				break
			}
			for _, comp := range components {
				if !cs.ComponentStatus[comp].Ready {
					done = false
					break
				}
			}
			if !done {
				break
			}
		}
		if done {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// TestDistributedScaleUp_F9 is the F9 scale-up assertion test.
//
// Flow:
//   1. Build a 3-cluster harness, drive to steady state (all Ready=true).
//   2. Capture the initial allocation count for shard-0/cluster-c.
//   3. Mutate: ReportProgress with Ready=false on shard-0/cluster-c (via
//      a custom-shaped proposal that flips Ready). Wait until the harness
//      converges back to Ready=true.
//   4. Repeat twice (simulating multiple scale-up steps).
//   5. Assert:
//      - allocations for shard-0/cluster-c happens 3 times total (1 initial + 2 rescales).
//      - AC version increments between each rescale.
//      - Final state has every (component, cluster) Ready=true.
func TestDistributedScaleUp_F9(t *testing.T) {
	clusters := []string{"cluster-a", "cluster-b", "cluster-c"}
	h := newScaleHarness(t, clusters)
	leader := h.leader.Load().(string)
	t.Logf("raft leader: %s", leader)

	// 1. Drive initial reconcile to completion.
	require.True(t, h.awaitReadyAll(15*time.Second), "initial reconcile did not converge")
	h.stsMu.Lock()
	initialAlloc := h.allocations["shard-0/cluster-c"]
	h.stsMu.Unlock()
	require.Equal(t, 1, initialAlloc, "shard-0/cluster-c should have one initial write")

	leaderCoord := h.coordinators[leader]
	acAfterInitial := leaderCoord.AcVersion(h.crKey)
	t.Logf("AC version after initial: %d", acAfterInitial)
	require.Greater(t, acAfterInitial, int64(0), "AC version should have advanced during initial reconcile")

	// 2. Scale-up 1: flip shard-0/cluster-c Ready=false directly via a
	//    Ready=false StatusReport that the reconcile loop will pick up.
	t.Log("scale-up 1: shard-0/cluster-c → not ready")
	require.Equal(t, coordination.LeaseOtherClusterDone, leaderCoord.AcquireOrRespect(h.crKey, "shard-0", "cluster-c", 0), "expected component already Ready before scale-up")
	// Force Ready=false via direct StatusReport with the component absent.
	// The reconcile loop will then see "not ready" and re-allocate.
	_ = leaderCoord.ReportProgress(h.crKey, "shard-0", "cluster-c", coordination.ProgressSnapshot{
		CurrentReplicas: 4, ReadyReplicas: 2, ObservedGeneration: 2,
	})
	// ReportProgress writes Ready=false because submitStatusReport sends
	// Ready=false on that path. Wait for the loop to re-acquire + Ready=true.
	require.Eventually(t, func() bool {
		h.stsMu.Lock()
		defer h.stsMu.Unlock()
		return h.allocations["shard-0/cluster-c"] >= 2
	}, 10*time.Second, 100*time.Millisecond, "shard-0/cluster-c should be re-allocated after scale-up 1")
	acAfterScale1 := leaderCoord.AcVersion(h.crKey)
	t.Logf("AC version after scale-up 1: %d (delta %d)", acAfterScale1, acAfterScale1-acAfterInitial)
	assert.Greater(t, acAfterScale1, acAfterInitial, "AC should have advanced over scale-up 1")

	// 3. Scale-up 2: same approach.
	t.Log("scale-up 2: shard-0/cluster-c → not ready")
	require.True(t, h.awaitReadyAll(5*time.Second), "did not return to Ready=true before scale-up 2")
	_ = leaderCoord.ReportProgress(h.crKey, "shard-0", "cluster-c", coordination.ProgressSnapshot{
		CurrentReplicas: 4, ReadyReplicas: 3, ObservedGeneration: 3,
	})
	require.Eventually(t, func() bool {
		h.stsMu.Lock()
		defer h.stsMu.Unlock()
		return h.allocations["shard-0/cluster-c"] >= 3
	}, 10*time.Second, 100*time.Millisecond, "shard-0/cluster-c should be re-allocated after scale-up 2")
	acAfterScale2 := leaderCoord.AcVersion(h.crKey)
	t.Logf("AC version after scale-up 2: %d", acAfterScale2)
	assert.Greater(t, acAfterScale2, acAfterScale1, "AC should have advanced over scale-up 2")

	// 4. Final convergence.
	require.True(t, h.awaitReadyAll(5*time.Second), "scale-up 2 did not converge")

	// 5. Capture lease-history check: between rescales the lease for
	//    shard-0/cluster-c must have been fully completed (no overlapping
	//    leases). We assert by looking at the leader's FSM after a sweep:
	//    ActiveLease is nil (everything is Ready and Released).
	assert.Nil(t, leaderCoord.FSM().GetActiveLease(h.raftCRKey), "no lease should be held after final convergence")

	// And the total number of (shard-0, cluster-c) allocations should be
	// at least 3 (1 initial + 2 rescales).
	h.stsMu.Lock()
	finalAlloc := h.allocations["shard-0/cluster-c"]
	totalStsWrites := len(h.stsWriteOrder)
	h.stsMu.Unlock()
	assert.GreaterOrEqual(t, finalAlloc, 3, "shard-0/cluster-c should have ≥3 writes (initial + 2 rescales)")
	t.Logf("total sts writes: %d; final shard-0/cluster-c allocs: %d; AC final: %d", totalStsWrites, finalAlloc, acAfterScale2)
}
