package backup

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// backupStatusReadWriter is a simple in-memory BackupStatusReadWriter for testing.
type backupStatusReadWriter struct {
	timestamp *metav1.Time
}

func (b *backupStatusReadWriter) GetEnableDelayStartTimestampStatus() *metav1.Time {
	return b.timestamp
}

func (b *backupStatusReadWriter) SetEnableDelayStartTimestampStatus(ts *metav1.Time) {
	b.timestamp = ts
}

func TestApplyShardedClusterBackupEnableDelay(t *testing.T) {
	log := zap.NewNop().Sugar()

	t.Run("delay started when timestamp is nil and remaining > 0", func(t *testing.T) {
		mdb := &backupStatusReadWriter{}
		_, stop := applyShardedClusterBackupEnableDelay(mdb, Started, Inactive, 60*time.Second, log)
		assert.True(t, stop)
		assert.NotNil(t, mdb.timestamp, "expected timestamp to be set when delay starts")
	})

	t.Run("proceeds immediately when delay is negative (no delay)", func(t *testing.T) {
		mdb := &backupStatusReadWriter{}
		_, stop := applyShardedClusterBackupEnableDelay(mdb, Started, Inactive, -1*time.Second, log)
		assert.False(t, stop)
		assert.Nil(t, mdb.timestamp, "expected no timestamp when delay is skipped")
	})

	t.Run("proceeds immediately when delay is zero", func(t *testing.T) {
		mdb := &backupStatusReadWriter{}
		_, stop := applyShardedClusterBackupEnableDelay(mdb, Started, Stopped, 0, log)
		assert.False(t, stop)
		assert.Nil(t, mdb.timestamp)
	})

	t.Run("delay still pending when timestamp is set and not elapsed", func(t *testing.T) {
		now := metav1.NewTime(time.Now().UTC())
		mdb := &backupStatusReadWriter{timestamp: &now}
		_, stop := applyShardedClusterBackupEnableDelay(mdb, Started, Inactive, 60*time.Second, log)
		assert.True(t, stop)
		assert.NotNil(t, mdb.timestamp, "expected timestamp to remain set while delay is pending")
	})

	t.Run("proceeds when timestamp is set and delay has elapsed", func(t *testing.T) {
		past := metav1.NewTime(time.Now().UTC().Add(-10 * time.Second))
		mdb := &backupStatusReadWriter{timestamp: &past}
		_, stop := applyShardedClusterBackupEnableDelay(mdb, Started, Inactive, 5*time.Second, log)
		assert.False(t, stop)
		assert.NotNil(t, mdb.timestamp, "expected timestamp to remain set until caller clears it after UpdateBackupConfig")
	})

	t.Run("timestamp cleared when desired is Stopped", func(t *testing.T) {
		past := metav1.NewTime(time.Now().UTC())
		mdb := &backupStatusReadWriter{timestamp: &past}
		_, stop := applyShardedClusterBackupEnableDelay(mdb, Stopped, Started, 60*time.Second, log)
		assert.False(t, stop)
		assert.Nil(t, mdb.timestamp, "expected timestamp to be cleared when disabling backup")
	})

	t.Run("timestamp cleared when desired is Terminating", func(t *testing.T) {
		past := metav1.NewTime(time.Now().UTC())
		mdb := &backupStatusReadWriter{timestamp: &past}
		_, stop := applyShardedClusterBackupEnableDelay(mdb, Terminating, Started, 60*time.Second, log)
		assert.False(t, stop)
		assert.Nil(t, mdb.timestamp, "expected timestamp to be cleared when terminating backup")
	})

	t.Run("timer not reset when desired is Started but current is Terminating", func(t *testing.T) {
		past := metav1.NewTime(time.Now().UTC())
		mdb := &backupStatusReadWriter{timestamp: &past}
		_, stop := applyShardedClusterBackupEnableDelay(mdb, Started, Terminating, 60*time.Second, log)
		assert.False(t, stop)
		assert.NotNil(t, mdb.timestamp, "expected timestamp to be preserved when OM is transiently Terminating")
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
