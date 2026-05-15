package haraft

import (
	"context"
	"testing"
	"time"

	"github.com/hashicorp/raft"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newClientsAB(t *testing.T) (map[string]client.Client, client.Client, client.Client) {
	t.Helper()
	cmA := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: InboxConfigMapName, Namespace: "ns"}}
	cmB := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: InboxConfigMapName, Namespace: "ns"}}
	a := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(cmA).Build()
	b := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(cmB).Build()
	return map[string]client.Client{"A": a, "B": b}, a, b
}

func TestTransport_RequestVote_WritesToTargetInbox(t *testing.T) {
	peerClients, _, b := newClientsAB(t)

	tr := NewKubeTransport("A", "ns", peerClients, 200*time.Millisecond)
	require.NoError(t, tr.start(context.Background()))
	defer tr.Close()

	args := &raft.RequestVoteRequest{
		RPCHeader:    raft.RPCHeader{ProtocolVersion: raft.ProtocolVersionMax},
		Term:         7,
		Candidate:    []byte("A"),
		LastLogIndex: 1,
		LastLogTerm:  1,
	}
	resp := &raft.RequestVoteResponse{}

	// async call into transport; expect it to write to B's inbox; we don't
	// expect a reply here so cancel after a short wait.
	go func() { _ = tr.RequestVote(raft.ServerID("B"), raft.ServerAddress("B"), args, resp) }()
	time.Sleep(150 * time.Millisecond)

	got := &corev1.ConfigMap{}
	require.NoError(t, b.Get(context.Background(), types.NamespacedName{Name: InboxConfigMapName, Namespace: "ns"}, got))
	assert.Len(t, got.Data, 1, "expected exactly one envelope written to peer B's inbox")
	for _, v := range got.Data {
		env, err := decodeEnvelope(v)
		require.NoError(t, err)
		assert.Equal(t, "RequestVote", env.MsgType)
		assert.Equal(t, "A", env.From)
	}
}
