package operator

import (
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

// Elector is the seam between the decentralized controllers and the leader-election machinery.
// A controller pulls its leadership belief through it exactly once per reconcile, at snapshot
// time; everything downstream (term fences on directive writes and the AC marker, takeover
// hold-off, one action per reconcile) is designed to tolerate that belief going stale the moment
// it is read.
//
// Leadership is per deployment: each MongoDBMultiCluster elects independently, because the lease
// ensemble follows that deployment's cluster list.
type Elector interface {
	// Current returns this operator's leadership belief for the given deployment. It hands out a
	// term, never a bare bool: the term rides on every guarded write so consumers can fence on it
	// even when the writer's belief was stale. isLeader == true implies the takeover hold-off has
	// already been served — the caller may perform guarded writes immediately.
	Current(deployment types.NamespacedName) (term int64, isLeader bool)
	// Events wakes the leader controller on leadership transitions through a source.Channel
	// watch — reconcile now, don't wait for a requeue. A nil channel means the elector has no
	// transitions to signal (the static elector) and the caller skips the watch.
	Events() <-chan event.GenericEvent
	// ObservedTerm is the highest leadership term this elector has seen anywhere for the
	// deployment — held, floored, or carried by any lease it observed — independent of whether
	// this operator leads. The member controllers fence stale directives against it: Current()
	// is useless for that on a follower (its term is zero when not leading), and a live forged
	// stale write proved the difference matters.
	ObservedTerm(deployment types.NamespacedName) int64
	// ObserveTermFloor pushes the term stamped in the automation config (T16): the elector
	// cannot read Ops Manager, so the leader controller reports the floor after every snapshot.
	// Candidacies start above it; a floor raise at or above the held term re-CASes the majority
	// at floor+1 on the next renewal. Electors that never mint terms ignore it.
	ObserveTermFloor(deployment types.NamespacedName, floor int64)
}

// staticElectorTerm is the constant term handed out by StaticElector. Leadership never changes
// hands with a static elector, so the term never advances.
const staticElectorTerm int64 = 1

// StaticElector is a stand-in for the majority-lease elector: leadership is fixed at
// construction time. The operator whose cluster identity equals the designated leader cluster
// name is the leader for every deployment. It exists so the controllers' shape can be built and
// unit-tested before the election protocol lands; the real elector swaps in behind the Elector
// interface without touching the controllers.
type StaticElector struct {
	selfClusterName   string
	leaderClusterName string
}

var _ Elector = &StaticElector{}

// NewStaticElector designates leaderClusterName as the leader for every deployment.
// selfClusterName is this operator's identity (GetOperatorClusterName); an empty identity is
// never leader.
func NewStaticElector(selfClusterName, leaderClusterName string) *StaticElector {
	return &StaticElector{selfClusterName: selfClusterName, leaderClusterName: leaderClusterName}
}

func (e *StaticElector) Current(types.NamespacedName) (term int64, isLeader bool) {
	return staticElectorTerm, e.selfClusterName != "" && e.selfClusterName == e.leaderClusterName
}

// ObservedTerm: static leadership has exactly one term, and every operator has observed it.
func (e *StaticElector) ObservedTerm(types.NamespacedName) int64 {
	return staticElectorTerm
}

// Events returns nil: static leadership never transitions, there is nothing to wake anyone for.
func (e *StaticElector) Events() <-chan event.GenericEvent {
	return nil
}

// ObserveTermFloor is a no-op: the static elector never mints terms.
func (e *StaticElector) ObserveTermFloor(types.NamespacedName, int64) {}
