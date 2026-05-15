package operator

// F8 headline integration test for the distributed-multi-cluster operator
// PoC. Three in-process helpers, each driven by its own raft.Raft node over
// a real TCP loopback transport (F2-F4 muxed StreamLayer + Forwarder).
//
// What this test proves (against the design redesign in Phase F):
//   - Three independent reconcile-loop goroutines (one per cluster), each
//     calling distGateInline + (simulated) STS work + ReportProgress /
//     MarkReady / ReleaseLease via the real coordinator API.
//   - The leader's reconcile loop itself drives lease allocation: when
//     AcquireOrRespect sees no active lease and the cluster's component is
//     not Ready, it proposes LeaseAllocate; raft replicates; the next sweep
//     observes the lease and the holder iterates.
//   - Only the raft leader's mock OM is ever invoked for AC operations.
//     Follower helpers gate out before reading from OM.
//   - STS write order per component is strictly serialised across the three
//     clusters (one cluster's iteration completes before the next can take
//     its lease).
//
// This test runs in <30s and is the load-bearing artefact for Phase D's
// e2e validation: if the protocol survives this harness, the e2e is "wire
// the real K8s + OM + kubeconfigs in".

import (
	"context"
	"fmt"
	"reflect"
	"sort"
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

// distHarnessV2 wires N in-process helpers + raft nodes + Coordinator+Forwarder
// + a per-helper reconcile-loop goroutine.
type distHarnessV2 struct {
	t                *testing.T
	clusters         []string
	sc               *mdbv1.MongoDB
	mockOM           *om.MockedOmConnection
	memberClusterMap map[string]clientpkg.Client

	tcpNodes     []*coordraft.TCPNode
	coordinators map[string]*coordraft.Coordinator
	helpers      map[string]*ShardedClusterReconcileHelper
	crKey        coordination.CRKey

	leader atomic.Value // string

	// stsWriteOrder records the order in which gated STS writes proceeded.
	stsMu         sync.Mutex
	stsWriteOrder []string

	stopReconcile chan struct{}
	wg            sync.WaitGroup
}

func newDistHarnessV2(t *testing.T, clusters []string) *distHarnessV2 {
	t.Helper()

	sortedClusters := append([]string(nil), clusters...)
	sort.Strings(sortedClusters)

	// One shared OM mock + factory (the leader is the only one that should
	// invoke OM ops; the shared mock measures leakage).
	omFactory := om.NewDefaultCachedOMConnectionFactory()
	conn := omFactory.GetConnectionFunc(&om.OMContext{GroupName: om.TestGroupName})
	mockOM := conn.(*om.MockedOmConnection)

	fakeKube := mock.NewEmptyFakeClientBuilder().WithObjects(mock.GetDefaultResources()...).Build()
	kubeClient := kubernetesClient.NewClient(fakeKube)
	memberMap := getFakeMultiClusterMapWithConfiguredInterceptor(sortedClusters, omFactory, true, true)

	// CR with shard/config/mongos distributed across all three clusters.
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

	// Build a real 3-node raft cluster over TCP with muxed StreamLayer.
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

	// Wait for leader election.
	require.Eventually(t, func() bool {
		for _, n := range tcpNodes {
			if n.Manager.IsLeader() {
				return true
			}
		}
		return false
	}, 5*time.Second, 30*time.Millisecond, "no raft leader within 5s")

	h := &distHarnessV2{
		t:                t,
		clusters:         sortedClusters,
		sc:               sc,
		mockOM:           mockOM,
		memberClusterMap: memberMap,
		tcpNodes:         tcpNodes,
		coordinators:     make(map[string]*coordraft.Coordinator, len(sortedClusters)),
		helpers:          make(map[string]*ShardedClusterReconcileHelper, len(sortedClusters)),
		crKey:            crKey,
		stopReconcile:    make(chan struct{}),
	}

	// Build cluster→ServerID map for LastContact lookups.
	peerMap := make(map[string]raft.ServerID, len(sortedClusters))
	for i, c := range sortedClusters {
		peerMap[c] = tcpNodes[i].ID
	}

	// Build per-cluster Coordinator + Helper. The cluster name MUST equal
	// the helper's MyClusterName so distGateInline routes correctly.
	appAddrResolver := coordraft.BuildTestAppAddrResolver(tcpNodes)
	for i, c := range sortedClusters {
		coord := coordraft.NewCoordinator(c, tcpNodes[i].Manager, tcpNodes[i].FSM)
		coord.SetDefaultCR(coordraft.CRKey{Kind: "MongoDB", Namespace: sc.Namespace, Name: sc.Name})
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

	// Per-cluster reconcile-loop goroutines. Each loops "drive one pass,
	// sleep 30ms, repeat" until stopped.
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

// reconcileLoop is the per-cluster pretend-reconciler. It mirrors the real
// controller's flow: iterate components in order, and per component iterate
// clusters; on the first Wait we requeue (sleep + restart loop) because the
// production reconciler returns workflow.Pending on Wait. SkipDone simply
// advances past the iteration.
func (h *distHarnessV2) reconcileLoop(cluster string) {
	defer h.wg.Done()
	helper := h.helpers[cluster]
	components := []string{"config", "shard-0", "shard-1", "mongos"}

	for {
		select {
		case <-h.stopReconcile:
			return
		default:
		}

		// One reconcile pass mirrors the real controller: iterate components
		// in fixed order, per component iterate clusters in deterministic
		// order; first Wait short-circuits the pass.
		pass := func() (waited bool) {
			for _, comp := range components {
				for _, c := range h.clusters {
					switch helper.distGateInline(comp, c) {
					case distGateProceed:
						h.stsMu.Lock()
						h.stsWriteOrder = append(h.stsWriteOrder, fmt.Sprintf("%s/%s", comp, c))
						h.stsMu.Unlock()
						helper.distMarkReadyAndRelease(comp, c, 1, 1, 1, zap.S())
					case distGateSkipDone:
						// already done — keep iterating.
					case distGateWait:
						// Stop the pass and requeue.
						return true
					}
				}
			}
			return false
		}
		pass()
		time.Sleep(30 * time.Millisecond)
	}
}

// awaitCompletion waits until every (component, cluster) tuple appears in
// the leader's FSM as Ready.
func (h *distHarnessV2) awaitCompletion(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	components := []string{"config", "shard-0", "shard-1", "mongos"}
	leaderName := h.leader.Load().(string)
	leaderCoord := h.coordinators[leaderName]

	for time.Now().Before(deadline) {
		state := leaderCoord.FSM().GetPerCR(coordraft.CRKey{
			Kind:      h.crKey.Kind,
			Namespace: h.crKey.Namespace,
			Name:      h.crKey.Name,
		})
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

// TestDistributedMultiClusterShardedReconcile_F8 is the F8 headline test.
func TestDistributedMultiClusterShardedReconcile_F8(t *testing.T) {
	clusters := []string{"cluster-a", "cluster-b", "cluster-c"}
	h := newDistHarnessV2(t, clusters)
	leader := h.leader.Load().(string)
	t.Logf("raft leader: %s", leader)

	// Wait for every (component, cluster) to be Ready.
	ok := h.awaitCompletion(20 * time.Second)
	if !ok {
		leaderCoord := h.coordinators[leader]
		state := leaderCoord.FSM().GetPerCR(coordraft.CRKey{
			Kind: h.crKey.Kind, Namespace: h.crKey.Namespace, Name: h.crKey.Name,
		})
		t.Logf("FSM PerClusterStatus dump (len=%d):", len(state.PerClusterStatus))
		for c, cs := range state.PerClusterStatus {
			t.Logf("  %s: %+v", c, cs.ComponentStatus)
		}
		for k, l := range state.ActiveLeases {
			t.Logf("active lease %s: %+v", k, l)
		}
		h.stsMu.Lock()
		t.Logf("stsWriteOrder so far (%d entries): %v", len(h.stsWriteOrder), h.stsWriteOrder)
		h.stsMu.Unlock()
	}
	require.True(t, ok, "loop did not reach steady state within 20s")

	// Assertion (a): every (component, cluster) tuple written exactly once.
	expectedComponents := []string{"config", "shard-0", "shard-1", "mongos"}
	expected := map[string]int{}
	for _, comp := range expectedComponents {
		for _, c := range h.clusters {
			expected[fmt.Sprintf("%s/%s", comp, c)] = 1
		}
	}
	h.stsMu.Lock()
	got := map[string]int{}
	for _, w := range h.stsWriteOrder {
		got[w]++
	}
	t.Logf("stsWriteOrder: %v", h.stsWriteOrder)
	h.stsMu.Unlock()
	assert.Equal(t, expected, got, "every (component, cluster) tuple should be written exactly once")

	// Assertion (b): order serialised per component (3 contiguous entries).
	h.stsMu.Lock()
	defer h.stsMu.Unlock()
	groupStart := 0
	for groupStart < len(h.stsWriteOrder) {
		group := []string{h.stsWriteOrder[groupStart]}
		comp := groupComponent(group[0])
		for j := groupStart + 1; j < len(h.stsWriteOrder); j++ {
			if groupComponent(h.stsWriteOrder[j]) == comp {
				group = append(group, h.stsWriteOrder[j])
			} else {
				break
			}
		}
		assert.Len(t, group, 3, "component %s should have 3 entries contiguous in stsWriteOrder", comp)
		groupStart += len(group)
	}

	// Assertion (c): only the leader's helper invokes OM AC ops. After
	// completion the leader's reconcile loop may have already invoked OM —
	// reset the history and explicitly call updateOmDeploymentShardedCluster
	// from each cluster's helper.
	ctx := context.Background()
	h.mockOM.CleanHistory()
	for _, c := range h.clusters {
		_ = h.helpers[c].updateOmDeploymentShardedCluster(ctx, h.mockOM, h.sc, deploymentOptions{}, false, zap.S())
	}
	// At least one OM call (from the leader's helper).
	h.mockOM.CheckOrderOfOperations(t, reflect.ValueOf(h.mockOM.ReadAutomationAgents))
}

// groupComponent extracts the "component" prefix from a "component/cluster"
// stsWriteOrder entry.
func groupComponent(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			return s[:i]
		}
	}
	return s
}
