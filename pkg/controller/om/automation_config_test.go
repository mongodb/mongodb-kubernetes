package om

import (
	"testing"

	"github.com/spf13/cast"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/stretchr/testify/assert"
)

var originalAutomationConfig = *getTestAutomationConfig()

func getTestAutomationConfig() *AutomationConfig {
	a, _ := BuildAutomationConfigFromBytes(loadBytesFromTestData("automation_config.json"))
	return a
}

func TestUntouchedFieldsAreNotDeleted(t *testing.T) {
	a := getTestAutomationConfig()
	a.Auth.AutoUser = "some-user"
	a.AgentSSL.ClientCertificateMode = util.RequireClientCertificates

	if err := a.Apply(); err != nil {
		t.Fatal(err)
	}

	auth := cast.ToStringMap(a.Deployment["auth"])
	originalAuth := cast.ToStringMap(originalAutomationConfig.Deployment["auth"])

	// ensure values aren't overridden
	assert.Equal(t, auth["usersDeleted"], originalAuth["usersDeleted"])
	assert.Equal(t, auth["autoAuthMechanisms"], originalAuth["autoAuthMechanisms"])
	assert.Equal(t, auth["autoAuthRestrictions"], originalAuth["autoAuthRestrictions"])
	assert.Equal(t, auth["disabled"], originalAuth["disabled"])
	assert.Equal(t, auth["usersWanted"], originalAuth["usersWanted"])

	// ensure values we specified are overridden
	assert.Equal(t, auth["autoUser"], "some-user")
	ssl := cast.ToStringMap(a.Deployment["ssl"])
	assert.Equal(t, ssl["clientCertificateMode"], util.RequireClientCertificates)

	// ensure values we know nothing about aren't touched
	options := cast.ToStringMap(a.Deployment["options"])
	assert.Equal(t, options["downloadBase"], "/var/lib/mongodb-mms-automation")
	assert.Equal(t, options["downloadBaseWindows"], "%SystemDrive%\\MMSAutomation\\versions")
}

func TestUserIsAddedToTheEnd(t *testing.T) {
	a := getTestAutomationConfig()

	a.Auth.AddUser(MongoDBUser{
		Database: "my-db",
		Username: "my-user",
		Roles:    []*Role{{Role: "my-role", Database: "role-db"}},
	})

	if err := a.Apply(); err != nil {
		t.Fatal(err)
	}

	assert.Len(t, getUsers(a.Deployment), 4)

	lastUser := getUser(a.Deployment, 3)

	assert.Equal(t, "my-db", lastUser["db"])
	assert.Equal(t, "my-user", lastUser["user"])
	roles := cast.ToSlice(lastUser["roles"])
	role := cast.ToStringMap(roles[0])
	assert.Equal(t, "my-role", role["role"])
	assert.Equal(t, "role-db", role["db"])

}

func TestUserIsUpdated_AndOtherUsersDontGetAffected(t *testing.T) {
	ac := getTestAutomationConfig()

	originalUser := getUser(ac.Deployment, 0)

	assert.Equal(t, "testDb0", originalUser["db"])
	assert.Equal(t, "testUser0", originalUser["user"])

	// change the fields on the struct
	user := ac.Auth.Users[0]

	// struct fields should be read correctly
	assert.Equal(t, "testDb0", user.Database)
	assert.Equal(t, "testUser0", user.Username)

	user.Database = "new-db"
	user.Username = "new-user"

	if err := ac.Apply(); err != nil {
		t.Fatal(err)
	}

	userFromDep := getUser(ac.Deployment, 0)
	assert.Equal(t, "new-db", userFromDep["db"])
	assert.Equal(t, "new-user", userFromDep["user"])

	allUsers := getUsers(ac.Deployment)
	for _, user := range allUsers[1:] {
		userMap := cast.ToStringMap(user)
		assert.NotEqual(t, "new-db", userMap["db"])
		assert.NotEqual(t, "new-user", userMap["user"])
	}
}

func TestUserIsDeleted(t *testing.T) {
	a := getTestAutomationConfig()

	a.Auth.Users[1] = nil
	a.Auth.Users[2] = nil

	if err := a.Apply(); err != nil {
		t.Fatal(err)
	}

	assert.Len(t, getUsers(a.Deployment), 1)
}

