package haraft

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func clusterClient() client.Client {
	return fake.NewClientBuilder().WithScheme(testScheme).WithObjects(
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: InboxConfigMapName, Namespace: "ns"}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: StateConfigMapName, Namespace: "ns"}},
	).Build()
}

func startNode(t *testing.T, ctx context.Context, name string, peers []string, peerClients map[string]client.Client) *RaftNode {
	t.Helper()
	heartbeat, election, commit, lease, poll := DefaultTimings()
	cfg := NodeConfig{
		ClusterName: name, Peers: peers, Namespace: "ns",
		HeartbeatTimeout: heartbeat, ElectionTimeout: election, CommitTimeout: commit,
		LeaderLeaseTimeout: lease, PollInterval: poll,
	}
	n, err := NewRaftNode(cfg, peerClients)
	require.NoError(t, err)
	require.NoError(t, n.Start(ctx))
	return n
}

func waitForLeader(t *testing.T, nodes []*RaftNode, timeout time.Duration) *RaftNode {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, n := range nodes {
			if n.IsLeader() {
				return n
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no leader elected within %v", timeout)
	return nil
}

func TestCluster_ElectsLeader_3Nodes(t *testing.T) {
	peerClients := map[string]client.Client{
		"A": clusterClient(),
		"B": clusterClient(),
		"C": clusterClient(),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	peers := []string{"A", "B", "C"}
	a := startNode(t, ctx, "A", peers, peerClients)
	b := startNode(t, ctx, "B", peers, peerClients)
	c := startNode(t, ctx, "C", peers, peerClients)
	defer a.Stop()
	defer b.Stop()
	defer c.Stop()

	leader := waitForLeader(t, []*RaftNode{a, b, c}, 15*time.Second)
	t.Logf("initial leader: %s", leader.cfg.ClusterName)
}

func TestCluster_FailoverOnLeaderShutdown_3Nodes(t *testing.T) {
	peerClients := map[string]client.Client{
		"A": clusterClient(), "B": clusterClient(), "C": clusterClient(),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	peers := []string{"A", "B", "C"}
	a := startNode(t, ctx, "A", peers, peerClients)
	b := startNode(t, ctx, "B", peers, peerClients)
	c := startNode(t, ctx, "C", peers, peerClients)

	leader := waitForLeader(t, []*RaftNode{a, b, c}, 15*time.Second)
	t.Logf("initial leader: %s", leader.cfg.ClusterName)

	// Stop the leader; expect re-election from the survivors.
	leader.Stop()
	delete(peerClients, leader.cfg.ClusterName)

	survivors := []*RaftNode{}
	for _, n := range []*RaftNode{a, b, c} {
		if n != leader {
			survivors = append(survivors, n)
		}
	}
	newLeader := waitForLeader(t, survivors, 15*time.Second)
	require.NotEqual(t, leader.cfg.ClusterName, newLeader.cfg.ClusterName)
	t.Logf("new leader: %s", newLeader.cfg.ClusterName)

	for _, n := range survivors {
		n.Stop()
	}
}
