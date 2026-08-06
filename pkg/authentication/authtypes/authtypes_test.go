package authtypes

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mongodb/mongodb-kubernetes/pkg/util/constants"
)

func TestGetLoginString(t *testing.T) {
	user := User{Username: "rob", AuthSource: "admin"}

	assert.Equal(t, "rob:pass%20word@", user.GetLoginString("pass word"))
	assert.Equal(t, "rob:pass%2Bword@", user.GetLoginString("pass+word"))
	colonUser := User{Username: "rob:name", AuthSource: "admin"}
	assert.Equal(t, "rob%3Aname:password@", colonUser.GetLoginString("password"))

	external := User{Username: "CN=rob", AuthSource: constants.ExternalDB}
	assert.Equal(t, "", external.GetLoginString("password"))
}

func TestGetPathDatabase(t *testing.T) {
	assert.Equal(t, "mflix", User{ConnectionStringDatabase: "mflix"}.GetPathDatabase())

	// unset leaves the URI path empty so the driver applies its own default
	assert.Equal(t, "", User{}.GetPathDatabase())

	// $external authenticates but is not a real database, so it must stay out of the path
	external := User{AuthSource: constants.ExternalDB, ConnectionStringDatabase: constants.ExternalDB}
	assert.Equal(t, "", external.GetPathDatabase())
}
