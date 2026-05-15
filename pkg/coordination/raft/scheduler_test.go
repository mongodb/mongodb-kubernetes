package coordraft

import (
	"context"
	"testing"
	"time"

	"github.com/hashicorp/raft"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildThreeNodeRaft returns three started managers backed by their own real
// FSMs and an in-memory transport pool. ids are "a","b","c"; "a" bootstraps.
// Test cleanup shuts everything down.
func buildThreeNodeRaft(t *testing.T) (map[raft.ServerID]*Manager, map[raft.ServerID]*FSM) {
	t.Helper()
	ids := []raft.ServerID{"a", "b", "c"}
	pool := NewInmemTransportPool(ids)

	peers := make([]PeerInfo, 0, len(ids))
	for _, id := range ids {
		peers = append(peers, PeerInfo{ID: id, Address: pool[id].Address})
	}

	mgrs := make(map[raft.ServerID]*Manager, len(ids))
	fsms := make(map[raft.ServerID]*FSM, len(ids))
	for i, id := range ids {
		fsm := NewFSM()
		cfg := ManagerConfig{
			NodeID:        id,
			BindAddr:      pool[id].Address,
			Peers:         peers,
			Bootstrap:     i == 0,
			LogStore:      raft.NewInmemStore(),
			StableStore:   raft.NewInmemStore(),
			SnapshotStore: raft.NewInmemSnapshotStore(),
			Transport:     pool[id].Transport,
			FSM:           fsm,
		}
		m, err := NewManager(cfg)
		require.NoError(t, err, "construct manager %s", id)
		mgrs[id] = m
		fsms[id] = fsm
		t.Cleanup(func() { _ = m.Shutdown() })
	}

	// Wait for a leader.
	require.Eventually(t, func() bool {
		for _, m := range mgrs {
			if m.IsLeader() {
				return true
			}
		}
		return false
	}, 3*time.Second, 20*time.Millisecond, "no leader within 3s")

	return mgrs, fsms
}

// findLeader returns the leader's id, manager, fsm.
func findLeader(mgrs map[raft.ServerID]*Manager, fsms map[raft.ServerID]*FSM) (raft.ServerID, *Manager, *FSM) {
	for id, m := range mgrs {
		if m.IsLeader() {
			return id, m, fsms[id]
		}
	}
	return "", nil, nil
}

// TestScheduler_AllocatesFirstLease verifies the scheduler, once started on
// the leader, proposes a lease for the very first (component, cluster) when
// no statuses have been reported yet.
func TestScheduler_AllocatesFirstLease(t *testing.T) {
	mgrs, fsms := buildThreeNodeRaft(t)
	_, leaderMgr, leaderFSM := findLeader(mgrs, fsms)
	require.NotNil(t, leaderMgr)

	sched, err := NewScheduler(leaderMgr, leaderFSM, SchedulerConfig{
		Clusters:     []string{"cluster-c", "cluster-a", "cluster-b"}, // intentionally unsorted
		Components:   []string{"config", "shard-0", "mongos"},
		TickInterval: 50 * time.Millisecond,
		LeaseTTL:     30 * time.Second,
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sched.Start(ctx)
	defer sched.Stop()

	// First lease should be (config, cluster-a) — sorted clusters, first component.
	require.Eventually(t, func() bool {
		l := leaderFSM.GetActiveLease()
		return l != nil && l.Component == "config" && l.ClusterName == "cluster-a"
	}, 3*time.Second, 50*time.Millisecond, "expected first lease (config, cluster-a)")
}

// TestScheduler_ProgressesAfterStatusReports verifies the scheduler walks
// (component, cluster) deterministically as clusters report their statuses
// Ready. We drive the protocol from the test by proposing StatusReports and
// LeaseComplete payloads through the leader's Apply API.
func TestScheduler_ProgressesAfterStatusReports(t *testing.T) {
	mgrs, fsms := buildThreeNodeRaft(t)
	_, leaderMgr, leaderFSM := findLeader(mgrs, fsms)
	require.NotNil(t, leaderMgr)

	clusters := []string{"cluster-a", "cluster-b", "cluster-c"}
	components := []string{"config", "shard-0", "mongos"}

	sched, err := NewScheduler(leaderMgr, leaderFSM, SchedulerConfig{
		Clusters:     clusters,
		Components:   components,
		TickInterval: 30 * time.Millisecond,
		LeaseTTL:     30 * time.Second,
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sched.Start(ctx)
	defer sched.Stop()

	// Drive the protocol: for each (component, cluster) tuple in order, wait
	// for the scheduler to allocate that lease, then propose LeaseComplete +
	// a StatusReport marking the component Ready on the cluster, so the
	// scheduler advances to the next tuple.
	for _, component := range components {
		for _, cluster := range clusters {
			require.Eventually(t, func() bool {
				l := leaderFSM.GetActiveLease()
				return l != nil && l.Component == component && l.ClusterName == cluster
			}, 3*time.Second, 30*time.Millisecond, "expected lease (%s, %s)", component, cluster)

			// Mark cluster ready for this component.
			data, err := EncodeProposal(ProposalStatusReport, StatusReportPayload{
				ClusterName:      cluster,
				ObservedSpecHash: "h1",
				ComponentStatus: map[string]ComponentStatusEntry{
					component: {Generation: 1, Ready: true},
				},
				ReportedAt: time.Now().UTC(),
			})
			require.NoError(t, err)
			require.NoError(t, leaderMgr.Apply(data, 2*time.Second).Error())

			// Complete the lease so the scheduler can allocate the next.
			data, err = EncodeProposal(ProposalLeaseComplete, LeaseCompletePayload{
				Component:   component,
				ClusterName: cluster,
			})
			require.NoError(t, err)
			require.NoError(t, leaderMgr.Apply(data, 2*time.Second).Error())
		}
	}

	// After every tuple is Ready, the scheduler should not allocate any more
	// leases. Wait briefly and assert.
	time.Sleep(300 * time.Millisecond)
	assert.Nil(t, leaderFSM.GetActiveLease(), "no more leases once all components are Ready everywhere")
}

// TestScheduler_FollowerDoesNotAllocate verifies that a scheduler bound to a
// follower never proposes a lease (Apply on a follower errors out).
func TestScheduler_FollowerDoesNotAllocate(t *testing.T) {
	mgrs, fsms := buildThreeNodeRaft(t)

	// Pick a follower.
	var followerID raft.ServerID
	var followerMgr *Manager
	var followerFSM *FSM
	for id, m := range mgrs {
		if !m.IsLeader() {
			followerID = id
			followerMgr = m
			followerFSM = fsms[id]
			break
		}
	}
	require.NotNil(t, followerMgr, "expected at least one follower")
	t.Logf("follower: %s", followerID)

	sched, err := NewScheduler(followerMgr, followerFSM, SchedulerConfig{
		Clusters:     []string{"cluster-a"},
		Components:   []string{"config"},
		TickInterval: 30 * time.Millisecond,
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sched.Start(ctx)
	defer sched.Stop()

	// Give it generous time; follower must never set a lease in its own FSM
	// (FSMs on followers see leases only via raft replication from the
	// leader's Apply, which never happens because we don't run the leader's
	// scheduler here).
	time.Sleep(300 * time.Millisecond)
	assert.Nil(t, followerFSM.GetActiveLease())
}
