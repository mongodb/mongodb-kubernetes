package coordraft

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/hashicorp/raft"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// crA is the single-CR test fixture used by FSM unit tests. F1+ tests construct
// CRKey explicitly; single-CR cases use this constant.
var crA = CRKey{Kind: "MongoDB", Namespace: "ns1", Name: "sharded-a"}

// applyHelper invokes FSM.Apply with the given encoded proposal at index 1.
func applyHelper(t *testing.T, f *FSM, typ ProposalType, payload interface{}) interface{} {
	t.Helper()
	data, err := EncodeProposal(typ, payload)
	require.NoError(t, err)
	return f.Apply(&raft.Log{Index: 1, Data: data})
}

func TestApplySpecUpdate_BumpsGeneration(t *testing.T) {
	f := NewFSM()
	r := applyHelper(t, f, ProposalSpecUpdate, SpecUpdatePayload{
		CRKey: crA, Generation: 1, Hash: "h1", Content: json.RawMessage(`{"x":1}`),
	})
	require.Nil(t, r)
	cr := f.GetPerCR(crA)
	require.NotNil(t, cr.AgreedSpec)
	assert.Equal(t, int64(1), cr.AgreedSpec.Generation)
	assert.Equal(t, "h1", cr.AgreedSpec.Hash)

	// Newer generation replaces.
	applyHelper(t, f, ProposalSpecUpdate, SpecUpdatePayload{CRKey: crA, Generation: 2, Hash: "h2"})
	assert.Equal(t, int64(2), f.GetPerCR(crA).AgreedSpec.Generation)

	// Older generation is ignored (replay safety).
	applyHelper(t, f, ProposalSpecUpdate, SpecUpdatePayload{CRKey: crA, Generation: 1, Hash: "h1"})
	assert.Equal(t, int64(2), f.GetPerCR(crA).AgreedSpec.Generation)
}

func TestApplyStatusReport_MergesComponentStatus(t *testing.T) {
	f := NewFSM()
	applyHelper(t, f, ProposalStatusReport, StatusReportPayload{
		CRKey:            crA,
		ClusterName:      "cluster-a",
		ObservedSpecHash: "h1",
		ComponentStatus: map[string]ComponentStatusEntry{
			"shard-0": {Generation: 1, Ready: false},
		},
		ReportedAt: time.Now().UTC(),
	})
	assert.Equal(t, "h1", f.GetClusterStatus(crA, "cluster-a").ObservedSpecHash)
	assert.False(t, f.GetClusterStatus(crA, "cluster-a").ComponentStatus["shard-0"].Ready)

	// Partial subsequent report overwrites scalar fields and merges component status.
	applyHelper(t, f, ProposalStatusReport, StatusReportPayload{
		CRKey:            crA,
		ClusterName:      "cluster-a",
		ObservedSpecHash: "h2",
		ComponentStatus:  map[string]ComponentStatusEntry{"shard-0": {Generation: 2, Ready: true}},
		ReportedAt:       time.Now().UTC(),
	})
	cs := f.GetClusterStatus(crA, "cluster-a")
	assert.Equal(t, "h2", cs.ObservedSpecHash)
	assert.True(t, cs.ComponentStatus["shard-0"].Ready)

	// Partial report mentioning ONLY "config" must not wipe "shard-0":
	applyHelper(t, f, ProposalStatusReport, StatusReportPayload{
		CRKey:            crA,
		ClusterName:      "cluster-a",
		ObservedSpecHash: "h2",
		ComponentStatus:  map[string]ComponentStatusEntry{"config": {Generation: 1, Ready: true}},
		ReportedAt:       time.Now().UTC(),
	})
	cs = f.GetClusterStatus(crA, "cluster-a")
	assert.True(t, cs.ComponentStatus["config"].Ready, "config Ready must be set")
	assert.True(t, cs.ComponentStatus["shard-0"].Ready, "earlier shard-0 Ready must NOT be wiped")

	// Different cluster lives alongside.
	applyHelper(t, f, ProposalStatusReport, StatusReportPayload{
		CRKey:           crA,
		ClusterName:     "cluster-b",
		ReportedAt:      time.Now().UTC(),
		ComponentStatus: map[string]ComponentStatusEntry{},
	})
	assert.Equal(t, 2, len(f.GetPerCR(crA).PerClusterStatus))
}

