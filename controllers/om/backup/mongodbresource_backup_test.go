package backup

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// inMemoryConfigMapClient is a simple in-memory configmap.GetUpdateCreator for testing.
type inMemoryConfigMapClient struct {
	data map[string]string
}

func (c *inMemoryConfigMapClient) GetConfigMap(_ context.Context, _ k8sclient.ObjectKey) (corev1.ConfigMap, error) {
	return corev1.ConfigMap{Data: c.data}, nil
}

func (c *inMemoryConfigMapClient) UpdateConfigMap(_ context.Context, cm corev1.ConfigMap) error {
	c.data = cm.Data
	return nil
}

func (c *inMemoryConfigMapClient) CreateConfigMap(_ context.Context, cm corev1.ConfigMap) error {
	c.data = cm.Data
	return nil
}

func (c *inMemoryConfigMapClient) DeleteConfigMap(_ context.Context, _ k8sclient.ObjectKey) error {
	c.data = nil
	return nil
}

func newTestCMClient(ts *metav1.Time) *inMemoryConfigMapClient {
	data := map[string]string{}
	if ts != nil {
		data[BackupDelayTimestampKey] = ts.UTC().Format(time.RFC3339)
	}
	return &inMemoryConfigMapClient{data: data}
}

func TestApplyShardedClusterBackupEnableDelay(t *testing.T) {
	ctx := context.Background()
	log := zap.NewNop().Sugar()
	ns, name := "ns", "resource-backup-delay"

	t.Run("delay started when timestamp is nil and remaining > 0", func(t *testing.T) {
		cm := newTestCMClient(nil)
		_, stop := applyShardedClusterBackupEnableDelay(ctx, cm, ns, name, 60*time.Second, nil, log)
		assert.True(t, stop)
		assert.NotNil(t, getBackupDelayTimestamp(ctx, cm, ns, name), "expected timestamp to be set when delay starts")
	})

	t.Run("proceeds immediately when delay is negative (no delay)", func(t *testing.T) {
		cm := newTestCMClient(nil)
		_, stop := applyShardedClusterBackupEnableDelay(ctx, cm, ns, name, -1*time.Second, nil, log)
		assert.False(t, stop)
		assert.Nil(t, getBackupDelayTimestamp(ctx, cm, ns, name), "expected no timestamp when delay is skipped")
	})

	t.Run("proceeds immediately when delay is zero", func(t *testing.T) {
		cm := newTestCMClient(nil)
		_, stop := applyShardedClusterBackupEnableDelay(ctx, cm, ns, name, 0, nil, log)
		assert.False(t, stop)
		assert.Nil(t, getBackupDelayTimestamp(ctx, cm, ns, name))
	})

	t.Run("delay still pending when timestamp is set and not elapsed", func(t *testing.T) {
		now := metav1.NewTime(time.Now().UTC())
		cm := newTestCMClient(&now)
		_, stop := applyShardedClusterBackupEnableDelay(ctx, cm, ns, name, 60*time.Second, nil, log)
		assert.True(t, stop)
		assert.NotNil(t, getBackupDelayTimestamp(ctx, cm, ns, name), "expected timestamp to remain set while delay is pending")
	})

	t.Run("proceeds when timestamp is set and delay has elapsed", func(t *testing.T) {
		past := metav1.NewTime(time.Now().UTC().Add(-10 * time.Second))
		cm := newTestCMClient(&past)
		_, stop := applyShardedClusterBackupEnableDelay(ctx, cm, ns, name, 5*time.Second, nil, log)
		assert.False(t, stop)
		assert.NotNil(t, getBackupDelayTimestamp(ctx, cm, ns, name), "expected timestamp to remain set until caller clears it after UpdateBackupConfig")
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
