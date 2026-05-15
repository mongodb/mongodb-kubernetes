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
type Coordinator struct {
	manager     *Manager
	fsm         *FSM
	clusterName string
}

// NewCoordinator constructs a coordinator for clusterName backed by mgr+fsm.
// The caller is expected to construct the Manager with the same fsm.
func NewCoordinator(clusterName string, mgr *Manager, fsm *FSM) *Coordinator {
	return &Coordinator{
		manager:     mgr,
		fsm:         fsm,
		clusterName: clusterName,
	}
}

// MyClusterName implements coordination.DistributedCoordinator.
func (c *Coordinator) MyClusterName() string { return c.clusterName }

// IsLeader implements coordination.DistributedCoordinator.
func (c *Coordinator) IsLeader() bool { return c.manager.IsLeader() }

// HasLeaseFor implements coordination.DistributedCoordinator.
func (c *Coordinator) HasLeaseFor(component, clusterName string) bool {
	lease := c.fsm.GetActiveLease()
	return lease != nil && lease.Component == component && lease.ClusterName == clusterName
}

// ProposeLeaseComplete implements coordination.DistributedCoordinator.
func (c *Coordinator) ProposeLeaseComplete(component, clusterName string) error {
	data, err := EncodeProposal(ProposalLeaseComplete, LeaseCompletePayload{
		Component: component, ClusterName: clusterName,
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
	data, err := EncodeProposal(ProposalACPublished, ACPublishedPayload{Generation: generation})
	if err != nil {
		return xerrors.Errorf("encode ac_published: %w", err)
	}
	return c.manager.Apply(data, applyTimeout).Error()
}

// ProposeLeaseAllocate is the leader-side counterpart of HasLeaseFor. Followers
// must not call this; the scheduler running on the leader does.
func (c *Coordinator) ProposeLeaseAllocate(component, clusterName string, ttl time.Duration) error {
	data, err := EncodeProposal(ProposalLeaseAllocate, LeaseAllocatePayload{
		Component: component, ClusterName: clusterName, TTL: ttl,
	})
	if err != nil {
		return xerrors.Errorf("encode lease_allocate: %w", err)
	}
	return c.manager.Apply(data, applyTimeout).Error()
}

// GetActiveLease implements coordination.DistributedCoordinator.
func (c *Coordinator) GetActiveLease() *coordination.LeaseInfo {
	l := c.fsm.GetActiveLease()
	if l == nil {
		return nil
	}
	return &coordination.LeaseInfo{Component: l.Component, ClusterName: l.ClusterName}
}

// GetPerClusterStatus implements coordination.DistributedCoordinator.
func (c *Coordinator) GetPerClusterStatus() map[string]coordination.ClusterStatusReport {
	state := c.fsm.GetState()
	out := make(map[string]coordination.ClusterStatusReport, len(state.PerClusterStatus))
	for name, cs := range state.PerClusterStatus {
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
func (c *Coordinator) GetACGeneration() int { return c.fsm.GetACGeneration() }

// Manager returns the underlying Manager (for scheduler wiring).
func (c *Coordinator) Manager() *Manager { return c.manager }

// FSM returns the underlying FSM (for scheduler reads).
func (c *Coordinator) FSM() *FSM { return c.fsm }
