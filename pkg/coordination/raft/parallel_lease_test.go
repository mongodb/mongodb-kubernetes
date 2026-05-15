package coordraft

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mongodb/mongodb-kubernetes/pkg/coordination"
)

// TestParallelLeasesPerComponent — different components remain independent
// (per Phase D's parallel-lease design), so a cluster can hold (config, *) and
// (shard-0, *) at the same time without deadlock.
//
// Historical context: the original test asserted that the SAME component
// could be held by every cluster in parallel — see the regression-deadlock
// comment in the git history. G'5 iter 13c narrowed the design so that at
// most ONE cluster holds a given (CR, component) at a time (see
// TestLeaseSerializesAcrossClustersPerComponent below). The
// initial-deploy deadlock the original test guarded against was already
// dissolved by an unrelated change: with scalingFirstTime=true the operator
// calls distMarkReadyAndRelease immediately after the STS-apply step
// (without waiting on pods to reach Ready), so each cluster's lease is
// extremely short-lived during initial deploy and serialisation introduces
// no deadlock.
func TestParallelLeasesPerComponent(t *testing.T) {
	nodes, coords, leaderIdx := newCoordinatorClusterForTest(t, 3)
	leader := coords[leaderIdx]

	crKey := coordination.CRKey{Kind: "MongoDB", Namespace: "ns", Name: "cr"}
	clusterNames := []string{string(nodes[0].ID), string(nodes[1].ID), string(nodes[2].ID)}

	// cluster-0 acquires (config, cluster-0).
	require.Equal(t, coordination.LeaseHeld,
		coords[0].AcquireOrRespect(crKey, "config", clusterNames[0], 0))

	// Different components are independent across the (CR, component) axis:
	// cluster-0 can hold (shard-0, cluster-0) alongside (config, cluster-0)
	// because shard-0 and config are different component scopes.
	assert.Equal(t, coordination.LeaseHeld,
		coords[0].AcquireOrRespect(crKey, "shard-0", clusterNames[0], 0),
		"different component on same cluster must NOT block")

	require.Eventually(t, func() bool {
		return leader.HasLeaseFor("config", clusterNames[0]) &&
			leader.HasLeaseFor("shard-0", clusterNames[0])
	}, 2*time.Second, 30*time.Millisecond)

	// Within the SAME component, cross-cluster is serialised — cluster-1
	// trying (shard-0, cluster-1) while cluster-0 holds (shard-0, cluster-0)
	// must Wait. (Tested in full by TestLeaseSerializesAcrossClustersPerComponent;
	// repeated here as a quick assertion that this design property holds at
	// the shard-0 scope too, not only config.)
	assert.Equal(t, coordination.LeaseWaitForLease,
		coords[1].AcquireOrRespect(crKey, "shard-0", clusterNames[1], 0),
		"cross-cluster (shard-0, *) must wait while another cluster holds it")

	// Initial-deploy pattern: cluster-0 releases (config, *) immediately
	// after the STS-apply step (mimicking scalingFirstTime=true). cluster-1
	// can then acquire (config, cluster-1).
	require.NoError(t, coords[0].ReleaseLease(crKey, "config", clusterNames[0]))
	require.Eventually(t, func() bool {
		return coords[1].AcquireOrRespect(crKey, "config", clusterNames[1], 0) == coordination.LeaseHeld
	}, 2*time.Second, 30*time.Millisecond,
		"after cluster-0 releases (config, *), cluster-1 can acquire (config, cluster-1)")

	// (config, cluster-1) is now held by cluster-1; (shard-0, cluster-0) by
	// cluster-0. Independence across components → untouched leases remain.
	assert.True(t, leader.HasLeaseFor("shard-0", clusterNames[0]),
		"untouched (shard-0, cluster-0) must remain held")
	assert.True(t, leader.HasLeaseFor("config", clusterNames[1]),
		"untouched (config, cluster-1) must remain held")
}

