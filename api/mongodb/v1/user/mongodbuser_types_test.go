package user

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
)

func TestMongoDBUser_ChangedIdentifier(t *testing.T) {
	before := MongoDBUser{
		Spec: MongoDBUserSpec{
			Username: "before-name",
			Database: "before-db",
		},
	}

	after := MongoDBUser{
		Spec: MongoDBUserSpec{
			Username: "after-name",
			Database: "after-db",
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
			Username: "before-name",
			Database: "before-db",
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
