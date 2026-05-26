package haraft

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/hashicorp/raft"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// RaftNode wraps *raft.Raft with the small surface the operator needs.
type RaftNode struct {
	cfg         NodeConfig
	peerClients map[string]client.Client

	raft      *raft.Raft
	transport *KubeTransport

	mu        sync.Mutex
	leaderCBs []func(bool)
	stopCh    chan struct{}
	stopOnce  sync.Once
}

func NewRaftNode(cfg NodeConfig, peerClients map[string]client.Client) (*RaftNode, error) {
	if _, ok := peerClients[cfg.ClusterName]; !ok {
		return nil, fmt.Errorf("peerClients missing local cluster %q", cfg.ClusterName)
	}
	return &RaftNode{cfg: cfg, peerClients: peerClients, stopCh: make(chan struct{})}, nil
}

func (n *RaftNode) Start(ctx context.Context) error {
	localClient := n.peerClients[n.cfg.ClusterName]
	logStore := NewConfigMapLogStore(localClient, n.cfg.Namespace)
	stableStore := NewConfigMapStableStore(localClient, n.cfg.Namespace)
	snapStore := raft.NewInmemSnapshotStore()

	n.transport = NewKubeTransport(n.cfg.ClusterName, n.cfg.Namespace, n.peerClients, n.cfg.PollInterval)
	if err := n.transport.start(ctx); err != nil {
		return err
	}

	rcfg := raft.DefaultConfig()
	rcfg.LocalID = raft.ServerID(n.cfg.ClusterName)
	rcfg.HeartbeatTimeout = n.cfg.HeartbeatTimeout
	rcfg.ElectionTimeout = n.cfg.ElectionTimeout
	rcfg.CommitTimeout = n.cfg.CommitTimeout
	rcfg.LeaderLeaseTimeout = n.cfg.LeaderLeaseTimeout

	// Seed the stores with the initial cluster configuration BEFORE constructing
	// the Raft instance, so NewRaft loads the voter list into in-memory state
	// from its very first tick. Calling BootstrapCluster after NewRaft has a
	// race window where the election timer fires against an empty configuration
	// and the node enters a silent stuck state — hashicorp/raft logs
	// "no known peers, aborting election" exactly once (didWarn=true) and
	// then never retries on its own, since runFollower's heartbeat path is a
	// silent no-op while latestIndex == 0.
	servers := make([]raft.Server, 0, len(n.cfg.Peers))
	for _, p := range n.cfg.Peers {
		servers = append(servers, raft.Server{ID: raft.ServerID(p), Address: raft.ServerAddress(p)})
	}
	hasState, err := raft.HasExistingState(logStore, stableStore, snapStore)
	if err != nil {
		return fmt.Errorf("haraft: HasExistingState: %w", err)
	}
	if !hasState {
		if err := raft.BootstrapCluster(rcfg, logStore, stableStore, snapStore, n.transport,
			raft.Configuration{Servers: servers}); err != nil {
			return fmt.Errorf("haraft: BootstrapCluster: %w", err)
		}
	}

	fsm := emptyFSM{}
	r, err := raft.NewRaft(rcfg, fsm, logStore, stableStore, snapStore, n.transport)
	if err != nil {
		return err
	}
	n.raft = r

	go n.watchLeadership()
	return nil
}

func (n *RaftNode) watchLeadership() {
	ch := n.raft.LeaderCh()
	for {
		select {
		case isLeader := <-ch:
			n.mu.Lock()
			cbs := append([]func(bool){}, n.leaderCBs...)
			n.mu.Unlock()
			for _, cb := range cbs {
				cb(isLeader)
			}
		case <-n.stopCh:
			return
		}
	}
}

func (n *RaftNode) IsLeader() bool {
	if n.raft == nil {
		return false
	}
	return n.raft.State() == raft.Leader
}

func (n *RaftNode) Leader() string {
	if n.raft == nil {
		return ""
	}
	_, id := n.raft.LeaderWithID()
	return string(id)
}

// LocalID returns this node's cluster identifier (the value passed in NodeConfig.ClusterName).
func (n *RaftNode) LocalID() string {
	return n.cfg.ClusterName
}

func (n *RaftNode) OnLeadershipChange(cb func(bool)) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.leaderCBs = append(n.leaderCBs, cb)
}

func (n *RaftNode) Stop() {
	n.stopOnce.Do(func() {
		close(n.stopCh)
		if n.raft != nil {
			// raft.Shutdown() will call Transport.Close() itself via the WithClose
			// interface, so we don't need to close the transport explicitly here.
			_ = n.raft.Shutdown().Error()
		} else if n.transport != nil {
			_ = n.transport.Close()
		}
	})
}

// emptyFSM is a no-op FSM. Raft replicates nothing of substance here.
type emptyFSM struct{}

func (emptyFSM) Apply(*raft.Log) interface{}          { return nil }
func (emptyFSM) Snapshot() (raft.FSMSnapshot, error)  { return emptyFSMSnapshot{}, nil }
func (emptyFSM) Restore(snapshot io.ReadCloser) error { return snapshot.Close() }

type emptyFSMSnapshot struct{}

func (emptyFSMSnapshot) Persist(sink raft.SnapshotSink) error { return sink.Close() }
func (emptyFSMSnapshot) Release()                             {}
