package project

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

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

func TestFindOrganization_WithOrgID(t *testing.T) {
	org := &om.Organization{ID: "org-123", Name: "my-org"}
	conn := newMockConnection(map[*om.Organization][]*om.Project{org: {}})

	result, err := FindOrganization("org-123", "my-project", conn, testLogger())
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "org-123", result.ID)
	assert.Equal(t, "my-org", result.Name)
}

func TestFindOrganization_WithoutOrgID_FoundByName(t *testing.T) {
	org := &om.Organization{ID: "org-456", Name: "my-project"}
	conn := newMockConnection(map[*om.Organization][]*om.Project{org: {}})

	result, err := FindOrganization("", "my-project", conn, testLogger())
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "org-456", result.ID)
}

func TestFindOrganization_WithoutOrgID_NotFound(t *testing.T) {
	conn := newMockConnection(map[*om.Organization][]*om.Project{})

	result, err := FindOrganization("", "nonexistent", conn, testLogger())
	require.NoError(t, err)
	assert.Nil(t, result)
}

func TestFindOrganization_InvalidOrgID(t *testing.T) {
	conn := newMockConnection(map[*om.Organization][]*om.Project{})

	_, err := FindOrganization("bad-id", "my-project", conn, testLogger())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestFindProjectInsideOrganization_SingleMatch(t *testing.T) {
	org := &om.Organization{ID: "org-1", Name: "my-org"}
	proj := &om.Project{ID: "proj-1", Name: "my-project", OrgID: "org-1"}
	conn := newMockConnection(map[*om.Organization][]*om.Project{org: {proj}})

	result, err := FindProjectInsideOrganization(conn, "my-project", org, testLogger())
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "proj-1", result.ID)
}

func TestFindProjectInsideOrganization_NotFound(t *testing.T) {
	org := &om.Organization{ID: "org-1", Name: "my-org"}
	conn := newMockConnection(map[*om.Organization][]*om.Project{org: {}})

	result, err := FindProjectInsideOrganization(conn, "missing-project", org, testLogger())
	require.NoError(t, err)
	assert.Nil(t, result)
}

func TestFindProjectInsideOrganization_MultipleMatches(t *testing.T) {
	org := &om.Organization{ID: "org-1", Name: "my-org"}
	proj1 := &om.Project{ID: "proj-1", Name: "dup-project", OrgID: "org-1"}
	proj2 := &om.Project{ID: "proj-2", Name: "dup-project", OrgID: "org-1"}
	conn := newMockConnection(map[*om.Organization][]*om.Project{org: {proj1, proj2}})

	_, err := FindProjectInsideOrganization(conn, "dup-project", org, testLogger())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "found more than one project")
}
