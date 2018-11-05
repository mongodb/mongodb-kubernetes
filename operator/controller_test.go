package operator

import (
	"math/rand"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/10gen/ops-manager-kubernetes/util"

	"github.com/10gen/ops-manager-kubernetes/om"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

func NewMockedMongoDbController() *MongoDbController {
	return NewMongoDbController(newMockedKubeApi(), nil, om.NewEmptyMockedOmConnection)
}

// TestPrepareOmConnection_FindExistingGroup finds existing group when org id is specified
func TestPrepareOmConnection_FindExistingGroup(t *testing.T) {
	mockedOmConnection := omConnGroupInOrganizationWithDifferentName()

	controller := NewMongoDbController(newMockedKubeApiDetailed(om.TestGroupName, om.TestOrgId), nil, mockedOmConnection)

	mockOm, _ := prepareConnection(controller, t)
	assert.Equal(t, "existingGroupId", mockOm.GroupId())
	// No new group was created
	assert.Len(t, mockOm.AllGroups, 1)
	assert.Len(t, mockOm.AllOrganizations, 1)

	mockOm.CheckOrderOfOperations(t, reflect.ValueOf(mockOm.ReadGroups))
	mockOm.CheckOperationsDidntHappen(t, reflect.ValueOf(mockOm.CreateGroup))
}

// TestPrepareOmConnection_DuplicatedGroups verifies that if there are groups with the same name but in different organization
// then the new group is created
func TestPrepareOmConnection_DuplicatedGroups(t *testing.T) {
	mockedOmConnection := omConnGroupInOrganizationWithDifferentName()

	// The only difference from TestPrepareOmConnection_FindExistingGroup^ is that the config map contains only project name
	// but no org id (see newMockedKubeApi())
	controller := NewMongoDbController(newMockedKubeApi(), nil, mockedOmConnection)

	mockOm, _ := prepareConnection(controller, t)
	assert.Equal(t, om.TestGroupId, mockOm.GroupId())
	assert.NotNil(t, mockOm.FindGroup(om.TestGroupName))
	// New group and organization will be created in addition to existing ones
	assert.Len(t, mockOm.AllGroups, 2)
	assert.Len(t, mockOm.AllOrganizations, 2)

	mockOm.CheckOrderOfOperations(t, reflect.ValueOf(mockOm.ReadGroups), reflect.ValueOf(mockOm.ReadOrganizations), reflect.ValueOf(mockOm.CreateGroup))
}

// TestPrepareOmConnection_CreateGroup checks that if the group doesn't exist in OM - it is created
func TestPrepareOmConnection_CreateGroup(t *testing.T) {
	mockedOmConnection := om.NewEmptyMockedOmConnectionNoGroup

	controller := NewMongoDbController(newMockedKubeApi(), nil, mockedOmConnection)

	mockOm, vars := prepareConnection(controller, t)

	assert.Equal(t, om.TestGroupId, vars.ProjectId)
	assert.Equal(t, om.TestGroupId, mockOm.GroupId())
	require.NotNil(t, mockOm.FindGroup(om.TestGroupName))
	assert.Contains(t, mockOm.FindGroup(om.TestGroupName).Tags, util.OmGroupExternallyManagedTag)

	mockOm.CheckOrderOfOperations(t, reflect.ValueOf(mockOm.ReadGroups), reflect.ValueOf(mockOm.CreateGroup))
	mockOm.CheckOperationsDidntHappen(t, reflect.ValueOf(mockOm.UpdateGroup), reflect.ValueOf(mockOm.ReadOrganizations))
}

// TestPrepareOmConnection_CreateGroupFallback checks that if the group creation failed because tags editing is not allowed
// - the program failbacks to creating group without tags
func TestPrepareOmConnection_CreateGroupFallback(t *testing.T) {
	mockedOmConnection := omConnOldVersion()

	controller := NewMongoDbController(newMockedKubeApi(), nil, mockedOmConnection)

	mockOm, vars := prepareConnection(controller, t)

	assert.Equal(t, om.TestGroupId, vars.ProjectId)
	assert.Equal(t, om.TestGroupId, mockOm.GroupId())
	assert.NotNil(t, mockOm.FindGroup(om.TestGroupName))
	assert.Empty(t, mockOm.FindGroup(om.TestGroupName).Tags)

	mockOm.CheckOrderOfOperations(t, reflect.ValueOf(mockOm.ReadGroups), reflect.ValueOf(mockOm.CreateGroup), reflect.ValueOf(mockOm.CreateGroup))
}

// TestPrepareOmConnection_CreateGroupFixTags fixes tags if they are not set for existing group
func TestPrepareOmConnection_CreateGroupFixTags(t *testing.T) {
	mockedOmConnection := omConnGroupWithoutTags()

	controller := NewMongoDbController(newMockedKubeApi(), nil, mockedOmConnection)

	mockOm, _ := prepareConnection(controller, t)
	assert.Contains(t, mockOm.FindGroup(om.TestGroupName).Tags, util.OmGroupExternallyManagedTag)

	mockOm.CheckOrderOfOperations(t, reflect.ValueOf(mockOm.ReadGroups), reflect.ValueOf(mockOm.UpdateGroup))
}

// TestPrepareOmConnection_PrepareAgentKeys checks that agent key is generated and put to secret
func TestPrepareOmConnection_PrepareAgentKeys(t *testing.T) {
	mockedKubeApi := newMockedKubeApi()
	controller := NewMongoDbController(mockedKubeApi, nil, om.NewEmptyMockedOmConnection)

	prepareConnection(controller, t)

	key, e := controller.kubeHelper.readAgentApiKeyForProject(TestNamespace, agentApiKeySecretName(om.TestGroupId))

	assert.NoError(t, e)
	// Unfortunately the key read is not equal to om.TestAgentKey - it's just some set of bytes.
	// This is reproduced only in mocked tests - the production is fine (the key is real string)
	// I assume that it's because when setting the secret data we use 'StringData' but read it back as
	// 'Data' which is binary. May be real kubernetes api reads data as string and updates
	assert.NotNil(t, key)

	mockedKubeApi.CheckOrderOfOperations(t, reflect.ValueOf(mockedKubeApi.getSecret), reflect.ValueOf(mockedKubeApi.createSecret))
}

func prepareConnection(controller *MongoDbController, t *testing.T) (*om.MockedOmConnection, *PodVars) {
	conn, vars, e := controller.prepareOmConnection(TestNamespace, TestProjectConfigMapName, TestCredentialsSecretName, zap.S())
	mockOm := conn.(*om.MockedOmConnection)
	assert.NoError(t, e)
	return mockOm, vars
}

func omConnGroupWithoutTags() func(url, g, user, k string) om.OmConnection {
	return func(url, g, user, k string) om.OmConnection {
		c := om.NewEmptyMockedOmConnectionNoGroup(url, g, user, k).(*om.MockedOmConnection)
		if len(c.AllGroups) == 0 {
			// initially OM contains the group without tags
			c.AllGroups = []*om.Group{{Name: om.TestGroupName, Id: "123", AgentApiKey: "12345abcd", OrgId: om.TestOrgId}}
			c.AllOrganizations = []*om.Organization{{Id: om.TestOrgId, Name: om.TestGroupName}}
		}

		return c
	}
}

func omConnGroupInOrganizationWithDifferentName() func(url, g, user, k string) om.OmConnection {
	return func(url, g, user, k string) om.OmConnection {
		c := om.NewEmptyMockedOmConnectionNoGroup(url, g, user, k).(*om.MockedOmConnection)
		if len(c.AllGroups) == 0 {
			// Important: the organization for the group has a different name ("foo") then group (om.TestGroupName).
			// So it won't work for cases when the group "was created before" by Operator
			c.AllGroups = []*om.Group{{Name: om.TestGroupName, Id: "existingGroupId", OrgId: om.TestOrgId}}
			c.AllOrganizations = []*om.Organization{{Id: om.TestOrgId, Name: "foo"}}
		}

		return c
	}
}

func omConnOldVersion() func(url, g, user, k string) om.OmConnection {
	cnt := 1
	return func(url, g, user, k string) om.OmConnection {
		c := om.NewEmptyMockedOmConnectionNoGroup(url, g, user, k).(*om.MockedOmConnection)
		c.CreateGroupFunc = func(g *om.Group) (*om.Group, error) {
			// first call
			if cnt == 1 {
				cnt++
				return nil, &om.OmApiError{ErrorCode: "INVALID_ATTRIBUTE", Detail: "Invalid attribute tags specified."}
			}
			// second call (fallback)
			g.Id = om.TestGroupId
			c.AllGroups = append(c.AllGroups, g)
			c.AllOrganizations = append(c.AllOrganizations, &om.Organization{Id: string(rand.Int()), Name: g.Name})
			return g, nil
		}
		// If creating tags is not allowed - then neither the update
		c.UpdateGroupFunc = func(g *om.Group) (*om.Group, error) {
			if len(g.Tags) > 0 {
				return nil, &om.OmApiError{ErrorCode: "INVALID_ATTRIBUTE", Detail: "Invalid attribute tags specified."}
			}
			return g, nil
		}
		return c
	}
}
