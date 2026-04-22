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

// Test_isCredChanged_MalformedSalt verifies that a malformed base64 salt is returned
// as (true, err) rather than silently regenerating creds.
func Test_isCredChanged_MalformedSalt(t *testing.T) {
	creds := &om.ScramShaCreds{Salt: "!!!not-valid-base64!!!"}
	changed, err := isCredChanged("alice", "pass", creds, ScramSha256)
	assert.True(t, changed)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "error decoding salt")
}

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
	_, _ = ConfigureScramCredentials(&mongoUser, userPassword, ac)

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

	followUp, err := ConfigureScramCredentials(&user, "pass", ac)
	require.NoError(t, err)

	assert.False(t, followUp)
	assert.NotNil(t, user.ScramSha256Creds)
	assert.NotNil(t, user.ScramSha1Creds)
	assert.Empty(t, user.Mechanisms)
}

// Test_ConfigureScramCredentials_OMUserBothMechanisms verifies that a user
// coming from OM with both mechanisms set has mechanisms populated correctly.
func Test_ConfigureScramCredentials_OMUserBothMechanisms(t *testing.T) {
	// Build an existing user in the AC with both creds and both mechanisms.
	seed := om.MongoDBUser{Username: "bob", Database: "admin", Mechanisms: []string{}}
	seedAC := emptyAC()
	_, err := ConfigureScramCredentials(&seed, "pass", seedAC)
	require.NoError(t, err)

	acUser := &om.MongoDBUser{
		Username:         "bob",
		Database:         "admin",
		ScramSha256Creds: seed.ScramSha256Creds,
		ScramSha1Creds:   seed.ScramSha1Creds,
		Mechanisms:       []string{util.AutomationConfigScramSha256Option, util.SCRAMSHA1},
	}
	ac := acWithUser(acUser)

	user := om.MongoDBUser{Username: "bob", Database: "admin", Mechanisms: []string{}}
	followUp, err := ConfigureScramCredentials(&user, "pass", ac)
	require.NoError(t, err)
	assert.True(t, followUp)

	assert.Nil(t, user.Mechanisms)
	assert.Equal(t, "pass", user.InitPassword)
}

// Test_ConfigureScramCredentials_OMUserOnlySha256 verifies that a user that
// only has SHA-256 in OM gets only SHA-256 creds and mechanisms = [SCRAM-SHA-256].
func Test_ConfigureScramCredentials_OMUserOnlySha256(t *testing.T) {
	seed := om.MongoDBUser{Username: "carol", Database: "admin", Mechanisms: []string{}}
	seedAC := emptyAC()
	_, err := ConfigureScramCredentials(&seed, "pass", seedAC)
	require.NoError(t, err)

	acUser := &om.MongoDBUser{
		Username:         "carol",
		Database:         "admin",
		ScramSha256Creds: seed.ScramSha256Creds,
		ScramSha1Creds:   nil,
		Mechanisms:       []string{util.AutomationConfigScramSha256Option},
	}
	ac := acWithUser(acUser)

	user := om.MongoDBUser{Username: "carol", Database: "admin", Mechanisms: []string{}}
	followUp, err := ConfigureScramCredentials(&user, "pass", ac)
	require.NoError(t, err)
	assert.True(t, followUp)

	assert.NotNil(t, user.ScramSha256Creds)
	assert.Nil(t, user.ScramSha1Creds)
	assert.Nil(t, user.Mechanisms)
	assert.Equal(t, "pass", user.InitPassword)
}

// Test_ConfigureScramCredentials_OMUserOnlySha1 verifies that a user that
// only has SHA-1 in OM gets only SHA-1 creds and mechanisms = [SCRAM-SHA-1].
func Test_ConfigureScramCredentials_OMUserOnlySha1(t *testing.T) {
	seed := om.MongoDBUser{Username: "dave", Database: "admin", Mechanisms: []string{}}
	seedAC := emptyAC()
	_, err := ConfigureScramCredentials(&seed, "pass", seedAC)
	require.NoError(t, err)

	acUser := &om.MongoDBUser{
		Username:         "dave",
		Database:         "admin",
		ScramSha256Creds: nil,
		ScramSha1Creds:   seed.ScramSha1Creds,
		Mechanisms:       []string{util.SCRAMSHA1},
	}
	ac := acWithUser(acUser)

	user := om.MongoDBUser{Username: "dave", Database: "admin", Mechanisms: []string{}}
	followUp, err := ConfigureScramCredentials(&user, "pass", ac)
	require.NoError(t, err)
	assert.True(t, followUp)

	assert.Nil(t, user.ScramSha256Creds)
	assert.NotNil(t, user.ScramSha1Creds)
	assert.Nil(t, user.Mechanisms)
	assert.Equal(t, "pass", user.InitPassword)
}

