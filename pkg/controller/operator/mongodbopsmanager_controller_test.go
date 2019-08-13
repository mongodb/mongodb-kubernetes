package operator

import (
	"testing"

	v1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/stretchr/testify/assert"
)

func TestOpsManagerReconciler_performValidation(t *testing.T) {
	assert.NoError(t, performValidation(omWithAppDBVersion("4.0.0")))
	assert.NoError(t, performValidation(omWithAppDBVersion("4.0.7")))
	assert.NoError(t, performValidation(omWithAppDBVersion("4.2.12")))
	assert.NoError(t, performValidation(omWithAppDBVersion("6.0.0")))
	assert.NoError(t, performValidation(omWithAppDBVersion("4.2.0-rc1")))
	assert.NoError(t, performValidation(omWithAppDBVersion("4.5.0-ent")))

	assert.Error(t, performValidation(omWithAppDBVersion("3.6.12")))
	assert.Error(t, performValidation(omWithAppDBVersion("3.4.0")))
	assert.Error(t, performValidation(omWithAppDBVersion("3.4.0.0.1.2")))
	assert.Error(t, performValidation(omWithAppDBVersion("foo")))
}

func omWithAppDBVersion(version string) *v1.MongoDBOpsManager {
	return &v1.MongoDBOpsManager{Spec: v1.MongoDBOpsManagerSpec{AppDB: v1.AppDB{MongoDbSpec: v1.MongoDbSpec{Version: version}}}}
}
