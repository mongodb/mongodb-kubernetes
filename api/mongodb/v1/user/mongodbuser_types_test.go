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

func clientCertUser() MongoDBUser {
	u := MongoDBUser{Spec: MongoDBUserSpec{
		Database:           "$external",
		MongoDBResourceRef: MongoDBResourceRef{Name: "my-rs"},
		ClientCertificate:  &ClientCertificate{},
	}}
	u.Name = "my-app"
	u.Namespace = "ns"
	return u
}

func TestMongoDBUser_GetUsername(t *testing.T) {
	t.Run("without clientCertificate it is spec.username, unchanged", func(t *testing.T) {
		u := MongoDBUser{Spec: MongoDBUserSpec{Username: "alice", Database: "admin"}}
		assert.Equal(t, "alice", u.GetUsername())
	})

	t.Run("with clientCertificate it is the default subject, built from the user", func(t *testing.T) {
		u := clientCertUser()
		assert.Equal(t, "CN=my-app,OU=ns,O=my-rs-app-client", u.GetUsername())
	})

	t.Run("spec.clientCertificate.subject overrides each part", func(t *testing.T) {
		u := clientCertUser()
		u.Spec.ClientCertificate.Subject = &ClientCertificateSubject{CommonName: "billing", Organizations: []string{"apps"}, OrganizationalUnits: []string{"team-a"}}
		assert.Equal(t, "CN=billing,OU=team-a,O=apps", u.GetUsername())
	})
}

func TestMongoDBUser_ClientCertificateStatusUsername(t *testing.T) {
	t.Run("the username option is recorded in the status", func(t *testing.T) {
		u := MongoDBUser{Spec: MongoDBUserSpec{Username: "alice", Database: "admin"}}
		u.UpdateStatus(status.PhaseRunning, status.NewUsernameOption("alice"))
		assert.Equal(t, "alice", u.Status.Username)
	})

	t.Run("without the option the status username is left as it is", func(t *testing.T) {
		u := MongoDBUser{
			Spec:   MongoDBUserSpec{Username: "bob", Database: "admin"},
			Status: MongoDBUserStatus{Username: "alice"},
		}

		// Only a successful registration passes the option, so a failed or pending update never
		// records a username that was not registered.
		u.UpdateStatus(status.PhasePending)
		assert.Equal(t, "alice", u.Status.Username)
		u.UpdateStatus(status.PhaseRunning)
		assert.Equal(t, "alice", u.Status.Username)
	})

	t.Run("a changed subject is a changed identifier, so the old user is removed", func(t *testing.T) {
		u := clientCertUser()
		u.Status = MongoDBUserStatus{Username: "CN=my-app,OU=ns,O=old", Database: "$external"}
		assert.True(t, u.ChangedIdentifier())
		u.Status.Username = u.GetUsername()
		assert.False(t, u.ChangedIdentifier())
	})
}

func TestMongoDBUser_RegisteredUsername(t *testing.T) {
	t.Run("without clientCertificate it is spec.username, even if status differs", func(t *testing.T) {
		u := MongoDBUser{Spec: MongoDBUserSpec{Username: "alice"}, Status: MongoDBUserStatus{Username: "old"}}
		assert.Equal(t, "alice", u.RegisteredUsername())
	})

	t.Run("with clientCertificate it is the recorded name, so an edited subject still removes the right user", func(t *testing.T) {
		u := clientCertUser()
		u.Status.Username = "CN=my-app,OU=ns,O=registered"
		assert.Equal(t, "CN=my-app,OU=ns,O=registered", u.RegisteredUsername())
	})

	t.Run("with clientCertificate and never registered it is the computed subject", func(t *testing.T) {
		u := clientCertUser()
		assert.Equal(t, u.GetUsername(), u.RegisteredUsername())
	})
}
