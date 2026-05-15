package coordraft

import (
	"sync"
	"time"

	"github.com/hashicorp/raft"

	"github.com/mongodb/mongodb-kubernetes/pkg/coordination"
)

// StuckDetectorConfig configures the leader-side stuck-step + heartbeat-TTL
// + cluster-unreachable revoke loop. Defaults match the architecture doc.
type StuckDetectorConfig struct {
	// HeartbeatTTL is how long a lease may go without a StatusReport from
	// the holder before the leader revokes it. Default 60s.
	HeartbeatTTL time.Duration
	// DeadlineCap is the hard maximum a lease may live regardless of
	// heartbeats. Default 30min.
	DeadlineCap time.Duration
	// StuckThreshold is how long a progress signature must remain
	// unchanged before the leader revokes for "stuck-step". Default 10min.
	StuckThreshold time.Duration
	// ClusterDownThreshold is how stale LastContact must be before the
	// leader treats the lease holder's cluster as unreachable. Default 120s.
	ClusterDownThreshold time.Duration
	// WarmupAfterAllocate suppresses stuck detection for this long after
	// a fresh allocate to let the holder start reporting. Default 30s.
	WarmupAfterAllocate time.Duration
}

func (c *StuckDetectorConfig) setDefaults() {
	// Treat zero values as "not set" and substitute defaults. Tests that
	// want a zero value (e.g. WarmupAfterAllocate = 0) should set a small
	// positive (1ns) instead. This keeps the API ergonomic for production
	// callers who default-construct the struct.
	if c.HeartbeatTTL == 0 {
		c.HeartbeatTTL = 60 * time.Second
	}
	if c.DeadlineCap == 0 {
		c.DeadlineCap = 30 * time.Minute
	}
	if c.StuckThreshold == 0 {
		c.StuckThreshold = 10 * time.Minute
	}
	if c.ClusterDownThreshold == 0 {
		c.ClusterDownThreshold = 120 * time.Second
	}
	if c.WarmupAfterAllocate == 0 {
		c.WarmupAfterAllocate = 30 * time.Second
	}
}

// progressKey is the in-memory map key for last-seen progress per scope.
type progressKey struct {
	CR        string
	Component string
	Cluster   string
}

// progressMemo is the last-seen progress signature for one scope.
type progressMemo struct {
	signature uint64
	seenAt    time.Time
}

// StuckDetector lives on the leader's Coordinator and tracks per-lease
// progress signatures. SweepLeases (invoked by the controller's periodic
// tick or each reconcile) checks every active lease across all known CRs and
// proposes LeaseExpire for any that have:
//   - HeartbeatAt older than HeartbeatTTL
//   - AllocatedAt older than DeadlineCap
//   - Same progress signature for >StuckThreshold (after warmup)
//   - LastContact for the holder's cluster older than ClusterDownThreshold
type StuckDetector struct {
	cfg   StuckDetectorConfig
	coord *Coordinator

	mu     sync.Mutex
	memo   map[progressKey]progressMemo
	nowFn  func() time.Time

	// Test hooks (private to package).
	contactByCluster map[string]time.Duration
}

// NewStuckDetector binds a detector to a Coordinator.
func NewStuckDetector(coord *Coordinator, cfg StuckDetectorConfig) *StuckDetector {
	cfg.setDefaults()
	return &StuckDetector{
		cfg:              cfg,
		coord:            coord,
		memo:             map[progressKey]progressMemo{},
		nowFn:            func() time.Time { return time.Now().UTC() },
		contactByCluster: map[string]time.Duration{},
	}
}

// SetNowFn lets tests inject a deterministic clock.
func (s *StuckDetector) SetNowFn(fn func() time.Time) { s.nowFn = fn }

// SetContactOverride lets tests inject a per-cluster LastContact value to
// avoid plumbing real raft Stats() / Observers in unit tests.
func (s *StuckDetector) SetContactOverride(m map[string]time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.contactByCluster = make(map[string]time.Duration, len(m))
	for k, v := range m {
		s.contactByCluster[k] = v
	}
}

