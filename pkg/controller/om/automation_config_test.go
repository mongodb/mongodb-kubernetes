package om

import (
	"testing"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/stretchr/testify/assert"
)

var testAC = *getTestAutomationConfig()

func getTestAutomationConfig() *AutomationConfig {
	a, _ := BuildAutomationConfigFromBytes(loadBytesFromTestData("automation_config.json"))
	return a
}

func TestUntouchedFieldsAreNotDeleted(t *testing.T) {
	a := getTestAutomationConfig()
	a.Auth.AutoUser = "some-user"
	a.AgentSSL.ClientCertificateMode = util.RequireClientCertificates

	a.Apply()

	auth := a.Deployment["auth"].(map[string]interface{})
	authTest := testAC.Deployment["auth"].(map[string]interface{})
	assert.Equal(t, auth["usersDeleted"], authTest["usersDeleted"])
	assert.Equal(t, auth["autoAuthMechanisms"], authTest["autoAuthMechanisms"])
	assert.Equal(t, auth["autoAuthRestrictions"], authTest["autoAuthRestrictions"])
	assert.Equal(t, auth["disabled"], authTest["disabled"])
	assert.Equal(t, auth["usersWanted"], authTest["usersWanted"])
}

func TestUserIsAddedToTheEnd(t *testing.T) {
	a := getTestAutomationConfig()

	a.Auth.AddUser(MongoDBUser{
		Database: "my-db",
		Username: "my-user",
		Roles:    []Role{{Role: "my-role", Database: "role-db"}},
	})

	a.Apply()

	assert.Len(t, getUsers(a.Deployment), 2)
}

func TestUserIsUpdated(t *testing.T) {
	a := getTestAutomationConfig()

	user := &a.Auth.Users[0]
	user.Database = "new-db"
	user.Username = "new-user"
	a.Apply()

	userFromDep := getUser(a.Deployment, 0)
	assert.Equal(t, "new-db", userFromDep["db"])
	assert.Equal(t, "new-user", userFromDep["user"])
}

// TestLastUserIsDeleted ensures our Transformer is working
// the default behaviour for merging an empty list with a non empty
// list is that the resulting list will be non-empty.
func TestLastUserIsDeleted(t *testing.T) {
	a := getTestAutomationConfig()

	a.Auth.RemoveUser("testUser", "testDb")
	assert.Equal(t, 0, len(a.Auth.Users))

	a.Apply()

	assert.Len(t, getUsers(a.Deployment), 0)
}

func TestUserIsDeleted(t *testing.T) {
	a := getTestAutomationConfig()

	// update the state of the automation config to have 2 users so we're not deleting the last one.
	a.Auth.AddUser(MongoDBUser{})
	a.Apply()

	assert.Len(t, getUsers(a.Deployment), 2)

	a.Auth.Users = removeUser(a.Auth.Users, 1)
	a.Apply()

	assert.Len(t, getUsers(a.Deployment), 1)
}

func TestRoleIsAddedToTheEnd(t *testing.T) {
	a := getTestAutomationConfig()

	a.Auth.Users[0].AddRole(Role{
		Database: "admin",
		Role:     "some-new-role",
	})
	a.Apply()
	roles := getRoles(a.Deployment, 0)

	assert.Len(t, roles, 4)
}

func TestRoleIsUpdated(t *testing.T) {
	a := getTestAutomationConfig()

	role := &a.Auth.Users[0].Roles[0]
	role.Database = "updated-db"
	role.Role = "updated-role"

	if err := a.Apply(); err != nil {
		assert.Fail(t, "Error applying changes")
	}

	actualRole := getRole(a.Deployment, 0, 0)
	assert.Equal(t, "updated-role", actualRole["role"])
	assert.Equal(t, "updated-db", actualRole["db"])
}

func TestRoleIsDeleted(t *testing.T) {
	a := getTestAutomationConfig()

	a.Auth.Users[0].Roles = remove(a.Auth.Users[0].Roles, 2)

	a.Apply()
	roles := getRoles(a.Deployment, 0)
	assert.Len(t, roles, 2)
}

