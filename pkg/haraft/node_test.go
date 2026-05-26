package haraft

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestRaftNode_SingleNodeBecomesLeader(t *testing.T) {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: InboxConfigMapName, Namespace: "ns"}}
	state := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: StateConfigMapName, Namespace: "ns"}}
	c := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(cm, state).Build()

	cfg := NodeConfig{
		ClusterName:        "A",
		Peers:              []string{"A"},
		Namespace:          "ns",
		HeartbeatTimeout:   200 * time.Millisecond,
		ElectionTimeout:    300 * time.Millisecond,
		CommitTimeout:      50 * time.Millisecond,
		LeaderLeaseTimeout: 200 * time.Millisecond,
		PollInterval:       50 * time.Millisecond,
	}
	node, err := NewRaftNode(cfg, map[string]client.Client{"A": c})
	require.NoError(t, err)

	leaderCalled := make(chan struct{}, 1)
	node.OnLeadershipChange(func(isLeader bool) {
		if isLeader {
			leaderCalled <- struct{}{}
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, node.Start(ctx))
	defer node.Stop()

	select {
	case <-leaderCalled:
		assert.True(t, node.IsLeader())
	case <-ctx.Done():
		t.Fatal("single-node cluster did not become leader within 5s")
	}
}

func TestRaftNode_LocalID_ReturnsConfiguredClusterName(t *testing.T) {
	cfg := NodeConfig{ClusterName: "cluster-A"}
	n := &RaftNode{cfg: cfg}
	assert.Equal(t, "cluster-A", n.LocalID())
}
