package coordraft

import (
	"time"

	"github.com/hashicorp/raft"
	"golang.org/x/xerrors"
)

// Manager wraps a single raft.Raft node plus the bits the operator needs to
// drive proposals and observe state.
type Manager struct {
	cfg      ManagerConfig
	raft     *raft.Raft
	fsm      raft.FSM
	transport raft.Transport
}

// NewManager constructs and starts a raft node according to cfg. If
// cfg.Bootstrap is true and the LogStore is empty, the node bootstraps a new
// cluster with cfg.Peers as the initial voter set.
func NewManager(cfg ManagerConfig) (*Manager, error) {
	if cfg.NodeID == "" {
		return nil, xerrors.New("NodeID required")
	}
	if cfg.FSM == nil {
		return nil, xerrors.New("FSM required")
	}
	if cfg.LogStore == nil || cfg.StableStore == nil || cfg.SnapshotStore == nil {
		return nil, xerrors.New("LogStore, StableStore, SnapshotStore required")
	}
	if cfg.Transport == nil {
		return nil, xerrors.New("Transport required")
	}

	rcfg := FastConfig(cfg.NodeID)
	if cfg.HeartbeatTimeout > 0 {
		rcfg.HeartbeatTimeout = cfg.HeartbeatTimeout
	}
	if cfg.ElectionTimeout > 0 {
		rcfg.ElectionTimeout = cfg.ElectionTimeout
	}
	if cfg.LeaderLeaseTimeout > 0 {
		rcfg.LeaderLeaseTimeout = cfg.LeaderLeaseTimeout
	}
	if cfg.CommitTimeout > 0 {
		rcfg.CommitTimeout = cfg.CommitTimeout
	}

	if cfg.Bootstrap {
		hasState, err := raft.HasExistingState(cfg.LogStore, cfg.StableStore, cfg.SnapshotStore)
		if err != nil {
			return nil, xerrors.Errorf("HasExistingState: %w", err)
		}
		if !hasState {
			servers := make([]raft.Server, 0, len(cfg.Peers))
			for _, p := range cfg.Peers {
				servers = append(servers, raft.Server{
					Suffrage: raft.Voter,
					ID:       p.ID,
					Address:  p.Address,
				})
			}
			if err := raft.BootstrapCluster(rcfg, cfg.LogStore, cfg.StableStore, cfg.SnapshotStore, cfg.Transport, raft.Configuration{Servers: servers}); err != nil {
				return nil, xerrors.Errorf("BootstrapCluster: %w", err)
			}
		}
	}

	r, err := raft.NewRaft(rcfg, cfg.FSM, cfg.LogStore, cfg.StableStore, cfg.SnapshotStore, cfg.Transport)
	if err != nil {
		return nil, xerrors.Errorf("raft.NewRaft: %w", err)
	}

	return &Manager{
		cfg:       cfg,
		raft:      r,
		fsm:       cfg.FSM,
		transport: cfg.Transport,
	}, nil
}

// Raft returns the underlying raft.Raft handle. Tests and the scheduler use it
// directly for AddVoter / Stats / etc.
func (m *Manager) Raft() *raft.Raft { return m.raft }

// FSM returns the FSM the manager was constructed with.
func (m *Manager) FSM() raft.FSM { return m.fsm }

// IsLeader returns true iff this node currently believes it is leader.
func (m *Manager) IsLeader() bool {
	return m.raft.State() == raft.Leader
}

// LeaderAddr returns the current believed leader address, or "" if unknown.
func (m *Manager) LeaderAddr() raft.ServerAddress {
	addr, _ := m.raft.LeaderWithID()
	return addr
}

// Apply submits a proposal to the raft log. On a follower this returns an
// error; the caller should redirect to the leader.
func (m *Manager) Apply(data []byte, timeout time.Duration) raft.ApplyFuture {
	return m.raft.Apply(data, timeout)
}

// Shutdown stops the raft node. Safe to call multiple times.
func (m *Manager) Shutdown() error {
	return m.raft.Shutdown().Error()
}
