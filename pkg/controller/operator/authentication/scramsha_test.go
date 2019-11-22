package authentication

import (
	"testing"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

func TestScramSha256_EnableAgentAuthentication(t *testing.T) {
	conn := om.NewMockedOmConnection(om.NewDeployment())
	ac, _ := conn.ReadAutomationConfig()

	s := NewConnectionScramSha256(conn, ac)

	if err := s.EnableAgentAuthentication(Options{AuthoritativeSet: true}, zap.S()); err != nil {
		t.Fatal(err)
	}

	ac, err := conn.ReadAutomationConfig()
	if err != nil {
		t.Fatal(err)
	}

	assertAuthenticationEnabled(t, ac.Auth)
	assert.Equal(t, ac.Auth.AutoUser, util.AutomationAgentName)
	assert.Len(t, ac.Auth.AutoAuthMechanisms, 1)
	assert.Contains(t, ac.Auth.AutoAuthMechanisms, string(ScramSha256))
	assert.NotEmpty(t, ac.Auth.AutoPwd)

	assert.True(t, s.IsAgentAuthenticationConfigured())

	for _, user := range buildScramAgentUsers(ac.Auth.AutoPwd) {
		assert.True(t, ac.Auth.HasUser(user.Username, user.Database))
	}

	for _, user := range buildX509AgentUsers() {
		assert.False(t, ac.Auth.HasUser(user.Username, user.Database))
	}

}

func TestScramSha1_EnableAgentAuthentication(t *testing.T) {
	conn := om.NewMockedOmConnection(om.NewDeployment())

	ac, _ := conn.ReadAutomationConfig()

	s := NewConnectionScramSha1(conn, ac)

	if err := s.EnableAgentAuthentication(Options{AuthoritativeSet: true}, zap.S()); err != nil {
		t.Fatal(err)
	}

	ac, err := conn.ReadAutomationConfig()
	if err != nil {
		t.Fatal(err)
	}

	assertAuthenticationEnabled(t, ac.Auth)
	assert.Equal(t, ac.Auth.AutoUser, util.AutomationAgentName)
	assert.Len(t, ac.Auth.AutoAuthMechanisms, 1)
	assert.Contains(t, ac.Auth.AutoAuthMechanisms, string(MongoDBCR))
	assert.NotEmpty(t, ac.Auth.AutoPwd)
	assert.Len(t, ac.Auth.Users, 2)

	assert.True(t, s.IsAgentAuthenticationConfigured())

	for _, user := range buildScramAgentUsers(ac.Auth.AutoPwd) {
		assert.True(t, ac.Auth.HasUser(user.Username, user.Database))
	}

	for _, user := range buildX509AgentUsers() {
		assert.False(t, ac.Auth.HasUser(user.Username, user.Database))
	}
}

func TestScramSha256_DeploymentConfigured(t *testing.T) {
	conn, ac := createConnectionAndAutomationConfig()
	assertDeploymentMechanismsConfigured(t, NewConnectionScramSha256(conn, ac))
}

func TestScramSha1_DeploymentConfigured(t *testing.T) {
	conn, ac := createConnectionAndAutomationConfig()
	assertDeploymentMechanismsConfigured(t, NewConnectionScramSha1(conn, ac))
}

func TestScramSha1_DisableAgentAuthentication(t *testing.T) {
	conn, ac := createConnectionAndAutomationConfig()
	assertAgentAuthenticationDisabled(t, NewConnectionScramSha1(conn, ac))
}

func TestScramSha256_DisableAgentAuthentication(t *testing.T) {
	conn, ac := createConnectionAndAutomationConfig()
	assertAgentAuthenticationDisabled(t, NewConnectionScramSha256(conn, ac))
}
