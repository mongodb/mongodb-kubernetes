package status

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_NotRefreshingLastTransitionTime(t *testing.T) {
	// given
	testTime := "test"

	status := &Common{
		Phase:              PhaseFailed,
		Message:            "test",
		LastTransition:     testTime,
		ObservedGeneration: 1,
	}

	// when
	status.UpdateCommonFields(PhaseFailed, 1)
	timeAfterTheTest := status.LastTransition

	// then
	assert.Equal(t, testTime, timeAfterTheTest)
}

func Test_RefreshingLastTransitionTime(t *testing.T) {
	// given
	testTime := "test"

	status := &Common{
		Phase:              PhaseFailed,
		Message:            "test",
		LastTransition:     testTime,
		ObservedGeneration: 1,
	}

	// when
	status.UpdateCommonFields(PhaseRunning, 2)
	timeAfterTheTest := status.LastTransition

	// then
	assert.NotEqual(t, testTime, timeAfterTheTest)
}
