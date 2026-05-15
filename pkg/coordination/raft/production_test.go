package coordraft

import (
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/raft"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mongodb/mongodb-kubernetes/pkg/coordination"
)

// pickFreeTCPPorts returns n loopback TCP ports that were free at the moment
// of the call. There's an inherent race between Close()/bind in tests; the
// helper is fine for non-flaky CI as long as the caller binds immediately.
func pickFreeTCPPorts(t *testing.T, n int) []int {
	t.Helper()
	ls := make([]net.Listener, n)
	ports := make([]int, n)
	for i := 0; i < n; i++ {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		ls[i] = l
		_, p, err := net.SplitHostPort(l.Addr().String())
		require.NoError(t, err)
		pi, err := strconv.Atoi(p)
		require.NoError(t, err)
		ports[i] = pi
	}
	for _, l := range ls {
		_ = l.Close()
	}
	return ports
}

// pickFreeTCPPortPairs returns n pairs of contiguous loopback TCP ports
// (each pair is `port, port+1`) that were free at the moment of the call.
// Used by tests that need a raft port plus a port+1 app-channel port per
// node without colliding across nodes.
func pickFreeTCPPortPairs(t *testing.T, n int) []int {
	t.Helper()
	ports := make([]int, 0, n)
	tried := map[int]bool{}
	deadline := time.Now().Add(5 * time.Second)
	for len(ports) < n && time.Now().Before(deadline) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		_, ps, err := net.SplitHostPort(l.Addr().String())
		require.NoError(t, err)
		p, err := strconv.Atoi(ps)
		require.NoError(t, err)
		_ = l.Close()
		if tried[p] || tried[p+1] || tried[p-1] {
			continue
		}
		tried[p] = true
		// Probe the +1 port: bind, close. If it works, we accept the pair.
		l2, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p+1))
		if err != nil {
			continue
		}
		_ = l2.Close()
		tried[p+1] = true
		ports = append(ports, p)
	}
	require.Len(t, ports, n, "could not pick %d port pairs", n)
	return ports
}

