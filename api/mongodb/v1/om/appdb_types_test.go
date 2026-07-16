package om

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestOpsManagerUserPasswordSecretName(t *testing.T) {
	assert.Equal(t, "my-om-om-password", OpsManagerUserPasswordSecretName("my-om"))
}

func TestOpsManagerUserPasswordSecretName_MatchesAppDBSpecMethod(t *testing.T) {
	appDB := &AppDBSpec{OpsManagerName: "my-om"}

	assert.Equal(t, OpsManagerUserPasswordSecretName(appDB.Name()), appDB.GetOpsManagerUserPasswordSecretName())
}
