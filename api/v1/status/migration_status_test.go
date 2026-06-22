package status

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestMigrationCondition_PhaseToStatus(t *testing.T) {
	t.Run("Passed maps to True", func(t *testing.T) {
		c := MigrationCondition(MigrationPhaseConnectivityCheckPassed, "R", "M")
		assert.Equal(t, metav1.ConditionTrue, c.Status)
	})
	t.Run("Failed maps to False", func(t *testing.T) {
		c := MigrationCondition(MigrationPhaseConnectivityCheckFailed, "R", "M")
		assert.Equal(t, metav1.ConditionFalse, c.Status)
	})
	t.Run("Running maps to Unknown", func(t *testing.T) {
		c := MigrationCondition(MigrationPhaseConnectivityCheckRunning, string(NetworkConnectivityVerifiedReasonRunning), "M")
		assert.Equal(t, metav1.ConditionUnknown, c.Status)
		assert.Equal(t, ConditionNetworkConnectivityVerified, c.Type)
		assert.Equal(t, string(NetworkConnectivityVerifiedReasonRunning), c.Reason)
	})
}
