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
		Generation: 1, Hash: "h1", Content: json.RawMessage(`{"x":1}`),
	})
	require.Nil(t, r)
	s := f.GetState()
	require.NotNil(t, s.AgreedSpec)
	assert.Equal(t, int64(1), s.AgreedSpec.Generation)
	assert.Equal(t, "h1", s.AgreedSpec.Hash)

	// Newer generation replaces.
	applyHelper(t, f, ProposalSpecUpdate, SpecUpdatePayload{Generation: 2, Hash: "h2"})
	assert.Equal(t, int64(2), f.GetState().AgreedSpec.Generation)

	// Older generation is ignored (replay safety).
	applyHelper(t, f, ProposalSpecUpdate, SpecUpdatePayload{Generation: 1, Hash: "h1"})
	assert.Equal(t, int64(2), f.GetState().AgreedSpec.Generation)
}

func TestApplyStatusReport_OverwritesPerCluster(t *testing.T) {
	f := NewFSM()
	applyHelper(t, f, ProposalStatusReport, StatusReportPayload{
		ClusterName:      "cluster-a",
		ObservedSpecHash: "h1",
		ComponentStatus: map[string]ComponentStatusEntry{
			"shard-0": {Generation: 1, Ready: false},
		},
		ReportedAt: time.Now().UTC(),
	})
	assert.Equal(t, "h1", f.GetClusterStatus("cluster-a").ObservedSpecHash)
	assert.False(t, f.GetClusterStatus("cluster-a").ComponentStatus["shard-0"].Ready)

	// Newer report overwrites.
	applyHelper(t, f, ProposalStatusReport, StatusReportPayload{
		ClusterName:      "cluster-a",
		ObservedSpecHash: "h2",
		ComponentStatus:  map[string]ComponentStatusEntry{"shard-0": {Generation: 2, Ready: true}},
		ReportedAt:       time.Now().UTC(),
	})
	cs := f.GetClusterStatus("cluster-a")
	assert.Equal(t, "h2", cs.ObservedSpecHash)
	assert.True(t, cs.ComponentStatus["shard-0"].Ready)

	// Different cluster lives alongside.
	applyHelper(t, f, ProposalStatusReport, StatusReportPayload{
		ClusterName:    "cluster-b",
		ReportedAt:     time.Now().UTC(),
		ComponentStatus: map[string]ComponentStatusEntry{},
	})
	assert.Equal(t, 2, len(f.GetState().PerClusterStatus))
}

func TestApplyPlanCreate_AndAdvance(t *testing.T) {
	f := NewFSM()
	applyHelper(t, f, ProposalPlanCreate, PlanCreatePayload{
		ID:         "plan-1",
		Generation: 1,
		Phases:     []string{"apply-sts", "publish-ac", "delete-pod"},
	})
	plan := f.GetState().CurrentPlan
	require.NotNil(t, plan)
	assert.Equal(t, 0, plan.CurrentPhase)
	assert.Equal(t, []string{"apply-sts", "publish-ac", "delete-pod"}, plan.Phases)

	// Advance from phase 0 → 1.
	applyHelper(t, f, ProposalPlanAdvance, PlanAdvancePayload{PlanID: "plan-1", ExpectFrom: 0})
	assert.Equal(t, 1, f.GetState().CurrentPlan.CurrentPhase)

	// Idempotent: repeated advance with same ExpectFrom is a no-op.
	applyHelper(t, f, ProposalPlanAdvance, PlanAdvancePayload{PlanID: "plan-1", ExpectFrom: 0})
	assert.Equal(t, 1, f.GetState().CurrentPlan.CurrentPhase)

	// Wrong plan ID errors.
	r := applyHelper(t, f, ProposalPlanAdvance, PlanAdvancePayload{PlanID: "other", ExpectFrom: 1})
	assert.Error(t, r.(error))
}

func TestApplyLeaseAllocate_AndComplete(t *testing.T) {
	f := NewFSM()
	r := applyHelper(t, f, ProposalLeaseAllocate, LeaseAllocatePayload{
		Component: "shard-0", ClusterName: "cluster-a", TTL: 30 * time.Second,
	})
	lease, _ := r.(*Lease)
	require.NotNil(t, lease)
	assert.Equal(t, "shard-0", lease.Component)
	assert.Equal(t, "cluster-a", lease.ClusterName)
	assert.WithinDuration(t, time.Now().UTC().Add(30*time.Second), lease.ExpiresAt, 2*time.Second)

	// Allocating while one is held returns the existing lease (no overwrite).
	r2 := applyHelper(t, f, ProposalLeaseAllocate, LeaseAllocatePayload{
		Component: "config", ClusterName: "cluster-b",
	})
	lease2, _ := r2.(*Lease)
	require.NotNil(t, lease2)
	assert.Equal(t, "shard-0", lease2.Component, "existing lease should be returned, not overwritten")

	// Complete with non-matching pair: lease unchanged.
	applyHelper(t, f, ProposalLeaseComplete, LeaseCompletePayload{Component: "config", ClusterName: "cluster-b"})
	require.NotNil(t, f.GetActiveLease())

	// Complete with matching pair: lease cleared.
	applyHelper(t, f, ProposalLeaseComplete, LeaseCompletePayload{Component: "shard-0", ClusterName: "cluster-a"})
	require.Nil(t, f.GetActiveLease())
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
	applyHelper(t, f, ProposalACPublished, ACPublishedPayload{Generation: 5})
	assert.Equal(t, 5, f.GetACGeneration())
	// Lower generation ignored.
	applyHelper(t, f, ProposalACPublished, ACPublishedPayload{Generation: 3})
	assert.Equal(t, 5, f.GetACGeneration())
	// Higher accepted.
	applyHelper(t, f, ProposalACPublished, ACPublishedPayload{Generation: 7})
	assert.Equal(t, 7, f.GetACGeneration())
}

func TestSnapshotRestore_PreservesAllFields(t *testing.T) {
	src := NewFSM()
	applyHelper(t, src, ProposalSpecUpdate, SpecUpdatePayload{Generation: 7, Hash: "hh", Content: json.RawMessage(`{"a":1}`)})
	applyHelper(t, src, ProposalStatusReport, StatusReportPayload{
		ClusterName: "cluster-a", ObservedSpecHash: "hh",
		ComponentStatus: map[string]ComponentStatusEntry{"config": {Generation: 1, Ready: true}},
		ReportedAt:      time.Now().UTC(),
	})
	applyHelper(t, src, ProposalLeaseAllocate, LeaseAllocatePayload{Component: "config", ClusterName: "cluster-a", TTL: 10 * time.Second})
	applyHelper(t, src, ProposalClusterIndexAssign, ClusterIndexAssignPayload{ClusterName: "cluster-a", Index: 0})
	applyHelper(t, src, ProposalClusterIndexAssign, ClusterIndexAssignPayload{ClusterName: "cluster-b", Index: 1})
	applyHelper(t, src, ProposalACPublished, ACPublishedPayload{Generation: 9})

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