// SweepLeases walks every PerCR entry's ActiveLeases map and proposes
// LeaseExpire if any of the four conditions hits per lease. Returns the
// count of revokes proposed (useful for tests).
func (s *StuckDetector) SweepLeases() int {
	if !s.coord.IsLeader() {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowFn()
	state := s.coord.fsm.GetState()
	revoked := 0

	for crStr, perCR := range state.PerCR {
		for _, lease := range perCR.ActiveLeases {
			if lease == nil {
				continue
			}
			// Heartbeat-TTL.
			if !lease.HeartbeatAt.IsZero() && now.Sub(lease.HeartbeatAt) > s.cfg.HeartbeatTTL {
				s.expire(crStr, lease, "heartbeat-ttl")
				revoked++
				continue
			}
			// Hard deadline.
			if !lease.DeadlineAt.IsZero() && now.After(lease.DeadlineAt) {
				s.expire(crStr, lease, "deadline")
				revoked++
				continue
			}
			// Cluster-unreachable.
			contactAge := s.lastContactFor(lease.ClusterName)
			if contactAge > s.cfg.ClusterDownThreshold {
				s.expire(crStr, lease, "cluster-unreachable")
				revoked++
				continue
			}
			// Stuck-step (only after warmup).
			if now.Sub(lease.AllocatedAt) <= s.cfg.WarmupAfterAllocate {
				continue
			}
			cs, hasCluster := perCR.PerClusterStatus[lease.ClusterName]
			if !hasCluster {
				continue
			}
			comp, hasComp := cs.ComponentStatus[lease.Component]
			if !hasComp {
				continue
			}
			sig := progressSignature(cs, comp)
			key := progressKey{CR: crStr, Component: lease.Component, Cluster: lease.ClusterName}
			prev, ok := s.memo[key]
			if !ok || prev.signature != sig {
				s.memo[key] = progressMemo{signature: sig, seenAt: now}
				continue
			}
			if now.Sub(prev.seenAt) > s.cfg.StuckThreshold {
				s.expire(crStr, lease, "stuck")
				delete(s.memo, key)
				revoked++
			}
		}
	}

	return revoked
}

func (s *StuckDetector) expire(crStr string, lease *Lease, reason string) {
	// Parse crStr back to CRKey via fast path: the FSM stores keys as
	// CRKey.String() == "Kind/Namespace/Name". For robustness we use the
	// stored field on lease via reading the FSM — but lease itself doesn't
	// carry CRKey. We split crStr.
	kind, ns, name := splitCRKeyString(crStr)
	k := CRKey{Kind: kind, Namespace: ns, Name: name}
	data, err := EncodeProposal(ProposalLeaseExpire, LeaseExpirePayload{
		CRKey: k, Component: lease.Component, ClusterName: lease.ClusterName, Reason: reason,
	})
	if err != nil {
		return
	}
	_ = s.coord.applyProposal(data, applyTimeout)
}

// progressSignature combines the fields that count as "progress" into a
// stable 64-bit hash. Two reports producing the same signature are deemed
// "no progress". Changes in current_replicas, ready_replicas,
// observed_generation, ready, generation, or per-component Ready flips all
// count.
func progressSignature(cs ClusterStatus, comp ComponentStatus) uint64 {
	// FNV-1a 64-bit, hand-rolled (no extra dep).
	const fnvOffset = uint64(14695981039346656037)
	const fnvPrime = uint64(1099511628211)
	h := fnvOffset
	mix := func(v uint64) {
		for i := 0; i < 8; i++ {
			h ^= (v >> (i * 8)) & 0xff
			h *= fnvPrime
		}
	}
	mix(uint64(comp.Generation))
	if comp.Ready {
		mix(1)
	} else {
		mix(0)
	}
	mix(uint64(cs.LastReportedAt.UnixNano() / int64(time.Second))) // second-precision
	mix(uint64(len(cs.ComponentStatus)))
	return h
}

func splitCRKeyString(s string) (kind, ns, name string) {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			kind = s[:i]
			rest := s[i+1:]
			for j := 0; j < len(rest); j++ {
				if rest[j] == '/' {
					ns = rest[:j]
					name = rest[j+1:]
					return
				}
			}
			return kind, rest, ""
		}
	}
	return s, "", ""
}

// lastContactFor returns the test override if set, else the Coordinator's
// LastContact reading. Called under s.mu.
func (s *StuckDetector) lastContactFor(cluster string) time.Duration {
	if v, ok := s.contactByCluster[cluster]; ok {
		return v
	}
	return s.coord.LastContact(cluster)
}

// ============================================================================
// Convenience wrapper on Coordinator
// ============================================================================

// SweepStuckLeases is the controller-facing entry point. It constructs a
// StuckDetector with default config (or reuses a cached one), invokes
// SweepLeases, and returns the revoke count.
//
// Most callers will hold their own *StuckDetector across reconciles. This
// helper exists for tests / one-shot invocations.
func (c *Coordinator) SweepStuckLeases() int {
	d := NewStuckDetector(c, StuckDetectorConfig{})
	return d.SweepLeases()
}

// PeerLastContact is a small helper exposed on Coordinator for the
// stuck detector to read per-peer LastContact via raft.Stats(). It is
// otherwise indistinguishable from LastContact but takes a raft.ServerID
// directly (useful when the cluster→ID map is empty).
func (c *Coordinator) PeerLastContact(id raft.ServerID) time.Duration {
	for name, sid := range c.peerByCluster {
		if sid == id {
			return c.LastContact(name)
		}
	}
	return 0
}

// ProgressSnapshotEntryFromSnapshot — helper used by F8/F9/F10 tests that
// want to read the last-known progress for a scope without exporting the
// internal memo map.
func (s *StuckDetector) ProgressSnapshotEntryFromSnapshot(k coordination.CRKey, component, cluster string) (sig uint64, seenAt time.Time, ok bool) { //nolint:unused
	s.mu.Lock()
	defer s.mu.Unlock()
	key := progressKey{CR: toRaftCRKey(k).String(), Component: component, Cluster: cluster}
	memo, found := s.memo[key]
	if !found {
		return 0, time.Time{}, false
	}
	return memo.signature, memo.seenAt, true
}
