package coordraft

import (
	"testing"
	"time"

	"github.com/hashicorp/raft"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mongodb/mongodb-kubernetes/pkg/coordination"
)

// newCoordinatorClusterForTest spins up n TCP nodes with app-channel
// forwarding pre-wired, builds a Coordinator+Forwarder for each, and returns
// the coords keyed by cluster name "node-0".."node-(n-1)".
func newCoordinatorClusterForTest(t *testing.T, n int) (
	[]*TCPNode, []*Coordinator, int,
) {
	t.Helper()
	fsms := make([]*FSM, n)
	for i := 0; i < n; i++ {
		fsms[i] = NewFSM()
	}
	nodes, err := NewTCPRaftCluster(n, fsms, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		for _, n := range nodes {
			_ = n.Close()
		}
	})
	WireAppChannelForwarding(nodes)

	coords := make([]*Coordinator, n)
	peerMap := make(map[string]raft.ServerID, n)
	for _, no := range nodes {
		peerMap[string(no.ID)] = no.ID
	}
	appAddrResolver := BuildTestAppAddrResolver(nodes)
	for i, no := range nodes {
		c := NewCoordinator(string(no.ID), no.Manager, no.FSM)
		c.SetDefaultCR(CRKey{Kind: "MongoDB", Namespace: "ns", Name: "cr"})
		fw := NewForwarder(no.Manager, no.StreamLayer)
		fw.ResolveAppAddr = appAddrResolver
		c.SetForwarder(fw)
		c.SetClusterPeerMap(peerMap)
		coords[i] = c
	}

	// Wait for leader.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for i, no := range nodes {
			if no.Manager.IsLeader() {
				return nodes, coords, i
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no leader within 5s")
	return nil, nil, -1
}

func TestCoordinator_AcquireOrRespect_BasicHeld(t *testing.T) {
	nodes, coords, leaderIdx := newCoordinatorClusterForTest(t, 3)
	leader := coords[leaderIdx]

	crKey := coordination.CRKey{Kind: "MongoDB", Namespace: "ns", Name: "cr"}

	// Leader acquires "config" on its own cluster.
	res := leader.AcquireOrRespect(crKey, "config", string(nodes[leaderIdx].ID), 0)
	assert.Equal(t, coordination.LeaseHeld, res)

	// Same call again is still Held (idempotent).
	res2 := leader.AcquireOrRespect(crKey, "config", string(nodes[leaderIdx].ID), 0)
	assert.Equal(t, coordination.LeaseHeld, res2)

	// A different cluster's request for the SAME component on a DIFFERENT
	// cluster must WAIT — G'5 iter 13c serialises (CR, component) across
	// clusters. See TestLeaseSerializesAcrossClustersPerComponent for the
	// rolling-restart safety violation this prevents.
	otherIdx := (leaderIdx + 1) % 3
	res3 := coords[otherIdx].AcquireOrRespect(crKey, "config", string(nodes[otherIdx].ID), 0)
	assert.Equal(t, coordination.LeaseWaitForLease, res3,
		"cross-cluster (config, other) must wait while (config, leader) is held")

	// Once the leader releases, the next cluster can acquire.
	require.NoError(t, leader.ReleaseLease(crKey, "config", string(nodes[leaderIdx].ID)))
	require.Eventually(t, func() bool {
		return coords[otherIdx].AcquireOrRespect(crKey, "config", string(nodes[otherIdx].ID), 0) == coordination.LeaseHeld
	}, 2*time.Second, 30*time.Millisecond)
}

