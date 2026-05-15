// Production helpers — build a real-TCP-backed Coordinator from a user-supplied
// config. Designed for main.go in distributed mode (D'1).
//
// The in-process test helpers (NewTCPRaftCluster) bind every listener on
// 127.0.0.1:0 (kernel-picked port). The production case is different: the
// operator process gets its bind address and peer list from CLI flags / env
// vars so the three operator processes co-located in the devc devcontainer
// know each other's addresses up front.
//
// Behaviour:
//   - One MuxedStreamLayer per operator (carries raft + app-channel).
//   - Raft NetworkTransport over the muxed layer.
//   - In-memory LogStore / StableStore / SnapshotStore (PoC; persistent
//     storage is post-PoC).
//   - Coordinator wired with a Forwarder so follower-side Propose* auto-routes
//     to whichever node currently believes itself to be the leader.
//   - SetClusterPeerMap populated from cfg.Peers (cluster-name → ServerID).
//   - App-channel handler set via WireAppChannelForwarding-equivalent so the
//     muxed listener serves both raft and forwarded proposals.

package coordraft

import (
	"io"
	"net"
	"strings"
	"time"

	"github.com/hashicorp/raft"
	"go.uber.org/zap"
	"golang.org/x/xerrors"
)

// ProductionConfig is what main.go passes to BuildProductionCoordinator.
//
// Invariants (validated):
//   - ClusterName is non-empty and matches one of Peers[i].ID.
//   - BindAddr is non-empty (host:port form).
//   - Peers has length ≥ 1 (single-node clusters are allowed for testing).
//   - Exactly one of the peers must have Bootstrap=true across the deployment
//     (this is a per-process configuration; BuildProductionCoordinator does
//     not enforce uniqueness, the operator launcher does).
type ProductionConfig struct {
	// ClusterName is this operator's cluster identity. Used as the raft
	// ServerID, as the coordinator's MyClusterName(), and as the key the
	// reconciler matches against during distGateInline.
	ClusterName string
	// BindAddr is what the raft StreamLayer listens on, e.g. "127.0.0.1:7001".
	// Raft peers will dial this address. Pure hashicorp/raft msgpack on the
	// wire (no handshake byte) — this address is what Istio sees as the
	// raft port.
	BindAddr string
	// AppBindAddr is where the proposal-forwarder TCP listener binds, e.g.
	// "127.0.0.1:7002". Leave empty to derive automatically from BindAddr
	// (host with port+1). Demuxing the app channel onto its own port is
	// required for Istio mesh deployments — the muxed 1-byte handshake byte
	// trips metadata_exchange's protocol-detection on inbound and the
	// listener resets the connection. See G'5 iter 7 handoff.
	AppBindAddr string
	// Peers is the full raft voter set. For 3 operators on one host this is:
	//   [{ID:"cluster-1",Address:"127.0.0.1:7001"},
	//    {ID:"cluster-2",Address:"127.0.0.1:7002"},
	//    {ID:"cluster-3",Address:"127.0.0.1:7003"}]
	// All operators in the cluster MUST supply the identical Peers slice.
	// The forwarder derives each peer's app-channel addr by adding 1 to its
	// raft port (AppPortFromRaftAddr); if your deployment uses an arbitrary
	// per-peer app port mapping, set ProductionNode.Forwarder.ResolveAppAddr
	// to a custom resolver before driving traffic.
	Peers []PeerInfo
	// Bootstrap, if true, this node will issue raft.BootstrapCluster on a fresh
	// state. Exactly one operator per cluster sets this true; the others wait
	// for the bootstrap node to elect itself leader and replicate the cluster
	// configuration. (In a real production deployment this is typically the
	// first operator; the PoC uses cluster-1.)
	Bootstrap bool
	// HeartbeatTimeout / ElectionTimeout / LeaderLeaseTimeout / CommitTimeout
	// are optional raft timing overrides. Leave zero for the hashicorp/raft
	// library defaults via ProductionRaftConfig — those defaults tolerate
	// cross-cluster RTT through an Istio mesh. Override only for tests that
	// want sub-second elections (the production_test.go three-node test sets
	// them all to ≤100ms to keep the suite fast; that's fine because it
	// listens on localhost).
	HeartbeatTimeout   time.Duration
	ElectionTimeout    time.Duration
	LeaderLeaseTimeout time.Duration
	CommitTimeout      time.Duration
}

// ProductionNode bundles all the moving parts so the caller can plumb the
// Coordinator into the reconciler and Close() the resources on shutdown.
type ProductionNode struct {
	Coordinator *Coordinator
	Manager     *Manager
	FSM         *FSM
	StreamLayer *MuxedStreamLayer
	Transport   raft.Transport
	Forwarder   *Forwarder
}

