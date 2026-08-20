package operator

import (
	"k8s.io/apimachinery/pkg/types"
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
