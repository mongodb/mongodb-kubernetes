package v1

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMongoDBOpsManager_AddWarningIfNotExists(t *testing.T) {
	resource := &MongoDBOpsManager{}
	resource.AddWarningIfNotExists("my test warning")
	resource.AddWarningIfNotExists("my test warning")
	resource.AddWarningIfNotExists("my other test warning")
	assert.Equal(t, []StatusWarning{"my test warning;", "my other test warning"}, resource.Status.Warnings)
}
