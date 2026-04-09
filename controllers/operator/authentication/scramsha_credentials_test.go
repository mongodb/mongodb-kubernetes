package authentication

import (
	"crypto/sha1" //nolint //Part of the algorithm
	"hash"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

func TestScramSha1SecretsMatch(t *testing.T) {
	// these were taken from MongoDB.  passwordHash is from authSchema
	// 3. iterationCount, salt, storedKey, and serverKey are from
	// authSchema 5 (after upgrading from authSchema 3)
	assertSecretsMatch(t, sha1.New, "caeec61ba3b15b15b188d29e876514e8", 10, "S3cuk2Rnu/MlbewzxrmmVA==", "sYBa3XlSPKNrgjzhOuEuRlJY4dQ=", "zuAxRSQb3gZkbaB1IGlusK4jy1M=")
	assertSecretsMatch(t, sha1.New, "4d9625b297999b3ca786d4a9622d04f1", 10, "kW9KbCQiCOll5Ljd44cjkQ==", "VJ8fFVHkPltibvT//mG/OWw44Hc=", "ceDRsgj9HezpZ4/vkZX8GZNNN50=")
	assertSecretsMatch(t, sha1.New, "fd0a78e418dcef39f8c768222810b894", 10, "hhX6xsoID6FeWjXncuNgAg==", "TxgaZJ4cIn+S9EfTcc9IOEG7RGc=", "d6/qjwBs0qkPKfUAjSh5eemsySE=")
}

func assertSecretsMatch(t *testing.T, hash func() hash.Hash, passwordHash string, iterationCount int, salt, storedKey, serverKey string) {
	computedStoredKey, computedServerKey, err := generateB64EncodedSecrets(hash, passwordHash, salt, iterationCount)
	assert.NoError(t, err)
	assert.Equal(t, computedStoredKey, storedKey)
	assert.Equal(t, computedServerKey, serverKey)
}

func emptyAC() *om.AutomationConfig {
	return &om.AutomationConfig{
		Auth: &om.Auth{
			Users: make([]*om.MongoDBUser, 0),
		},
	}
}

func acWithUser(u *om.MongoDBUser) *om.AutomationConfig {
	ac := emptyAC()
	ac.Auth.Users = append(ac.Auth.Users, u)
	return ac
}

func Test_IsPasswordChanged(t *testing.T) {
	userPassword := "secretpassword"
	userNewPassword := "newsecretpassword"

	mongoUser := om.MongoDBUser{
		Username: "new-user",
		Database: "admin",
	}

	ac := emptyAC()
	// will generate scram creds for the user mongoUser and set it in its fields
	_ = ConfigureScramCredentials(&mongoUser, userPassword, ac)

	ac.Auth.Users = append(ac.Auth.Users, &om.MongoDBUser{
		Username:         mongoUser.Username,
		Database:         mongoUser.Database,
		ScramSha256Creds: mongoUser.ScramSha256Creds,
		ScramSha1Creds:   mongoUser.ScramSha1Creds,
	})

	// now that the scram creds are set in the automation config, let's say the reconciliation happens again
	// with the same user and same password, IsPasswordChanged should return false
	_, u := ac.Auth.GetUser(mongoUser.Username, mongoUser.Database)
	op, err := IsPasswordChanged(&mongoUser, userPassword, u)
	assert.Nil(t, err)
	assert.False(t, op)

	// if reconciliation happens again with diff password, IsPasswordChanged should return true
	op, err = IsPasswordChanged(&mongoUser, userNewPassword, u)
	assert.Nil(t, err)
	assert.True(t, op)
}

// Test_ConfigureScramCredentials_NewUser verifies that a brand-new user (not in
// AC) gets both SHA-256 and SHA-1 creds generated and mechanisms left empty [].
func Test_ConfigureScramCredentials_NewUser(t *testing.T) {
	ac := emptyAC()
	user := om.MongoDBUser{Username: "alice", Database: "admin", Mechanisms: []string{}}

	require.NoError(t, ConfigureScramCredentials(&user, "pass", ac))

	assert.NotNil(t, user.ScramSha256Creds)
	assert.NotNil(t, user.ScramSha1Creds)
	assert.Empty(t, user.Mechanisms, "new user should have empty mechanisms")
}

// Test_ConfigureScramCredentials_OMUserBothMechanisms verifies that a user
// coming from OM with both mechanisms set has mechanisms populated correctly.
func Test_ConfigureScramCredentials_OMUserBothMechanisms(t *testing.T) {
	// Build an existing user in the AC with both creds and both mechanisms.
	seed := om.MongoDBUser{Username: "bob", Database: "admin", Mechanisms: []string{}}
	seedAC := emptyAC()
	require.NoError(t, ConfigureScramCredentials(&seed, "pass", seedAC))

	acUser := &om.MongoDBUser{
		Username:         "bob",
		Database:         "admin",
		ScramSha256Creds: seed.ScramSha256Creds,
		ScramSha1Creds:   seed.ScramSha1Creds,
		Mechanisms:       []string{util.AutomationConfigScramSha256Option, util.SCRAMSHA1},
	}
	ac := acWithUser(acUser)

	user := om.MongoDBUser{Username: "bob", Database: "admin", Mechanisms: []string{}}
	require.NoError(t, ConfigureScramCredentials(&user, "pass", ac))

	assert.Equal(t, []string{util.AutomationConfigScramSha256Option, util.SCRAMSHA1}, user.Mechanisms)
}

// Test_ConfigureScramCredentials_OMUserOnlySha256 verifies that a user that
// only has SHA-256 in OM gets only SHA-256 creds and mechanisms = [SCRAM-SHA-256].
func Test_ConfigureScramCredentials_OMUserOnlySha256(t *testing.T) {
	seed := om.MongoDBUser{Username: "carol", Database: "admin", Mechanisms: []string{}}
	seedAC := emptyAC()
	require.NoError(t, ConfigureScramCredentials(&seed, "pass", seedAC))

	acUser := &om.MongoDBUser{
		Username:         "carol",
		Database:         "admin",
		ScramSha256Creds: seed.ScramSha256Creds,
		ScramSha1Creds:   nil,
		Mechanisms:       []string{util.AutomationConfigScramSha256Option},
	}
	ac := acWithUser(acUser)

	user := om.MongoDBUser{Username: "carol", Database: "admin", Mechanisms: []string{}}
	require.NoError(t, ConfigureScramCredentials(&user, "pass", ac))

	assert.NotNil(t, user.ScramSha256Creds)
	assert.Nil(t, user.ScramSha1Creds)
	assert.Equal(t, []string{util.AutomationConfigScramSha256Option}, user.Mechanisms)
}

// Test_ConfigureScramCredentials_OMUserOnlySha1 verifies that a user that
// only has SHA-1 in OM gets only SHA-1 creds and mechanisms = [SCRAM-SHA-1].
func Test_ConfigureScramCredentials_OMUserOnlySha1(t *testing.T) {
	seed := om.MongoDBUser{Username: "dave", Database: "admin", Mechanisms: []string{}}
	seedAC := emptyAC()
	require.NoError(t, ConfigureScramCredentials(&seed, "pass", seedAC))

	acUser := &om.MongoDBUser{
		Username:         "dave",
		Database:         "admin",
		ScramSha256Creds: nil,
		ScramSha1Creds:   seed.ScramSha1Creds,
		Mechanisms:       []string{util.SCRAMSHA1},
	}
	ac := acWithUser(acUser)

	user := om.MongoDBUser{Username: "dave", Database: "admin", Mechanisms: []string{}}
	require.NoError(t, ConfigureScramCredentials(&user, "pass", ac))

	assert.Nil(t, user.ScramSha256Creds)
	assert.NotNil(t, user.ScramSha1Creds)
	assert.Equal(t, []string{util.SCRAMSHA1}, user.Mechanisms)
}

// Test_ConfigureScramCredentials_OMUserNoMechanisms verifies that a user that
// exists in OM but has no mechanisms set (k8s-created) keeps mechanisms empty [].
func Test_ConfigureScramCredentials_OMUserNoMechanisms(t *testing.T) {
	seed := om.MongoDBUser{Username: "eve", Database: "admin", Mechanisms: []string{}}
	seedAC := emptyAC()
	require.NoError(t, ConfigureScramCredentials(&seed, "pass", seedAC))

	acUser := &om.MongoDBUser{
		Username:         "eve",
		Database:         "admin",
		ScramSha256Creds: seed.ScramSha256Creds,
		ScramSha1Creds:   seed.ScramSha1Creds,
		Mechanisms:       []string{},
	}
	ac := acWithUser(acUser)

	user := om.MongoDBUser{Username: "eve", Database: "admin", Mechanisms: []string{}}
	require.NoError(t, ConfigureScramCredentials(&user, "pass", ac))

	assert.NotNil(t, user.ScramSha256Creds)
	assert.NotNil(t, user.ScramSha1Creds)
	assert.Empty(t, user.Mechanisms, "k8s-originated user should keep empty mechanisms")
}

// Test_ConfigureScramCredentials_PasswordChange_MaintainsMechanisms verifies
// that changing a password keeps the same mechanism set as in the AC.
func Test_ConfigureScramCredentials_PasswordChange_MaintainsMechanisms(t *testing.T) {
	seed := om.MongoDBUser{Username: "frank", Database: "admin", Mechanisms: []string{}}
	seedAC := emptyAC()
	require.NoError(t, ConfigureScramCredentials(&seed, "oldpass", seedAC))

	acUser := &om.MongoDBUser{
		Username:         "frank",
		Database:         "admin",
		ScramSha256Creds: seed.ScramSha256Creds,
		ScramSha1Creds:   nil,
		Mechanisms:       []string{util.AutomationConfigScramSha256Option},
	}
	ac := acWithUser(acUser)

	user := om.MongoDBUser{Username: "frank", Database: "admin", Mechanisms: []string{}}
	require.NoError(t, ConfigureScramCredentials(&user, "newpass", ac))

	// Only SHA-256 should be regenerated; SHA-1 stays nil.
	assert.NotNil(t, user.ScramSha256Creds)
	assert.Nil(t, user.ScramSha1Creds)
	assert.Equal(t, []string{util.AutomationConfigScramSha256Option}, user.Mechanisms)
}
