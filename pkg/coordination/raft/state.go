package coordraft

import (
	"encoding/json"
	"time"
)

// FSMState is the entire replicated state machine: a JSON-serialisable bag of
// fields the operator's coordination layer needs to read.
//
// Conventions:
//   - Maps are never nil after construction (NewFSMState initialises empties).
//   - PerCR is keyed by CRKey-as-string and partitions the per-CR state.
//   - ClusterIndex (global) is keyed by cluster name; values are stable
//     integers, never reused after assignment.
type FSMState struct {
	// PerCR partitions all CR-scoped state by CRKey. Every proposal that
	// carries a CRKey routes into one PerCRState entry.
	PerCR map[string]PerCRState `json:"perCr"`

	// ClusterIndex maps cluster name → stable integer index. Global to this
	// coordinator (not per-CR).
	ClusterIndex map[string]int `json:"clusterIndex"`

	// LastAppliedIndex is informational for tests; bumped on every Apply.
	LastAppliedIndex uint64 `json:"lastAppliedIndex"`
}

// PerCRState is the per-CR slice of FSMState. F1+ partitions FSM state by
// CRKey; every proposal type lives inside one of these entries.
type PerCRState struct {
	// AgreedSpec is the latest content-hash-agreed CR spec.
	AgreedSpec *AgreedSpec `json:"agreedSpec,omitempty"`

	// PerClusterStatus is what each cluster reported on its last reconcile.
	PerClusterStatus map[string]ClusterStatus `json:"perClusterStatus"`

	// ActiveLease — single PoC lease per CR.
	ActiveLease *Lease `json:"activeLease,omitempty"`

	// ACGeneration is bumped when the leader publishes AC to OM for this CR.
	ACGeneration int `json:"acGeneration"`
}

// AgreedSpec is the canonical CR content as agreed via Raft.
type AgreedSpec struct {
	Generation int64           `json:"generation"`
	Hash       string          `json:"hash"`
	Content    json.RawMessage `json:"content"`
}

// ClusterStatus is the per-cluster reported state.
type ClusterStatus struct {
	ClusterName      string                     `json:"clusterName"`
	LastReportedAt   time.Time                  `json:"lastReportedAt"`
	ObservedSpecHash string                     `json:"observedSpecHash"`
	ComponentStatus  map[string]ComponentStatus `json:"componentStatus"`
	LastReconcileErr string                     `json:"lastReconcileErr"`
}

// ComponentStatus is per-component readiness; mirrors ComponentStatusEntry on
// the wire.
type ComponentStatus struct {
	Generation int64 `json:"generation"`
	Ready      bool  `json:"ready"`
}

// Lease — single PoC lease per CR. Holds the per-CR coordination concurrency
// (one cluster doing real STS work at a time per component scope).
type Lease struct {
	Component   string    `json:"component"`
	ClusterName string    `json:"clusterName"`
	AllocatedAt time.Time `json:"allocatedAt"`
	// HeartbeatAt is refreshed implicitly by every StatusReport from the
	// holder; if HeartbeatTTL elapses without a refresh the leader revokes.
	HeartbeatAt time.Time `json:"heartbeatAt"`
	// DeadlineAt is the hard cap regardless of heartbeats (e.g. 30 min).
	DeadlineAt time.Time `json:"deadlineAt"`
	// ExpiresAt is preserved for backwards compatibility / observability.
	// PoC code treats DeadlineAt as authoritative.
	ExpiresAt time.Time `json:"expiresAt"`
}

// NewFSMState returns a zero state with all maps initialised.
func NewFSMState() FSMState {
	return FSMState{
		PerCR:        map[string]PerCRState{},
		ClusterIndex: map[string]int{},
	}
}

// NewPerCRState returns a zero per-CR state with all maps initialised.
func NewPerCRState() PerCRState {
	return PerCRState{
		PerClusterStatus: map[string]ClusterStatus{},
	}
}

// Clone returns a deep copy of s suitable for handing out to readers without
// risk of them mutating the FSM's internal map.
func (s FSMState) Clone() FSMState {
	out := NewFSMState()
	for k, v := range s.PerCR {
		out.PerCR[k] = v.Clone()
	}
	for k, v := range s.ClusterIndex {
		out.ClusterIndex[k] = v
	}
	out.LastAppliedIndex = s.LastAppliedIndex
	return out
}

// Clone returns a deep copy of the per-CR slice.
func (p PerCRState) Clone() PerCRState {
	out := NewPerCRState()
	if p.AgreedSpec != nil {
		spec := *p.AgreedSpec
		out.AgreedSpec = &spec
	}
	for k, v := range p.PerClusterStatus {
		cs := v
		if v.ComponentStatus != nil {
			cs.ComponentStatus = make(map[string]ComponentStatus, len(v.ComponentStatus))
			for ck, cv := range v.ComponentStatus {
				cs.ComponentStatus[ck] = cv
			}
		}
		out.PerClusterStatus[k] = cs
	}
	if p.ActiveLease != nil {
		l := *p.ActiveLease
		out.ActiveLease = &l
	}
	out.ACGeneration = p.ACGeneration
	return out
}
