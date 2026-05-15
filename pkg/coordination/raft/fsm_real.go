package coordraft

import (
	"encoding/json"
	"io"
	"sync"
	"time"

	"github.com/hashicorp/raft"
	"golang.org/x/xerrors"
)

// FSM is the real raft.FSM that backs every PoC operator's coordinator.
//
// Concurrency model:
//   - raft.Raft serialises calls to Apply, Snapshot, and Restore on its own
//     goroutine. We additionally take an internal mutex so reader methods
//     (GetState, GetActiveLease, etc.) can run concurrently with Apply.
//   - Readers always get a deep copy (FSMState.Clone) so they can't mutate the
//     authoritative state.
type FSM struct {
	mu    sync.RWMutex
	state FSMState
}

// NewFSM constructs a fresh FSM with empty state.
func NewFSM() *FSM {
	return &FSM{state: NewFSMState()}
}

// Apply implements raft.FSM. Called on every replicated log entry on every
// node. Returns whatever the dispatcher wants the leader's apply-future to
// see — for lease_allocate we return the new *Lease so the leader's caller
// can inspect it.
func (f *FSM) Apply(log *raft.Log) interface{} {
	typ, payload, err := DecodeProposal(log.Data)
	if err != nil {
		return xerrors.Errorf("decode proposal: %w", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state.LastAppliedIndex = log.Index

	switch typ {
	case ProposalSpecUpdate:
		return f.applySpecUpdate(payload)
	case ProposalStatusReport:
		return f.applyStatusReport(payload)
	case ProposalPlanCreate:
		return f.applyPlanCreate(payload)
	case ProposalPlanAdvance:
		return f.applyPlanAdvance(payload)
	case ProposalLeaseAllocate:
		return f.applyLeaseAllocate(payload)
	case ProposalLeaseComplete:
		return f.applyLeaseComplete(payload)
	case ProposalClusterIndexAssign:
		return f.applyClusterIndexAssign(payload)
	case ProposalACPublished:
		return f.applyACPublished(payload)
	default:
		return xerrors.Errorf("unknown proposal type %q", typ)
	}
}

// applySpecUpdate replaces AgreedSpec iff the new generation strictly exceeds
// the existing one. This makes the proposal idempotent across replays.
func (f *FSM) applySpecUpdate(payload json.RawMessage) interface{} {
	var p SpecUpdatePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return xerrors.Errorf("decode spec_update: %w", err)
	}
	if f.state.AgreedSpec == nil || p.Generation > f.state.AgreedSpec.Generation {
		f.state.AgreedSpec = &AgreedSpec{
			Generation: p.Generation,
			Hash:       p.Hash,
			Content:    append(json.RawMessage(nil), p.Content...),
		}
	}
	return nil
}

// applyStatusReport merges the incoming ComponentStatus map into the cluster's
// existing entry. Scalar fields (ObservedSpecHash, LastReportedAt, LastReconcileErr)
// are overwritten by the report; ComponentStatus entries are union'd so a
// partial report (e.g. "shard-0 Ready=true" only) does not wipe previously
// reported components like "config Ready=true".
func (f *FSM) applyStatusReport(payload json.RawMessage) interface{} {
	var p StatusReportPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return xerrors.Errorf("decode status_report: %w", err)
	}
	if f.state.PerClusterStatus == nil {
		f.state.PerClusterStatus = map[string]ClusterStatus{}
	}
	existing, ok := f.state.PerClusterStatus[p.ClusterName]
	if !ok {
		existing = ClusterStatus{
			ClusterName:     p.ClusterName,
			ComponentStatus: map[string]ComponentStatus{},
		}
	}
	existing.LastReportedAt = p.ReportedAt
	existing.ObservedSpecHash = p.ObservedSpecHash
	existing.LastReconcileErr = p.LastReconcileErr
	if existing.ComponentStatus == nil {
		existing.ComponentStatus = map[string]ComponentStatus{}
	}
	for k, v := range p.ComponentStatus {
		existing.ComponentStatus[k] = ComponentStatus(v)
	}
	f.state.PerClusterStatus[p.ClusterName] = existing
	return nil
}

// applyPlanCreate replaces CurrentPlan with the new plan (leader-only producer).
func (f *FSM) applyPlanCreate(payload json.RawMessage) interface{} {
	var p PlanCreatePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return xerrors.Errorf("decode plan_create: %w", err)
	}
	f.state.CurrentPlan = &Plan{
		ID:           p.ID,
		Generation:   p.Generation,
		Phases:       append([]string(nil), p.Phases...),
		CurrentPhase: 0,
	}
	return nil
}

// applyPlanAdvance bumps CurrentPhase iff ExpectFrom matches (idempotent).
func (f *FSM) applyPlanAdvance(payload json.RawMessage) interface{} {
	var p PlanAdvancePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return xerrors.Errorf("decode plan_advance: %w", err)
	}
	if f.state.CurrentPlan == nil || f.state.CurrentPlan.ID != p.PlanID {
		return xerrors.Errorf("plan_advance: no matching plan %q", p.PlanID)
	}
	if f.state.CurrentPlan.CurrentPhase != p.ExpectFrom {
		// Replay or out-of-order advance; no-op for idempotency.
		return nil
	}
	f.state.CurrentPlan.CurrentPhase++
	return nil
}

