package migratetomck

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
)

func testLogger() *zap.SugaredLogger {
	logger, _ := zap.NewDevelopment()
	return logger.Sugar()
}

func newMockConnection(orgsAndProjects map[*om.Organization][]*om.Project) *om.MockedOmConnection {
	conn := om.NewMockedOmConnection(nil)
	conn.OrganizationsWithGroups = orgsAndProjects
	return conn
}

func mdbv1ProjectConfig(baseURL, orgID, projectName string) mdbv1.ProjectConfig {
	return mdbv1.ProjectConfig{
		BaseURL:     baseURL,
		OrgID:       orgID,
		ProjectName: projectName,
	}
}

func mdbv1Credentials(pub, priv string) mdbv1.Credentials {
	return mdbv1.Credentials{
		PublicAPIKey:  pub,
		PrivateAPIKey: priv,
	}
}

func TestResolveProjectReadOnly_Success(t *testing.T) {
	org := &om.Organization{ID: "org-1", Name: "my-org"}
	proj := &om.Project{ID: "proj-1", Name: "my-project", OrgID: "org-1"}
	mockConn := newMockConnection(map[*om.Organization][]*om.Project{
		org: {proj},
	})

	origFactory := omConnectionFactory
	defer func() { omConnectionFactory = origFactory }()
	omConnectionFactory = func(_ *om.OMContext) om.Connection {
		return mockConn
	}

	config := mdbv1ProjectConfig("http://localhost:8080", "org-1", "my-project")
	creds := mdbv1Credentials("pub", "priv")

	conn, err := resolveProjectReadOnly(config, creds, testLogger())
	require.NoError(t, err)
	require.NotNil(t, conn)
}

func TestResolveProjectReadOnly_OrgNotFound(t *testing.T) {
	mockConn := newMockConnection(map[*om.Organization][]*om.Project{})

	origFactory := omConnectionFactory
	defer func() { omConnectionFactory = origFactory }()
	omConnectionFactory = func(_ *om.OMContext) om.Connection {
		return mockConn
	}

	config := mdbv1ProjectConfig("http://localhost:8080", "", "nonexistent")
	creds := mdbv1Credentials("pub", "priv")

	_, err := resolveProjectReadOnly(config, creds, testLogger())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "organization not found")
}

func TestResolveProjectReadOnly_ProjectNotFound(t *testing.T) {
	org := &om.Organization{ID: "org-1", Name: "my-org"}
	mockConn := newMockConnection(map[*om.Organization][]*om.Project{
		org: {},
	})

	origFactory := omConnectionFactory
	defer func() { omConnectionFactory = origFactory }()
	omConnectionFactory = func(_ *om.OMContext) om.Connection {
		return mockConn
	}

	config := mdbv1ProjectConfig("http://localhost:8080", "org-1", "missing-project")
	creds := mdbv1Credentials("pub", "priv")

	_, err := resolveProjectReadOnly(config, creds, testLogger())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found in organization")
}
