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
//   - PerClusterStatus is keyed by cluster name.
//   - ClusterIndex is keyed by cluster name; values are stable integers,
//     never reused after assignment.
//   - ActiveLease is a pointer so nil means "no lease outstanding". PoC keeps
//     a single global lease; multi-lease support is post-PoC.
type FSMState struct {
	// AgreedSpec is the latest content-hash-agreed CR spec. Followers use
	// this as the canonical spec to reconcile against.
	AgreedSpec *AgreedSpec `json:"agreedSpec,omitempty"`

	// PerClusterStatus is what each cluster reported on its last reconcile.
	PerClusterStatus map[string]ClusterStatus `json:"perClusterStatus"`

	// ActiveLease — single global lease for PoC. Whoever holds it may
	// perform STS writes for that (component, cluster).
	ActiveLease *Lease `json:"activeLease,omitempty"`

	// ACGeneration is bumped when the leader publishes AC to OM.
	ACGeneration int `json:"acGeneration"`

	// CurrentPlan is the leader-produced plan derived from
	// diff(prevAgreedSpec, AgreedSpec). PoC keeps it minimal; full plan
	// vocabulary lands post-PoC (see arch doc §6.5).
	CurrentPlan *Plan `json:"currentPlan,omitempty"`

	// ClusterIndex maps cluster name → stable integer index for STS naming.
	ClusterIndex map[string]int `json:"clusterIndex"`

	// LastAppliedIndex is informational for tests; bumped on every Apply.
	LastAppliedIndex uint64 `json:"lastAppliedIndex"`
}

// AgreedSpec is the canonical CR content as agreed via Raft.
type AgreedSpec struct {
	Generation int64           `json:"generation"`
	Hash       string          `json:"hash"`
	Content    json.RawMessage `json:"content"`
}

// ClusterStatus is the per-cluster reported state.
type ClusterStatus struct {
	ClusterName      string                          `json:"clusterName"`
	LastReportedAt   time.Time                       `json:"lastReportedAt"`
	ObservedSpecHash string                          `json:"observedSpecHash"`
	ComponentStatus  map[string]ComponentStatus      `json:"componentStatus"`
	LastReconcileErr string                          `json:"lastReconcileErr"`
}

// ComponentStatus is per-component readiness; mirrors ComponentStatusEntry on
// the wire.
type ComponentStatus struct {
	Generation int64 `json:"generation"`
	Ready      bool  `json:"ready"`
}

// Lease — single PoC lease.
type Lease struct {
	Component   string    `json:"component"`
	ClusterName string    `json:"clusterName"`
	AllocatedAt time.Time `json:"allocatedAt"`
	ExpiresAt   time.Time `json:"expiresAt"`
}

// Plan — minimal PoC shape. Phases are just strings (component keys); the
// full Plan vocabulary from arch §6.5 is post-PoC.
type Plan struct {
	ID           string   `json:"id"`
	Generation   int64    `json:"generation"`
	Phases       []string `json:"phases"`
	CurrentPhase int      `json:"currentPhase"`
}

// NewFSMState returns a zero state with all maps initialised.
func NewFSMState() FSMState {
	return FSMState{
		PerClusterStatus: map[string]ClusterStatus{},
		ClusterIndex:     map[string]int{},
	}
}

// Clone returns a deep copy of s suitable for handing out to readers without
// risk of them mutating the FSM's internal map.
func (s FSMState) Clone() FSMState {
	out := NewFSMState()
	if s.AgreedSpec != nil {
		spec := *s.AgreedSpec
		out.AgreedSpec = &spec
	}
	for k, v := range s.PerClusterStatus {
		// ComponentStatus map needs its own copy too.
		cs := v
		if v.ComponentStatus != nil {
			cs.ComponentStatus = make(map[string]ComponentStatus, len(v.ComponentStatus))
			for ck, cv := range v.ComponentStatus {
				cs.ComponentStatus[ck] = cv
			}
		}
		out.PerClusterStatus[k] = cs
	}
	if s.ActiveLease != nil {
		l := *s.ActiveLease
		out.ActiveLease = &l
	}
	out.ACGeneration = s.ACGeneration
	if s.CurrentPlan != nil {
		p := *s.CurrentPlan
		if s.CurrentPlan.Phases != nil {
			p.Phases = append([]string(nil), s.CurrentPlan.Phases...)
		}
		out.CurrentPlan = &p
	}
	for k, v := range s.ClusterIndex {
		out.ClusterIndex[k] = v
	}
	out.LastAppliedIndex = s.LastAppliedIndex
	return out
}