// Test_ConfigureScramCredentials_OMUserNoMechanisms verifies that a user that
// exists in OM but has no mechanisms set (k8s-created) keeps mechanisms empty [].
func Test_ConfigureScramCredentials_OMUserNoMechanisms(t *testing.T) {
	seed := om.MongoDBUser{Username: "eve", Database: "admin", Mechanisms: []string{}}
	seedAC := emptyAC()
	_, err := ConfigureScramCredentials(&seed, "pass", seedAC)
	require.NoError(t, err)

	acUser := &om.MongoDBUser{
		Username:         "eve",
		Database:         "admin",
		ScramSha256Creds: seed.ScramSha256Creds,
		ScramSha1Creds:   seed.ScramSha1Creds,
		Mechanisms:       []string{},
	}
	ac := acWithUser(acUser)

	user := om.MongoDBUser{Username: "eve", Database: "admin", Mechanisms: []string{}}
	followUp, err := ConfigureScramCredentials(&user, "pass", ac)
	require.NoError(t, err)
	assert.False(t, followUp)

	assert.NotNil(t, user.ScramSha256Creds)
	assert.NotNil(t, user.ScramSha1Creds)
	assert.Empty(t, user.Mechanisms, "k8s-originated user should keep empty mechanisms")
}

// Test_ConfigureScramCredentials_PasswordChange_ImportedUserErrors verifies that
// supplying a non-matching password for an imported user (mechanisms set) returns
// an error instead of silently regenerating the credentials.
func Test_ConfigureScramCredentials_PasswordChange_ImportedUserErrors(t *testing.T) {
	seed := om.MongoDBUser{Username: "frank", Database: "admin", Mechanisms: []string{}}
	seedAC := emptyAC()
	_, err := ConfigureScramCredentials(&seed, "oldpass", seedAC)
	require.NoError(t, err)

	acUser := &om.MongoDBUser{
		Username:         "frank",
		Database:         "admin",
		ScramSha256Creds: seed.ScramSha256Creds,
		ScramSha1Creds:   nil,
		Mechanisms:       []string{util.AutomationConfigScramSha256Option},
	}
	ac := acWithUser(acUser)

	user := om.MongoDBUser{Username: "frank", Database: "admin", Mechanisms: []string{}}
	_, err = ConfigureScramCredentials(&user, "newpass", ac)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match existing scramSha256 credentials")
}

// Test_ConfigureScramCredentials_PasswordChange_ImportedSha1Errors verifies the
// same mismatch rejection for a SHA-1-only imported user.
func Test_ConfigureScramCredentials_PasswordChange_ImportedSha1Errors(t *testing.T) {
	seed := om.MongoDBUser{Username: "grace", Database: "admin", Mechanisms: []string{}}
	seedAC := emptyAC()
	_, err := ConfigureScramCredentials(&seed, "oldpass", seedAC)
	require.NoError(t, err)

	acUser := &om.MongoDBUser{
		Username:       "grace",
		Database:       "admin",
		ScramSha1Creds: seed.ScramSha1Creds,
		Mechanisms:     []string{util.SCRAMSHA1},
	}
	ac := acWithUser(acUser)

	user := om.MongoDBUser{Username: "grace", Database: "admin", Mechanisms: []string{}}
	_, err = ConfigureScramCredentials(&user, "newpass", ac)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match existing scramSha1 credentials")
}

// Test_ConfigureScramCredentials_K8sPartialChange_MissingSha1 verifies that when SHA-1 creds
// are absent in the AC but SHA-256 is intact, only SHA-1 is generated and SHA-256 is preserved.
func Test_ConfigureScramCredentials_K8sPartialChange_MissingSha1(t *testing.T) {
	seed := om.MongoDBUser{Username: "irene", Database: "admin", Mechanisms: []string{}}
	seedAC := emptyAC()
	_, err := ConfigureScramCredentials(&seed, "pass", seedAC)
	require.NoError(t, err)

	acUser := &om.MongoDBUser{
		Username:         "irene",
		Database:         "admin",
		ScramSha256Creds: seed.ScramSha256Creds,
		ScramSha1Creds:   nil,
		Mechanisms:       []string{},
	}
	ac := acWithUser(acUser)

	user := om.MongoDBUser{Username: "irene", Database: "admin", Mechanisms: []string{}}
	followUp, err := ConfigureScramCredentials(&user, "pass", ac)
	require.NoError(t, err)

	assert.False(t, followUp)
	assert.Equal(t, seed.ScramSha256Creds, user.ScramSha256Creds, "SHA-256 must be preserved unchanged")
	assert.NotNil(t, user.ScramSha1Creds, "SHA-1 must be generated")
}

