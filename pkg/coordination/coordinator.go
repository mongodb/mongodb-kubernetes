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

// DistributedCoordinator is the consultation surface the reconciler uses to
// decide whether *this* operator instance should perform a given action.
//
// Method-level contracts:
//   - Read methods (IsLeader, MyClusterName, HasLeaseFor, GetActiveLease,
//     GetPerClusterStatus) are cheap and must not block. They may return
//     slightly stale values; callers tolerate this by re-checking on every
//     reconcile.
//   - Propose methods enqueue an entry into the underlying consensus log. They
//     may block for up to a few seconds while raft commits; callers should
//     treat failures as transient (return Pending and let the controller
//     requeue).
type DistributedCoordinator interface {
	// MyClusterName returns the cluster identity this operator instance was
	// configured with. Used in per-cluster iteration loops to decide
	// "is this iteration for my cluster?".
	MyClusterName() string

	// IsLeader returns true iff this node currently believes it is the Raft
	// leader. AC-publication sites consult this.
	IsLeader() bool

	// HasLeaseFor returns true iff the FSM's currently-active lease matches
	// (component, clusterName). STS-write sites gate on this.
	HasLeaseFor(component string, clusterName string) bool

	// ProposeLeaseComplete announces that the leaseholder's work for the
	// component+cluster is done. Idempotent (matches the lease before
	// clearing). Returns an error if the underlying raft.Apply fails.
	ProposeLeaseComplete(component string, clusterName string) error

	// ProposeStatusReport replicates this cluster's reported status. Called
	// at every reconcile (once per cluster) so the leader can see fresh data.
	ProposeStatusReport(r ClusterStatusReport) error

	// ProposeACPublished announces "I, the leader, have pushed AC version N".
	// Followers observe ACGeneration via GetACGeneration to unblock dependent
	// work. Idempotent (monotonic).
	ProposeACPublished(generation int) error

	// GetActiveLease returns the current global lease, or nil. For logging
	// and observability — gating uses HasLeaseFor.
	GetActiveLease() *LeaseInfo

	// GetPerClusterStatus returns a snapshot of all clusters' reported
	// statuses. Used by the after-loop barrier check in distributed mode.
	GetPerClusterStatus() map[string]ClusterStatusReport

	// GetACGeneration returns the agreed AC generation. Followers may wait
	// for this to bump before considering themselves up-to-date.
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
