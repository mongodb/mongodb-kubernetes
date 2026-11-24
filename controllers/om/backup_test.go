package om

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/mongodb/mongodb-kubernetes/controllers/om/apierror"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/backup"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

// TestBackupWaitsForTermination tests that 'StopBackupIfEnabled' procedure waits for backup statuses on each stage
// (STARTED -> STOPPED, STOPPED -> INACTIVE)
func TestBackupWaitsForTermination(t *testing.T) {
	ctx := t.Context()
	t.Setenv(util.BackupDisableWaitSecondsEnv, "1")
	t.Setenv(util.BackupDisableWaitRetriesEnv, "3")

	connection := NewMockedOmConnection(NewDeployment())
	connection.EnableBackup("test", backup.ReplicaSetType, uuid.New().String())
	err := backup.StopBackupIfEnabled(ctx, connection, connection, "test", backup.ReplicaSetType, zap.S())
	assert.NoError(t, err)

	connection.CheckResourcesAndBackupDeleted(t, "test")
}

// A host cluster can be missing during deletion. Ops Manager answers that
// lookup with a 404, and cleanup must skip the config, not panic.
func TestStopBackupIfEnabledSkipsMissingHostCluster(t *testing.T) {
	ctx := t.Context()
	clusterID := uuid.New().String()
	connection := NewMockedOmConnection(NewDeployment())
	connection.EnableBackup("test", backup.ReplicaSetType, clusterID)
	delete(connection.BackupHostClusters, clusterID)

	assert.NotPanics(t, func() {
		err := backup.StopBackupIfEnabled(ctx, connection, connection, "test", backup.ReplicaSetType, zap.S())
		assert.NoError(t, err)
	})
}

// A transient read error must keep the backup stop failing so the deletion
// retries. Only a missing cluster may be skipped.
func TestStopBackupIfEnabledReturnsTransientHostClusterErrors(t *testing.T) {
	ctx := t.Context()
	clusterID := uuid.New().String()
	connection := NewMockedOmConnection(NewDeployment())
	connection.EnableBackup("test", backup.ReplicaSetType, clusterID)

	status := http.StatusUnauthorized
	reader := failingHostClusterReader{err: &apierror.Error{Status: &status, Detail: "unauthorized"}}

	err := backup.StopBackupIfEnabled(ctx, connection, reader, "test", backup.ReplicaSetType, zap.S())
	require.Error(t, err)
}

type failingHostClusterReader struct {
	err error
}

func (f failingHostClusterReader) ReadHostCluster(_ context.Context, _ string) (*backup.HostCluster, error) {
	return nil, f.err
}
