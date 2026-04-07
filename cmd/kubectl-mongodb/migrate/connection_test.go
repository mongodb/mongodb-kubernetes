package migrate

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

func TestResolveOrganization_WithOrgID(t *testing.T) {
	org := &om.Organization{ID: "org-123", Name: "my-org"}
	conn := newMockConnection(map[*om.Organization][]*om.Project{
		org: {},
	})

	result, err := resolveOrganization(conn, "org-123", "my-project", testLogger())
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "org-123", result.ID)
	assert.Equal(t, "my-org", result.Name)
}

func TestResolveOrganization_WithoutOrgID_FoundByName(t *testing.T) {
	org := &om.Organization{ID: "org-456", Name: "my-project"}
	conn := newMockConnection(map[*om.Organization][]*om.Project{
		org: {},
	})

	result, err := resolveOrganization(conn, "", "my-project", testLogger())
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "org-456", result.ID)
}

func TestResolveOrganization_WithoutOrgID_NotFound(t *testing.T) {
	conn := newMockConnection(map[*om.Organization][]*om.Project{})

	result, err := resolveOrganization(conn, "", "nonexistent", testLogger())
	require.NoError(t, err)
	assert.Nil(t, result)
}

func TestResolveOrganization_InvalidOrgID(t *testing.T) {
	conn := newMockConnection(map[*om.Organization][]*om.Project{})

	_, err := resolveOrganization(conn, "bad-id", "my-project", testLogger())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestResolveProjectInOrg_SingleMatch(t *testing.T) {
	org := &om.Organization{ID: "org-1", Name: "my-org"}
	proj := &om.Project{ID: "proj-1", Name: "my-project", OrgID: "org-1"}
	conn := newMockConnection(map[*om.Organization][]*om.Project{
		org: {proj},
	})

	result, err := resolveProjectInOrg(conn, "my-project", org)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "proj-1", result.ID)
	assert.Equal(t, "my-project", result.Name)
}

func TestResolveProjectInOrg_NotFound(t *testing.T) {
	org := &om.Organization{ID: "org-1", Name: "my-org"}
	conn := newMockConnection(map[*om.Organization][]*om.Project{
		org: {},
	})

	result, err := resolveProjectInOrg(conn, "missing-project", org)
	require.NoError(t, err)
	assert.Nil(t, result)
}

func TestResolveProjectInOrg_MultipleMatches(t *testing.T) {
	org := &om.Organization{ID: "org-1", Name: "my-org"}
	proj1 := &om.Project{ID: "proj-1", Name: "dup-project", OrgID: "org-1"}
	proj2 := &om.Project{ID: "proj-2", Name: "dup-project", OrgID: "org-1"}
	conn := newMockConnection(map[*om.Organization][]*om.Project{
		org: {proj1, proj2},
	})

	_, err := resolveProjectInOrg(conn, "dup-project", org)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "found more than one project")
}

func TestResolveOrgIDByName_Found(t *testing.T) {
	org := &om.Organization{ID: "org-abc", Name: "test-org"}
	conn := newMockConnection(map[*om.Organization][]*om.Project{
		org: {},
	})

	id, err := resolveOrgIDByName(conn, "test-org")
	require.NoError(t, err)
	assert.Equal(t, "org-abc", id)
}

func TestResolveOrgIDByName_NotFound(t *testing.T) {
	conn := newMockConnection(map[*om.Organization][]*om.Project{})

	id, err := resolveOrgIDByName(conn, "nonexistent")
	require.NoError(t, err)
	assert.Empty(t, id)
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