// TestBuildProductionCoordinator_ThreeNodeCluster mirrors what main.go does
// in distributed mode: it constructs three ProductionNodes from
// independently-prepared ProductionConfigs (one with Bootstrap=true, two
// without), waits for a leader, and verifies the coordinator's Propose* path
// works end-to-end (follower → app channel → leader → Apply → FSM commit).
//
// This is the unit-test proof requested by D'1 ("raft cluster forms in-process
// across 3 invocations"). Each invocation is independent (no shared state in
// the helper); the only coupling is the Peers list each gets handed.
func TestBuildProductionCoordinator_ThreeNodeCluster(t *testing.T) {
	// Each node needs a contiguous (raftPort, raftPort+1) pair because the
	// production code derives the app-channel listener from BindAddr by
	// adding 1 to the port. pickFreeTCPPortPairs guarantees the +1 port is
	// also free and that no two pairs overlap.
	ports := pickFreeTCPPortPairs(t, 3)
	names := []string{"cluster-1", "cluster-2", "cluster-3"}
	peers := []PeerInfo{}
	for i, name := range names {
		peers = append(peers, PeerInfo{
			ID:      raft.ServerID(name),
			Address: raft.ServerAddress("127.0.0.1:" + strconv.Itoa(ports[i])),
		})
	}

	nodes := make([]*ProductionNode, 3)
	for i, name := range names {
		cfg := ProductionConfig{
			ClusterName: name,
			BindAddr:    "127.0.0.1:" + strconv.Itoa(ports[i]),
			Peers:       peers,
			Bootstrap:   i == 0,
			// Tight timings so the test runs in <5s.
			HeartbeatTimeout:   50 * time.Millisecond,
			ElectionTimeout:    100 * time.Millisecond,
			LeaderLeaseTimeout: 50 * time.Millisecond,
			CommitTimeout:      10 * time.Millisecond,
		}
		n, err := BuildProductionCoordinator(cfg)
		require.NoError(t, err, "BuildProductionCoordinator for %s", name)
		nodes[i] = n
	}
	t.Cleanup(func() {
		for _, n := range nodes {
			_ = n.Close()
		}
	})

	// Wait for a leader to emerge.
	leaderIdx := -1
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for i, n := range nodes {
			if n.Manager.IsLeader() {
				leaderIdx = i
				break
			}
		}
		if leaderIdx >= 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.GreaterOrEqual(t, leaderIdx, 0, "no leader elected within 5s")

	// All nodes should agree on who the leader is.
	leaderAddr := nodes[leaderIdx].StreamLayer.Addr().String()
	for i, n := range nodes {
		// LeaderAddr returns "" briefly during transitions; poll.
		var addr raft.ServerAddress
		d := time.Now().Add(2 * time.Second)
		for time.Now().Before(d) {
			addr = n.Manager.LeaderAddr()
			if string(addr) != "" {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		assert.Equal(t, leaderAddr, string(addr), "node %s should see leader at %s", names[i], leaderAddr)
	}

	// End-to-end proposal: pick a follower, make it propose via the
	// coordinator, verify the leader's FSM observes the change. This
	// exercises the muxed app-channel path (follower → leader Apply).
	followerIdx := (leaderIdx + 1) % 3
	follower := nodes[followerIdx].Coordinator
	leader := nodes[leaderIdx].Coordinator

	crk := coordination.CRKey{Kind: "MongoDB", Namespace: "ns", Name: "cr"}
	follower.SetDefaultCR(toRaftCRKey(crk))
	leader.SetDefaultCR(toRaftCRKey(crk))

	res := follower.AcquireOrRespect(crk, "config", names[followerIdx], 0)
	assert.Equal(t, coordination.LeaseHeld, res, "follower should acquire lease via forwarder")

	// Leader's FSM should reflect the lease (give it a heartbeat to replicate
	// — followers apply async).
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		l := leader.GetActiveLease()
		if l != nil && l.Component == "config" && l.ClusterName == names[followerIdx] {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("leader's FSM never observed the lease allocated by follower %s", names[followerIdx])
}

// TestParsePeers covers the comma-separated flag parser.
func TestParsePeers(t *testing.T) {
	cases := []struct {
		in       string
		wantErr  bool
		wantIDs  []string
		wantAddr []string
	}{
		{
			in:       "cluster-1=127.0.0.1:7001,cluster-2=127.0.0.1:7002,cluster-3=127.0.0.1:7003",
			wantIDs:  []string{"cluster-1", "cluster-2", "cluster-3"},
			wantAddr: []string{"127.0.0.1:7001", "127.0.0.1:7002", "127.0.0.1:7003"},
		},
		{
			// Whitespace tolerance.
			in:       " cluster-1 = 127.0.0.1:7001 , cluster-2=127.0.0.1:7002 ",
			wantIDs:  []string{"cluster-1", "cluster-2"},
			wantAddr: []string{"127.0.0.1:7001", "127.0.0.1:7002"},
		},
		{
			// Empty input: explicit "no peers" — caller should treat as
			// non-distributed mode.
			in:      "",
			wantIDs: nil,
		},
		{
			in:      "missingequal",
			wantErr: true,
		},
		{
			in:      "name=",
			wantErr: true,
		},
		{
			in:      "=addr:1",
			wantErr: true,
		},
		{
			in:      "name=not-a-host-port",
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.in, "=", "_"), func(t *testing.T) {
			peers, err := ParsePeers(tc.in)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Len(t, peers, len(tc.wantIDs))
			for i, want := range tc.wantIDs {
				assert.Equal(t, want, string(peers[i].ID))
				assert.Equal(t, tc.wantAddr[i], string(peers[i].Address))
			}
		})
	}
}

// TestBuildProductionCoordinator_AdvertisesSelfFQDN proves that the
// constructed Manager's BindAddr (which is what raft hands to peers via
// LeaderWithID()) equals the FQDN from the self entry in cfg.Peers, NOT
// the listener's resolved bind addr. This is the iter 12 design fix that
// makes iter 11's PeerAddrs workaround a no-op safety net.
//
// We use a real listener bound on 127.0.0.1:0 (kernel-picked port) and a
// peer-list entry with an FQDN-looking host. After BuildProduction-
// Coordinator returns, the manager's BindAddr should be the FQDN from
// cfg.Peers — not the 127.0.0.1:N the kernel picked.
func TestBuildProductionCoordinator_AdvertisesSelfFQDN(t *testing.T) {
	// We need a real port for the *self* entry because the manager binds
	// on cfg.BindAddr; the advertise is purely informational from the OS
	// layer's perspective. We just need a free port pair for the listener.
	ports := pickFreeTCPPortPairs(t, 1)
	bindAddr := "127.0.0.1:" + strconv.Itoa(ports[0])

	// FQDN-shaped peer entries. The self peer uses a host:port that is
	// deliberately different from bindAddr so we can prove the advertise
	// follows the peer entry, not the listener.
	const selfFQDN = "operator.cluster-1.svc.cluster.local:7000"
	peers := []PeerInfo{
		{ID: "cluster-1", Address: raft.ServerAddress(selfFQDN)},
		{ID: "cluster-2", Address: "operator.cluster-2.svc.cluster.local:7000"},
		{ID: "cluster-3", Address: "operator.cluster-3.svc.cluster.local:7000"},
	}
	cfg := ProductionConfig{
		ClusterName:        "cluster-1",
		BindAddr:           bindAddr,
		Peers:              peers,
		Bootstrap:          false, // don't actually bootstrap — single-node won't elect, that's fine; we only inspect Manager.cfg.BindAddr
		HeartbeatTimeout:   50 * time.Millisecond,
		ElectionTimeout:    100 * time.Millisecond,
		LeaderLeaseTimeout: 50 * time.Millisecond,
		CommitTimeout:      10 * time.Millisecond,
	}
	node, err := BuildProductionCoordinator(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = node.Close() })

	// Manager.cfg.BindAddr is set from sl.Addr().String() in
	// BuildProductionCoordinator. With the iter 12 advertise wire-up that
	// MUST be the self peer's FQDN, not the kernel-picked loopback port.
	assert.Equal(t, raft.ServerAddress(selfFQDN), node.Manager.cfg.BindAddr,
		"Manager.BindAddr should advertise the self peer's FQDN from cfg.Peers")
	// And StreamLayer.Addr() should also return the FQDN (this is what
	// raft passes to peers via AppendEntries).
	assert.Equal(t, selfFQDN, node.StreamLayer.Addr().String(),
		"StreamLayer.Addr() should return the FQDN advertise")
}

func TestBuildProductionCoordinator_ValidationErrors(t *testing.T) {
	peers := []PeerInfo{{ID: "cluster-1", Address: "127.0.0.1:7001"}}
	cases := []struct {
		name   string
		mutate func(*ProductionConfig)
	}{
		{"empty cluster name", func(c *ProductionConfig) { c.ClusterName = "" }},
		{"empty bind addr", func(c *ProductionConfig) { c.BindAddr = "" }},
		{"no peers", func(c *ProductionConfig) { c.Peers = nil }},
		{"cluster name not in peers", func(c *ProductionConfig) { c.ClusterName = "elsewhere" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := ProductionConfig{
				ClusterName: "cluster-1",
				BindAddr:    "127.0.0.1:0",
				Peers:       peers,
				Bootstrap:   true,
			}
			tc.mutate(&cfg)
			_, err := BuildProductionCoordinator(cfg)
			assert.Error(t, err)
		})
	}
}
