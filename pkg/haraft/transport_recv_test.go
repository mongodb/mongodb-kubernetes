package haraft

import (
	"context"
	"testing"
	"time"

	"github.com/hashicorp/raft"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

// Inject an envelope into A's own inbox and assert it surfaces on Consumer().
func TestTransport_DeliversIncomingRPC(t *testing.T) {
	peerClients, a, _ := newClientsAB(t)
	tr := NewKubeTransport("A", "ns", peerClients, 50*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, tr.start(ctx))
	defer tr.Close()

	// Build an envelope that "B" supposedly sent to A.
	args := &raft.RequestVoteRequest{Term: 9, Candidate: []byte("B"), LastLogIndex: 0, LastLogTerm: 0}
	payload, err := encodeRPC(args)
	require.NoError(t, err)
	env := RaftEnvelope{MsgType: "RequestVote", From: "B", ReplyKey: "r-1", Payload: payload}
	encoded, err := encodeEnvelope(env)
	require.NoError(t, err)

	cm := &corev1.ConfigMap{}
	require.NoError(t, a.Get(ctx, types.NamespacedName{Name: InboxConfigMapName, Namespace: "ns"}, cm))
	cm.Data = map[string]string{"msg-1": encoded}
	require.NoError(t, a.Update(ctx, cm))

	select {
	case rpc := <-tr.Consumer():
		req, ok := rpc.Command.(*raft.RequestVoteRequest)
		require.True(t, ok)
		assert.Equal(t, uint64(9), req.Term)
		// Respond on the RPC respChan so the transport sends the reply back.
		rpc.Respond(&raft.RequestVoteResponse{Term: 9, Granted: true}, nil)
	case <-time.After(2 * time.Second):
		t.Fatal("RPC not delivered")
	}

	// After consumer responds, transport should write a reply envelope into
	// B's inbox keyed by replyKey "r-1".
	time.Sleep(200 * time.Millisecond)
	cmB := &corev1.ConfigMap{}
	_, b := peerClients["A"], peerClients["B"]
	_ = b
	require.NoError(t, peerClients["B"].Get(ctx, types.NamespacedName{Name: InboxConfigMapName, Namespace: "ns"}, cmB))
	assert.Len(t, cmB.Data, 1)
	for k := range cmB.Data {
		assert.Contains(t, k, "msg-")
	}
}
