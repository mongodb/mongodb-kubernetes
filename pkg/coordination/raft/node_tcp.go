package coordraft

import (
	"io"
	"net"
	"time"

	"github.com/hashicorp/raft"
	"golang.org/x/xerrors"
)

// TCPNode bundles everything needed to run one raft node over a muxed TCP
// stream layer. Used by F3+ tests and (eventually) the production operator
// that wants real-network raft instead of the in-memory transport.
type TCPNode struct {
	ID         raft.ServerID
	Addr       net.Addr
	StreamLayer *MuxedStreamLayer
	Transport  raft.Transport
	FSM        *FSM
	Manager    *Manager
}

// Close shuts the node's raft instance down then closes the stream layer.
func (n *TCPNode) Close() error {
	var merr error
	if n.Manager != nil {
		if err := n.Manager.Shutdown(); err != nil {
			merr = err
		}
	}
	if n.StreamLayer != nil {
		_ = n.StreamLayer.Close()
	}
	return merr
}

// NewTCPNode constructs a single node over a muxed StreamLayer at bindAddr.
// The caller supplies the FSM (so the test can keep a reference) and the
// bootstrap peer set / flag.
//
// appHandler is the AppChannelHandler the muxed transport will dispatch
// app-channel ('A' handshake) conns to. Tests can pass nil to ignore the
// app channel; F4 wires this up to the proposal forwarder.
func NewTCPNode(
	id raft.ServerID,
	bindAddr string,
	peers []PeerInfo,
	bootstrap bool,
	fsm *FSM,
	appHandler AppChannelHandler,
) (*TCPNode, error) {
	sl, err := NewMuxedStreamLayer(bindAddr, nil, appHandler)
	if err != nil {
		return nil, xerrors.Errorf("muxed listener: %w", err)
	}
	// Wrap with raft.NetworkTransport.
	trans := raft.NewNetworkTransport(sl, 3, 2*time.Second, io.Discard)

	mgr, err := NewManager(ManagerConfig{
		NodeID:        id,
		BindAddr:      raft.ServerAddress(sl.Addr().String()),
		Peers:         peers,
		Bootstrap:     bootstrap,
		LogStore:      raft.NewInmemStore(),
		StableStore:   raft.NewInmemStore(),
		SnapshotStore: raft.NewInmemSnapshotStore(),
		Transport:     trans,
		FSM:           fsm,
	})
	if err != nil {
		_ = sl.Close()
		return nil, xerrors.Errorf("manager: %w", err)
	}
	return &TCPNode{
		ID:          id,
		Addr:        sl.Addr(),
		StreamLayer: sl,
		Transport:   trans,
		FSM:         fsm,
		Manager:     mgr,
	}, nil
}

// NewTCPRaftCluster spins up n nodes on localhost random ports. Returns the
// nodes in deterministic order ("node-0".."node-(n-1)"). Node-0 bootstraps
// the cluster.
//
// If wireAppHandlers is true, each node's muxed StreamLayer gets a default
// AppChannelHandler bound to its raft instance (makeAppChannelHandler) so
// follower→leader proposal forwarding works out of the box. Tests that want
// custom handlers can pass false and call sl.SetAppHandler themselves.
//
// Callers are expected to Close() each node when done (or use t.Cleanup in
// tests via the test helper variant in node_tcp_test.go).
func NewTCPRaftCluster(n int, fsms []*FSM, appHandlers []AppChannelHandler) ([]*TCPNode, error) {
	if n <= 0 {
		return nil, xerrors.New("n must be > 0")
	}
	if len(fsms) != n {
		return nil, xerrors.Errorf("fsms must have len %d, got %d", n, len(fsms))
	}
	if len(appHandlers) != 0 && len(appHandlers) != n {
		return nil, xerrors.Errorf("appHandlers must be empty or have len %d, got %d", n, len(appHandlers))
	}

	// First pass: bind every listener so we know each peer's resolved
	// localhost:port address before constructing the rafts.
	listeners := make([]*MuxedStreamLayer, n)
	ids := make([]raft.ServerID, n)
	for i := 0; i < n; i++ {
		var h AppChannelHandler
		if len(appHandlers) == n {
			h = appHandlers[i]
		}
		sl, err := NewMuxedStreamLayer("127.0.0.1:0", nil, h)
		if err != nil {
			// Close everything we opened so far.
			for j := 0; j < i; j++ {
				_ = listeners[j].Close()
			}
			return nil, xerrors.Errorf("listener %d: %w", i, err)
		}
		listeners[i] = sl
		ids[i] = raft.ServerID(nodeID(i))
	}
	peers := make([]PeerInfo, n)
	for i := 0; i < n; i++ {
		peers[i] = PeerInfo{ID: ids[i], Address: raft.ServerAddress(listeners[i].Addr().String())}
	}

	// Second pass: wrap each listener with a raft NetworkTransport + Manager.
	nodes := make([]*TCPNode, n)
	for i := 0; i < n; i++ {
		trans := raft.NewNetworkTransport(listeners[i], 3, 2*time.Second, io.Discard)
		mgr, err := NewManager(ManagerConfig{
			NodeID:        ids[i],
			BindAddr:      peers[i].Address,
			Peers:         peers,
			Bootstrap:     i == 0,
			LogStore:      raft.NewInmemStore(),
			StableStore:   raft.NewInmemStore(),
			SnapshotStore: raft.NewInmemSnapshotStore(),
			Transport:     trans,
			FSM:           fsms[i],
		})
		if err != nil {
			// Tear down everything.
			for _, n := range nodes {
				if n != nil {
					_ = n.Close()
				}
			}
			for _, sl := range listeners {
				_ = sl.Close()
			}
			return nil, xerrors.Errorf("manager %d: %w", i, err)
		}
		nodes[i] = &TCPNode{
			ID:          ids[i],
			Addr:        listeners[i].Addr(),
			StreamLayer: listeners[i],
			Transport:   trans,
			FSM:         fsms[i],
			Manager:     mgr,
		}
	}
	return nodes, nil
}

// WireAppChannelForwarding sets each node's MuxedStreamLayer app handler to a
// makeAppChannelHandler bound to its raft.Raft. After this call, follower
// Forwarder.Submit can dial any node and have its proposal forwarded to the
// leader's raft.Apply.
func WireAppChannelForwarding(nodes []*TCPNode) {
	for _, n := range nodes {
		n.StreamLayer.SetAppHandler(makeAppChannelHandler(n.Manager.Raft()))
	}
}

// nodeID returns the deterministic id for cluster node i.
func nodeID(i int) string {
	switch i {
	case 0:
		return "node-0"
	case 1:
		return "node-1"
	case 2:
		return "node-2"
	case 3:
		return "node-3"
	case 4:
		return "node-4"
	default:
		// More than 5 isn't expected in PoC tests.
		return "node-x"
	}
}
