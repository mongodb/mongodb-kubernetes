package coordraft

import (
	"encoding/json"
	"fmt"
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
	// ProposalStatusReport replaces one cluster's reported status. Also
	// implicitly heartbeats the active lease when the reporter is the holder.
	ProposalStatusReport ProposalType = "status_report"
	// ProposalLeaseAllocate sets ActiveLease to a new (component, cluster).
	// PoC keeps a single global lease per CR — multi-lease support is
	// post-PoC.
	ProposalLeaseAllocate ProposalType = "lease_allocate"
	// ProposalLeaseComplete clears ActiveLease iff it matches the payload.
	ProposalLeaseComplete ProposalType = "lease_complete"
	// ProposalLeaseExpire revokes the lease (heartbeat-TTL, hard deadline,
	// stuck, or cluster-unreachable). Carries a reason for observability.
	ProposalLeaseExpire ProposalType = "lease_expire"
	// ProposalClusterIndexAssign assigns a stable integer index to a cluster
	// name. Once written, never reused. See architecture doc §6.10.
	ProposalClusterIndexAssign ProposalType = "cluster_index_assign"
	// ProposalACPublished bumps ACGeneration. Leader announces "I've pushed
	// AC version N to OM"; followers stop blocking on AC convergence.
	ProposalACPublished ProposalType = "ac_published"
	// ProposalCRDelete removes the entire PerCRState entry for a CRKey.
	// Used when a CR is removed from the cluster.
	ProposalCRDelete ProposalType = "cr_delete"
	// ProposalResourceObserved records one cluster's observed content-hash
	// for a spec-referenced K8s resource (ConfigMap / Secret). F12a adds this
	// so every operator must agree on the bytes of every spec-referenced
	// resource before any of them touches OM. Raft leader election rotates
	// between clusters; divergent local copies would otherwise produce a
	// "whichever cluster happens to be leader wins" inconsistency.
	ProposalResourceObserved ProposalType = "resource_observed"
)

// CRKey identifies a single CR — every proposal carries one so the FSM can
// partition state per (Kind, Namespace, Name).
type CRKey struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// String returns a stable key for logging / map keys outside the FSM.
func (k CRKey) String() string {
	return fmt.Sprintf("%s/%s/%s", k.Kind, k.Namespace, k.Name)
}

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
// Payload types (one struct per ProposalType). Every payload carries CRKey.
// ============================================================================

// SpecUpdatePayload — content-hashed spec replication.
type SpecUpdatePayload struct {
	CRKey      CRKey           `json:"crKey"`
	Generation int64           `json:"generation"`
	Hash       string          `json:"hash"`
	Content    json.RawMessage `json:"content"`
}

// StatusReportPayload — one cluster's observed state. Also acts as a heartbeat
// for the active lease if the reporter holds it.
type StatusReportPayload struct {
	CRKey            CRKey                           `json:"crKey"`
	ClusterName      string                          `json:"clusterName"`
	ObservedSpecHash string                          `json:"observedSpecHash"`
	ComponentStatus  map[string]ComponentStatusEntry `json:"componentStatus"`
	LastReconcileErr string                          `json:"lastReconcileErr"`
	ReportedAt       time.Time                       `json:"reportedAt"`
	// Progress is the leader-visible signature the stuck-step detector reads
	// from. Optional per-cluster — followers can omit any field they don't
	// observe yet. The leader maintains a per-scope last-seen map and
	// compares signatures across reports to detect "no progress".
	Progress ProgressSnapshotEntry `json:"progress,omitempty"`
	// IdempotencyID is a per-proposal UUID the client retries with on
	// ErrNotLeader; the leader-side forwarder may surface this back to the
	// caller for end-to-end "applied?" reasoning.
	IdempotencyID string `json:"idempotencyId,omitempty"`
}

// ComponentStatusEntry mirrors ComponentStatus on the FSM state. Sent over the
// wire as a value, applied into FSMState.
type ComponentStatusEntry struct {
	Generation int64 `json:"generation"`
	Ready      bool  `json:"ready"`
}

// ProgressSnapshotEntry — what the leader compares to detect stuck steps.
// Unchanged signature across stuck_threshold means revoke. Mirrors the public
// coordination.ProgressSnapshot.
type ProgressSnapshotEntry struct {
	CurrentReplicas         int    `json:"currentReplicas"`
	ReadyReplicas           int    `json:"readyReplicas"`
	ObservedGeneration      int64  `json:"observedGeneration"`
	AgentGoalVersionAchieve int64  `json:"agentGoalVersionAchieved"`
	LastEventTS             int64  `json:"lastEventTs"`
	PendingError            string `json:"pendingError,omitempty"`
}

// LeaseAllocatePayload — propose a single global lease for a CR.
type LeaseAllocatePayload struct {
	CRKey         CRKey         `json:"crKey"`
	Component     string        `json:"component"`
	ClusterName   string        `json:"clusterName"`
	TTL           time.Duration `json:"ttl"`
	IdempotencyID string        `json:"idempotencyId,omitempty"`
}

// LeaseCompletePayload — leaseholder announces completion.
type LeaseCompletePayload struct {
	CRKey         CRKey  `json:"crKey"`
	Component     string `json:"component"`
	ClusterName   string `json:"clusterName"`
	IdempotencyID string `json:"idempotencyId,omitempty"`
}

// LeaseExpirePayload — leader revokes a lease for {heartbeat-TTL, deadline,
// stuck, cluster-unreachable}. Idempotent (matches before clearing).
type LeaseExpirePayload struct {
	CRKey         CRKey  `json:"crKey"`
	Component     string `json:"component"`
	ClusterName   string `json:"clusterName"`
	Reason        string `json:"reason"`
	IdempotencyID string `json:"idempotencyId,omitempty"`
}

// ClusterIndexAssignPayload — content-hashed cluster index assignment.
// Cluster indices are global to a coordinator (NOT per-CR).
type ClusterIndexAssignPayload struct {
	ClusterName string `json:"clusterName"`
	Index       int    `json:"index"`
}

// ACPublishedPayload — bumps a CR's ACGeneration to Generation.
type ACPublishedPayload struct {
	CRKey      CRKey `json:"crKey"`
	Generation int   `json:"generation"`
}

// CRDeletePayload — clears the entire PerCRState entry for a CRKey.
type CRDeletePayload struct {
	CRKey CRKey `json:"crKey"`
}

// ResourceRef identifies a spec-referenced K8s resource — kind, namespace,
// name. F12a uses this to track per-cluster observations of resources every
// operator must see identically before any of them touches OM.
type ResourceRef struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// String returns a stable representation for logs / diagnostics.
func (r ResourceRef) String() string {
	return fmt.Sprintf("%s/%s/%s", r.Kind, r.Namespace, r.Name)
}

// ResourceObservedPayload records one cluster's content-hash for one resource.
type ResourceObservedPayload struct {
	CRKey       CRKey       `json:"crKey"`
	Ref         ResourceRef `json:"ref"`
	ContentHash string      `json:"contentHash"`
	ObservedBy  string      `json:"observedBy"`
	ObservedAt  time.Time   `json:"observedAt"`
}
