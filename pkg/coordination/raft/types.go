// Package coordraft provides a thin wrapper around hashicorp/raft for the
// distributed-multi-cluster-operator PoC. It bundles a real raft.Raft node, an
// FSM that tracks cross-cluster reconcile state, and helpers (in-memory
// transport, in-memory stores) so unit tests can run N nodes in one process.
package coordraft

import (
	"time"

	"github.com/hashicorp/raft"
)

// PeerInfo identifies a single raft node in the cluster.
type PeerInfo struct {
	ID      raft.ServerID
	Address raft.ServerAddress
}

// ManagerConfig is the input to NewManager.
type ManagerConfig struct {
	// NodeID is this node's identity (must be unique in the cluster).
	NodeID raft.ServerID
	// BindAddr is informational for in-memory transport; it must equal the
	// transport's local address. Callers using NewInmemTransportPool should
	// pass the address it returned for this node.
	BindAddr raft.ServerAddress
	// Peers is the bootstrap set of voters. Only the bootstrap node sets
	// Bootstrap=true; others receive the configuration via raft replication.
	Peers []PeerInfo
	// Bootstrap, if true, this node will issue raft.BootstrapCluster on a
	// fresh state.
	Bootstrap bool
	// LogStore / StableStore / SnapshotStore / Transport: bring-your-own.
	// For in-process tests, pass raft.NewInmemStore() and the in-memory
	// transport returned by NewInmemTransportPool.
	LogStore       raft.LogStore
	StableStore    raft.StableStore
	SnapshotStore  raft.SnapshotStore
	Transport      raft.Transport
	FSM            raft.FSM
	// HeartbeatTimeout / ElectionTimeout / LeaderLeaseTimeout / CommitTimeout:
	// optional overrides. Defaults (from raft.DefaultConfig) are fine for prod
	// but unit tests usually want them tightened to ~50ms.
	HeartbeatTimeout   time.Duration
	ElectionTimeout    time.Duration
	LeaderLeaseTimeout time.Duration
	CommitTimeout      time.Duration
}
