package search

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
)

func TestWorstOfPhase(t *testing.T) {
	tests := []struct {
		name   string
		input  []status.Phase
		expect status.Phase
	}{
		{
			name:   "empty input returns empty phase",
			input:  []status.Phase{},
			expect: "",
		},
		{
			name:   "empty plus Pending returns Pending",
			input:  []status.Phase{"", status.PhasePending},
			expect: status.PhasePending,
		},
		{
			name:   "empty plus Running returns Running",
			input:  []status.Phase{"", status.PhaseRunning},
			expect: status.PhaseRunning,
		},
		{
			name:   "Failed beats Running",
			input:  []status.Phase{status.PhaseFailed, status.PhaseRunning},
			expect: status.PhaseFailed,
		},
		{
			name:   "Pending beats Failed when Pending comes first",
			input:  []status.Phase{status.PhasePending, status.PhaseFailed},
			expect: status.PhaseFailed,
		},
		{
			name:   "Running beats Updated",
			input:  []status.Phase{status.PhaseRunning, status.PhaseUpdated},
			expect: status.PhaseRunning,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expect, WorstOfPhase(tc.input...))
		})
	}
}
