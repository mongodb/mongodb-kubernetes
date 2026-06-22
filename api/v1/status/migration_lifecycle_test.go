package status

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func intPtr(i int) *int { return &i }

func TestComputeMigratingConditionReason(t *testing.T) {
	tests := []struct {
		name                     string
		isDryRun                 bool
		externalCount            int
		prevObservedExternal     *int
		desiredK8sMembers        int
		lastReconciledK8sMembers int
		expected                 MigratingConditionReason
	}{
		{
			name:                     "dry-run forces Validating regardless of counts",
			isDryRun:                 true,
			externalCount:            1,
			prevObservedExternal:     intPtr(1),
			desiredK8sMembers:        3,
			lastReconciledK8sMembers: 1,
			expected:                 MigratingReasonValidating,
		},
		{
			name:                     "external count decreased → Pruning",
			externalCount:            1,
			prevObservedExternal:     intPtr(2),
			desiredK8sMembers:        3,
			lastReconciledK8sMembers: 3,
			expected:                 MigratingReasonPruning,
		},
		{
			name:                     "desired k8s exceeds last reconciled → Extending",
			externalCount:            1,
			prevObservedExternal:     intPtr(1),
			desiredK8sMembers:        3,
			lastReconciledK8sMembers: 1,
			expected:                 MigratingReasonExtending,
		},
		{
			name:                     "stable counts → InProgress",
			externalCount:            1,
			prevObservedExternal:     intPtr(1),
			desiredK8sMembers:        3,
			lastReconciledK8sMembers: 3,
			expected:                 MigratingReasonInProgress,
		},
		{
			name:                     "first reconcile with externals (nil prev) → InProgress",
			externalCount:            1,
			prevObservedExternal:     nil,
			desiredK8sMembers:        1,
			lastReconciledK8sMembers: 1,
			expected:                 MigratingReasonInProgress,
		},
		{
			name:                     "desired k8s exceeds last reconciled by 1 → Extending",
			externalCount:            1,
			prevObservedExternal:     intPtr(1),
			desiredK8sMembers:        4,
			lastReconciledK8sMembers: 3,
			expected:                 MigratingReasonExtending,
		},
		{
			name:                     "member being provisioned (status not yet updated) → Extending",
			externalCount:            3,
			prevObservedExternal:     intPtr(3),
			desiredK8sMembers:        1,
			lastReconciledK8sMembers: 0,
			expected:                 MigratingReasonExtending,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := ComputeMigratingConditionReason(
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
