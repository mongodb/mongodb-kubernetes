package status

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestComputeMigrationLifecyclePhase(t *testing.T) {
	tests := []struct {
		name                    string
		isDryRun                bool
		externalCount           int
		prevObservedExternal    int
		desiredK8sMembers       int
		lastReconciledK8sMembers int
		expected                MigrationLifecyclePhase
	}{
		{
			name:                    "dry-run forces Validating regardless of counts",
			isDryRun:                true,
			externalCount:           1,
			prevObservedExternal:    1,
			desiredK8sMembers:       3,
			lastReconciledK8sMembers: 1,
			expected:                MigrationPhaseValidating,
		},
		{
			name:                    "external count decreased → Pruning",
			externalCount:           1,
			prevObservedExternal:    2,
			desiredK8sMembers:       3,
			lastReconciledK8sMembers: 3,
			expected:                MigrationPhasePruning,
		},
		{
			name:                    "desired k8s exceeds last reconciled → Extending",
			externalCount:           1,
			prevObservedExternal:    1,
			desiredK8sMembers:       3,
			lastReconciledK8sMembers: 1,
			expected:                MigrationPhaseExtending,
		},
		{
			name:                    "stable counts → InProgress",
			externalCount:           1,
			prevObservedExternal:    1,
			desiredK8sMembers:       3,
			lastReconciledK8sMembers: 3,
			expected:                MigrationPhaseInProgress,
		},
		{
			name:                    "first observation of externals, stable k8s → InProgress",
			externalCount:           1,
			prevObservedExternal:    0,
			desiredK8sMembers:       1,
			lastReconciledK8sMembers: 1,
			expected:                MigrationPhaseInProgress,
		},
		{
			name:                    "desired k8s exceeds last reconciled by 1 → Extending",
			externalCount:           1,
			prevObservedExternal:    1,
			desiredK8sMembers:       4,
			lastReconciledK8sMembers: 3,
			expected:                MigrationPhaseExtending,
		},
		{
			name:                    "pruning takes precedence over extending",
			externalCount:           2,
			prevObservedExternal:    3,
			desiredK8sMembers:       4,
			lastReconciledK8sMembers: 3,
			expected:                MigrationPhasePruning,
		},
		{
			name:                    "member being provisioned (status not yet updated) → Extending",
			externalCount:           3,
			prevObservedExternal:    3,
			desiredK8sMembers:       1,
			lastReconciledK8sMembers: 0,
			expected:                MigrationPhaseExtending,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := ComputeMigrationLifecyclePhase(
				tc.isDryRun,
				tc.externalCount,
				tc.prevObservedExternal,
				tc.desiredK8sMembers,
				tc.lastReconciledK8sMembers,
			)
			assert.Equal(t, tc.expected, result)
		})
	}
}
