package search

import (
	"github.com/mongodb/mongodb-kubernetes/api/v1/status"
)

// phaseRank assigns a severity rank to each Phase. Higher == worse.
// Unknown phases get rank -1 so any known phase beats them.
func phaseRank(p status.Phase) int {
	switch p {
	case status.PhaseFailed:
		return 5
	case status.PhasePending:
		return 4
	case status.PhaseRunning:
		return 3
	case status.PhaseUpdated:
		return 2
	case status.PhaseDisabled:
		return 1
	case status.PhaseUnsupported:
		return 0
	default:
		return -1
	}
}

// WorstOfPhase returns the most severe Phase among the inputs
// (Failed > Pending > Running > Updated > Disabled > Unsupported).
// Empty input returns the empty Phase. Any known Phase wins over an unknown
// Phase; with only unknown inputs, the first one is returned.
func WorstOfPhase(phases ...status.Phase) status.Phase {
	// worstRank starts below the unknown-phase rank (-1) so the first input
	// always replaces the empty default — even an unknown one.
	var (
		worst     status.Phase
		worstRank = -2
	)
	for _, p := range phases {
		r := phaseRank(p)
		if r > worstRank {
			worst = p
			worstRank = r
		}
	}
	return worst
}

// AggregateClusterStatuses populates Status.ClusterStatusList.ClusterStatuses
// with the supplied per-cluster items and rolls the top-level Status.Phase to
// the worst-of any per-cluster Phase. When items is empty (legacy
// single-cluster reconcile), this is a no-op: top-level fields keep the
// semantics they have today.
func (s *MongoDBSearch) AggregateClusterStatuses(items []SearchClusterStatusItem) {
	if len(items) == 0 {
		return
	}
	s.Status.ClusterStatusList.ClusterStatuses = items

	phases := make([]status.Phase, 0, len(items))
	for _, it := range items {
		phases = append(phases, it.Phase)
	}
	if worst := WorstOfPhase(phases...); worst != "" {
		s.Status.Phase = worst
	}
}