func TestCoordinator_AcquireOrRespect_OtherClusterDoneShortCircuit(t *testing.T) {
	nodes, coords, leaderIdx := newCoordinatorClusterForTest(t, 3)
	leader := coords[leaderIdx]
	crKey := coordination.CRKey{Kind: "MongoDB", Namespace: "ns", Name: "cr"}

	otherIdx := (leaderIdx + 1) % 3
	otherName := string(nodes[otherIdx].ID)

	// Mark the "other" cluster's component Ready directly via MarkReady on
	// the leader's coord (which forwards through raft to all FSMs).
	require.NoError(t, leader.MarkReady(crKey, "config", otherName, coordination.ProgressSnapshot{
		CurrentReplicas: 1, ReadyReplicas: 1, AgentGoalVersionAchieve: 1, ObservedGeneration: 1,
	}))

	// Now AcquireOrRespect for that scope should short-circuit to
	// OtherClusterDone (regardless of who calls).
	require.Eventually(t, func() bool {
		return leader.AcquireOrRespect(crKey, "config", otherName, 0) == coordination.LeaseOtherClusterDone
	}, 2*time.Second, 30*time.Millisecond)
}

func TestCoordinator_IsComponentReady(t *testing.T) {
	nodes, coords, leaderIdx := newCoordinatorClusterForTest(t, 3)
	leader := coords[leaderIdx]
	crKey := coordination.CRKey{Kind: "MongoDB", Namespace: "ns", Name: "cr"}
	otherIdx := (leaderIdx + 1) % 3
	otherName := string(nodes[otherIdx].ID)

	assert.False(t, leader.IsComponentReady(crKey, "config", otherName, 0))
	require.NoError(t, leader.MarkReady(crKey, "config", otherName, coordination.ProgressSnapshot{
		CurrentReplicas: 1, ReadyReplicas: 1, AgentGoalVersionAchieve: 1, ObservedGeneration: 1,
	}))
	require.Eventually(t, func() bool {
		return leader.IsComponentReady(crKey, "config", otherName, 0)
	}, 2*time.Second, 30*time.Millisecond)
}

func TestCoordinator_ReportProgress_HeartbeatsLease(t *testing.T) {
	nodes, coords, leaderIdx := newCoordinatorClusterForTest(t, 3)
	leader := coords[leaderIdx]
	leaderName := string(nodes[leaderIdx].ID)
	crKey := coordination.CRKey{Kind: "MongoDB", Namespace: "ns", Name: "cr"}

	// Leader acquires its own lease.
	require.Equal(t, coordination.LeaseHeld, leader.AcquireOrRespect(crKey, "config", leaderName, 0))
	initial := leader.FSM().GetActiveLease(toRaftCRKey(crKey)).HeartbeatAt

	time.Sleep(10 * time.Millisecond)

	require.NoError(t, leader.ReportProgress(crKey, "config", leaderName, coordination.ProgressSnapshot{
		CurrentReplicas: 2, ReadyReplicas: 1, ObservedGeneration: 1,
	}))

	// Heartbeat refreshed.
	require.Eventually(t, func() bool {
		l := leader.FSM().GetActiveLease(toRaftCRKey(crKey))
		return l != nil && l.HeartbeatAt.After(initial)
	}, 2*time.Second, 30*time.Millisecond)
}

func TestCoordinator_MarkReadyAndReleaseLease(t *testing.T) {
	nodes, coords, leaderIdx := newCoordinatorClusterForTest(t, 3)
	leader := coords[leaderIdx]
	leaderName := string(nodes[leaderIdx].ID)
	crKey := coordination.CRKey{Kind: "MongoDB", Namespace: "ns", Name: "cr"}

	require.Equal(t, coordination.LeaseHeld, leader.AcquireOrRespect(crKey, "config", leaderName, 0))
	require.NoError(t, leader.MarkReady(crKey, "config", leaderName, coordination.ProgressSnapshot{
		CurrentReplicas: 1, ReadyReplicas: 1, AgentGoalVersionAchieve: 1, ObservedGeneration: 1,
	}))
	require.NoError(t, leader.ReleaseLease(crKey, "config", leaderName))

	require.Eventually(t, func() bool {
		return leader.FSM().GetActiveLease(toRaftCRKey(crKey)) == nil
	}, 2*time.Second, 30*time.Millisecond)
	assert.True(t, leader.IsComponentReady(crKey, "config", leaderName, 0))
}

