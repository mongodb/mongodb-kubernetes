package coordraft

import (
	"io"

	"github.com/hashicorp/raft"
)

// StubFSM is a no-op FSM used by C1's "do nodes elect a leader?" test and any
// other call site that doesn't need real state. C2 replaces this with the
// real FSM (see fsm_real.go).
type StubFSM struct{}

func (StubFSM) Apply(_ *raft.Log) interface{}    { return nil }
func (StubFSM) Snapshot() (raft.FSMSnapshot, error) { return stubSnapshot{}, nil }
func (StubFSM) Restore(rc io.ReadCloser) error {
	_ = rc.Close()
	return nil
}

type stubSnapshot struct{}

func (stubSnapshot) Persist(sink raft.SnapshotSink) error { return sink.Close() }
func (stubSnapshot) Release()                             {}
