package user

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
)

func TestMongoDBUserSpec_ApplyDefaults(t *testing.T) {
	t.Run("empty authSource defaults to admin, defaultDatabase left empty", func(t *testing.T) {
		spec := MongoDBUserSpec{}
		spec.ApplyDefaults()
		assert.Equal(t, "admin", spec.AuthSource)
		assert.Equal(t, "", spec.DefaultDatabase)
	})

	t.Run("already-set fields are left untouched", func(t *testing.T) {
		spec := MongoDBUserSpec{AuthSource: "admin", DefaultDatabase: "myapp"}
		spec.ApplyDefaults()
		assert.Equal(t, "admin", spec.AuthSource)
		assert.Equal(t, "myapp", spec.DefaultDatabase)
	})

	t.Run("defaultDatabase alone is left as-is", func(t *testing.T) {
		spec := MongoDBUserSpec{DefaultDatabase: "myapp"}
		spec.ApplyDefaults()
		assert.Equal(t, "admin", spec.AuthSource)
		assert.Equal(t, "myapp", spec.DefaultDatabase)
	})
}

func TestMongoDBUser_ChangedIdentifier(t *testing.T) {
	before := MongoDBUser{
		Spec: MongoDBUserSpec{
			Username:   "before-name",
			AuthSource: "before-db",
		},
	}

	after := MongoDBUser{
		Spec: MongoDBUserSpec{
			Username:   "after-name",
			AuthSource: "after-db",
		},
		Status: MongoDBUserStatus{
			Username: "before-name",
			Database: "before-db",
		},
	}

	assert.False(t, before.ChangedIdentifier(), "Status has not be initialized yet so the identifier should not have changed")
	assert.True(t, after.ChangedIdentifier(), "Status differs from Spec, so identifier should have changed")

	before = MongoDBUser{
		Spec: MongoDBUserSpec{
			Username:   "before-name",
			AuthSource: "before-db",
		},
		Status: MongoDBUserStatus{
			Username: "before-name",
			Database: "before-db",
		},
	}
	assert.False(t, before.ChangedIdentifier(), "Identifier before and after are the same, identifier should not have changed")
}

func TestMongoDBUser_UpdateStatus_SetsProjectId(t *testing.T) {
	u := &MongoDBUser{}
	u.UpdateStatus(status.PhaseRunning, status.NewProjectIdOption("test-project-id"))
	assert.Equal(t, "test-project-id", u.Status.ProjectId)
}

func TestMongoDBUser_UpdateStatus_DoesNotSetProjectIdWhenOptionAbsent(t *testing.T) {
	u := &MongoDBUser{Status: MongoDBUserStatus{ProjectId: "existing-id"}}
	u.UpdateStatus(status.PhaseRunning)
	assert.Equal(t, "existing-id", u.Status.ProjectId)
}
