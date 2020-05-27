package v1

import (
	"testing"

	"github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/status"
	"github.com/stretchr/testify/assert"
)

func TestMongoDBOpsManager_AddWarningIfNotExists(t *testing.T) {
	resource := &MongoDBOpsManager{}
	resource.AddWarningIfNotExists("my test warning")
	resource.AddWarningIfNotExists("my test warning")
	resource.AddWarningIfNotExists("my other test warning")
	assert.Equal(t, []status.Warning{"my test warning;", "my other test warning"}, resource.Status.Warnings)
}
