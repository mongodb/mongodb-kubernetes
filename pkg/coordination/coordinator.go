// Package coordination defines the DistributedCoordinator abstraction the
// operator uses to consult cross-cluster consensus when running in
// distributed mode.
//
// Implementations live in subpackages — see pkg/coordination/raft for the
// hashicorp/raft-backed PoC implementation.
//
// In non-distributed mode the operator's ShardedClusterReconcileHelper carries
// a nil coordinator and the existing hub-spoke code path executes unchanged.
package coordination

import "time"

// CRKey identifies one CR within the FSM-partitioned coordinator state.
// Every distributed-mode call carries one so multiple CRs sharing a
// coordinator stay isolated.
type CRKey struct {
	Kind      string
	Namespace string
	Name      string
}

// LeaseResult is what AcquireOrRespect returns.
type LeaseResult int

const (
	// LeaseHeld — we own the lease and may proceed with the per-cluster work.
	LeaseHeld LeaseResult = iota
	// LeaseWaitForLease — someone else holds (or the leader hasn't allocated
	// it to us yet). Caller should return workflow.Pending and requeue.
	LeaseWaitForLease
	// LeaseOtherClusterDone — the work is already complete per the FSM's
	// StatusReports. Caller may treat the iteration as a no-op (continue).
	LeaseOtherClusterDone
)

// ResourceRef identifies one K8s resource the reconcile reads from the local
// cluster (project ConfigMap, credentials Secret, CA bundle, TLS certs, etc.).
// F12a uses these to make every operator agree on the bytes of every
// spec-referenced resource before any of them touches OM. Raft leader election
// rotates between clusters; divergent local copies would otherwise yield a
// "whichever cluster happens to be leader wins" inconsistency.
type ResourceRef struct {
	Kind      string
	Namespace string
	Name      string
}

// String returns a stable representation for logs and diagnostics.
func (r ResourceRef) String() string {
	return r.Kind + "/" + r.Namespace + "/" + r.Name
}

// ResourceAgreement is the cross-cluster verdict on a set of required refs.
type ResourceAgreement int

const (
	// ResourcesAgreed — every required ref has been observed by every known
	// cluster and all reported content hashes match. Caller may proceed.
	ResourcesAgreed ResourceAgreement = iota
	// ResourcesPending — at least one required observation is missing or
	// hashes disagree. Caller should return workflow.Pending and surface the
	// diagnostic in MDB status.
	ResourcesPending
)

// ProgressSnapshot is the per-(CR, component, cluster) snapshot the leader
// uses to detect stuck steps. The leader compares signature equality across
// successive StatusReports; if it doesn't change for stuck_threshold the
// lease is revoked.
type ProgressSnapshot struct {
	CurrentReplicas         int
	ReadyReplicas           int
	ObservedGeneration      int64
	AgentGoalVersionAchieve int64
	LastEventTS             time.Time
	PendingError            string
}

// IsReady reports whether this snapshot represents a fully-converged scope:
// observed generation matches, agent goal achieved, ready/current replicas
// match.
func (p ProgressSnapshot) IsReady() bool {
	return p.PendingError == "" &&
		p.CurrentReplicas > 0 &&
		p.ReadyReplicas == p.CurrentReplicas &&
		p.AgentGoalVersionAchieve >= p.ObservedGeneration
}