func TestApplyStatusReport_HeartbeatsLease(t *testing.T) {
	f := NewFSM()
	// Allocate a lease.
	applyHelper(t, f, ProposalLeaseAllocate, LeaseAllocatePayload{
		CRKey: crA, Component: "shard-0", ClusterName: "cluster-a", TTL: 30 * time.Second,
	})
	initial := f.GetActiveLease(crA).HeartbeatAt
	time.Sleep(5 * time.Millisecond)

	// StatusReport from the holder bumps HeartbeatAt.
	t1 := time.Now().UTC().Add(50 * time.Millisecond)
	applyHelper(t, f, ProposalStatusReport, StatusReportPayload{
		CRKey:       crA,
		ClusterName: "cluster-a",
		ReportedAt:  t1,
	})
	assert.True(t, f.GetActiveLease(crA).HeartbeatAt.Equal(t1), "lease HeartbeatAt should be refreshed by holder's report")
	assert.True(t, f.GetActiveLease(crA).HeartbeatAt.After(initial))

	// StatusReport from a non-holder does NOT bump HeartbeatAt.
	current := f.GetActiveLease(crA).HeartbeatAt
	t2 := t1.Add(100 * time.Millisecond)
	applyHelper(t, f, ProposalStatusReport, StatusReportPayload{
		CRKey:       crA,
		ClusterName: "cluster-b",
		ReportedAt:  t2,
	})
	assert.True(t, f.GetActiveLease(crA).HeartbeatAt.Equal(current), "non-holder report must not heartbeat lease")
}

func TestApplyLeaseAllocate_AndComplete(t *testing.T) {
	f := NewFSM()
	r := applyHelper(t, f, ProposalLeaseAllocate, LeaseAllocatePayload{
		CRKey: crA, Component: "shard-0", ClusterName: "cluster-a", TTL: 30 * time.Second,
	})
	lease, _ := r.(*Lease)
	require.NotNil(t, lease)
	assert.Equal(t, "shard-0", lease.Component)
	assert.Equal(t, "cluster-a", lease.ClusterName)
	assert.WithinDuration(t, time.Now().UTC().Add(30*time.Second), lease.ExpiresAt, 2*time.Second)
	assert.False(t, lease.AllocatedAt.IsZero())
	assert.False(t, lease.HeartbeatAt.IsZero())
	assert.False(t, lease.DeadlineAt.IsZero())

	// Allocating while one is held returns the existing lease (no overwrite).
	r2 := applyHelper(t, f, ProposalLeaseAllocate, LeaseAllocatePayload{
		CRKey: crA, Component: "config", ClusterName: "cluster-b",
	})
	lease2, _ := r2.(*Lease)
	require.NotNil(t, lease2)
	assert.Equal(t, "shard-0", lease2.Component, "existing lease should be returned, not overwritten")

	// Complete with non-matching pair: lease unchanged.
	applyHelper(t, f, ProposalLeaseComplete, LeaseCompletePayload{
		CRKey: crA, Component: "config", ClusterName: "cluster-b",
	})
	require.NotNil(t, f.GetActiveLease(crA))

	// Complete with matching pair: lease cleared.
	applyHelper(t, f, ProposalLeaseComplete, LeaseCompletePayload{
		CRKey: crA, Component: "shard-0", ClusterName: "cluster-a",
	})
	require.Nil(t, f.GetActiveLease(crA))
}

func TestApplyLeaseExpire(t *testing.T) {
	f := NewFSM()
	applyHelper(t, f, ProposalLeaseAllocate, LeaseAllocatePayload{
		CRKey: crA, Component: "config", ClusterName: "cluster-a",
	})
	require.NotNil(t, f.GetActiveLease(crA))

	// Non-matching expire: lease unchanged.
	applyHelper(t, f, ProposalLeaseExpire, LeaseExpirePayload{
		CRKey: crA, Component: "shard-0", ClusterName: "cluster-z", Reason: "stuck",
	})
	require.NotNil(t, f.GetActiveLease(crA))

	// Matching expire: lease cleared.
	res := applyHelper(t, f, ProposalLeaseExpire, LeaseExpirePayload{
		CRKey: crA, Component: "config", ClusterName: "cluster-a", Reason: "heartbeat-ttl",
	})
	require.Nil(t, f.GetActiveLease(crA))
	assert.Equal(t, "heartbeat-ttl", res)
}

func TestApplyClusterIndexAssign_StableAndUnique(t *testing.T) {
	f := NewFSM()
	r := applyHelper(t, f, ProposalClusterIndexAssign, ClusterIndexAssignPayload{ClusterName: "cluster-a", Index: 0})
	assert.Equal(t, 0, r.(int))
	// Re-assign attempts are no-ops (stable assignment).
	r2 := applyHelper(t, f, ProposalClusterIndexAssign, ClusterIndexAssignPayload{ClusterName: "cluster-a", Index: 99})
	assert.Equal(t, 0, r2.(int), "existing assignment must be preserved")
	assert.Equal(t, 0, f.GetClusterIndex("cluster-a"))
	assert.Equal(t, -1, f.GetClusterIndex("never-seen"))
}

