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
	case ProposalLeaseAllocate:
		return f.applyLeaseAllocate(payload)
	case ProposalLeaseComplete:
		return f.applyLeaseComplete(payload)
	case ProposalLeaseExpire:
		return f.applyLeaseExpire(payload)
	case ProposalClusterIndexAssign:
		return f.applyClusterIndexAssign(payload)
	case ProposalACPublished:
		return f.applyACPublished(payload)
	case ProposalCRDelete:
		return f.applyCRDelete(payload)
	default:
		return xerrors.Errorf("unknown proposal type %q", typ)
	}
}

// getOrCreatePerCR returns a mutable handle to the PerCR entry for k. NOT
// safe to call outside the FSM lock.
func (f *FSM) getOrCreatePerCR(k CRKey) PerCRState {
	key := k.String()
	cur, ok := f.state.PerCR[key]
	if !ok {
		cur = NewPerCRState()
	}
	return cur
}

func (f *FSM) putPerCR(k CRKey, s PerCRState) {
	f.state.PerCR[k.String()] = s
}

// applySpecUpdate replaces AgreedSpec iff the new generation strictly exceeds
// the existing one. This makes the proposal idempotent across replays.
func (f *FSM) applySpecUpdate(payload json.RawMessage) interface{} {
	var p SpecUpdatePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return xerrors.Errorf("decode spec_update: %w", err)
	}
	cr := f.getOrCreatePerCR(p.CRKey)
	if cr.AgreedSpec == nil || p.Generation > cr.AgreedSpec.Generation {
		cr.AgreedSpec = &AgreedSpec{
			Generation: p.Generation,
			Hash:       p.Hash,
			Content:    append(json.RawMessage(nil), p.Content...),
		}
	}
	f.putPerCR(p.CRKey, cr)
	return nil
}

// applyStatusReport merges the incoming ComponentStatus map into the cluster's
// existing entry. Scalar fields (ObservedSpecHash, LastReportedAt, LastReconcileErr)
// are overwritten by the report; ComponentStatus entries are union'd so a
// partial report (e.g. "shard-0 Ready=true" only) does not wipe previously
// reported components like "config Ready=true".
//
// As of F1 the status report ALSO acts as an implicit heartbeat for the
// active lease iff the reporting cluster is the current lease holder.
func (f *FSM) applyStatusReport(payload json.RawMessage) interface{} {
	var p StatusReportPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return xerrors.Errorf("decode status_report: %w", err)
	}
	cr := f.getOrCreatePerCR(p.CRKey)
	existing, ok := cr.PerClusterStatus[p.ClusterName]
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
	cr.PerClusterStatus[p.ClusterName] = existing

	// Heartbeat-on-StatusReport: if the reporter is the lease holder, refresh
	// HeartbeatAt. The leader checks this in SweepStuckLeases.
	if cr.ActiveLease != nil && cr.ActiveLease.ClusterName == p.ClusterName {
		ts := p.ReportedAt
		if ts.IsZero() {
			ts = time.Now().UTC()
		}
		cr.ActiveLease.HeartbeatAt = ts
	}

	f.putPerCR(p.CRKey, cr)
	return nil
}

// applyLeaseAllocate sets ActiveLease iff none is currently set.
func (f *FSM) applyLeaseAllocate(payload json.RawMessage) interface{} {
	var p LeaseAllocatePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return xerrors.Errorf("decode lease_allocate: %w", err)
	}
	cr := f.getOrCreatePerCR(p.CRKey)
	if cr.ActiveLease != nil {
		// Lease already held — no-op. Caller should observe FSM state and
		// retry only after lease completion.
		out := *cr.ActiveLease
		f.putPerCR(p.CRKey, cr)
		return &out
	}
	now := time.Now().UTC()
	ttl := p.TTL
	if ttl <= 0 {
		ttl = 60 * time.Second
	}
	// Deadline default 30 min if caller didn't specify a TTL >= 30min.
	deadline := now.Add(30 * time.Minute)
	if ttl >= 30*time.Minute {
		deadline = now.Add(ttl)
	}
	cr.ActiveLease = &Lease{
		Component:   p.Component,
		ClusterName: p.ClusterName,
		AllocatedAt: now,
		HeartbeatAt: now,
		DeadlineAt:  deadline,
		ExpiresAt:   now.Add(ttl),
	}
	f.putPerCR(p.CRKey, cr)
	out := *cr.ActiveLease
	return &out
}

