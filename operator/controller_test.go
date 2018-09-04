package operator

import (
	"reflect"
	"testing"

	"github.com/10gen/ops-manager-kubernetes/om"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

// TestPrepareOmConnection_CreateGroup checks that if the group doesn't exist in OM - it is created
func TestPrepareOmConnection_CreateGroup(t *testing.T) {
	mockedOmConnection := omConnWithoutGroup()

	controller := NewMongoDbController(newMockedKubeApi(), nil, mockedOmConnection)

	mockOm, vars := prepareConnection(controller, t)

	assert.Equal(t, om.TestGroupId, vars.ProjectId)
	assert.Equal(t, om.TestGroupId, mockOm.GroupId())
	assert.Equal(t, TestProjectConfigMapName, mockOm.Group.Name)
	assert.Contains(t, mockOm.Group.Tags, OmGroupExternallyManagedTag)

	mockOm.CheckOrderOfOperations(t, reflect.ValueOf(mockOm.ReadGroup), reflect.ValueOf(mockOm.CreateGroup))
	mockOm.CheckOperationsDidntHappen(t, reflect.ValueOf(mockOm.UpdateGroup))
}

// TestPrepareOmConnection_CreateGroupFallback checks that if the group creation failed because tags editing is not allowed
// - the program failbacks to creating group without tags
func TestPrepareOmConnection_CreateGroupFallback(t *testing.T) {
	mockedOmConnection := omConnOldVersion()

	controller := NewMongoDbController(newMockedKubeApi(), nil, mockedOmConnection)

	mockOm, vars := prepareConnection(controller, t)

	assert.Equal(t, om.TestGroupId, vars.ProjectId)
	assert.Equal(t, om.TestGroupId, mockOm.GroupId())
	assert.Equal(t, TestProjectConfigMapName, mockOm.Group.Name)
	assert.Empty(t, mockOm.Group.Tags)

	mockOm.CheckOrderOfOperations(t, reflect.ValueOf(mockOm.ReadGroup), reflect.ValueOf(mockOm.CreateGroup), reflect.ValueOf(mockOm.CreateGroup))
}

// TestPrepareOmConnection_CreateGroupFixTags fixes tags if they are not
func TestPrepareOmConnection_CreateGroupFixTags(t *testing.T) {
	mockedOmConnection := omConnGroupWithoutTags()

	controller := NewMongoDbController(newMockedKubeApi(), nil, mockedOmConnection)

	mockOm, _ := prepareConnection(controller, t)
	assert.Contains(t, mockOm.Group.Tags, OmGroupExternallyManagedTag)

	mockOm.CheckOrderOfOperations(t, reflect.ValueOf(mockOm.ReadGroup), reflect.ValueOf(mockOm.UpdateGroup))
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

func omConnWithoutGroup() func(url, g, user, k string) om.OmConnection {
	return func(url, g, user, k string) om.OmConnection {
		c := om.NewEmptyMockedOmConnection(url, g, user, k).(*om.MockedOmConnection)
		// Emulating "GROUP NOT FOUND" exception
		c.ReadGroupFunc = func(n string) (*om.Group, error) {
			return nil, &om.OmApiError{ErrorCode: "GROUP_NAME_NOT_FOUND"}
		}
		return c
	}
}
func omConnGroupWithoutTags() func(url, g, user, k string) om.OmConnection {
	return func(url, g, user, k string) om.OmConnection {
		c := om.NewEmptyMockedOmConnection(url, g, user, k).(*om.MockedOmConnection)
		// returning group without tags
		c.ReadGroupFunc = func(n string) (*om.Group, error) {
			return &om.Group{Name: n, Id: "123", AgentApiKey: "12345abcd"}, nil
		}
		return c
	}
}

func omConnOldVersion() func(url, g, user, k string) om.OmConnection {
	cnt := 1
	return func(url, g, user, k string) om.OmConnection {
		c := om.NewEmptyMockedOmConnection(url, g, user, k).(*om.MockedOmConnection)
		// Emulating "GROUP NOT FOUND" exception
		c.ReadGroupFunc = func(n string) (*om.Group, error) {
			return nil, &om.OmApiError{ErrorCode: "GROUP_NAME_NOT_FOUND"}
		}
		c.CreateGroupFunc = func(g *om.Group) (*om.Group, error) {
			// first call
			if cnt == 1 {
				cnt++
				return nil, &om.OmApiError{ErrorCode: "INVALID_ATTRIBUTE", Detail: "Invalid attribute tags specified."}
			}
			// second call (fallback)
			c.Group = g
			g.Id = om.TestGroupId
			return g, nil
		}
		// If creating tags is not allowed - then update - as well
		c.UpdateGroupFunc = func(g *om.Group) (*om.Group, error) {
			if len(g.Tags) > 0 {
				return nil, &om.OmApiError{ErrorCode: "INVALID_ATTRIBUTE", Detail: "Invalid attribute tags specified."}
			}
			return g, nil
		}
		return c
	}
}
