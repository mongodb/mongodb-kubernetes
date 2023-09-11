package om

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/google/uuid"

	"github.com/10gen/ops-manager-kubernetes/controllers/om/backup"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"

	"go.uber.org/zap"
)

// TestBackupWaitsForTermination tests that 'StopBackupIfEnabled' procedure waits for backup statuses on each stage
// (STARTED -> STOPPED, STOPPED -> INACTIVE)
func TestBackupWaitsForTermination(t *testing.T) {
	t.Setenv(util.BackupDisableWaitSecondsEnv, "1")
	t.Setenv(util.BackupDisableWaitRetriesEnv, "3")

	connection := NewMockedOmConnection(NewDeployment())
	connection.EnableBackup("test", backup.ReplicaSetType, uuid.New().String())
	connection.UpdateBackupStatusFunc = func(clusterId string, status backup.Status) error {
		go func() {
			// adding slight delay for each update
			time.Sleep(200 * time.Millisecond)
			connection.doUpdateBackupStatus(clusterId, status)
		}()
		return nil
	}
	err := backup.StopBackupIfEnabled(connection, connection, "test", backup.ReplicaSetType, zap.S())
	assert.NoError(t, err)

	connection.CheckResourcesAndBackupDeleted(t, "test")
}
