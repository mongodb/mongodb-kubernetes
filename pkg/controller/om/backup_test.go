package om

import (
	"os"
	"testing"
	"time"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"

	"go.uber.org/zap"
)

// TestBackupWaitsForTermination tests that 'StopBackupIfEnabled' procedure waits for backup statuses on each stage
// (STARTED -> STOPPED, STOPPED -> INACTIVE)
func TestBackupWaitsForTermination(t *testing.T) {
	os.Setenv(util.BackupDisableWaitSecondsEnv, "1")
	os.Setenv(util.BackupDisableWaitRetriesEnv, "3")

	connection := NewMockedOmConnection(NewDeployment())
	connection.EnableBackup("test", ReplicaSetType)
	connection.UpdateBackupStatusFunc = func(clusterId string, status BackupStatus) error {
		go func() {
			// adding slight delay for each update
			time.Sleep(200 * time.Millisecond)
			connection.doUpdateBackupStatus(clusterId, status)
		}()
		return nil
	}
	StopBackupIfEnabled(connection, "test", ReplicaSetType, zap.S())

	connection.CheckResourcesDeleted(t, "test", true)
}
