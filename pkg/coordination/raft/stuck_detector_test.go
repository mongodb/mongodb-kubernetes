package coordraft

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mongodb/mongodb-kubernetes/pkg/coordination"
)

// stuckDetectorSetup builds a 3-node TCP cluster + coords, returns the
// leader's coord and a detector with the supplied config + an injectable
// clock.
//
// The clock starts at real-now + 24h so that elapsed-time checks against
// the FSM's wall-clock fields (AllocatedAt, HeartbeatAt, ReportedAt) all
// see positive durations.
func stuckDetectorSetup(t *testing.T, cfg StuckDetectorConfig) (
	*Coordinator, *StuckDetector, coordination.CRKey, string, *clock,
) {
	t.Helper()
	nodes, coords, leaderIdx := newCoordinatorClusterForTest(t, 3)
	leader := coords[leaderIdx]
	crKey := coordination.CRKey{Kind: "MongoDB", Namespace: "ns", Name: "cr"}

	cfg.setDefaults()
	d := NewStuckDetector(leader, cfg)
	c := &clock{now: time.Now().UTC().Add(24 * time.Hour)}
	d.SetNowFn(c.Now)
	return leader, d, crKey, string(nodes[leaderIdx].ID), c
}

// clock is a simple injectable monotonic clock for tests.
type clock struct{ now time.Time }

func (c *clock) Now() time.Time          { return c.now }
func (c *clock) Advance(d time.Duration) { c.now = c.now.Add(d) }

// quietCfg returns a StuckDetectorConfig with every threshold set high so
// only the field overridden in the test fires.
func quietCfg() StuckDetectorConfig {
	return StuckDetectorConfig{
		HeartbeatTTL:         48 * time.Hour,
		DeadlineCap:          48 * time.Hour,
		StuckThreshold:       48 * time.Hour,
		ClusterDownThreshold: 48 * time.Hour,
		WarmupAfterAllocate:  1 * time.Nanosecond,
	}
}

// TestStuckDetector_HeartbeatTTL — leader revokes a lease whose holder has
// not reported in HeartbeatTTL.
func TestStuckDetector_HeartbeatTTL(t *testing.T) {
	cfg := quietCfg()
	cfg.HeartbeatTTL = 100 * time.Millisecond
	leader, d, crKey, leaderName, _ := stuckDetectorSetup(t, cfg)

	require.Equal(t, coordination.LeaseHeld, leader.AcquireOrRespect(crKey, "config", leaderName, 0))
	require.NotNil(t, leader.FSM().GetActiveLease(toRaftCRKey(crKey)))

	// d.nowFn is real-now + 24h, easily past the 100ms TTL relative to
	// the FSM's HeartbeatAt (which was set to real-now by applyLeaseAllocate).
	revoked := d.SweepLeases()
	assert.Equal(t, 1, revoked)
	require.Eventually(t, func() bool {
		return leader.FSM().GetActiveLease(toRaftCRKey(crKey)) == nil
	}, 2*time.Second, 30*time.Millisecond, "lease should be revoked after heartbeat TTL")
}

// TestStuckDetector_HardDeadline — leader revokes a lease whose hard
// DeadlineAt has been reached, even when heartbeats are recent.
func TestStuckDetector_HardDeadline(t *testing.T) {
	cfg := quietCfg()
	cfg.DeadlineCap = 1 * time.Second // not actually consulted (FSM-side DeadlineAt is what matters)
	leader, d, crKey, leaderName, _ := stuckDetectorSetup(t, cfg)

	require.Equal(t, coordination.LeaseHeld, leader.AcquireOrRespect(crKey, "config", leaderName, 0))
	// FSM sets DeadlineAt = real-now + 30min. Our clock at real-now + 24h
	// is well past it, so the deadline check fires.
	revoked := d.SweepLeases()
	assert.Equal(t, 1, revoked)
}

// TestStuckDetector_ClusterUnreachable — leader revokes when LastContact for
// the holder cluster exceeds ClusterDownThreshold.
func TestStuckDetector_ClusterUnreachable(t *testing.T) {
	cfg := quietCfg()
	cfg.HeartbeatTTL = 48 * time.Hour
	cfg.DeadlineCap = 48 * time.Hour
	cfg.ClusterDownThreshold = 100 * time.Millisecond
	cfg.StuckThreshold = 48 * time.Hour
	leader, d, crKey, leaderName, c := stuckDetectorSetup(t, cfg)
	_ = c
	// Pin the detector's clock so the deadline check (FSM-side DeadlineAt =
	// real-now + 30min) does not also fire and over-count revokes.
	d.SetNowFn(func() time.Time { return time.Now().UTC() })

	require.Equal(t, coordination.LeaseHeld, leader.AcquireOrRespect(crKey, "config", leaderName, 0))
	d.SetContactOverride(map[string]time.Duration{leaderName: 5 * time.Second})

	revoked := d.SweepLeases()
	assert.Equal(t, 1, revoked)
}

