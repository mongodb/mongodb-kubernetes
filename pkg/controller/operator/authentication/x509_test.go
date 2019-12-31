package authentication

import (
	"testing"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

func TestX509EnableAgentAuthentication(t *testing.T) {
	conn, ac := createConnectionAndAutomationConfig()
	x := NewConnectionX509(conn, ac)
	if err := x.EnableAgentAuthentication(Options{AuthoritativeSet: true}, zap.S()); err != nil {
		t.Fatal(err)
	}

	ac, err := conn.ReadAutomationConfig()
	if err != nil {
		t.Fatal(err)
	}

	assert.Equal(t, ac.Auth.AutoUser, util.AutomationAgentSubject)
	assert.Len(t, ac.Auth.AutoAuthMechanisms, 1)
	assert.Contains(t, ac.Auth.AutoAuthMechanisms, string(MongoDBX509))
	assert.Equal(t, ac.Auth.AutoPwd, util.MergoDelete)
	assert.False(t, ac.Auth.Disabled)
	assert.Len(t, ac.Auth.Users, 2)

	assert.True(t, ac.Auth.AuthoritativeSet)
	assert.NotEmpty(t, ac.Auth.Key)
	assert.NotEmpty(t, ac.Auth.KeyFileWindows)
	assert.NotEmpty(t, ac.Auth.KeyFile)

	for _, user := range buildX509AgentUsers() {
		assert.True(t, ac.Auth.HasUser(user.Username, user.Database))
	}

	for _, user := range buildScramAgentUsers("") {
		assert.False(t, ac.Auth.HasUser(user.Username, user.Database))
	}
}

func TestX509_DisableAgentAuthentication(t *testing.T) {
	conn, ac := createConnectionAndAutomationConfig()
	assertAgentAuthenticationDisabled(t, NewConnectionX509(conn, ac))
}

func TestX509_DeploymentConfigured(t *testing.T) {
	conn, ac := createConnectionAndAutomationConfig()
	assertDeploymentMechanismsConfigured(t, NewConnectionX509(conn, ac))
	assert.Equal(t, ac.AgentSSL.CAFilePath, util.CAFilePathInContainer)
}