// applyLeaseAllocate sets ActiveLease iff none is currently set.
func (f *FSM) applyLeaseAllocate(payload json.RawMessage) interface{} {
	var p LeaseAllocatePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return xerrors.Errorf("decode lease_allocate: %w", err)
	}
	if f.state.ActiveLease != nil {
		// Lease already held — no-op. Caller should observe FSM state and
		// retry only after lease completion.
		return f.state.ActiveLease
	}
	now := time.Now().UTC()
	ttl := p.TTL
	if ttl <= 0 {
		ttl = 60 * time.Second
	}
	f.state.ActiveLease = &Lease{
		Component:   p.Component,
		ClusterName: p.ClusterName,
		AllocatedAt: now,
		ExpiresAt:   now.Add(ttl),
	}
	return f.state.ActiveLease
}

// applyLeaseComplete clears ActiveLease iff it matches.
func (f *FSM) applyLeaseComplete(payload json.RawMessage) interface{} {
	var p LeaseCompletePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return xerrors.Errorf("decode lease_complete: %w", err)
	}
	if f.state.ActiveLease == nil {
		return nil
	}
	if f.state.ActiveLease.Component == p.Component && f.state.ActiveLease.ClusterName == p.ClusterName {
		f.state.ActiveLease = nil
	}
	return nil
}

// applyClusterIndexAssign records the index iff the cluster doesn't have one.
// Once assigned, never reused — even if the cluster is removed.
func (f *FSM) applyClusterIndexAssign(payload json.RawMessage) interface{} {
	var p ClusterIndexAssignPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return xerrors.Errorf("decode cluster_index_assign: %w", err)
	}
	if _, exists := f.state.ClusterIndex[p.ClusterName]; !exists {
		f.state.ClusterIndex[p.ClusterName] = p.Index
	}
	return f.state.ClusterIndex[p.ClusterName]
}

// applyACPublished bumps ACGeneration iff strictly larger (idempotent replay).
func (f *FSM) applyACPublished(payload json.RawMessage) interface{} {
	var p ACPublishedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return xerrors.Errorf("decode ac_published: %w", err)
	}
	if p.Generation > f.state.ACGeneration {
		f.state.ACGeneration = p.Generation
	}
	return f.state.ACGeneration
}

// ============================================================================
// Snapshot / Restore — JSON.
// ============================================================================

// Snapshot returns a snapshot of the current state. Called under raft's own
// lock, but we still take ours to make the deep-copy clean.
func (f *FSM) Snapshot() (raft.FSMSnapshot, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	clone := f.state.Clone()
	return &fsmSnapshot{state: clone}, nil
}

// Restore replaces the FSM's state with the JSON-decoded snapshot contents.
func (f *FSM) Restore(rc io.ReadCloser) error {
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return xerrors.Errorf("read snapshot: %w", err)
	}
	var s FSMState
	if err := json.Unmarshal(data, &s); err != nil {
		return xerrors.Errorf("decode snapshot: %w", err)
	}
	// Defensive: empty maps if missing.
	if s.PerClusterStatus == nil {
		s.PerClusterStatus = map[string]ClusterStatus{}
	}
	if s.ClusterIndex == nil {
		s.ClusterIndex = map[string]int{}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state = s
	return nil
}

type fsmSnapshot struct{ state FSMState }

func (s *fsmSnapshot) Persist(sink raft.SnapshotSink) error {
	data, err := json.Marshal(s.state)
	if err != nil {
		_ = sink.Cancel()
		return xerrors.Errorf("marshal snapshot: %w", err)
	}
	if _, err := sink.Write(data); err != nil {
		_ = sink.Cancel()
		return xerrors.Errorf("write snapshot: %w", err)
	}
	return sink.Close()
}

func (s *fsmSnapshot) Release() {}

// ============================================================================
// Read accessors — always return deep copies / values.
// ============================================================================

// GetState returns a deep copy of the current FSM state.
func (f *FSM) GetState() FSMState {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.state.Clone()
}

// GetActiveLease returns the current lease (deep-copied) or nil.
func (f *FSM) GetActiveLease() *Lease {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.state.ActiveLease == nil {
		return nil
	}
	l := *f.state.ActiveLease
	return &l
}

// GetClusterStatus returns the status of one cluster (zero value if absent).
func (f *FSM) GetClusterStatus(name string) ClusterStatus {
	f.mu.RLock()
	defer f.mu.RUnlock()
	cs, ok := f.state.PerClusterStatus[name]
	if !ok {
		return ClusterStatus{ClusterName: name}
	}
	// Deep-copy ComponentStatus map.
	out := cs
	if cs.ComponentStatus != nil {
		out.ComponentStatus = make(map[string]ComponentStatus, len(cs.ComponentStatus))
		for k, v := range cs.ComponentStatus {
			out.ComponentStatus[k] = v
		}
	}
	return out
}

// GetACGeneration returns the AC generation.
func (f *FSM) GetACGeneration() int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.state.ACGeneration
}

// GetClusterIndex returns the index for a cluster name, or -1 if unassigned.
func (f *FSM) GetClusterIndex(name string) int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if idx, ok := f.state.ClusterIndex[name]; ok {
		return idx
	}
	return -1
}