func TestCoordinator_AcVersion_AnnounceAcPublished(t *testing.T) {
	_, coords, leaderIdx := newCoordinatorClusterForTest(t, 3)
	leader := coords[leaderIdx]
	crKey := coordination.CRKey{Kind: "MongoDB", Namespace: "ns", Name: "cr"}

	assert.Equal(t, int64(0), leader.AcVersion(crKey))
	require.NoError(t, leader.AnnounceAcPublished(crKey, 5))
	require.Eventually(t, func() bool { return leader.AcVersion(crKey) == 5 }, 2*time.Second, 30*time.Millisecond)
	// Lower version is a no-op.
	require.NoError(t, leader.AnnounceAcPublished(crKey, 3))
	assert.Equal(t, int64(5), leader.AcVersion(crKey))
}

func TestCoordinator_FollowerProposalForwarding(t *testing.T) {
	nodes, coords, leaderIdx := newCoordinatorClusterForTest(t, 3)
	followerIdx := (leaderIdx + 1) % 3
	follower := coords[followerIdx]
	leader := coords[leaderIdx]
	crKey := coordination.CRKey{Kind: "MongoDB", Namespace: "ns", Name: "cr"}

	// Follower fires AnnounceAcPublished — forwarder routes it to the leader.
	require.NoError(t, follower.AnnounceAcPublished(crKey, 11))
	require.Eventually(t, func() bool { return leader.AcVersion(crKey) == 11 }, 2*time.Second, 30*time.Millisecond)

	// Same follower fires AcquireOrRespect for ITS OWN cluster.
	res := follower.AcquireOrRespect(crKey, "config", string(nodes[followerIdx].ID), 0)
	assert.Equal(t, coordination.LeaseHeld, res)
}

func TestCoordinator_ClusterIndex(t *testing.T) {
	_, coords, leaderIdx := newCoordinatorClusterForTest(t, 3)
	leader := coords[leaderIdx]

	// Initially no index.
	_, ok := leader.ClusterIndex("cluster-x")
	assert.False(t, ok)

	// Assign via Apply directly (this method isn't exposed on the new
	// interface; F7+ post-PoC may add an assignment proposal helper).
	data, err := EncodeProposal(ProposalClusterIndexAssign, ClusterIndexAssignPayload{ClusterName: "cluster-x", Index: 7})
	require.NoError(t, err)
	require.NoError(t, leader.applyProposal(data, applyTimeout))
	require.Eventually(t, func() bool {
		idx, ok := leader.ClusterIndex("cluster-x")
		return ok && idx == 7
	}, 2*time.Second, 30*time.Millisecond)
}

func TestCoordinator_ProgressSnapshot_IsReady(t *testing.T) {
	// Not ready when ready replicas < current.
	assert.False(t, coordination.ProgressSnapshot{CurrentReplicas: 3, ReadyReplicas: 2, AgentGoalVersionAchieve: 1, ObservedGeneration: 1}.IsReady())
	// Not ready when current is 0 (e.g. not yet seen any).
	assert.False(t, coordination.ProgressSnapshot{CurrentReplicas: 0, ReadyReplicas: 0}.IsReady())
	// Not ready when agent goal trails observed gen.
	assert.False(t, coordination.ProgressSnapshot{CurrentReplicas: 3, ReadyReplicas: 3, AgentGoalVersionAchieve: 0, ObservedGeneration: 1}.IsReady())
	// Ready.
	assert.True(t, coordination.ProgressSnapshot{CurrentReplicas: 3, ReadyReplicas: 3, AgentGoalVersionAchieve: 1, ObservedGeneration: 1}.IsReady())
	// Pending error blocks ready.
	assert.False(t, coordination.ProgressSnapshot{CurrentReplicas: 3, ReadyReplicas: 3, AgentGoalVersionAchieve: 1, ObservedGeneration: 1, PendingError: "x"}.IsReady())
}
