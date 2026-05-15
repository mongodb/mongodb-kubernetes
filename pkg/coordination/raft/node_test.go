package coordraft

import (
	"testing"
	"time"

	"github.com/hashicorp/raft"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestThreeNodesElectLeader spins up three managers backed by an in-memory
// transport pool, bootstraps the cluster on node 0, and verifies exactly one
// node believes it is leader within a few seconds.
func TestThreeNodesElectLeader(t *testing.T) {
	ids := []raft.ServerID{"a", "b", "c"}
	pool := NewInmemTransportPool(ids)

	peers := make([]PeerInfo, 0, len(ids))
	for _, id := range ids {
		peers = append(peers, PeerInfo{ID: id, Address: pool[id].Address})
	}

	mgrs := make(map[raft.ServerID]*Manager, len(ids))
	for i, id := range ids {
		cfg := ManagerConfig{
			NodeID:        id,
			BindAddr:      pool[id].Address,
			Peers:         peers,
			Bootstrap:     i == 0, // only node "a" bootstraps
			LogStore:      raft.NewInmemStore(),
			StableStore:   raft.NewInmemStore(),
			SnapshotStore: raft.NewInmemSnapshotStore(),
			Transport:     pool[id].Transport,
			FSM:           StubFSM{},
		}
		m, err := NewManager(cfg)
		require.NoError(t, err, "construct manager %s", id)
		mgrs[id] = m
		t.Cleanup(func() { _ = m.Shutdown() })
	}

	// Within ~3s exactly one node should be leader.
	deadline := time.Now().Add(3 * time.Second)
	var leaderID raft.ServerID
	for time.Now().Before(deadline) {
		leaders := 0
		leaderID = ""
		for id, m := range mgrs {
			if m.IsLeader() {
				leaders++
				leaderID = id
			}
		}
		if leaders == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	leaders := 0
	for _, m := range mgrs {
		if m.IsLeader() {
			leaders++
		}
	}
	assert.Equal(t, 1, leaders, "expected exactly one leader")
	assert.NotEmpty(t, string(leaderID))
	t.Logf("leader elected: %s", leaderID)
}
