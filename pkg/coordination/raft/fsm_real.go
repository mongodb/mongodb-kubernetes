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
	case ProposalResourceObserved:
		return f.applyResourceObserved(payload)
	case ProposalAgentKeyPublished:
		return f.applyAgentKeyPublished(payload)
	default:
		return xerrors.Errorf("unknown proposal type %q", typ)
	}
}

// applyAgentKeyPublished stores the agent API key for a (CR, projectID).
// Idempotent: re-publishing the same key is a no-op; a different key
// overwrites (an OM-side rotation should bump the project anyway, which
// changes the projectID key).
func (f *FSM) applyAgentKeyPublished(payload json.RawMessage) interface{} {
	var p AgentKeyPublishedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return xerrors.Errorf("decode agent_key_published: %w", err)
	}
	if p.ProjectID == "" || p.AgentKey == "" {
		return xerrors.New("agent_key_published: empty ProjectID or AgentKey")
	}
	cr := f.getOrCreatePerCR(p.CRKey)
	if cr.AgentKeys == nil {
		cr.AgentKeys = map[string]string{}
	}
	cr.AgentKeys[p.ProjectID] = p.AgentKey
	f.putPerCR(p.CRKey, cr)
	return nil
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

	// Heartbeat-on-StatusReport: if the reporter holds any lease in this CR's
	// ActiveLeases map, refresh the matching lease's HeartbeatAt. With the
	// per-(component, cluster) lease split a single status report can refresh
	// multiple leases (e.g. one cluster could be working on shard-0 + mongos
	// in parallel; we walk the map and refresh every entry whose
	// ClusterName matches the reporter).
	if cr.ActiveLeases != nil {
		ts := p.ReportedAt
		if ts.IsZero() {
			ts = time.Now().UTC()
		}
		for _, l := range cr.ActiveLeases {
			if l != nil && l.ClusterName == p.ClusterName {
				l.HeartbeatAt = ts
			}
		}
	}

	f.putPerCR(p.CRKey, cr)
	return nil
}

// applyLeaseAllocate sets ActiveLeases[component|cluster] iff that specific
// slot is empty AND no sibling lease exists for the same (CR, component) on
// a different cluster. The cross-cluster guard (added in G'5 iter 13c) is the
// authoritative serialisation point for rolling-restart / scale: at any one
// time, at most one cluster may hold a lease for (CR, component). Without
// this, all member-cluster operators acquired their own per-cluster lease in
// parallel and rolled their pods simultaneously, breaking replicaset quorum.
//
// Initial deploy (scalingFirstTime) is unaffected because the operator
// releases its lease immediately after the STS-apply step, well before the
// next cluster's reconcile-retry runs — the slot is free by the time the
// next caller polls.
//
// A sibling-conflict allocation is a NO-OP (slot stays empty; the proposing
// caller's poll loop times out → AcquireOrRespect returns LeaseWaitForLease).
func (f *FSM) applyLeaseAllocate(payload json.RawMessage) interface{} {
	var p LeaseAllocatePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return xerrors.Errorf("decode lease_allocate: %w", err)
	}
	cr := f.getOrCreatePerCR(p.CRKey)
	if cr.ActiveLeases == nil {
		cr.ActiveLeases = map[string]*Lease{}
	}
	key := leaseKey(p.Component, p.ClusterName)
	if existing, ok := cr.ActiveLeases[key]; ok && existing != nil {
		// Slot already held — no-op. Caller should observe FSM state.
		out := *existing
		f.putPerCR(p.CRKey, cr)
		return &out
	}
	// G'5 iter 13c cross-cluster guard: refuse this slot if any OTHER lease
	// for the same (CR, component) is currently held on a different cluster.
	// The proposal is committed (we hold the lock) but produces no state
	// change — the caller's coordinator-side poll will see the slot empty
	// and return LeaseWaitForLease.
	for _, existing := range cr.ActiveLeases {
		if existing != nil && existing.Component == p.Component && existing.ClusterName != p.ClusterName {
			f.putPerCR(p.CRKey, cr)
			return nil
		}
	}
	now := time.Now().UTC()
	ttl := p.TTL
	if ttl <= 0 {
		ttl = 60 * time.Second
	}
	deadline := now.Add(30 * time.Minute)
	if ttl >= 30*time.Minute {
		deadline = now.Add(ttl)
	}
	newLease := &Lease{
		Component:   p.Component,
		ClusterName: p.ClusterName,
		AllocatedAt: now,
		HeartbeatAt: now,
		DeadlineAt:  deadline,
		ExpiresAt:   now.Add(ttl),
	}
	cr.ActiveLeases[key] = newLease
	f.putPerCR(p.CRKey, cr)
	out := *newLease
	return &out
}

// applyLeaseComplete clears the specific (component, cluster) slot iff present.
func (f *FSM) applyLeaseComplete(payload json.RawMessage) interface{} {
	var p LeaseCompletePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return xerrors.Errorf("decode lease_complete: %w", err)
	}
	cr := f.getOrCreatePerCR(p.CRKey)
	if cr.ActiveLeases != nil {
		delete(cr.ActiveLeases, leaseKey(p.Component, p.ClusterName))
	}
	f.putPerCR(p.CRKey, cr)
	return nil
}