// TestLeaseSerializesAcrossClustersPerComponent — G'5 iter 13c regression.
//
// For a given (CR, component), at most one cluster may hold an active lease
// at a time. Concurrent AcquireOrRespect calls from three different operators
// for (config, cluster-{1,2,3}) on the same CR must serialise: exactly one
// gets LeaseHeld, the others get LeaseWaitForLease. Once the holder releases,
// the next caller (or its next retry) can acquire.
//
// Why this matters: pod-mode rolling-restart was flapping `configSrv: 3
// NotReady` concurrently — all 3 member-cluster operators acquired their own
// (config, cluster-N) leases simultaneously and rolled their config-srv pods
// in parallel, breaking the replicaset quorum. The FSM previously had
// per-(component, cluster) independent slots (see state.go ActiveLeases) so
// applyLeaseAllocate granted every slot regardless of cross-cluster siblings.
//
// The fix narrows applyLeaseAllocate: a (component, cluster-X) allocation is
// refused (slot stays empty) when there's an existing active lease for the
// same (CR, component) on any OTHER cluster. Initial deploy (scalingFirstTime)
// is unaffected because the operator releases its lease immediately after the
// STS-apply — the next cluster's next reconcile attempt picks the slot up.
func TestLeaseSerializesAcrossClustersPerComponent(t *testing.T) {
	nodes, coords, leaderIdx := newCoordinatorClusterForTest(t, 3)
	leader := coords[leaderIdx]

	crKey := coordination.CRKey{Kind: "MongoDB", Namespace: "ns", Name: "cr"}
	clusterNames := []string{string(nodes[0].ID), string(nodes[1].ID), string(nodes[2].ID)}

	// First cluster acquires (config, cluster-0).
	res0 := coords[0].AcquireOrRespect(crKey, "config", clusterNames[0], 0)
	require.Equal(t, coordination.LeaseHeld, res0)

	// Second cluster's attempt for (config, cluster-1) must NOT short-circuit
	// to Held — the (CR, config) slot is occupied by cluster-0.
	res1 := coords[1].AcquireOrRespect(crKey, "config", clusterNames[1], 0)
	assert.Equal(t, coordination.LeaseWaitForLease, res1,
		"concurrent (config, cluster-1) must wait while (config, cluster-0) is held")

	// Third cluster's attempt for (config, cluster-2) likewise waits.
	res2 := coords[2].AcquireOrRespect(crKey, "config", clusterNames[2], 0)
	assert.Equal(t, coordination.LeaseWaitForLease, res2,
		"concurrent (config, cluster-2) must wait while (config, cluster-0) is held")

	// Leader FSM should reflect only cluster-0's lease for (config, *).
	fsmCR := leader.FSM().GetPerCR(toRaftCRKey(crKey))
	_, has0 := fsmCR.ActiveLeases[leaseKey("config", clusterNames[0])]
	_, has1 := fsmCR.ActiveLeases[leaseKey("config", clusterNames[1])]
	_, has2 := fsmCR.ActiveLeases[leaseKey("config", clusterNames[2])]
	assert.True(t, has0, "leader FSM must hold (config, cluster-0)")
	assert.False(t, has1, "leader FSM must NOT hold (config, cluster-1) while cluster-0 holds it")
	assert.False(t, has2, "leader FSM must NOT hold (config, cluster-2) while cluster-0 holds it")

	// Different component on any cluster is independent — (shard-0, cluster-1)
	// can be held while (config, cluster-0) is held.
	resShard := coords[1].AcquireOrRespect(crKey, "shard-0", clusterNames[1], 0)
	assert.Equal(t, coordination.LeaseHeld, resShard,
		"different component (shard-0, cluster-1) must NOT be serialised against (config, cluster-0)")

	// Once cluster-0 releases, the next cluster's retry succeeds.
	require.NoError(t, coords[0].ReleaseLease(crKey, "config", clusterNames[0]))
	require.Eventually(t, func() bool {
		return coords[1].AcquireOrRespect(crKey, "config", clusterNames[1], 0) == coordination.LeaseHeld
	}, 2*time.Second, 30*time.Millisecond, "after cluster-0 releases, cluster-1 must acquire (config, cluster-1)")

	// Now cluster-2's retry must STILL wait — cluster-1 holds it.
	res2b := coords[2].AcquireOrRespect(crKey, "config", clusterNames[2], 0)
	assert.Equal(t, coordination.LeaseWaitForLease, res2b,
		"(config, cluster-2) must wait while (config, cluster-1) is held")
}

// TestParallelLeasesAreCRScoped — different CRs have independent lease pools.
func TestParallelLeasesAreCRScoped(t *testing.T) {
	_, coords, leaderIdx := newCoordinatorClusterForTest(t, 3)
	leader := coords[leaderIdx]

	crA := coordination.CRKey{Kind: "MongoDB", Namespace: "ns", Name: "a"}
	crB := coordination.CRKey{Kind: "MongoDB", Namespace: "ns", Name: "b"}

	assert.Equal(t, coordination.LeaseHeld, leader.AcquireOrRespect(crA, "config", "c1", 0))
	assert.Equal(t, coordination.LeaseHeld, leader.AcquireOrRespect(crB, "config", "c1", 0))

	// Releasing on crA must not touch crB.
	require.NoError(t, leader.ReleaseLease(crA, "config", "c1"))
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		fsmA := leader.FSM().GetPerCR(toRaftCRKey(crA))
		if _, ok := fsmA.ActiveLeases[leaseKey("config", "c1")]; !ok {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	fsmA := leader.FSM().GetPerCR(toRaftCRKey(crA))
	_, hasA := fsmA.ActiveLeases[leaseKey("config", "c1")]
	fsmB := leader.FSM().GetPerCR(toRaftCRKey(crB))
	_, hasB := fsmB.ActiveLeases[leaseKey("config", "c1")]
	assert.False(t, hasA, "crA's (config, c1) should be released")
	assert.True(t, hasB, "crB's (config, c1) should still be held")
}