func TestApplyACPublished_Monotonic(t *testing.T) {
	f := NewFSM()
	applyHelper(t, f, ProposalACPublished, ACPublishedPayload{CRKey: crA, Generation: 5})
	assert.Equal(t, 5, f.GetACGeneration(crA))
	// Lower generation ignored.
	applyHelper(t, f, ProposalACPublished, ACPublishedPayload{CRKey: crA, Generation: 3})
	assert.Equal(t, 5, f.GetACGeneration(crA))
	// Higher accepted.
	applyHelper(t, f, ProposalACPublished, ACPublishedPayload{CRKey: crA, Generation: 7})
	assert.Equal(t, 7, f.GetACGeneration(crA))
}

func TestApplyCRDelete_RemovesPerCR(t *testing.T) {
	f := NewFSM()
	applyHelper(t, f, ProposalSpecUpdate, SpecUpdatePayload{CRKey: crA, Generation: 1, Hash: "h"})
	applyHelper(t, f, ProposalLeaseAllocate, LeaseAllocatePayload{CRKey: crA, Component: "x", ClusterName: "y"})
	require.NotNil(t, f.GetActiveLease(crA))

	applyHelper(t, f, ProposalCRDelete, CRDeletePayload{CRKey: crA})
	assert.Nil(t, f.GetActiveLease(crA))
	assert.Nil(t, f.GetPerCR(crA).AgreedSpec)
}

// TestMultipleCRsIndependent verifies the FSM partitions state per CRKey: a
// lease on one CR doesn't block another.
func TestMultipleCRsIndependent(t *testing.T) {
	f := NewFSM()
	crB := CRKey{Kind: "MongoDB", Namespace: "ns1", Name: "sharded-b"}

	applyHelper(t, f, ProposalLeaseAllocate, LeaseAllocatePayload{CRKey: crA, Component: "config", ClusterName: "ca"})
	applyHelper(t, f, ProposalLeaseAllocate, LeaseAllocatePayload{CRKey: crB, Component: "config", ClusterName: "cb"})

	la := f.GetActiveLease(crA)
	lb := f.GetActiveLease(crB)
	require.NotNil(t, la)
	require.NotNil(t, lb)
	assert.Equal(t, "ca", la.ClusterName)
	assert.Equal(t, "cb", lb.ClusterName)
}

func TestSnapshotRestore_PreservesAllFields(t *testing.T) {
	src := NewFSM()
	applyHelper(t, src, ProposalSpecUpdate, SpecUpdatePayload{CRKey: crA, Generation: 7, Hash: "hh", Content: json.RawMessage(`{"a":1}`)})
	applyHelper(t, src, ProposalStatusReport, StatusReportPayload{
		CRKey: crA, ClusterName: "cluster-a", ObservedSpecHash: "hh",
		ComponentStatus: map[string]ComponentStatusEntry{"config": {Generation: 1, Ready: true}},
		ReportedAt:      time.Now().UTC(),
	})
	applyHelper(t, src, ProposalLeaseAllocate, LeaseAllocatePayload{CRKey: crA, Component: "config", ClusterName: "cluster-a", TTL: 10 * time.Second})
	applyHelper(t, src, ProposalClusterIndexAssign, ClusterIndexAssignPayload{ClusterName: "cluster-a", Index: 0})
	applyHelper(t, src, ProposalClusterIndexAssign, ClusterIndexAssignPayload{ClusterName: "cluster-b", Index: 1})
	applyHelper(t, src, ProposalACPublished, ACPublishedPayload{CRKey: crA, Generation: 9})

	// Persist into a buffer using the FSMSnapshot interface, then restore on a
	// fresh FSM.
	snap, err := src.Snapshot()
	require.NoError(t, err)
	var buf bytes.Buffer
	require.NoError(t, snap.Persist(&fakeSink{buf: &buf}))

	dst := NewFSM()
	require.NoError(t, dst.Restore(io.NopCloser(&buf)))

	srcState := src.GetState()
	dstState := dst.GetState()

	// Compare via JSON to avoid time.Time monotonic-clock comparison surprises.
	srcJSON, _ := json.Marshal(srcState)
	dstJSON, _ := json.Marshal(dstState)
	assert.JSONEq(t, string(srcJSON), string(dstJSON))
}

// fakeSink is a minimal raft.SnapshotSink for use in tests.
type fakeSink struct {
	buf *bytes.Buffer
	id  string
}

func (s *fakeSink) Write(p []byte) (int, error) { return s.buf.Write(p) }
func (s *fakeSink) Close() error                { return nil }
func (s *fakeSink) ID() string                  { return s.id }
func (s *fakeSink) Cancel() error               { return nil }