func TestAllRolesAreDeleted(t *testing.T) {
	a := getTestAutomationConfig()
	a.Auth.Users[0].Roles = []Role{}
	a.Apply()
	roles := getRoles(a.Deployment, 0)
	assert.Len(t, roles, 0)
}

func TestRoleIsDeletedAndAppended(t *testing.T) {
	a := getTestAutomationConfig()

	a.Auth.Users[0].Roles = remove(a.Auth.Users[0].Roles, 2)

	newRole := Role{
		Database: "updated-db",
		Role:     "updated-role",
	}
	a.Auth.Users[0].Roles = append(a.Auth.Users[0].Roles, newRole)

	a.Apply()

	actualRole := getRole(a.Deployment, 0, 2)
	assert.Equal(t, "updated-role", actualRole["role"])
	assert.Equal(t, "updated-db", actualRole["db"])
}

func TestNoAdditionalFieldsAreAddedToAgentSSL(t *testing.T) {
	ac := getTestAutomationConfig()
	ac.AgentSSL = &AgentSSL{
		CAFilePath:            util.MergoDelete,
		ClientCertificateMode: util.OptionalClientCertficates,
		AutoPEMKeyFilePath:    util.MergoDelete,
	}

	ac.Apply()

	ssl := ac.Deployment["ssl"].(map[string]interface{})
	assert.Contains(t, ssl, "clientCertificateMode")

	assert.NotContains(t, ssl, "autoPEMKeyFilePath")
	assert.NotContains(t, ssl, "CAFilePath")
}

func TestCanResetAgentSSL(t *testing.T) {
	ac := getTestAutomationConfig()
	ac.AgentSSL = &AgentSSL{
		ClientCertificateMode: util.OptionalClientCertficates,
		CAFilePath:            util.CAFilePathInContainer,
		AutoPEMKeyFilePath:    util.AutomationAgentPemFilePath,
	}

	ac.Apply()

	ssl := ac.Deployment["ssl"].(map[string]interface{})
	assert.Equal(t, ssl["clientCertificateMode"], util.OptionalClientCertficates)
	assert.Equal(t, ssl["autoPEMKeyFilePath"], util.AutomationAgentPemFilePath)
	assert.Equal(t, ssl["CAFilePath"], util.CAFilePathInContainer)

	ac.AgentSSL = &AgentSSL{
		CAFilePath:            util.MergoDelete,
		AutoPEMKeyFilePath:    util.MergoDelete,
		ClientCertificateMode: util.OptionalClientCertficates,
	}

	ac.Apply()

	ssl = ac.Deployment["ssl"].(map[string]interface{})
	assert.Equal(t, ssl["clientCertificateMode"], util.OptionalClientCertficates)
	assert.NotContains(t, ssl, "autoPEMKeyFilePath")
	assert.NotContains(t, ssl, "CAFilePath")
}

func getUsers(deployment map[string]interface{}) []interface{} {
	auth := deployment["auth"].(map[string]interface{})
	if users, ok := auth["usersWanted"]; ok {
		return users.([]interface{})
	}
	return make([]interface{}, 0)
}

func getUser(deployment map[string]interface{}, i int) map[string]interface{} {
	users := getUsers(deployment)
	return users[i].(map[string]interface{})
}

func getRoles(deployment map[string]interface{}, userIdx int) []interface{} {
	user := getUser(deployment, userIdx)
	return user["roles"].([]interface{})
}

func getRole(deployment map[string]interface{}, userIdx, roleIdx int) map[string]interface{} {
	roles := getRoles(deployment, userIdx)
	return roles[roleIdx].(map[string]interface{})
}

func remove(slice []Role, i int) []Role {
	copy(slice[i:], slice[i+1:])
	return slice[:len(slice)-1]
}

func removeUser(slice []MongoDBUser, i int) []MongoDBUser {
	copy(slice[i:], slice[i+1:])
	return slice[:len(slice)-1]
}
