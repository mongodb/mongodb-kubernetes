package project

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
)

func TestReadProjectFindsPreProvisionedProject(t *testing.T) {
	factory := om.NewDefaultCachedOMConnectionFactory()

	config := mdbv1.ProjectConfig{ProjectName: om.TestGroupName, BaseURL: om.TestURL}
	project, conn, err := ReadProject(config, mdbv1.Credentials{}, factory.GetConnectionFunc, zap.S())

	require.NoError(t, err)
	assert.Equal(t, om.TestGroupID, project.ID)
	assert.Equal(t, om.TestOrgID, project.OrgID)
	// ConfigureProject must have stamped the group onto the connection context
	assert.Equal(t, om.TestGroupID, conn.GroupID())

	mockedConn := factory.GetConnection().(*om.MockedOmConnection)
	mockedConn.CheckOperationsDidntHappen(t,
		reflect.ValueOf(mockedConn.CreateProject),
		reflect.ValueOf(mockedConn.UpdateProject),
		reflect.ValueOf(mockedConn.GenerateAgentKey))
}

func TestReadProjectWithExplicitOrgID(t *testing.T) {
	factory := om.NewDefaultCachedOMConnectionFactory()

	config := mdbv1.ProjectConfig{ProjectName: om.TestGroupName, OrgID: om.TestOrgID, BaseURL: om.TestURL}
	project, _, err := ReadProject(config, mdbv1.Credentials{}, factory.GetConnectionFunc, zap.S())

	require.NoError(t, err)
	assert.Equal(t, om.TestGroupID, project.ID)
}

func TestReadProjectNeverCreatesAMissingProject(t *testing.T) {
	factory := om.NewDefaultCachedOMConnectionFactory()

	config := mdbv1.ProjectConfig{ProjectName: "not-pre-provisioned", BaseURL: om.TestURL}
	_, _, err := ReadProject(config, mdbv1.Credentials{}, factory.GetConnectionFunc, zap.S())

	require.Error(t, err)
	mockedConn := factory.GetConnection().(*om.MockedOmConnection)
	mockedConn.CheckOperationsDidntHappen(t, reflect.ValueOf(mockedConn.CreateProject))
}