// DistributedCoordinator is the consultation surface the reconciler uses to
// decide whether *this* operator instance should perform a given action.
//
// F5 reshapes this from the C3 surface (single global lease, no CRKey) into
// the inline-gating model:
//   - AcquireOrRespect / IsComponentReady / ReportProgress / MarkReady /
//     ReleaseLease are called from each STS-write site, one per cluster
//     iteration.
//   - The leader's reconcile loop is itself the scheduler — AcquireOrRespect
//     proposes a lease iff none is held.
//   - StatusReports implicitly heartbeat the active lease (no LeaseRenew
//     proposal exists).
//
// All Propose* methods block on raft commit (sync proposals) with a default
// 5s timeout. Followers' calls auto-forward to the leader via the app channel.
type DistributedCoordinator interface {
	// IsLeader reports whether this node is the current raft leader.
	IsLeader() bool

	// MyClusterName is the cluster identity this operator instance was
	// configured with.
	MyClusterName() string

	// ClusterIndex returns the stable integer index for a cluster name, or
	// (-1, false) if no proposal has yet assigned one.
	ClusterIndex(name string) (int, bool)

	// AcquireOrRespect is the gate-point for an STS write site. Returns:
	//  - LeaseHeld: caller may proceed with the per-cluster work.
	//  - LeaseWaitForLease: someone else (or no-one yet) holds the lease.
	//  - LeaseOtherClusterDone: the cluster is already reported Ready for the
	//    component. Caller should `continue` past this iteration.
	AcquireOrRespect(crKey CRKey, component, cluster string) LeaseResult

	// IsComponentReady reads from FSM Statuses without proposing anything.
	IsComponentReady(crKey CRKey, component, cluster string) bool

	// ReportProgress submits a progress-only StatusReport (Ready=false). The
	// FSM merges this into the cluster's per-component status and refreshes
	// the lease's HeartbeatAt iff the reporter is the holder.
	ReportProgress(crKey CRKey, component, cluster string, progress ProgressSnapshot) error

	// MarkReady submits a StatusReport with Ready=true and the final progress
	// snapshot for the (component, cluster) scope.
	MarkReady(crKey CRKey, component, cluster string, finalProgress ProgressSnapshot) error

	// ReleaseLease announces that the holder's work is done.
	ReleaseLease(crKey CRKey, component, cluster string) error

	// AcVersion returns the AC generation for a CR.
	AcVersion(crKey CRKey) int64

	// AnnounceAcPublished bumps the AC generation for a CR.
	AnnounceAcPublished(crKey CRKey, version int64) error

	// LastContact returns how long ago the local raft node last heard from
	// the named cluster's raft peer. Used by F7 to detect unreachable peers.
	// Returns a very large duration if the cluster has never been contacted.
	LastContact(cluster string) time.Duration

	// ReportResource submits a content-hash observation for one
	// spec-referenced resource on the calling cluster. F12a — every operator
	// reads its local copy of every spec-referenced ConfigMap/Secret,
	// computes the canonical content hash, and reports it via this method.
	// Synchronous: returns after the proposal commits (or the forwarder
	// errors).
	ReportResource(crKey CRKey, ref ResourceRef, contentHash string) error

	// WaitForResourcesAgreed returns ResourcesAgreed iff every required ref
	// has been observed by every known cluster AND every cluster reports the
	// same content hash. Otherwise ResourcesPending plus a human-readable
	// diagnostic suitable for an MDB status condition. "Known clusters" is
	// the union of clusters that have already reported any resource for this
	// CR (the PoC has no separate "cluster roster" — clusters announce
	// themselves by reporting).
	WaitForResourcesAgreed(crKey CRKey, refs []ResourceRef) (ResourceAgreement, string)
}

// LegacyCoordinator is the C3-shape surface that controller code still uses
// via SetCoordinator. F6 reworks the call sites to use DistributedCoordinator
// directly; for now the controller takes LegacyCoordinator and the impl
// implements both so the migration is a single chunk.
type LegacyCoordinator interface {
	MyClusterName() string
	IsLeader() bool
	HasLeaseFor(component string, clusterName string) bool
	ProposeLeaseComplete(component string, clusterName string) error
	ProposeStatusReport(r ClusterStatusReport) error
	ProposeACPublished(generation int) error
	GetActiveLease() *LeaseInfo
	GetPerClusterStatus() map[string]ClusterStatusReport
	GetACGeneration() int
}

// ClusterStatusReport is the cross-package status report shape. Mirrors
// raft.ClusterStatus / raft.StatusReportPayload but lives at the coordination
// package level so the operator code can depend on it without importing raft.
type ClusterStatusReport struct {
	ClusterName      string
	ObservedSpecHash string
	ComponentStatus  map[string]ComponentStatus
	LastReconcileErr string
}

// ComponentStatus is the per-component readiness shape used in status reports.
type ComponentStatus struct {
	Generation int64
	Ready      bool
}

// LeaseInfo is the cross-package lease shape — also mirrored from raft.Lease.
type LeaseInfo struct {
	Component   string
	ClusterName string
}
