package haraft

import (
	"testing"

	"github.com/hashicorp/raft"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncodeDecode_AppendEntries(t *testing.T) {
	req := &raft.AppendEntriesRequest{
		RPCHeader:    raft.RPCHeader{ProtocolVersion: raft.ProtocolVersionMax},
		Term:         5,
		Leader:       []byte("cluster-A"),
		PrevLogEntry: 3,
		PrevLogTerm:  4,
	}
	payload, err := encodeRPC(req)
	require.NoError(t, err)

	env := RaftEnvelope{
		MsgType:  "AppendEntries",
		From:     "cluster-A",
		ReplyKey: "abc",
		Payload:  payload,
	}
	raw, err := encodeEnvelope(env)
	require.NoError(t, err)

	got, err := decodeEnvelope(raw)
	require.NoError(t, err)
	assert.Equal(t, env, got)

	out := &raft.AppendEntriesRequest{}
	require.NoError(t, decodeRPC(got.Payload, out))
	assert.Equal(t, req.Term, out.Term)
	assert.Equal(t, req.PrevLogEntry, out.PrevLogEntry)
}
