package authtypes

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mongodb/mongodb-kubernetes/pkg/util/constants"
)

func TestGetLoginString(t *testing.T) {
	user := User{Username: "rob", Database: "admin"}

	assert.Equal(t, "rob:pass%20word@", user.GetLoginString("pass word"))
	assert.Equal(t, "rob:pass%2Bword@", user.GetLoginString("pass+word"))
	colonUser := User{Username: "rob:name", Database: "admin"}
	assert.Equal(t, "rob%3Aname:password@", colonUser.GetLoginString("password"))

	external := User{Username: "CN=rob", Database: constants.ExternalDB}
	assert.Equal(t, "", external.GetLoginString("password"))
}

func TestGetConnectionStringDatabase(t *testing.T) {
	assert.Equal(t, "mflix", User{ConnectionStringDatabase: "mflix"}.GetConnectionStringDatabase())

	// unset leaves the URI path empty
	assert.Equal(t, "", User{}.GetConnectionStringDatabase())

	// $external is auth-only and must not appear in the path
	external := User{Database: constants.ExternalDB, ConnectionStringDatabase: constants.ExternalDB}
	assert.Equal(t, "", external.GetConnectionStringDatabase())
}
