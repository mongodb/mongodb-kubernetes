package backup

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	"golang.org/x/xerrors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
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

func (m *mockCommonStatusResource) GetStatus(_ ...status.Option) any {
	return m.GetCommonStatus()
}

func TestApplyShardedClusterBackupEnableDelay(t *testing.T) {
	log := zap.NewNop().Sugar()

	t.Run("delay starts on first entry (phase is not pending with our message)", func(t *testing.T) {
		mdb := &mockCommonStatusResource{phase: status.PhaseRunning}
		s := applyShardedClusterBackupEnableDelay(mdb, 60*time.Second, log)
		assert.False(t, s.IsOK(), "expected non-OK on first entry")
	})

	t.Run("proceeds immediately when delay is zero", func(t *testing.T) {
		mdb := &mockCommonStatusResource{phase: status.PhaseRunning}
		s := applyShardedClusterBackupEnableDelay(mdb, 0, log)
		assert.True(t, s.IsOK())
	})

	t.Run("proceeds immediately when delay is negative", func(t *testing.T) {
		mdb := &mockCommonStatusResource{phase: status.PhaseRunning}
		s := applyShardedClusterBackupEnableDelay(mdb, -1*time.Second, log)
		assert.True(t, s.IsOK())
	})

	t.Run("delay still pending when LastTransition is recent", func(t *testing.T) {
		mdb := &mockCommonStatusResource{
			phase:          status.PhasePending,
			message:        BackupEnableDelayPendingMessage,
			lastTransition: time.Now().UTC().Format(time.RFC3339),
		}
		s := applyShardedClusterBackupEnableDelay(mdb, 60*time.Second, log)
		assert.False(t, s.IsOK(), "expected non-OK while delay is pending")
	})

	t.Run("proceeds when LastTransition is old enough", func(t *testing.T) {
		mdb := &mockCommonStatusResource{
			phase:          status.PhasePending,
			message:        BackupEnableDelayPendingMessage,
			lastTransition: time.Now().UTC().Add(-10 * time.Second).Format(time.RFC3339),
		}
		s := applyShardedClusterBackupEnableDelay(mdb, 5*time.Second, log)
		assert.True(t, s.IsOK(), "expected OK after delay has elapsed")
	})

	t.Run("proceeds when LastTransition cannot be parsed", func(t *testing.T) {
		mdb := &mockCommonStatusResource{
			phase:          status.PhasePending,
			message:        BackupEnableDelayPendingMessage,
			lastTransition: "not-a-timestamp",
		}
		s := applyShardedClusterBackupEnableDelay(mdb, 60*time.Second, log)
		assert.True(t, s.IsOK(), "expected OK to avoid getting stuck when timestamp is unparseable")
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

// fakeBackupClient is an in-memory Ops Manager backup API that records the updates sent to it.
type fakeBackupClient struct {
	configs      []*Config
	hostClusters map[string]*HostCluster
	updated      []*Config
}

func (f *fakeBackupClient) ReadBackupConfigs(_ context.Context) (*ConfigsResponse, error) {
	return &ConfigsResponse{Configs: f.configs}, nil
}

func (f *fakeBackupClient) ReadBackupConfig(_ context.Context, clusterID string) (*Config, error) {
	for _, c := range f.configs {
		if c.ClusterId == clusterID {
			return c, nil
		}
	}
	return nil, xerrors.Errorf("no backup config for cluster %s", clusterID)
}

func (f *fakeBackupClient) ReadSnapshotSchedule(_ context.Context, clusterID string) (*SnapshotSchedule, error) {
	return &SnapshotSchedule{ClusterID: clusterID}, nil
}

func (f *fakeBackupClient) UpdateBackupConfig(_ context.Context, config *Config) (*Config, error) {
	f.updated = append(f.updated, config)
	return config, nil
}

func (f *fakeBackupClient) UpdateBackupStatus(context.Context, string, Status) error { return nil }

func (f *fakeBackupClient) UpdateSnapshotSchedule(context.Context, string, *SnapshotSchedule) error {
	return nil
}

func (f *fakeBackupClient) ReadHostCluster(_ context.Context, clusterID string) (*HostCluster, error) {
	return f.hostClusters[clusterID], nil
}

func replicaSetWithBackupMode(mode mdbv1.BackupMode) *mdbv1.MongoDB {
	rs := &mdbv1.MongoDB{ObjectMeta: metav1.ObjectMeta{Name: "my-rs", Namespace: "ns"}}
	rs.Spec.ResourceType = mdbv1.ReplicaSet
	rs.Spec.Backup = &mdbv1.Backup{Mode: mode}
	return rs
}

func backupStatusOf(opts []status.Option) (string, bool) {
	option, exists := status.GetOption(opts, status.BackupStatusOption{})
	if !exists {
		return "", false
	}
	return option.(status.BackupStatusOption).Value().(string), true
}

func TestEnsureBackupConfigStatuses_ReportsStatusWhenOpsManagerAlreadyInDesiredState(t *testing.T) {
	tests := []struct {
		name           string
		mode           mdbv1.BackupMode
		statusInOM     Status
		expectedStatus string
	}{
		{name: "backup started outside of the operator", mode: "enabled", statusInOM: Started, expectedStatus: "STARTED"},
		{name: "backup disabled and never started", mode: "disabled", statusInOM: Inactive, expectedStatus: "INACTIVE"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rs := replicaSetWithBackupMode(tt.mode)
			client := &fakeBackupClient{
				configs:      []*Config{{ClusterId: "cluster-1", Status: tt.statusInOM}},
				hostClusters: map[string]*HostCluster{"cluster-1": {ClusterName: "my-rs", TypeName: "REPLICA_SET"}},
			}

			result, opts := ensureBackupConfigStatuses(t.Context(), rs, client.configs, getMongoDBBackupConfig(rs.GetBackupSpec(), "project-id"), zap.S(), client, 0)

			assert.True(t, result.IsOK())
			statusName, exists := backupStatusOf(opts)
			assert.True(t, exists, "status.backup must be reported")
			assert.Equal(t, tt.expectedStatus, statusName)
			assert.Empty(t, client.updated, "nothing has to be changed in Ops Manager")
		})
	}
}

func TestEnsureBackupConfigStatuses_DoesNotReportStatusOfOtherDeployments(t *testing.T) {
	rs := replicaSetWithBackupMode("enabled")
	client := &fakeBackupClient{
		configs:      []*Config{{ClusterId: "cluster-1", Status: Started}},
		hostClusters: map[string]*HostCluster{"cluster-1": {ClusterName: "another-rs", TypeName: "REPLICA_SET"}},
	}

	result, opts := ensureBackupConfigStatuses(t.Context(), rs, client.configs, getMongoDBBackupConfig(rs.GetBackupSpec(), "project-id"), zap.S(), client, 0)

	assert.True(t, result.IsOK())
	_, exists := backupStatusOf(opts)
	assert.False(t, exists)
}
