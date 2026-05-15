package coordraft

import (
	"time"

	"golang.org/x/xerrors"

	"github.com/mongodb/mongodb-kubernetes/pkg/coordination"
)

// applyTimeout is how long we wait for raft.Apply to commit on the leader.
// Followers' Apply calls error out immediately with raft.ErrNotLeader.
const applyTimeout = 5 * time.Second

// Coordinator is the DistributedCoordinator implementation backed by Manager
// (real hashicorp/raft) and FSM (real state machine). One Coordinator per
// operator instance.
//
// F1+: the FSM is partitioned by CRKey. For F1 the Coordinator continues to
// expose a single-CR API surface — callers carry one CRKey on construction
// (the operator's CR-of-interest). F5 reworks this to take CRKey per call.
type Coordinator struct {
	manager     *Manager
	fsm         *FSM
	clusterName string
	defaultCR   CRKey
}

// NewCoordinator constructs a coordinator for clusterName backed by mgr+fsm.
// The caller is expected to construct the Manager with the same fsm.
//
// defaultCR is the CR this coordinator instance treats as "current" — for
// single-CR PoC tests/operators this is set once at construction.
func NewCoordinator(clusterName string, mgr *Manager, fsm *FSM) *Coordinator {
	return &Coordinator{
		manager:     mgr,
		fsm:         fsm,
		clusterName: clusterName,
	}
}

// SetDefaultCR sets the CRKey this coordinator's single-CR API methods route
// through. Single-CR PoC callers call this once after construction.
func (c *Coordinator) SetDefaultCR(k CRKey) { c.defaultCR = k }

// MyClusterName implements coordination.DistributedCoordinator.
func (c *Coordinator) MyClusterName() string { return c.clusterName }

// IsLeader implements coordination.DistributedCoordinator.
func (c *Coordinator) IsLeader() bool { return c.manager.IsLeader() }

// HasLeaseFor implements coordination.DistributedCoordinator.
func (c *Coordinator) HasLeaseFor(component, clusterName string) bool {
	lease := c.fsm.GetActiveLease(c.defaultCR)
	return lease != nil && lease.Component == component && lease.ClusterName == clusterName
}

// ProposeLeaseComplete implements coordination.DistributedCoordinator.
func (c *Coordinator) ProposeLeaseComplete(component, clusterName string) error {
	data, err := EncodeProposal(ProposalLeaseComplete, LeaseCompletePayload{
		CRKey: c.defaultCR, Component: component, ClusterName: clusterName,
	})
	if err != nil {
		return xerrors.Errorf("encode lease_complete: %w", err)
	}
	return c.manager.Apply(data, applyTimeout).Error()
}

// ProposeStatusReport implements coordination.DistributedCoordinator.
func (c *Coordinator) ProposeStatusReport(r coordination.ClusterStatusReport) error {
	cs := make(map[string]ComponentStatusEntry, len(r.ComponentStatus))
	for k, v := range r.ComponentStatus {
		cs[k] = ComponentStatusEntry{Generation: v.Generation, Ready: v.Ready}
	}
	data, err := EncodeProposal(ProposalStatusReport, StatusReportPayload{
		CRKey:            c.defaultCR,
		ClusterName:      r.ClusterName,
		ObservedSpecHash: r.ObservedSpecHash,
		ComponentStatus:  cs,
		LastReconcileErr: r.LastReconcileErr,
		ReportedAt:       time.Now().UTC(),
	})
	if err != nil {
		return xerrors.Errorf("encode status_report: %w", err)
	}
	return c.manager.Apply(data, applyTimeout).Error()
}

// ProposeACPublished implements coordination.DistributedCoordinator.
func (c *Coordinator) ProposeACPublished(generation int) error {
	data, err := EncodeProposal(ProposalACPublished, ACPublishedPayload{
		CRKey:      c.defaultCR,
		Generation: generation,
	})
	if err != nil {
		return xerrors.Errorf("encode ac_published: %w", err)
	}
	return c.manager.Apply(data, applyTimeout).Error()
}

// ProposeLeaseAllocate is the leader-side counterpart of HasLeaseFor.
// Followers' calls error out with raft.ErrNotLeader; the inline-gating code
// in the reconciler is responsible for retrying / requeueing.
func (c *Coordinator) ProposeLeaseAllocate(component, clusterName string, ttl time.Duration) error {
	data, err := EncodeProposal(ProposalLeaseAllocate, LeaseAllocatePayload{
		CRKey: c.defaultCR, Component: component, ClusterName: clusterName, TTL: ttl,
	})
	if err != nil {
		return xerrors.Errorf("encode lease_allocate: %w", err)
	}
	return c.manager.Apply(data, applyTimeout).Error()
}

// ProposeLeaseExpire is the leader-side revoke for heartbeat-TTL / stuck /
// cluster-unreachable. Used by F7's SweepStuckLeases.
func (c *Coordinator) ProposeLeaseExpire(component, clusterName, reason string) error {
	data, err := EncodeProposal(ProposalLeaseExpire, LeaseExpirePayload{
		CRKey: c.defaultCR, Component: component, ClusterName: clusterName, Reason: reason,
	})
	if err != nil {
		return xerrors.Errorf("encode lease_expire: %w", err)
	}
	return c.manager.Apply(data, applyTimeout).Error()
}

// GetActiveLease implements coordination.DistributedCoordinator.
func (c *Coordinator) GetActiveLease() *coordination.LeaseInfo {
	l := c.fsm.GetActiveLease(c.defaultCR)
	if l == nil {
		return nil
	}
	return &coordination.LeaseInfo{Component: l.Component, ClusterName: l.ClusterName}
}

// GetPerClusterStatus implements coordination.DistributedCoordinator.
func (c *Coordinator) GetPerClusterStatus() map[string]coordination.ClusterStatusReport {
	cr := c.fsm.GetPerCR(c.defaultCR)
	out := make(map[string]coordination.ClusterStatusReport, len(cr.PerClusterStatus))
	for name, cs := range cr.PerClusterStatus {
		comp := make(map[string]coordination.ComponentStatus, len(cs.ComponentStatus))
		for k, v := range cs.ComponentStatus {
			comp[k] = coordination.ComponentStatus{Generation: v.Generation, Ready: v.Ready}
		}
		out[name] = coordination.ClusterStatusReport{
			ClusterName:      cs.ClusterName,
			ObservedSpecHash: cs.ObservedSpecHash,
			ComponentStatus:  comp,
			LastReconcileErr: cs.LastReconcileErr,
		}
	}
	return out
}

// GetACGeneration implements coordination.DistributedCoordinator.
func (c *Coordinator) GetACGeneration() int { return c.fsm.GetACGeneration(c.defaultCR) }

// Manager returns the underlying Manager.
func (c *Coordinator) Manager() *Manager { return c.manager }

// FSM returns the underlying FSM.
func (c *Coordinator) FSM() *FSM { return c.fsm }

// DefaultCR returns the CRKey this coordinator currently treats as "current".
func (c *Coordinator) DefaultCR() CRKey { return c.defaultCR }