func TestRoleIsAddedToTheEnd(t *testing.T) {
	ac := getTestAutomationConfig()

	roles := getRoles(ac.Deployment, 0)
	assert.Len(t, roles, 3)

	ac.Auth.Users[0].AddRole(&Role{
		Database: "admin",
		Role:     "some-new-role",
	})

	if err := ac.Apply(); err != nil {
		t.Fatal(err)
	}

	roles = getRoles(ac.Deployment, 0)
	assert.Len(t, roles, 4)

	lastRole := cast.ToStringMap(roles[len(roles)-1])

	assert.Equal(t, "admin", lastRole["db"])
	assert.Equal(t, "some-new-role", lastRole["role"])
}

func TestRoleIsUpdated(t *testing.T) {
	ac := getTestAutomationConfig()

	originalRole := getRole(ac.Deployment, 0, 0)

	assert.Equal(t, "admin", originalRole["db"])
	assert.Equal(t, "backup", originalRole["role"])

	role := ac.Auth.Users[0].Roles[0]
	role.Database = "updated-db"
	role.Role = "updated-role"

	if err := ac.Apply(); err != nil {
		assert.Fail(t, "Error applying changes")
	}

	actualRole := getRole(ac.Deployment, 0, 0)
	assert.Equal(t, "updated-role", actualRole["role"])
	assert.Equal(t, "updated-db", actualRole["db"])
}

func TestMiddleRoleIsCorrectlyDeleted(t *testing.T) {
	a := getTestAutomationConfig()

	a.Auth.Users[0].Roles = remove(a.Auth.Users[0].Roles, 1)

	if err := a.Apply(); err != nil {
		t.Fatal(err)
	}

	roles := getRoles(a.Deployment, 0)
	assert.Len(t, roles, 2)

	firstRole := cast.ToStringMap(roles[0])
	secondRole := cast.ToStringMap(roles[1])

	// first role from automation_config.json
	assert.Equal(t, "admin", firstRole["db"])
	assert.Equal(t, "backup", firstRole["role"])

	// third role from automation_config.json
	assert.Equal(t, "admin", secondRole["db"])
	assert.Equal(t, "automation", secondRole["role"])

}

func TestAllRolesAreDeleted(t *testing.T) {
	a := getTestAutomationConfig()
	a.Auth.Users[0].Roles = []*Role{}
	if err := a.Apply(); err != nil {
		t.Fatal(err)
	}

	roles := getRoles(a.Deployment, 0)
	assert.Len(t, roles, 0)
}

func TestRoleIsDeletedAndAppended(t *testing.T) {
	a := getTestAutomationConfig()

	a.Auth.Users[0].Roles = remove(a.Auth.Users[0].Roles, 2)

	newRole := &Role{
		Database: "updated-db",
		Role:     "updated-role",
	}
	a.Auth.Users[0].Roles = append(a.Auth.Users[0].Roles, newRole)

	if err := a.Apply(); err != nil {
		t.Fatal(err)
	}

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

	if err := ac.Apply(); err != nil {
		t.Fatal(err)
	}

	ssl := cast.ToStringMap(ac.Deployment["ssl"])
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

	if err := ac.Apply(); err != nil {
		t.Fatal(err)
	}

	ssl := cast.ToStringMap(ac.Deployment["ssl"])
	assert.Equal(t, ssl["clientCertificateMode"], util.OptionalClientCertficates)
	assert.Equal(t, ssl["autoPEMKeyFilePath"], util.AutomationAgentPemFilePath)
	assert.Equal(t, ssl["CAFilePath"], util.CAFilePathInContainer)

	ac.AgentSSL = &AgentSSL{
		CAFilePath:            util.MergoDelete,
		AutoPEMKeyFilePath:    util.MergoDelete,
		ClientCertificateMode: util.OptionalClientCertficates,
	}

	if err := ac.Apply(); err != nil {
		t.Fatal(err)
	}

	ssl = cast.ToStringMap(ac.Deployment["ssl"])
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

func remove(slice []*Role, i int) []*Role {
	copy(slice[i:], slice[i+1:])
	return slice[:len(slice)-1]
}

func getMongoDbVersions(deployment map[string]interface{}) []interface{} {
	return deployment["mongoDbVersions"].([]interface{})
}

func getVersionBuilds(deployment map[string]interface{}, versionIndex int) []interface{} {
	versions := deployment["mongoDbVersions"].([]interface{})
	return versions[versionIndex].(map[string]interface{})["builds"].([]interface{})
}

func getVersionBuild(deployment map[string]interface{}, versionIndex, buildIndex int) map[string]interface{} {
	return getVersionBuilds(deployment, versionIndex)[buildIndex].(map[string]interface{})
}