// Test_ConfigureScramCredentials_K8sPartialChange_MissingSha256 verifies that when SHA-256 creds
// are absent in the AC but SHA-1 is intact, only SHA-256 is generated and SHA-1 is preserved.
func Test_ConfigureScramCredentials_K8sPartialChange_MissingSha256(t *testing.T) {
	seed := om.MongoDBUser{Username: "judy", Database: "admin", Mechanisms: []string{}}
	seedAC := emptyAC()
	_, err := ConfigureScramCredentials(&seed, "pass", seedAC)
	require.NoError(t, err)

	acUser := &om.MongoDBUser{
		Username:         "judy",
		Database:         "admin",
		ScramSha256Creds: nil,
		ScramSha1Creds:   seed.ScramSha1Creds,
		Mechanisms:       []string{},
	}
	ac := acWithUser(acUser)

	user := om.MongoDBUser{Username: "judy", Database: "admin", Mechanisms: []string{}}
	followUp, err := ConfigureScramCredentials(&user, "pass", ac)
	require.NoError(t, err)

	assert.False(t, followUp)
	assert.NotNil(t, user.ScramSha256Creds, "SHA-256 must be generated")
	assert.Equal(t, seed.ScramSha1Creds, user.ScramSha1Creds, "SHA-1 must be preserved unchanged")
}

// Test_ConfigureScramCredentials_K8sUserPasswordChange verifies that a K8s-managed
// user (no mechanisms) regenerates both creds on password change and returns followUp false.
func Test_ConfigureScramCredentials_K8sUserPasswordChange(t *testing.T) {
	seed := om.MongoDBUser{Username: "henry", Database: "admin", Mechanisms: []string{}}
	seedAC := emptyAC()
	_, err := ConfigureScramCredentials(&seed, "oldpass", seedAC)
	require.NoError(t, err)

	acUser := &om.MongoDBUser{
		Username:         "henry",
		Database:         "admin",
		ScramSha256Creds: seed.ScramSha256Creds,
		ScramSha1Creds:   seed.ScramSha1Creds,
		Mechanisms:       []string{},
	}
	ac := acWithUser(acUser)

	user := om.MongoDBUser{Username: "henry", Database: "admin", Mechanisms: []string{}}
	followUp, err := ConfigureScramCredentials(&user, "newpass", ac)
	require.NoError(t, err)

	assert.False(t, followUp)
	assert.NotNil(t, user.ScramSha256Creds)
	assert.NotNil(t, user.ScramSha1Creds)
	assert.NotEqual(t, seed.ScramSha256Creds, user.ScramSha256Creds)
	assert.NotEqual(t, seed.ScramSha1Creds, user.ScramSha1Creds)
	assert.Empty(t, user.Mechanisms)
}

// Test_ConfigureScramCredentials_ImportedUser_SecondReconcile verifies the state machine
// transition: once OM has consumed initPwd and cleared mechanisms in the AC, the second
// reconcile must treat the user as K8s-managed (mechanisms=[]) and not trigger another
// follow-up requeue.
func Test_ConfigureScramCredentials_ImportedUser_SecondReconcile(t *testing.T) {
	// First reconcile: user arrives from OM with mechanisms set.
	seed := om.MongoDBUser{Username: "jan", Database: "admin", Mechanisms: []string{}}
	seedAC := emptyAC()
	_, err := ConfigureScramCredentials(&seed, "pass", seedAC)
	require.NoError(t, err)

	firstPassAC := acWithUser(&om.MongoDBUser{
		Username:         "jan",
		Database:         "admin",
		ScramSha256Creds: seed.ScramSha256Creds,
		ScramSha1Creds:   seed.ScramSha1Creds,
		Mechanisms:       []string{util.AutomationConfigScramSha256Option, util.SCRAMSHA1},
	})
	firstUser := om.MongoDBUser{Username: "jan", Database: "admin", Mechanisms: []string{}}
	followUp, err := ConfigureScramCredentials(&firstUser, "pass", firstPassAC)
	require.NoError(t, err)
	assert.True(t, followUp, "first reconcile must request follow-up")

	// Second reconcile: OM processed initPwd and cleared mechanisms (mechanisms=[]).
	secondPassAC := acWithUser(&om.MongoDBUser{
		Username:         "jan",
		Database:         "admin",
		ScramSha256Creds: firstUser.ScramSha256Creds,
		ScramSha1Creds:   firstUser.ScramSha1Creds,
		Mechanisms:       []string{},
	})
	secondUser := om.MongoDBUser{Username: "jan", Database: "admin", Mechanisms: []string{}}
	followUp, err = ConfigureScramCredentials(&secondUser, "pass", secondPassAC)
	require.NoError(t, err)
	assert.False(t, followUp, "second reconcile must not request follow-up once mechanisms are cleared")
	assert.Empty(t, secondUser.InitPassword, "InitPassword must not be set on the K8s-managed path")
}
