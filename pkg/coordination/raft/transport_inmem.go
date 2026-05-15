package coordraft

import (
	"time"

	"github.com/hashicorp/raft"
)

// NewInmemTransportPool returns a map of pre-wired in-memory transports —
// one per node ID — that can all talk to each other. The returned addresses
// are stable: each node's address is the raft.ServerAddress produced by
// raft.NewInmemTransport for that node ID.
//
// Used exclusively for unit tests: it never opens a real TCP socket.
func NewInmemTransportPool(nodeIDs []raft.ServerID) map[raft.ServerID]*InmemNetwork {
	pool := make(map[raft.ServerID]*InmemNetwork, len(nodeIDs))
	// First pass: create one (addr, transport) per node.
	for _, id := range nodeIDs {
		addr, trans := raft.NewInmemTransport("")
		pool[id] = &InmemNetwork{
			ID:        id,
			Address:   addr,
			Transport: trans,
		}
	}
	// Second pass: wire every transport to every other transport so a node
	// can reach its peers by address. raft.InmemTransport.Connect adds a
	// peer to the local routing table.
	for srcID, src := range pool {
		for dstID, dst := range pool {
			if srcID == dstID {
				continue
			}
			src.Transport.Connect(dst.Address, dst.Transport)
		}
	}
	return pool
}

// InmemNetwork is the per-node bundle returned by NewInmemTransportPool.
type InmemNetwork struct {
	ID        raft.ServerID
	Address   raft.ServerAddress
	Transport *raft.InmemTransport
}

// FastConfig returns a raft.Config tuned for in-process unit tests: low
// heartbeat + election timeouts so elections finish within ~200ms.
func FastConfig(id raft.ServerID) *raft.Config {
	cfg := raft.DefaultConfig()
	cfg.LocalID = id
	cfg.HeartbeatTimeout = 50 * time.Millisecond
	cfg.ElectionTimeout = 50 * time.Millisecond
	cfg.LeaderLeaseTimeout = 50 * time.Millisecond
	cfg.CommitTimeout = 5 * time.Millisecond
	// Quieter logs in tests.
	cfg.LogLevel = "ERROR"
	return cfg
}