// Close shuts the raft node down and closes the listener. Safe to call once.
func (p *ProductionNode) Close() error {
	var firstErr error
	if p.Manager != nil {
		if err := p.Manager.Shutdown(); err != nil {
			firstErr = err
		}
	}
	if p.StreamLayer != nil {
		if err := p.StreamLayer.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// BuildProductionCoordinator constructs a Coordinator backed by a real TCP
// MuxedStreamLayer. The caller is responsible for shutting down the returned
// node via Close().
//
// The returned Coordinator is wired with:
//   - SetForwarder(NewForwarder(...))           — follower→leader proposal routing
//   - SetClusterPeerMap                          — name→ServerID for LastContact
//   - app-channel handler on the muxed listener  — receives forwarded proposals
//
// The caller must set the default CR via SetDefaultCR before driving any
// legacy single-CR API path. F5+ (CRKey-aware) call sites carry CRKey per-call
// and don't need SetDefaultCR.
func BuildProductionCoordinator(cfg ProductionConfig) (*ProductionNode, error) {
	if cfg.ClusterName == "" {
		return nil, xerrors.New("ClusterName required")
	}
	if cfg.BindAddr == "" {
		return nil, xerrors.New("BindAddr required")
	}
	if len(cfg.Peers) == 0 {
		return nil, xerrors.New("Peers must be non-empty")
	}
	// Validate ClusterName appears in Peers.
	var selfFound bool
	for _, p := range cfg.Peers {
		if string(p.ID) == cfg.ClusterName {
			selfFound = true
			break
		}
	}
	if !selfFound {
		return nil, xerrors.Errorf("ClusterName %q not present in Peers", cfg.ClusterName)
	}

	// Soft warn: peer entries with IP literals are a smell when the bind
	// addr looks like a wildcard, because the kind/Istio mesh resolves
	// FQDNs via cross-cluster Service endpoint discovery — IPs won't.
	// Tests + local-mode launcher use 127.0.0.1:N peers paired with
	// 127.0.0.1:N binds, which is fine (advertise == bind == loopback).
	// Don't error — the validation is purely advisory.
	if isWildcardBindAddr(cfg.BindAddr) {
		log := zap.S()
		for _, p := range cfg.Peers {
			host, _, splitErr := net.SplitHostPort(string(p.Address))
			if splitErr != nil {
				continue
			}
			if net.ParseIP(host) != nil {
				log.Warnw("coordraft: peer entry uses IP literal with wildcard bind — use FQDN for cross-cluster reachability",
					"peer_id", string(p.ID), "peer_addr", string(p.Address), "bind_addr", cfg.BindAddr,
				)
			}
		}
	}

	fsm := NewFSM()

	// Derive the app-channel bind address: use cfg.AppBindAddr verbatim
	// when set, else port+1 on cfg.BindAddr.
	appBindAddr := cfg.AppBindAddr
	if appBindAddr == "" {
		derived, derr := AppPortFromRaftAddr(cfg.BindAddr)
		if derr != nil {
			return nil, xerrors.Errorf("derive AppBindAddr from BindAddr=%q: %w", cfg.BindAddr, derr)
		}
		appBindAddr = derived
	}

	// Find this operator's own peer entry so we can advertise its FQDN as
	// the raft address. Without an explicit advertise, the raft library
	// hands out the listener's resolved bind addr — which is the wildcard
	// "[::]:7000" when cfg.BindAddr is "0.0.0.0:7000". Followers that
	// receive that wildcard via AppendEntries / LeaderWithID() can't dial
	// it (it resolves to their own localhost), so the proposal forwarder
	// self-loops. See G iter 11 (workaround in Forwarder.PeerAddrs) and
	// iter 12 (this clean fix) handoff.
	//
	// selfPeerAddr is empty for callers who use a name that's not in Peers
	// (impossible — validated above) or for test callers that constructed
	// Peers with a name-different-from-listener. In practice all production
	// configurations match: self entry exists, advertise = FQDN.
	var selfPeerAddr string
	for _, p := range cfg.Peers {
		if string(p.ID) == cfg.ClusterName {
			selfPeerAddr = string(p.Address)
			break
		}
	}

	// Bind the raft listener with an advertise addr equal to this peer's
	// own FQDN entry. The underlying listener still binds cfg.BindAddr
	// (typically "0.0.0.0:7000") so it accepts inbound connections on all
	// interfaces — only the advertised addr (the one raft hands to peers)
	// is the FQDN. When the self entry's addr exactly matches the bind
	// addr (local-mode unit tests that bind 127.0.0.1:N and supply
	// 127.0.0.1:N in Peers), advertise is functionally identical to the
	// listener's resolved addr — preserves pre-iter-12 semantics.
	var advertise net.Addr
	if selfPeerAddr != "" {
		advertise = NewStringAddr(selfPeerAddr)
	}
	sl, err := NewMuxedStreamLayer(cfg.BindAddr, advertise, nil)
	if err != nil {
		return nil, xerrors.Errorf("listen %s: %w", cfg.BindAddr, err)
	}

	// Wrap with raft.NetworkTransport. The advertise addr is the listener's
	// resolved address; if the user supplied "127.0.0.1:0" (rare in
	// production but valid in tests), the kernel-picked port is what peers
	// will dial.
	trans := raft.NewNetworkTransport(sl, 3, 2*time.Second, io.Discard)

	mgr, err := NewManager(ManagerConfig{
		NodeID:             raft.ServerID(cfg.ClusterName),
		BindAddr:           raft.ServerAddress(sl.Addr().String()),
		Peers:              cfg.Peers,
		Bootstrap:          cfg.Bootstrap,
		LogStore:           raft.NewInmemStore(),
		StableStore:        raft.NewInmemStore(),
		SnapshotStore:      raft.NewInmemSnapshotStore(),
		Transport:          trans,
		FSM:                fsm,
		HeartbeatTimeout:   cfg.HeartbeatTimeout,
		ElectionTimeout:    cfg.ElectionTimeout,
		LeaderLeaseTimeout: cfg.LeaderLeaseTimeout,
		CommitTimeout:      cfg.CommitTimeout,
		Production:         true,
	})
	if err != nil {
		_ = sl.Close()
		return nil, xerrors.Errorf("raft manager: %w", err)
	}

	// Bind the second TCP listener for the proposal forwarder. The handler
	// routes accepted conns to this node's raft.Apply when it's leader, or
	// returns ErrNotLeader so the follower retries against the new leader.
	if err := sl.BindAppListener(appBindAddr, makeAppChannelHandler(mgr.Raft())); err != nil {
		_ = mgr.Shutdown()
		_ = sl.Close()
		return nil, xerrors.Errorf("bind app listener %s: %w", appBindAddr, err)
	}

	// Build Coordinator + Forwarder.
	coord := NewCoordinator(cfg.ClusterName, mgr, fsm)
	fw := NewForwarder(mgr, sl)
	// Plumb the configured peer addresses into the Forwarder so it can
	// resolve a leader's ServerID to a routable host:port even when the
	// leader's self-advertised raft addr is a wildcard bind addr
	// ([::]:7000). See G iter 11 handoff for context.
	peerAddrs := make(map[raft.ServerID]string, len(cfg.Peers))
	for _, p := range cfg.Peers {
		peerAddrs[p.ID] = string(p.Address)
	}
	fw.PeerAddrs = peerAddrs
	coord.SetForwarder(fw)
	peerMap := make(map[string]raft.ServerID, len(cfg.Peers))
	for _, p := range cfg.Peers {
		peerMap[string(p.ID)] = p.ID
	}
	coord.SetClusterPeerMap(peerMap)

	return &ProductionNode{
		Coordinator: coord,
		Manager:     mgr,
		FSM:         fsm,
		StreamLayer: sl,
		Transport:   trans,
		Forwarder:   fw,
	}, nil
}

// isWildcardBindAddr reports whether bindAddr is one of the conventional
// wildcards ("0.0.0.0:<port>" or "[::]:<port>" or ":<port>"). Used only
// by the soft warning above — never enforced.
func isWildcardBindAddr(bindAddr string) bool {
	host, _, err := net.SplitHostPort(bindAddr)
	if err != nil {
		return false
	}
	switch host {
	case "", "0.0.0.0", "::":
		return true
	}
	return false
}

// ParsePeers parses a comma-separated peer list of the form
//
//	"cluster-1=127.0.0.1:7001,cluster-2=127.0.0.1:7002,cluster-3=127.0.0.1:7003"
//
// into a []PeerInfo. Whitespace around entries and around the `=` is tolerated.
// Empty input returns (nil, nil). Malformed entries (no `=`, empty name/addr)
// return an error.
func ParsePeers(s string) ([]PeerInfo, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	var out []PeerInfo
	for _, entry := range strings.Split(s, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		eq := strings.IndexByte(entry, '=')
		if eq <= 0 || eq == len(entry)-1 {
			return nil, xerrors.Errorf("peer %q: must be of the form name=host:port", entry)
		}
		name := strings.TrimSpace(entry[:eq])
		addr := strings.TrimSpace(entry[eq+1:])
		if name == "" || addr == "" {
			return nil, xerrors.Errorf("peer %q: empty name or addr", entry)
		}
		// Light validation that the addr looks like host:port (no further
		// resolution — that's deferred to dial time).
		if _, _, err := net.SplitHostPort(addr); err != nil {
			return nil, xerrors.Errorf("peer %q: bad host:port: %w", entry, err)
		}
		out = append(out, PeerInfo{ID: raft.ServerID(name), Address: raft.ServerAddress(addr)})
	}
	if len(out) == 0 {
		return nil, xerrors.New("no peers parsed from input")
	}
	return out, nil
}
