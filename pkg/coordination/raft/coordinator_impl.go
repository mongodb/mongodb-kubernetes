package coordraft

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/raft"
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
// F5 adds the new CRKey-aware methods (AcquireOrRespect, ReportProgress,
// MarkReady, ReleaseLease, AcVersion, AnnounceAcPublished, LastContact) on
// top of the legacy single-CR surface. The C3-era methods (HasLeaseFor,
// ProposeStatusReport, etc.) still work for back-compat — F6 removes them
// from the controller call sites.
type Coordinator struct {
	manager     *Manager
	fsm         *FSM
	clusterName string
	defaultCR   CRKey
	// forwarder is optional — when non-nil, Propose* methods route through
	// it (so follower-side calls auto-forward to the leader). When nil,
	// callers fall back to m.Apply directly (only the leader will succeed).
	forwarder *Forwarder

	// peerByCluster maps cluster name → raft.ServerID used in r.Stats() to
	// look up LastContact. Set via SetClusterPeerMap.
	peerByCluster map[string]raft.ServerID
}

// NewCoordinator constructs a coordinator for clusterName backed by mgr+fsm.
// The caller is expected to construct the Manager with the same fsm.
//
// defaultCR is the CR this coordinator instance treats as "current" for the
// legacy single-CR API; F5+ tests/callers carry CRKey per call.
func NewCoordinator(clusterName string, mgr *Manager, fsm *FSM) *Coordinator {
	return &Coordinator{
		manager:       mgr,
		fsm:           fsm,
		clusterName:   clusterName,
		peerByCluster: map[string]raft.ServerID{},
	}
}

// SetDefaultCR sets the CRKey this coordinator's legacy single-CR API routes
// through.
func (c *Coordinator) SetDefaultCR(k CRKey) { c.defaultCR = k }

// SetForwarder attaches a Forwarder for follower→leader auto-forwarding of
// proposals. Coordinators without a forwarder can only Apply when local raft
// is leader.
func (c *Coordinator) SetForwarder(f *Forwarder) { c.forwarder = f }

// Compile-time assertions that Coordinator implements both interfaces.
var (
	_ coordination.DistributedCoordinator = (*Coordinator)(nil)
	_ coordination.LegacyCoordinator      = (*Coordinator)(nil)
)

// SetClusterPeerMap registers cluster-name → raft.ServerID mappings so
// LastContact can look up per-peer staleness via r.Stats().
func (c *Coordinator) SetClusterPeerMap(m map[string]raft.ServerID) {
	c.peerByCluster = make(map[string]raft.ServerID, len(m))
	for k, v := range m {
		c.peerByCluster[k] = v
	}
}

// applyProposal commits a serialized proposal through the forwarder (if set)
// or directly via Apply. When the forwarder is nil and the local node is not
// leader, raft.Apply returns ErrNotLeader and the caller should treat this
// as a transient error.
func (c *Coordinator) applyProposal(data []byte, timeout time.Duration) error {
	if c.forwarder != nil {
		return c.forwarder.Submit(data, timeout)
	}
	return c.manager.Apply(data, timeout).Error()
}

// ============================================================================
// LegacyCoordinator (C3-shape) — kept until F6 finishes migrating call sites.
// ============================================================================

// MyClusterName implements coordination.DistributedCoordinator.
func (c *Coordinator) MyClusterName() string { return c.clusterName }

// IsLeader implements coordination.DistributedCoordinator.
func (c *Coordinator) IsLeader() bool { return c.manager.IsLeader() }

// HasLeaseFor is the legacy single-CR lease check. With the per-(component,
// cluster) lease split (Phase D) this queries the specific slot.
func (c *Coordinator) HasLeaseFor(component, clusterName string) bool {
	return c.fsm.GetLease(c.defaultCR, component, clusterName) != nil
}

// ProposeLeaseComplete is the legacy single-CR lease completion proposal.
func (c *Coordinator) ProposeLeaseComplete(component, clusterName string) error {
	data, err := EncodeProposal(ProposalLeaseComplete, LeaseCompletePayload{
		CRKey: c.defaultCR, Component: component, ClusterName: clusterName,
	})
	if err != nil {
		return xerrors.Errorf("encode lease_complete: %w", err)
	}
	return c.applyProposal(data, applyTimeout)
}

