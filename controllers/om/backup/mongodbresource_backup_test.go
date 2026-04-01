package backup

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"

	"github.com/mongodb/mongodb-kubernetes/api/v1/status"
)

// mockCommonStatusResource is a minimal commonStatusReader implementation for testing.
type mockCommonStatusResource struct {
	phase          status.Phase
	message        string
	lastTransition string
}

func (m *mockCommonStatusResource) GetCommonStatus(_ ...status.Option) *status.Common {
	return &status.Common{
		Phase:          m.phase,
		Message:        m.message,
		LastTransition: m.lastTransition,
	}
}

func TestApplyShardedClusterBackupEnableDelay(t *testing.T) {
	log := zap.NewNop().Sugar()

	t.Run("delay starts on first entry (phase is not pending with our message)", func(t *testing.T) {
		mdb := &mockCommonStatusResource{phase: status.PhaseRunning}
		_, stop := applyShardedClusterBackupEnableDelay(mdb, 60*time.Second, log)
		assert.True(t, stop, "expected stop=true on first entry")
	})

	t.Run("proceeds immediately when delay is zero", func(t *testing.T) {
		mdb := &mockCommonStatusResource{phase: status.PhaseRunning}
		_, stop := applyShardedClusterBackupEnableDelay(mdb, 0, log)
		assert.False(t, stop)
	})

	t.Run("proceeds immediately when delay is negative", func(t *testing.T) {
		mdb := &mockCommonStatusResource{phase: status.PhaseRunning}
		_, stop := applyShardedClusterBackupEnableDelay(mdb, -1*time.Second, log)
		assert.False(t, stop)
	})

	t.Run("delay still pending when LastTransition is recent", func(t *testing.T) {
		mdb := &mockCommonStatusResource{
			phase:          status.PhasePending,
			message:        BackupEnableDelayPendingMessage,
			lastTransition: time.Now().UTC().Format(time.RFC3339),
		}
		_, stop := applyShardedClusterBackupEnableDelay(mdb, 60*time.Second, log)
		assert.True(t, stop, "expected stop=true while delay is pending")
	})

	t.Run("proceeds when LastTransition is old enough", func(t *testing.T) {
		mdb := &mockCommonStatusResource{
			phase:          status.PhasePending,
			message:        BackupEnableDelayPendingMessage,
			lastTransition: time.Now().UTC().Add(-10 * time.Second).Format(time.RFC3339),
		}
		_, stop := applyShardedClusterBackupEnableDelay(mdb, 5*time.Second, log)
		assert.False(t, stop, "expected stop=false after delay has elapsed")
	})

	t.Run("restarts delay when LastTransition cannot be parsed", func(t *testing.T) {
		mdb := &mockCommonStatusResource{
			phase:          status.PhasePending,
			message:        BackupEnableDelayPendingMessage,
			lastTransition: "not-a-timestamp",
		}
		_, stop := applyShardedClusterBackupEnableDelay(mdb, 60*time.Second, log)
		assert.True(t, stop, "expected stop=true to restart delay when timestamp is unparseable")
	})
}

func TestGetDesiredStatus(t *testing.T) {
	t.Run("Test transition from enabled to disabled", func(t *testing.T) {
		desired := Config{
			Status: Stopped,
		}
		current := Config{
			Status: Started,
		}
		assert.Equal(t, Stopped, getDesiredStatus(&desired, &current))
	})
	t.Run("Test transition from disabled to enabled", func(t *testing.T) {
		desired := Config{
			Status: Started,
		}
		current := Config{
			Status: Stopped,
		}
		assert.Equal(t, Started, getDesiredStatus(&desired, &current))
	})
	t.Run("Test transition from enabled to terminated", func(t *testing.T) {
		desired := Config{
			Status: Terminating,
		}
		current := Config{
			Status: Started,
		}
		assert.Equal(t, Stopped, getDesiredStatus(&desired, &current))
	})

	t.Run("Test transition from terminated to disabled", func(t *testing.T) {
		desired := Config{
			Status: Stopped,
		}
		current := Config{
			Status: Terminating,
		}
		assert.Equal(t, Started, getDesiredStatus(&desired, &current))
	})
}
