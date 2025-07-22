package om

import (
	"go.uber.org/zap/zaptest"
	"testing"

	"github.com/google/uuid"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/backup"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/stretchr/testify/assert"
)

// TestBackupWaitsForTermination tests that 'StopBackupIfEnabled' procedure waits for backup statuses on each stage
// (STARTED -> STOPPED, STOPPED -> INACTIVE)
func TestBackupWaitsForTermination(t *testing.T) {
	t.Setenv(util.BackupDisableWaitSecondsEnv, "1")
	t.Setenv(util.BackupDisableWaitRetriesEnv, "3")

	connection := NewMockedOmConnection(NewDeployment())
	connection.EnableBackup("test", backup.ReplicaSetType, uuid.New().String())
	err := backup.StopBackupIfEnabled(connection, connection, "test", backup.ReplicaSetType, zaptest.NewLogger(t).Sugar())
	assert.NoError(t, err)

	connection.CheckResourcesAndBackupDeleted(t, "test")
}