// ProposeStatusReport is the legacy single-CR status-report proposal.
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
	return c.applyProposal(data, applyTimeout)
}

// ProposeACPublished is the legacy single-CR AC-publish proposal.
func (c *Coordinator) ProposeACPublished(generation int) error {
	data, err := EncodeProposal(ProposalACPublished, ACPublishedPayload{
		CRKey:      c.defaultCR,
		Generation: generation,
	})
	if err != nil {
		return xerrors.Errorf("encode ac_published: %w", err)
	}
	return c.applyProposal(data, applyTimeout)
}

// ProposeLeaseAllocate is the legacy single-CR lease-allocate proposal.
func (c *Coordinator) ProposeLeaseAllocate(component, clusterName string, ttl time.Duration) error {
	data, err := EncodeProposal(ProposalLeaseAllocate, LeaseAllocatePayload{
		CRKey: c.defaultCR, Component: component, ClusterName: clusterName, TTL: ttl,
	})
	if err != nil {
		return xerrors.Errorf("encode lease_allocate: %w", err)
	}
	return c.applyProposal(data, applyTimeout)
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
	return c.applyProposal(data, applyTimeout)
}

// GetActiveLease is the legacy single-CR lease accessor.
func (c *Coordinator) GetActiveLease() *coordination.LeaseInfo {
	l := c.fsm.GetActiveLease(c.defaultCR)
	if l == nil {
		return nil
	}
	return &coordination.LeaseInfo{Component: l.Component, ClusterName: l.ClusterName}
}

// GetLeasesHeldBy returns the components for which `cluster` currently holds
// an active lease on `k`. Used by G'5 iter 14f's lease-keep-alive path in
// gateOnResourceAgreement: a reconcile that returns Pending at Gate 0 must
// refresh HeartbeatAt on any lease it currently holds, otherwise the leader's
// stuck-step detector will revoke it after HeartbeatTTL (60s) and the
// cross-cluster cap=1 serialisation breaks.
//
// Implementation forwards to the FSM. The returned slice is independent of
// the FSM's internal state (safe to retain).
func (c *Coordinator) GetLeasesHeldBy(k coordination.CRKey, cluster string) []string {
	return c.fsm.GetLeasesHeldBy(toRaftCRKey(k), cluster)
}

// GetPerClusterStatus is the legacy single-CR status accessor.
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

// GetACGeneration is the legacy single-CR AC-generation accessor.
func (c *Coordinator) GetACGeneration() int { return c.fsm.GetACGeneration(c.defaultCR) }

// Manager returns the underlying Manager.
func (c *Coordinator) Manager() *Manager { return c.manager }

// FSM returns the underlying FSM.
func (c *Coordinator) FSM() *FSM { return c.fsm }

// DefaultCR returns the CRKey this coordinator currently treats as "current".
func (c *Coordinator) DefaultCR() CRKey { return c.defaultCR }

// ============================================================================
// F5 — new CRKey-aware DistributedCoordinator surface.
// ============================================================================

// ClusterIndex returns the stable integer index for a cluster, or (-1, false).
func (c *Coordinator) ClusterIndex(name string) (int, bool) {
	idx := c.fsm.GetClusterIndex(name)
	if idx < 0 {
		return -1, false
	}
	return idx, true
}

