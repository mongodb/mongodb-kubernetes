package om

import (
	"encoding/json"
	"testing"

	"github.com/spf13/cast"
	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/api/equality"

	"github.com/mongodb/mongodb-kubernetes/controllers/operator/ldap"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

var originalAutomationConfig = *getTestAutomationConfig()

func getTestAutomationConfig() *AutomationConfig {
	a, _ := BuildAutomationConfigFromBytes(loadBytesFromTestData("automation_config.json"))
	return a
}

func TestScramShaCreds_AreRemovedCorrectly(t *testing.T) {
	ac := getTestAutomationConfig()
	user := ac.Auth.Users[0]
	user.ScramSha256Creds = nil

	if err := ac.Apply(); err != nil {
		t.Fatal(err)
	}

	deploymentUser := getUser(ac.Deployment, 0)
	assert.NotContains(t, deploymentUser, "scramSha256Creds")
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
	tls := cast.ToStringMap(a.Deployment["tls"])
	assert.Equal(t, tls["clientCertificateMode"], util.RequireClientCertificates)

	// ensures fields in nested fields we don't know about are retained
	scramSha256Creds := cast.ToStringMap(getUser(a.Deployment, 0)["scramSha256Creds"])
	assert.Equal(t, float64(15000), scramSha256Creds["iterationCount"])
	assert.Equal(t, "I570PanWIx1eNUTo7j4ROl2/zIqMsVd6CcIE+A==", scramSha256Creds["salt"])
	assert.Equal(t, "M4/jskiMM0DpvG/qgMELWlfReqV2ZmwdU8+vJZ/4prc=", scramSha256Creds["serverKey"])
	assert.Equal(t, "m1dXf5hHJk7EOAAyJBxfsZvFx1HwtTdda6pFPm0BlOE=", scramSha256Creds["storedKey"])

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

func TestCanPrependUser(t *testing.T) {
	ac := getTestAutomationConfig()

	newUser := &MongoDBUser{
		Database:                   "myDatabase",
		Username:                   "myUsername",
		AuthenticationRestrictions: []string{},
		Roles: []*Role{
			{
				Role:     "myRole",
				Database: "myDb",
			},
		},
	}

	assert.Len(t, getUsers(ac.Deployment), 3)
	assert.Len(t, ac.Auth.Users, 3)

	ac.Auth.Users = append([]*MongoDBUser{newUser}, ac.Auth.Users...)

	if err := ac.Apply(); err != nil {
		t.Fatal(err)
	}

	dep := ac.Deployment

	firstUser := getUser(dep, 0)
	firstUsersRole := getRole(dep, 0, 0)
	secondUser := getUser(dep, 1)
	secondUsersRoles := getRoles(dep, 1)

	// the user added to the start of the list should be the first element
	assert.Equal(t, "myUsername", firstUser["user"])
	assert.Equal(t, "myDatabase", firstUser["db"])

	// it should have the single role provided
	assert.Equal(t, "myRole", firstUsersRole["role"])
	assert.Equal(t, "myDb", firstUsersRole["db"])

	// the already existing user should be the second element
	assert.Equal(t, "testUser0", secondUser["user"])
	assert.Equal(t, "testDb0", secondUser["db"])

	assert.Len(t, secondUsersRoles, 3, "second user should not have an additional role")

	// already existing user should not have been granted the additional role
	for _, omRoleInterface := range secondUsersRoles {
		omRoleMap := cast.ToStringMap(omRoleInterface)
		assert.False(t, omRoleMap["db"] == "myDb")
		assert.False(t, omRoleMap["role"] == "myRole")
	}

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

func TestUnknownFields_AreNotMergedWithOtherElements(t *testing.T) {
	ac := getTestAutomationConfig()

	userToDelete := getUser(ac.Deployment, 1)
	assert.Contains(t, userToDelete, "unknownFieldOne")
	assert.Contains(t, userToDelete, "unknownFieldTwo")

	ac.Auth.Users[1] = nil

	if err := ac.Apply(); err != nil {
		t.Fatal(err)
	}

	users := getUsers(ac.Deployment)

	// user got removed
	assert.Len(t, users, 2)

	// other users didn't accidentally get unknown fields merged into them
	for _, user := range users {
		userMap := cast.ToStringMap(user)
		assert.NotContains(t, userMap, "unknownFieldOne")
		assert.NotContains(t, userMap, "unknownFieldTwo")
	}
}

func TestSettingFieldInListToNil_RemovesElement(t *testing.T) {
	ac := getTestAutomationConfig()

	ac.Auth.Users[1] = nil

	if err := ac.Apply(); err != nil {
		t.Fatal(err)
	}

	assert.Len(t, getUsers(ac.Deployment), 2)
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

	tls := cast.ToStringMap(ac.Deployment["tls"])
	assert.Contains(t, tls, "clientCertificateMode")

	assert.NotContains(t, tls, "autoPEMKeyFilePath")
	assert.NotContains(t, tls, "CAFilePath")
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

	tls := cast.ToStringMap(ac.Deployment["tls"])
	assert.Equal(t, tls["clientCertificateMode"], util.OptionalClientCertficates)
	assert.Equal(t, tls["autoPEMKeyFilePath"], util.AutomationAgentPemFilePath)
	assert.Equal(t, tls["CAFilePath"], util.CAFilePathInContainer)

	ac.AgentSSL = &AgentSSL{
		CAFilePath:            util.MergoDelete,
		AutoPEMKeyFilePath:    util.MergoDelete,
		ClientCertificateMode: util.OptionalClientCertficates,
	}

	if err := ac.Apply(); err != nil {
		t.Fatal(err)
	}

	tls = cast.ToStringMap(ac.Deployment["tls"])
	assert.Equal(t, tls["clientCertificateMode"], util.OptionalClientCertficates)
	assert.NotContains(t, tls, "autoPEMKeyFilePath")
	assert.NotContains(t, tls, "CAFilePath")
}

func TestVersionsAndBuildsRetained(t *testing.T) {
	ac := getTestAutomationConfig()

	if err := ac.Apply(); err != nil {
		t.Fatal(err)
	}

	dep := ac.Deployment
	versions := getMongoDbVersions(dep)
	assert.Len(t, versions, 2)

	// ensure no elements lost from array
	builds1 := getVersionBuilds(dep, 0)
	builds2 := getVersionBuilds(dep, 1)

	// ensure the correct number of elements in each nested array
	assert.Len(t, builds1, 6)
	assert.Len(t, builds2, 11)

	/*
		{
			"architecture": "amd64",
			"bits": 64,
			"flavor": "suse",
			"gitVersion": "45d947729a0315accb6d4f15a6b06be6d9c19fe7",
			"maxOsVersion": "12",
			"minOsVersion": "11",
			"modules": [
				"enterprise"
			],
			"platform": "linux",
			"url": "https://downloads.mongodb.com/linux/mongodb-linux-x86_64-enterprise-suse11-3.2.0.tgz"
		}
	*/
	// ensure correct values for nested fields
	build1 := getVersionBuild(dep, 1, 4)
	assert.Equal(t, "amd64", build1["architecture"])
	assert.Equal(t, float64(64), build1["bits"])
	assert.Equal(t, "suse", build1["flavor"])
	assert.Equal(t, "45d947729a0315accb6d4f15a6b06be6d9c19fe7", build1["gitVersion"])
	assert.Equal(t, "12", build1["maxOsVersion"])
	assert.Equal(t, "11", build1["minOsVersion"])
	assert.Equal(t, "linux", build1["platform"])
	assert.Equal(t, "https://downloads.mongodb.com/linux/mongodb-linux-x86_64-enterprise-suse11-3.2.0.tgz", build1["url"])

	// nested list maintains untouched
	modulesList := build1["modules"].([]interface{})
	assert.Equal(t, "enterprise", modulesList[0])
}

func TestMergoDeleteWorksInNestedMapsWithFieldsNotReturnedByAutomationConfig(t *testing.T) {
	ac := getTestAutomationConfig()

	ac.Auth.Users[0].Database = util.MergoDelete

	if err := ac.Apply(); err != nil {
		t.Fatal(err)
	}

	user := getUser(ac.Deployment, 0)

	assert.NotContains(t, user, "db") // specify value to delete, it does not remain in final map
	assert.Contains(t, user, "user")  // value untouched remains
}

func TestDeletionOfMiddleElements(t *testing.T) {
	ac := getTestAutomationConfig()

	ac.Auth.AddUser(MongoDBUser{
		Database: "my-db",
		Username: "my-user",
		Roles:    []*Role{{Role: "my-role", Database: "role-db"}},
	})

	ac.Auth.AddUser(MongoDBUser{
		Database: "my-db-1",
		Username: "my-user-1",
		Roles:    []*Role{{Role: "my-role", Database: "role-db"}},
	})

	ac.Auth.AddUser(MongoDBUser{
		Database: "my-db-2",
		Username: "my-user-2",
		Roles:    []*Role{{Role: "my-role", Database: "role-db"}},
	})

	if err := ac.Apply(); err != nil {
		t.Fatal(err)
	}

	assert.Len(t, getUsers(ac.Deployment), 6)

	// remove the 3rd element of the list
	ac.Auth.Users[2] = nil

	if err := ac.Apply(); err != nil {
		t.Fatal(err)
	}

	assert.Len(t, getUsers(ac.Deployment), 5)

	users := getUsers(ac.Deployment)
	lastUser := cast.ToStringMap(users[len(users)-1])
	assert.Equal(t, lastUser["user"], "my-user-2")
	assert.Equal(t, lastUser["db"], "my-db-2")

	// my-user-1 was correctly removed from between the other two elements
	secondLastUser := cast.ToStringMap(users[len(users)-2])
	assert.Equal(t, "my-user-1", secondLastUser["user"])
	assert.Equal(t, "my-db-1", secondLastUser["db"])
}

func TestDeleteLastElement(t *testing.T) {
	ac := getTestAutomationConfig()
	ac.Auth.Users[len(ac.Auth.Users)-1] = nil

	if err := ac.Apply(); err != nil {
		t.Fatal(err)
	}

	users := getUsers(ac.Deployment)
	assert.Len(t, users, 2)

	lastUser := cast.ToStringMap(users[1])
	assert.Equal(t, "testDb1", lastUser["db"])
	assert.Equal(t, "testUser1", lastUser["user"])
}

func TestCanDeleteUsers_AndAddNewOnes_InSingleOperation(t *testing.T) {
	ac := getTestAutomationConfig()

	for i := range ac.Auth.Users {
		ac.Auth.Users[i] = nil
	}

	ac.Auth.AddUser(MongoDBUser{
		Database: "my-added-db",
		Username: "my-added-user",
		Roles:    []*Role{},
	})

	if err := ac.Apply(); err != nil {
		t.Fatal(err)
	}

	users := getUsers(ac.Deployment)
	assert.Len(t, users, 1)

	addedUser := cast.ToStringMap(users[0])
	assert.Equal(t, "my-added-db", addedUser["db"])
	assert.Equal(t, "my-added-user", addedUser["user"])
}

func TestOneUserDeleted_OneUserUpdated(t *testing.T) {
	ac := getTestAutomationConfig()
	ac.Auth.Users[1] = nil
	ac.Auth.Users[2].Database = "updated-database"

	if err := ac.Apply(); err != nil {
		t.Fatal(err)
	}

	users := getUsers(ac.Deployment)

	assert.Len(t, users, 2)
	updatedUser := cast.ToStringMap(users[1])
	assert.Equal(t, "updated-database", updatedUser["db"])
}

func TestAssigningListsReassignsInDeployment(t *testing.T) {
	ac := getTestAutomationConfig()

	ac.Auth.AutoAuthMechanisms = append(ac.Auth.AutoAuthMechanisms, "one", "two", "three")

	if err := ac.Apply(); err != nil {
		t.Fatal(err)
	}

	auth := cast.ToStringMap(ac.Deployment["auth"])
	authMechanisms := cast.ToSlice(auth["autoAuthMechanisms"])
	assert.Len(t, authMechanisms, 3)

	ac.Auth.AutoAuthMechanisms = []string{"two"}

	if err := ac.Apply(); err != nil {
		t.Fatal(err)
	}

	auth = cast.ToStringMap(ac.Deployment["auth"])
	authMechanisms = cast.ToSlice(auth["autoAuthMechanisms"])
	assert.Len(t, authMechanisms, 1)
	assert.Contains(t, authMechanisms, "two")

	ac.Auth.AutoAuthMechanisms = []string{}

	if err := ac.Apply(); err != nil {
		t.Fatal(err)
	}

	auth = cast.ToStringMap(ac.Deployment["auth"])
	authMechanisms = cast.ToSlice(auth["autoAuthMechanisms"])
	assert.Len(t, authMechanisms, 0)
}

func TestAutomationConfigEquality(t *testing.T) {
	deployment1 := NewDeployment()
	deployment1.setReplicaSets([]ReplicaSet{NewReplicaSet("1", "5.0.0")})

	deployment2 := NewDeployment()
	deployment2.setReplicaSets([]ReplicaSet{NewReplicaSet("2", "5.0.0")})

	authConfig := Auth{
		Users: []*MongoDBUser{
			{
				Roles: []*Role{
					{
						Role:     "root",
						Database: "db1",
					},
				},
			},
		},
		Disabled: false,
	}
	authConfig2 := authConfig

	agentSSLConfig := AgentSSL{
		CAFilePath: "/tmp/mypath",
	}
	agentSSLConfig2 := agentSSLConfig

	ldapConfig := ldap.Ldap{
		Servers: "server1",
	}
	ldapConfig2 := ldapConfig

	tests := map[string]struct {
		a                *AutomationConfig
		b                *AutomationConfig
		expectedEquality bool
	}{
		"Two empty configs are equal": {
			a:                &AutomationConfig{},
			b:                &AutomationConfig{},
			expectedEquality: true,
		},
		"Two different configs are not equal": {
			a:                getTestAutomationConfig(),
			b:                &AutomationConfig{},
			expectedEquality: false,
		},
		"Two different configs are equal apart from the deployment": {
			a: &AutomationConfig{
				Deployment: deployment1,
			},
			b: &AutomationConfig{
				Deployment: deployment2,
			},
			expectedEquality: true,
		},
		"Two the same configs created using the same structs are the same": {
			a: &AutomationConfig{
				Auth:       &authConfig,
				AgentSSL:   &agentSSLConfig,
				Deployment: deployment1,
				Ldap:       &ldapConfig,
			},
			b: &AutomationConfig{
				Auth:       &authConfig,
				AgentSSL:   &agentSSLConfig,
				Deployment: deployment1,
				Ldap:       &ldapConfig,
			},
			expectedEquality: true,
		},
		"Two the same configs created using deep copy (and structs with different addresses) are the same": {
			a: &AutomationConfig{
				Auth:     &authConfig,
				AgentSSL: &agentSSLConfig,
				Ldap:     &ldapConfig,
			},
			b: &AutomationConfig{
				Auth:     &authConfig2,
				AgentSSL: &agentSSLConfig2,
				Ldap:     &ldapConfig2,
			},
			expectedEquality: true,
		},
		"Same configs, except for MergoDelete, which is ignored": {
			a: &AutomationConfig{
				Auth: &Auth{
					NewAutoPwd:  util.MergoDelete,
					LdapGroupDN: "abc",
				},
				Ldap: &ldapConfig,
			},
			b: &AutomationConfig{
				Auth: &Auth{
					LdapGroupDN: "abc",
				},
				AgentSSL: &AgentSSL{
					AutoPEMKeyFilePath: util.MergoDelete,
				},
				Ldap: &ldapConfig2,
			},
			expectedEquality: true,
		},
	}
	for testName, testParameters := range tests {
		t.Run(testName, func(t *testing.T) {
			result := testParameters.a.EqualsWithoutDeployment(*testParameters.b)
			assert.Equalf(t, testParameters.expectedEquality, result, "Expected %v, got %v", testParameters.expectedEquality, result)
		})
	}
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

func TestLDAPIsMerged(t *testing.T) {
	ac := getTestAutomationConfig()
	ac.Ldap = &ldap.Ldap{
		AuthzQueryTemplate:            "AuthzQueryTemplate",
		BindMethod:                    "",
		BindQueryUser:                 "BindQueryUser",
		BindSaslMechanisms:            "BindSaslMechanisms",
		Servers:                       "",
		TransportSecurity:             "TransportSecurity",
		UserToDnMapping:               "UserToDnMapping",
		ValidateLDAPServerConfig:      false,
		BindQueryPassword:             "",
		TimeoutMS:                     1000,
		UserCacheInvalidationInterval: 60,
		CaFileContents:                "",
	}
	if err := ac.Apply(); err != nil {
		t.Fatal(err)
	}
	ldapMap := cast.ToStringMap(ac.Deployment["ldap"])
	assert.Equal(t, "AuthzQueryTemplate", ldapMap["authzQueryTemplate"])
	assert.Equal(t, "BindQueryUser", ldapMap["bindQueryUser"])
	assert.Equal(t, "BindSaslMechanisms", ldapMap["bindSaslMechanisms"])
	assert.Equal(t, "TransportSecurity", ldapMap["transportSecurity"])
	// ldap.Ldap is being merged by marshalling it to a map first, so ints end up as float64
	assert.Equal(t, float64(1000), ldapMap["timeoutMS"])
	assert.Equal(t, float64(60), ldapMap["userCacheInvalidationInterval"])
	// ensure zero value fields are added
	assert.Contains(t, ldapMap, "bindMethod")
	assert.Contains(t, ldapMap, "servers")
	assert.Contains(t, ldapMap, "validateLDAPServerConfig")
	assert.Contains(t, ldapMap, "bindQueryPassword")
	assert.Contains(t, ldapMap, "CAFileContents")
}

func TestApplyInto(t *testing.T) {
	config := AutomationConfig{
		Auth: NewAuth(),
		AgentSSL: &AgentSSL{
			CAFilePath:            util.MergoDelete,
			ClientCertificateMode: "test",
		},
		Deployment: Deployment{"tls": map[string]interface{}{"test": ""}},
		Ldap:       nil,
	}
	deepCopy := Deployment{"tls": map[string]interface{}{}}
	err := applyInto(config, &deepCopy)
	assert.NoError(t, err)

	// initial config.Deployment did not change
	assert.NotEqual(t, config.Deployment, deepCopy)

	// new deployment is the merge result of the previous config.Deployment + config
	assert.Equal(t, Deployment{"tls": map[string]interface{}{"clientCertificateMode": "test", "test": ""}}, deepCopy)
}

func changeTypes(deployment Deployment) error {
	rs := deployment.getReplicaSets()
	deployment.setReplicaSets(rs)
	return nil
}

func TestIsEqual(t *testing.T) {
	type args struct {
		depFunc    func(Deployment) error
		deployment Deployment
	}
	tests := []struct {
		name string
		args args
		want bool
	}{
		{
			name: "depFunc does not do anything",
			args: args{
				depFunc: func(deployment Deployment) error {
					return nil
				},
				deployment: getDeploymentWithRSOverTheWire(t),
			},
			want: true,
		},
		{
			name: "depFunc does changes types, but content does not change",
			args: args{
				depFunc:    changeTypes,
				deployment: getDeploymentWithRSOverTheWire(t),
			},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := isEqualAfterModification(tt.args.depFunc, tt.args.deployment)
			assert.NoError(t, err)
			assert.Equalf(t, tt.want, got, "isEqualAfterModification(%v, %v)", tt.args.depFunc, tt.args.deployment)
		})
	}
}

// TestIsEqualNotWorkingWithTypeChanges is a test that shows that deep equality does not work if our depFunc changes
// the underlying types as we mostly do.
func TestIsEqualNotWorkingWithTypeChanges(t *testing.T) {
	t.Run("is not working", func(t *testing.T) {
		overTheWire := getDeploymentWithRSOverTheWire(t)

		original, err := util.MapDeepCopy(overTheWire)
		assert.NoError(t, err)

		_ = changeTypes(overTheWire)

		equal := equality.Semantic.DeepEqual(original, overTheWire)
		assert.False(t, equal)
	})
}

func getDeploymentWithRSOverTheWire(t *testing.T) Deployment {
	overTheWire := getTestAutomationConfig().Deployment
	overTheWire.addReplicaSet(NewReplicaSet("rs-1", "3.2.0"))
	overTheWire.addReplicaSet(NewReplicaSet("rs-2", "3.2.0"))
	marshal, err := json.Marshal(overTheWire) // as we get it over the wire, we need to reflect that
	assert.NoError(t, err)
	err = json.Unmarshal(marshal, &overTheWire)
	assert.NoError(t, err)
	return overTheWire
}