// applyLeaseComplete clears ActiveLease iff it matches.
func (f *FSM) applyLeaseComplete(payload json.RawMessage) interface{} {
	var p LeaseCompletePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return xerrors.Errorf("decode lease_complete: %w", err)
	}
	cr := f.getOrCreatePerCR(p.CRKey)
	if cr.ActiveLease == nil {
		f.putPerCR(p.CRKey, cr)
		return nil
	}
	if cr.ActiveLease.Component == p.Component && cr.ActiveLease.ClusterName == p.ClusterName {
		cr.ActiveLease = nil
	}
	f.putPerCR(p.CRKey, cr)
	return nil
}

// applyLeaseExpire revokes the active lease iff it matches the payload's
// (Component, ClusterName). Idempotent across replays.
func (f *FSM) applyLeaseExpire(payload json.RawMessage) interface{} {
	var p LeaseExpirePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return xerrors.Errorf("decode lease_expire: %w", err)
	}
	cr := f.getOrCreatePerCR(p.CRKey)
	if cr.ActiveLease == nil {
		f.putPerCR(p.CRKey, cr)
		return nil
	}
	if cr.ActiveLease.Component == p.Component && cr.ActiveLease.ClusterName == p.ClusterName {
		cr.ActiveLease = nil
	}
	f.putPerCR(p.CRKey, cr)
	return p.Reason
}

// applyClusterIndexAssign records the index iff the cluster doesn't have one.
// Once assigned, never reused — even if the cluster is removed. Cluster
// indices live at FSMState top level (global to this coordinator).
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
	cr := f.getOrCreatePerCR(p.CRKey)
	if p.Generation > cr.ACGeneration {
		cr.ACGeneration = p.Generation
	}
	f.putPerCR(p.CRKey, cr)
	return cr.ACGeneration
}

// applyCRDelete clears the entire PerCR entry for the CRKey. Idempotent.
func (f *FSM) applyCRDelete(payload json.RawMessage) interface{} {
	var p CRDeletePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return xerrors.Errorf("decode cr_delete: %w", err)
	}
	delete(f.state.PerCR, p.CRKey.String())
	return nil
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
	if s.PerCR == nil {
		s.PerCR = map[string]PerCRState{}
	}
	if s.ClusterIndex == nil {
		s.ClusterIndex = map[string]int{}
	}
	for k, v := range s.PerCR {
		if v.PerClusterStatus == nil {
			v.PerClusterStatus = map[string]ClusterStatus{}
			s.PerCR[k] = v
		}
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
// Read accessors — always return deep copies / values. Reads are scoped per
// CRKey now that the FSM is partitioned.
// ============================================================================

// GetState returns a deep copy of the full FSM state.
func (f *FSM) GetState() FSMState {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.state.Clone()
}

// GetPerCR returns the per-CR slice for k, or a zero-valued (initialised)
// PerCRState if no proposal has touched this CRKey yet.
func (f *FSM) GetPerCR(k CRKey) PerCRState {
	f.mu.RLock()
	defer f.mu.RUnlock()
	cur, ok := f.state.PerCR[k.String()]
	if !ok {
		return NewPerCRState()
	}
	return cur.Clone()
}

// GetActiveLease returns the active lease for a CR (deep-copied) or nil.
func (f *FSM) GetActiveLease(k CRKey) *Lease {
	f.mu.RLock()
	defer f.mu.RUnlock()
	cur, ok := f.state.PerCR[k.String()]
	if !ok || cur.ActiveLease == nil {
		return nil
	}
	l := *cur.ActiveLease
	return &l
}

// GetClusterStatus returns the status of one cluster for a CR (zero value if
// absent).
func (f *FSM) GetClusterStatus(k CRKey, name string) ClusterStatus {
	f.mu.RLock()
	defer f.mu.RUnlock()
	cur, ok := f.state.PerCR[k.String()]
	if !ok {
		return ClusterStatus{ClusterName: name}
	}
	cs, ok := cur.PerClusterStatus[name]
	if !ok {
		return ClusterStatus{ClusterName: name}
	}
	out := cs
	if cs.ComponentStatus != nil {
		out.ComponentStatus = make(map[string]ComponentStatus, len(cs.ComponentStatus))
		for kk, vv := range cs.ComponentStatus {
			out.ComponentStatus[kk] = vv
		}
	}
	return out
}

// GetACGeneration returns a CR's AC generation.
func (f *FSM) GetACGeneration(k CRKey) int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	cur, ok := f.state.PerCR[k.String()]
	if !ok {
		return 0
	}
	return cur.ACGeneration
}

// GetClusterIndex returns the index for a cluster name, or -1 if unassigned.
// Cluster indices are global (not CR-scoped).
func (f *FSM) GetClusterIndex(name string) int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if idx, ok := f.state.ClusterIndex[name]; ok {
		return idx
	}
	return -1
}
