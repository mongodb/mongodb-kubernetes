package om

import (
	"testing"

	"github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/status"
	"github.com/stretchr/testify/assert"
)

func TestMongoDBOpsManager_AddWarningIfNotExists(t *testing.T) {
	resource := &MongoDBOpsManager{}
	resource.AddOpsManagerWarningIfNotExists("my test warning")
	resource.AddOpsManagerWarningIfNotExists("my test warning")
	resource.AddOpsManagerWarningIfNotExists("my other test warning")
	assert.Equal(t, []status.Warning{"my test warning;", "my other test warning"}, resource.Status.OpsManagerStatus.Warnings)
	assert.Empty(t, resource.Status.AppDbStatus.Warnings)
	assert.Empty(t, resource.Status.BackupStatus.Warnings)
}

func TestAppDB_AddWarningIfNotExists(t *testing.T) {
	resource := &MongoDBOpsManager{}
	resource.AddAppDBWarningIfNotExists("my test warning")
	resource.AddAppDBWarningIfNotExists("my test warning")
	resource.AddAppDBWarningIfNotExists("my other test warning")
	assert.Equal(t, []status.Warning{"my test warning;", "my other test warning"}, resource.Status.AppDbStatus.Warnings)
	assert.Empty(t, resource.Status.BackupStatus.Warnings)
	assert.Empty(t, resource.Status.OpsManagerStatus.Warnings)
}

func TestBackup_AddWarningIfNotExists(t *testing.T) {
	resource := &MongoDBOpsManager{}
	resource.AddBackupWarningIfNotExists("my test warning")
	resource.AddBackupWarningIfNotExists("my test warning")
	resource.AddBackupWarningIfNotExists("my other test warning")
	assert.Equal(t, []status.Warning{"my test warning;", "my other test warning"}, resource.Status.BackupStatus.Warnings)
	assert.Empty(t, resource.Status.AppDbStatus.Warnings)
	assert.Empty(t, resource.Status.OpsManagerStatus.Warnings)
}

func TestGetPartsFromStatusOptions(t *testing.T) {

	t.Run("Empty list returns nil slice", func(t *testing.T) {
		assert.Nil(t, getPartsFromStatusOptions())
	})

	t.Run("Ops Manager parts are extracted correctly", func(t *testing.T) {
		statusOptions := []status.Option{
			status.NewBackupStatusOption("some-status"),
			status.NewOMPartOption(status.OpsManager),
			status.NewOMPartOption(status.Backup),
			status.NewOMPartOption(status.AppDb),
			status.NewBaseUrlOption("base-url"),
		}
		res := getPartsFromStatusOptions(statusOptions...)
		assert.Len(t, res, 3)
		assert.Equal(t, status.OpsManager, res[0])
		assert.Equal(t, status.Backup, res[1])
		assert.Equal(t, status.AppDb, res[2])
	})
}