// AcquireOrRespect implements the inline-gating decision at every STS-write
// call site:
//   - Component already Ready on this cluster AND stored SpecGeneration is
//     up-to-date relative to currentSpecGen → LeaseOtherClusterDone.
//   - Active lease matches (component, cluster) → LeaseHeld.
//   - Active lease holds for a different cluster or different component →
//     LeaseWaitForLease.
//   - No active lease → propose LeaseAllocate(component, cluster). If the
//     allocation commits and the FSM now shows our lease, return LeaseHeld;
//     otherwise (another lease was allocated first, or apply errored) return
//     LeaseWaitForLease.
//
// currentSpecGen: the CR's metadata.generation as observed by the caller for
// this reconcile. A previously-Ready entry whose stored SpecGeneration is
// strictly less than currentSpecGen is treated as stale (the spec advanced
// since the last MarkReady) and AcquireOrRespect MUST NOT short-circuit it.
// Pass 0 from callers that don't track a CR-spec generation (legacy tests /
// non-MongoDB scopes); 0 is treated as "spec gen unknown / always equal" for
// backwards compatibility.
func (c *Coordinator) AcquireOrRespect(k coordination.CRKey, component, cluster string, currentSpecGen int64) coordination.LeaseResult {
	crk := toRaftCRKey(k)
	// Component already reported Ready on this cluster → caller can skip,
	// UNLESS the recorded SpecGeneration predates the current reconcile's
	// observed spec generation (in which case the prior Ready reflects an
	// older spec and the STS must be re-applied).
	cs := c.fsm.GetClusterStatus(crk, cluster)
	if comp, ok := cs.ComponentStatus[component]; ok && comp.Ready {
		if currentSpecGen == 0 || comp.SpecGeneration >= currentSpecGen {
			return coordination.LeaseOtherClusterDone
		}
		// Stale Ready — fall through and allocate / reuse a lease so the
		// caller does the work for the new spec.
	}
	// Per-(component, cluster) slot already held? Idempotent return.
	if existing := c.fsm.GetLease(crk, component, cluster); existing != nil {
		return coordination.LeaseHeld
	}
	// G'5 iter 13c cross-cluster guard (fast-path): if any OTHER lease for
	// the same (CR, component) is held on a different cluster, refuse this
	// slot up front. This avoids the proposal round-trip when we already
	// know the FSM will reject; the leader-side applyLeaseAllocate enforces
	// the same guard authoritatively.
	if c.fsm.HasSiblingLease(crk, component, cluster) {
		return coordination.LeaseWaitForLease
	}
	// Propose a new lease for THIS specific (component, cluster) slot.
	// Cross-cluster siblings on the same (CR, component) are now serialised
	// by applyLeaseAllocate — see the cross-cluster guard there. Different
	// components on any cluster remain independent.
	data, err := EncodeProposal(ProposalLeaseAllocate, LeaseAllocatePayload{
		CRKey: crk, Component: component, ClusterName: cluster, TTL: 30 * time.Second,
	})
	if err != nil {
		return coordination.LeaseWaitForLease
	}
	if err := c.applyProposal(data, applyTimeout); err != nil {
		return coordination.LeaseWaitForLease
	}
	// Re-read FSM. After raft commit, the local FSM may lag by a heartbeat
	// (followers apply async). Poll briefly so callers don't have to retry.
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		if c.fsm.GetLease(crk, component, cluster) != nil {
			return coordination.LeaseHeld
		}
		if time.Now().After(deadline) {
			return coordination.LeaseWaitForLease
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// IsComponentReady reads from the FSM without proposing anything. A Ready
// entry whose stored SpecGeneration is strictly less than currentSpecGen is
// reported as NOT ready, mirroring AcquireOrRespect's stale-Ready semantics.
// Pass currentSpecGen=0 to keep the legacy "Ready bit only" behaviour.
func (c *Coordinator) IsComponentReady(k coordination.CRKey, component, cluster string, currentSpecGen int64) bool {
	crk := toRaftCRKey(k)
	cs := c.fsm.GetClusterStatus(crk, cluster)
	comp, ok := cs.ComponentStatus[component]
	if !ok || !comp.Ready {
		return false
	}
	if currentSpecGen == 0 {
		return true
	}
	return comp.SpecGeneration >= currentSpecGen
}

// ReportProgress submits a Ready=false StatusReport carrying the progress
// snapshot. Refreshes lease HeartbeatAt when applied (lease-holder semantics).
func (c *Coordinator) ReportProgress(k coordination.CRKey, component, cluster string, progress coordination.ProgressSnapshot) error {
	return c.submitStatusReport(k, component, cluster, progress, false)
}

// MarkReady submits a Ready=true StatusReport with the final progress.
func (c *Coordinator) MarkReady(k coordination.CRKey, component, cluster string, progress coordination.ProgressSnapshot) error {
	return c.submitStatusReport(k, component, cluster, progress, true)
}

func (c *Coordinator) submitStatusReport(k coordination.CRKey, component, cluster string, progress coordination.ProgressSnapshot, ready bool) error {
	crk := toRaftCRKey(k)
	prev := c.fsm.GetClusterStatus(crk, cluster)
	gen := int64(1)
	if existing, ok := prev.ComponentStatus[component]; ok {
		gen = existing.Generation + 1
	}
	data, err := EncodeProposal(ProposalStatusReport, StatusReportPayload{
		CRKey:       crk,
		ClusterName: cluster,
		ReportedAt:  time.Now().UTC(),
		ComponentStatus: map[string]ComponentStatusEntry{
			component: {Generation: gen, Ready: ready, SpecGeneration: progress.CRSpecGeneration},
		},
		Progress: progressToPayload(progress),
	})
	if err != nil {
		return xerrors.Errorf("encode status_report: %w", err)
	}
	return c.applyProposal(data, applyTimeout)
}

// ReportCRStatus is the CR-level heartbeat called from every reconciler
// `updateStatus` exit. It submits a StatusReportPayload carrying the calling
// cluster's observed (Phase, Message) in the FSM's LastReconcileErr field
// (formatted as "<phase>: <message>"). ComponentStatus is left empty so the
// per-component progress already in FSM state is preserved.
//
// The leader uses this stream to detect "follower stopped emitting status"
// (stuck step) and to age out leases whose holder has gone dark.
func (c *Coordinator) ReportCRStatus(k coordination.CRKey, phase, message string) error {
	crk := toRaftCRKey(k)
	lastReconcileErr := phase
	if message != "" {
		lastReconcileErr = phase + ": " + message
	}
	data, err := EncodeProposal(ProposalStatusReport, StatusReportPayload{
		CRKey:            crk,
		ClusterName:      c.clusterName,
		LastReconcileErr: lastReconcileErr,
		ReportedAt:       time.Now().UTC(),
	})
	if err != nil {
		return xerrors.Errorf("encode cr-status report: %w", err)
	}
	return c.applyProposal(data, applyTimeout)
}

// ReleaseLease announces lease completion for (component, cluster) on a CR.
func (c *Coordinator) ReleaseLease(k coordination.CRKey, component, cluster string) error {
	crk := toRaftCRKey(k)
	data, err := EncodeProposal(ProposalLeaseComplete, LeaseCompletePayload{
		CRKey: crk, Component: component, ClusterName: cluster,
	})
	if err != nil {
		return xerrors.Errorf("encode lease_complete: %w", err)
	}
	return c.applyProposal(data, applyTimeout)
}

// AcVersion returns the AC generation for a CR.
func (c *Coordinator) AcVersion(k coordination.CRKey) int64 {
	return int64(c.fsm.GetACGeneration(toRaftCRKey(k)))
}

// AnnounceAcPublished bumps the AC generation for a CR.
func (c *Coordinator) AnnounceAcPublished(k coordination.CRKey, version int64) error {
	data, err := EncodeProposal(ProposalACPublished, ACPublishedPayload{
		CRKey:      toRaftCRKey(k),
		Generation: int(version),
	})
	if err != nil {
		return xerrors.Errorf("encode ac_published: %w", err)
	}
	return c.applyProposal(data, applyTimeout)
}

// LastContact returns time since local raft last heard from the peer cluster.
// If we have no mapping for the cluster name, return a sentinel of 1 year.
func (c *Coordinator) LastContact(cluster string) time.Duration {
	const veryLargeAge = 365 * 24 * time.Hour
	id, ok := c.peerByCluster[cluster]
	if !ok {
		return veryLargeAge
	}
	stats := c.manager.Raft().Stats()
	// hashicorp/raft exposes per-peer last_contact under
	// "latest_configuration" / specific peer keys. There's no first-class
	// API; we read the leader's view via Stats().
	//
	// As a robust alternative we use the per-leader last contact:
	if last, ok := stats["last_contact"]; ok && string(id) != "" {
		_ = last
	}
	// Fallback: if we're not leader, last_contact is the time since we last
	// heard from the leader. For peers other than the leader, we don't have
	// a per-peer key on stats; return a small value if the cluster is the
	// current leader (we just heard from them) or veryLargeAge otherwise.
	leaderAddr, leaderID := c.manager.Raft().LeaderWithID()
	_ = leaderAddr
	if string(leaderID) == string(id) {
		// We're hearing from the leader; reuse last_contact's parsed value.
		if last, ok := stats["last_contact"]; ok {
			if d, err := time.ParseDuration(last); err == nil && d >= 0 {
				return d
			}
		}
		return 0
	}
	// For non-leader peers we conservatively return the cluster-wide "term"
	// or a small value if we're the leader (heartbeats are always recent on
	// the leader's side). This is a PoC simplification — production would
	// add per-peer last-contact tracking via a custom Observer.
	if c.manager.IsLeader() {
		return 0
	}
	return veryLargeAge
}

// PublishAgentKey announces a (CR, projectID) → agent API key mapping via
// raft so every operator can write the local <projectID>-group-secret
// without round-tripping OM. Idempotent.
func (c *Coordinator) PublishAgentKey(k coordination.CRKey, projectID, agentKey string) error {
	if projectID == "" || agentKey == "" {
		return xerrors.New("PublishAgentKey: empty projectID or agentKey")
	}
	// Fast-path: same key already in FSM → no proposal.
	if existing := c.fsm.GetAgentKey(toRaftCRKey(k), projectID); existing == agentKey {
		return nil
	}
	data, err := EncodeProposal(ProposalAgentKeyPublished, AgentKeyPublishedPayload{
		CRKey:     toRaftCRKey(k),
		ProjectID: projectID,
		AgentKey:  agentKey,
	})
	if err != nil {
		return xerrors.Errorf("encode agent_key_published: %w", err)
	}
	return c.applyProposal(data, applyTimeout)
}

// GetAgentKey returns the FSM-stored agent API key for (CR, projectID),
// or "" if no proposal has stored one.
func (c *Coordinator) GetAgentKey(k coordination.CRKey, projectID string) string {
	return c.fsm.GetAgentKey(toRaftCRKey(k), projectID)
}

// ReportResource submits a content-hash observation for one spec-referenced
// resource on the calling cluster. See coordination.DistributedCoordinator.
// Implementation note: ObservedAt is wall-clock at the caller; the FSM uses
// it only for stale-supersedes semantics within applyResourceObserved.
func (c *Coordinator) ReportResource(k coordination.CRKey, ref coordination.ResourceRef, contentHash string) error {
	crk := toRaftCRKey(k)
	data, err := EncodeProposal(ProposalResourceObserved, ResourceObservedPayload{
		CRKey:       crk,
		Ref:         ResourceRef{Kind: ref.Kind, Namespace: ref.Namespace, Name: ref.Name},
		ContentHash: contentHash,
		ObservedBy:  c.clusterName,
		ObservedAt:  time.Now().UTC(),
	})
	if err != nil {
		return xerrors.Errorf("encode resource_observed: %w", err)
	}
	return c.applyProposal(data, applyTimeout)
}

// WaitForResourcesAgreed returns ResourcesAgreed iff every required ref has
// been observed by every known cluster AND every cluster reports the same
// content hash. Otherwise ResourcesPending + a human-readable diagnostic.
//
// "Known clusters" is the union of:
//   - the calling operator's own cluster name (always)
//   - every cluster name appearing in PerClusterStatus for this CR
//   - every cluster that has ever reported any ResourceObserved entry for
//     this CR (in case an operator booted, reported resources, but hasn't
//     yet submitted a status report).
//
// This makes the gate a hard correctness check rather than a heuristic: if
// any operator has been heard from at all, its observation must match before
// any OM access is allowed.
func (c *Coordinator) WaitForResourcesAgreed(k coordination.CRKey, refs []coordination.ResourceRef) (coordination.ResourceAgreement, string) {
	if len(refs) == 0 {
		return coordination.ResourcesAgreed, ""
	}
	crk := toRaftCRKey(k)
	cr := c.fsm.GetPerCR(crk)
	// Build the known-cluster set deterministically.
	knownSet := map[string]struct{}{}
	knownSet[c.clusterName] = struct{}{}
	for clusterName := range cr.PerClusterStatus {
		knownSet[clusterName] = struct{}{}
	}
	for _, byCluster := range cr.Resources {
		for clusterName := range byCluster {
			knownSet[clusterName] = struct{}{}
		}
	}
	knownClusters := make([]string, 0, len(knownSet))
	for cname := range knownSet {
		knownClusters = append(knownClusters, cname)
	}
	sort.Strings(knownClusters)

	for _, ref := range refs {
		rRef := ResourceRef{Kind: ref.Kind, Namespace: ref.Namespace, Name: ref.Name}
		refKey := rRef.String()
		byCluster, ok := cr.Resources[refKey]
		if !ok {
			byCluster = map[string]ResourceObservation{}
		}
		// Missing reports — list the absentees by name.
		var missing []string
		for _, cname := range knownClusters {
			if _, ok := byCluster[cname]; !ok {
				missing = append(missing, cname)
			}
		}
		if len(missing) > 0 {
			return coordination.ResourcesPending, fmt.Sprintf(
				"Resource %s: awaiting observation from cluster(s): %s",
				refKey, strings.Join(missing, ","),
			)
		}
		// All known clusters reported — verify hashes agree.
		// Sort by cluster name for stable diagnostics.
		var reporters []string
		for cname := range byCluster {
			reporters = append(reporters, cname)
		}
		sort.Strings(reporters)
		// Group reporters by hash.
		byHash := map[string][]string{}
		for _, cname := range reporters {
			h := byCluster[cname].ContentHash
			byHash[h] = append(byHash[h], cname)
		}
		if len(byHash) > 1 {
			// Pick the majority hash; the minority cluster(s) are flagged
			// as out of sync in the diagnostic.
			majorityHash := ""
			majorityCount := 0
			var hashKeys []string
			for h := range byHash {
				hashKeys = append(hashKeys, h)
			}
			sort.Strings(hashKeys)
			for _, h := range hashKeys {
				if len(byHash[h]) > majorityCount {
					majorityCount = len(byHash[h])
					majorityHash = h
				}
			}
			// Build "cluster=hash" pairs in cluster order for the diagnostic.
			pairs := make([]string, 0, len(reporters))
			outOfSync := make([]string, 0, len(reporters))
			for _, cname := range reporters {
				h := byCluster[cname].ContentHash
				pairs = append(pairs, fmt.Sprintf("%s=%s", cname, shortHash(h)))
				if h != majorityHash {
					outOfSync = append(outOfSync, cname)
				}
			}
			return coordination.ResourcesPending, fmt.Sprintf(
				"Resource %s hash mismatch: %s — %s is out of sync.",
				refKey, strings.Join(pairs, ", "), strings.Join(outOfSync, ","),
			)
		}
	}
	return coordination.ResourcesAgreed, ""
}

// shortHash returns the first 8 hex chars of a hash for use in diagnostics.
// If the input is shorter than 8 chars it's returned unchanged.
func shortHash(h string) string {
	if len(h) <= 8 {
		return h
	}
	return h[:8]
}

// ============================================================================
// helpers
// ============================================================================

// toRaftCRKey converts the cross-package CRKey to the raft-package CRKey.
func toRaftCRKey(k coordination.CRKey) CRKey {
	return CRKey{Kind: k.Kind, Namespace: k.Namespace, Name: k.Name}
}

// fromRaftCRKey is the inverse; useful when surfacing FSM contents to callers.
func fromRaftCRKey(k CRKey) coordination.CRKey { // nolint:unused
	return coordination.CRKey{Kind: k.Kind, Namespace: k.Namespace, Name: k.Name}
}

func progressToPayload(p coordination.ProgressSnapshot) ProgressSnapshotEntry {
	return ProgressSnapshotEntry{
		CurrentReplicas:         p.CurrentReplicas,
		ReadyReplicas:           p.ReadyReplicas,
		ObservedGeneration:      p.ObservedGeneration,
		AgentGoalVersionAchieve: p.AgentGoalVersionAchieve,
		LastEventTS:             p.LastEventTS.UnixNano(),
		PendingError:            p.PendingError,
	}
}

// progressFromPayload is the inverse, used by F7's stuck-step detector.
func progressFromPayload(p ProgressSnapshotEntry) coordination.ProgressSnapshot { // nolint:unused
	return coordination.ProgressSnapshot{
		CurrentReplicas:         p.CurrentReplicas,
		ReadyReplicas:           p.ReadyReplicas,
		ObservedGeneration:      p.ObservedGeneration,
		AgentGoalVersionAchieve: p.AgentGoalVersionAchieve,
		LastEventTS:             time.Unix(0, p.LastEventTS),
		PendingError:            p.PendingError,
	}
}