// TestStuckDetector_StuckProgress — leader revokes a lease whose StatusReport
// progress signature has not changed for StuckThreshold.
func TestStuckDetector_StuckProgress(t *testing.T) {
	cfg := quietCfg()
	cfg.StuckThreshold = 500 * time.Millisecond
	cfg.WarmupAfterAllocate = 10 * time.Millisecond
	leader, d, crKey, leaderName, _ := stuckDetectorSetup(t, cfg)
	// Reset the test clock to real-now-ish.
	c := &clock{now: time.Now().UTC()}
	d.SetNowFn(c.Now)

	require.Equal(t, coordination.LeaseHeld, leader.AcquireOrRespect(crKey, "config", leaderName, 0))

	require.NoError(t, leader.ReportProgress(crKey, "config", leaderName, coordination.ProgressSnapshot{
		CurrentReplicas: 3, ReadyReplicas: 1, ObservedGeneration: 1,
	}))
	require.Eventually(t, func() bool {
		cs := leader.FSM().GetClusterStatus(toRaftCRKey(crKey), leaderName)
		_, ok := cs.ComponentStatus["config"]
		return ok
	}, 2*time.Second, 30*time.Millisecond)

	// Step past warmup so the first sweep is allowed to record the signature.
	c.Advance(20 * time.Millisecond)

	// First sweep: record signature (no revoke).
	assert.Equal(t, 0, d.SweepLeases())

	// Advance clock past stuck threshold.
	c.Advance(750 * time.Millisecond)

	revoked := d.SweepLeases()
	assert.Equal(t, 1, revoked, "should revoke after stuck threshold")
}

// TestStuckDetector_ProgressChangeResetsTimer — submitting a new
// signature resets the stuck timer.
func TestStuckDetector_ProgressChangeResetsTimer(t *testing.T) {
	cfg := quietCfg()
	cfg.StuckThreshold = 500 * time.Millisecond
	cfg.WarmupAfterAllocate = 10 * time.Millisecond
	leader, d, crKey, leaderName, _ := stuckDetectorSetup(t, cfg)
	c := &clock{now: time.Now().UTC()}
	d.SetNowFn(c.Now)

	require.Equal(t, coordination.LeaseHeld, leader.AcquireOrRespect(crKey, "config", leaderName, 0))

	require.NoError(t, leader.ReportProgress(crKey, "config", leaderName, coordination.ProgressSnapshot{
		CurrentReplicas: 3, ReadyReplicas: 1, ObservedGeneration: 1,
	}))
	require.Eventually(t, func() bool {
		_, ok := leader.FSM().GetClusterStatus(toRaftCRKey(crKey), leaderName).ComponentStatus["config"]
		return ok
	}, 2*time.Second, 30*time.Millisecond)
	c.Advance(20 * time.Millisecond)
	// First sweep: record signature.
	assert.Equal(t, 0, d.SweepLeases())

	// Advance ALMOST to threshold, then submit a progress change.
	c.Advance(300 * time.Millisecond)
	require.NoError(t, leader.ReportProgress(crKey, "config", leaderName, coordination.ProgressSnapshot{
		CurrentReplicas: 3, ReadyReplicas: 2, ObservedGeneration: 1,
	}))
	require.Eventually(t, func() bool {
		comp := leader.FSM().GetClusterStatus(toRaftCRKey(crKey), leaderName).ComponentStatus["config"]
		return comp.Generation >= 2
	}, 2*time.Second, 30*time.Millisecond)

	// New signature observed — sweep records but doesn't revoke.
	assert.Equal(t, 0, d.SweepLeases())

	// Advance past threshold; seenAt was just reset so still no revoke.
	c.Advance(450 * time.Millisecond)
	assert.Equal(t, 0, d.SweepLeases())
}

// TestStuckDetector_FollowerDoesNotRevoke — a detector bound to a follower
// must never propose LeaseExpire (its Apply would error out).
func TestStuckDetector_FollowerDoesNotRevoke(t *testing.T) {
	nodes, coords, leaderIdx := newCoordinatorClusterForTest(t, 3)
	followerIdx := (leaderIdx + 1) % 3
	follower := coords[followerIdx]
	cfg := quietCfg()
	cfg.HeartbeatTTL = 1 * time.Nanosecond
	d := NewStuckDetector(follower, cfg)

	leader := coords[leaderIdx]
	crKey := coordination.CRKey{Kind: "MongoDB", Namespace: "ns", Name: "cr"}
	require.Equal(t, coordination.LeaseHeld, leader.AcquireOrRespect(crKey, "config", string(nodes[leaderIdx].ID), 0))

	d.SetNowFn(func() time.Time { return time.Now().UTC().Add(time.Hour) })
	revoked := d.SweepLeases()
	assert.Equal(t, 0, revoked, "follower must not propose LeaseExpire")
}