// applyLeaseExpire revokes a specific (component, cluster) lease.
// Idempotent across replays.
func (f *FSM) applyLeaseExpire(payload json.RawMessage) interface{} {
	var p LeaseExpirePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return xerrors.Errorf("decode lease_expire: %w", err)
	}
	cr := f.getOrCreatePerCR(p.CRKey)
	if cr.ActiveLeases != nil {
		delete(cr.ActiveLeases, leaseKey(p.Component, p.ClusterName))
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

// applyResourceObserved records (or supersedes) a single cluster's content
// hash for one ResourceRef. Newer ObservedAt timestamps overwrite older ones;
// equal timestamps overwrite — this lets fresh reports replace stale ones
// without a separate purge proposal. Idempotent across replay (the last
// committed log entry wins for any given (CRKey, Ref, ObservedBy)).
func (f *FSM) applyResourceObserved(payload json.RawMessage) interface{} {
	var p ResourceObservedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return xerrors.Errorf("decode resource_observed: %w", err)
	}
	cr := f.getOrCreatePerCR(p.CRKey)
	if cr.Resources == nil {
		cr.Resources = map[string]map[string]ResourceObservation{}
	}
	refKey := p.Ref.String()
	byCluster, ok := cr.Resources[refKey]
	if !ok {
		byCluster = map[string]ResourceObservation{}
	}
	existing, has := byCluster[p.ObservedBy]
	if has && p.ObservedAt.Before(existing.ObservedAt) {
		// Stale message; ignore.
		f.putPerCR(p.CRKey, cr)
		return nil
	}
	byCluster[p.ObservedBy] = ResourceObservation{
		Ref:         p.Ref,
		ContentHash: p.ContentHash,
		ObservedAt:  p.ObservedAt,
	}
	cr.Resources[refKey] = byCluster
	f.putPerCR(p.CRKey, cr)
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
		dirty := false
		if v.PerClusterStatus == nil {
			v.PerClusterStatus = map[string]ClusterStatus{}
			dirty = true
		}
		if v.Resources == nil {
			v.Resources = map[string]map[string]ResourceObservation{}
			dirty = true
		}
		if dirty {
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

// GetActiveLease returns the FIRST active lease for a CR (deep-copied) or
// nil. Legacy single-CR API; with the per-(component, cluster) lease split
// the iteration order across the underlying map is undefined. Use
// GetLease(k, component, cluster) for deterministic queries.
func (f *FSM) GetActiveLease(k CRKey) *Lease {
	f.mu.RLock()
	defer f.mu.RUnlock()
	cur, ok := f.state.PerCR[k.String()]
	if !ok || len(cur.ActiveLeases) == 0 {
		return nil
	}
	for _, l := range cur.ActiveLeases {
		if l == nil {
			continue
		}
		out := *l
		return &out
	}
	return nil
}

// GetAgentKey returns the published agent API key for (CR, projectID), or
// "" if none has been published yet.
func (f *FSM) GetAgentKey(k CRKey, projectID string) string {
	f.mu.RLock()
	defer f.mu.RUnlock()
	cur, ok := f.state.PerCR[k.String()]
	if !ok || cur.AgentKeys == nil {
		return ""
	}
	return cur.AgentKeys[projectID]
}

// GetLease returns the lease for the specific (component, cluster) slot of a
// CR (deep-copied) or nil.
func (f *FSM) GetLease(k CRKey, component, clusterName string) *Lease {
	f.mu.RLock()
	defer f.mu.RUnlock()
	cur, ok := f.state.PerCR[k.String()]
	if !ok || cur.ActiveLeases == nil {
		return nil
	}
	l, ok := cur.ActiveLeases[leaseKey(component, clusterName)]
	if !ok || l == nil {
		return nil
	}
	out := *l
	return &out
}

// GetLeasesHeldBy returns the components for which `cluster` currently holds
// an active lease on `k`. The result is the deduplicated set of component
// strings (e.g. "shard-0", "config"). Used by G'5 iter 14f's lease-keep-alive
// path in gateOnResourceAgreement: when a reconcile blocks at Gate 0 (or any
// Pending gate above the per-component STS-write site), the caller iterates
// the returned components and emits a ReportProgress so the FSM-side
// HeartbeatAt is refreshed and the leader's stuck-step detector doesn't
// revoke an actively-held lease for spec-replication-induced wait time.
//
// Returns nil for an unknown CR or a cluster that holds no leases.
func (f *FSM) GetLeasesHeldBy(k CRKey, cluster string) []string {
	f.mu.RLock()
	defer f.mu.RUnlock()
	cur, ok := f.state.PerCR[k.String()]
	if !ok || cur.ActiveLeases == nil {
		return nil
	}
	out := make([]string, 0, len(cur.ActiveLeases))
	for _, l := range cur.ActiveLeases {
		if l == nil || l.ClusterName != cluster {
			continue
		}
		out = append(out, l.Component)
	}
	return out
}

// HasSiblingLease reports whether any active lease for (CR, component)
// exists on a cluster other than excludeCluster. Used by AcquireOrRespect's
// G'5 iter 13c cross-cluster guard fast-path: if true, the caller knows the
// FSM-side applyLeaseAllocate will refuse a new (component, excludeCluster)
// allocation, so we skip the proposal round-trip and return WaitForLease.
func (f *FSM) HasSiblingLease(k CRKey, component, excludeCluster string) bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	cur, ok := f.state.PerCR[k.String()]
	if !ok || cur.ActiveLeases == nil {
		return false
	}
	for _, l := range cur.ActiveLeases {
		if l == nil {
			continue
		}
		if l.Component == component && l.ClusterName != excludeCluster {
			return true
		}
	}
	return false
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
