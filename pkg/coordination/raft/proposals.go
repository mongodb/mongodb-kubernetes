package coordraft

import (
	"encoding/json"
	"time"

	"golang.org/x/xerrors"
)

// ProposalType is the discriminant on Envelope.
type ProposalType string

const (
	// ProposalSpecUpdate replicates a CR spec (content-hashed). Followers
	// learn about the canonical spec via this entry even if their local CR
	// has not yet been GitOps-synced. See architecture doc §6.3.
	ProposalSpecUpdate ProposalType = "spec_update"
	// ProposalStatusReport replaces one cluster's reported status.
	ProposalStatusReport ProposalType = "status_report"
	// ProposalPlanCreate replaces the current Plan (leader-only producer).
	ProposalPlanCreate ProposalType = "plan_create"
	// ProposalPlanAdvance bumps Plan.CurrentPhase by one.
	ProposalPlanAdvance ProposalType = "plan_advance"
	// ProposalLeaseAllocate sets ActiveLease to a new (component, cluster).
	// PoC keeps a single global lease — multi-lease support is post-PoC.
	ProposalLeaseAllocate ProposalType = "lease_allocate"
	// ProposalLeaseComplete clears ActiveLease iff it matches the payload.
	ProposalLeaseComplete ProposalType = "lease_complete"
	// ProposalClusterIndexAssign assigns a stable integer index to a cluster
	// name. Once written, never reused. See architecture doc §6.10.
	ProposalClusterIndexAssign ProposalType = "cluster_index_assign"
	// ProposalACPublished bumps ACGeneration. Leader announces "I've pushed
	// AC version N to OM"; followers stop blocking on AC convergence.
	ProposalACPublished ProposalType = "ac_published"
)

// Envelope is the on-the-wire form of every Raft log entry.
type Envelope struct {
	Type    ProposalType    `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// EncodeProposal marshals (typ, payload) into the byte slice we pass to raft.Apply.
func EncodeProposal(typ ProposalType, payload interface{}) ([]byte, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, xerrors.Errorf("marshal payload: %w", err)
	}
	env := Envelope{Type: typ, Payload: raw}
	return json.Marshal(env)
}

// DecodeProposal returns the type + raw payload from a raft log entry's data.
func DecodeProposal(data []byte) (ProposalType, json.RawMessage, error) {
	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return "", nil, xerrors.Errorf("decode envelope: %w", err)
	}
	return env.Type, env.Payload, nil
}

// ============================================================================
// Payload types (one struct per ProposalType).
// ============================================================================

// SpecUpdatePayload — content-hashed spec replication.
type SpecUpdatePayload struct {
	Generation int64           `json:"generation"`
	Hash       string          `json:"hash"`
	Content    json.RawMessage `json:"content"`
}

// StatusReportPayload — one cluster's observed state.
type StatusReportPayload struct {
	ClusterName      string                          `json:"clusterName"`
	ObservedSpecHash string                          `json:"observedSpecHash"`
	ComponentStatus  map[string]ComponentStatusEntry `json:"componentStatus"`
	LastReconcileErr string                          `json:"lastReconcileErr"`
	ReportedAt       time.Time                       `json:"reportedAt"`
}

// ComponentStatusEntry mirrors ComponentStatus on the FSM state. Sent over the
// wire as a value, applied into FSMState.
type ComponentStatusEntry struct {
	Generation int64 `json:"generation"`
	Ready      bool  `json:"ready"`
}

// PlanCreatePayload — a leader's diff-of-spec plan.
type PlanCreatePayload struct {
	ID         string   `json:"id"`
	Generation int64    `json:"generation"`
	Phases     []string `json:"phases"`
}

// PlanAdvancePayload — bump CurrentPhase by one if it matches ExpectFrom.
type PlanAdvancePayload struct {
	PlanID     string `json:"planId"`
	ExpectFrom int    `json:"expectFrom"`
}

// LeaseAllocatePayload — propose a single global lease.
type LeaseAllocatePayload struct {
	Component   string        `json:"component"`
	ClusterName string        `json:"clusterName"`
	TTL         time.Duration `json:"ttl"`
}

// LeaseCompletePayload — leaseholder announces completion.
type LeaseCompletePayload struct {
	Component   string `json:"component"`
	ClusterName string `json:"clusterName"`
}

// ClusterIndexAssignPayload — content-hashed cluster index assignment.
type ClusterIndexAssignPayload struct {
	ClusterName string `json:"clusterName"`
	Index       int    `json:"index"`
}

// ACPublishedPayload — bumps ACGeneration to Generation.
type ACPublishedPayload struct {
	Generation int `json:"generation"`
}
