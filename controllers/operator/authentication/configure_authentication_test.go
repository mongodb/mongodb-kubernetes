package authentication

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

func init() {
	logger, _ := zap.NewDevelopment()
	zap.ReplaceGlobals(logger)
}

func TestConfigureScramSha256(t *testing.T) {
	dep := om.NewDeployment()
	conn := om.NewMockedOmConnection(dep)

	opts := Options{
		AuthoritativeSet: true,
		ProcessNames:     []string{"process-1", "process-2", "process-3"},
		Mechanisms:       []string{"SCRAM"},
		AgentMechanism:   "SCRAM",
	}

	if err := Configure(conn, opts, false, zap.S()); err != nil {
		t.Fatal(err)
	}

	ac, err := conn.ReadAutomationConfig()
	if err != nil {
		t.Fatal(err)
	}

	assertAuthenticationEnabled(t, ac.Auth)
	assertAuthenticationMechanism(t, ac.Auth, "SCRAM-SHA-256")
}

func TestConfigureX509(t *testing.T) {
	dep := om.NewDeployment()
	conn := om.NewMockedOmConnection(dep)

	opts := Options{
		AuthoritativeSet:   true,
		ProcessNames:       []string{"process-1", "process-2", "process-3"},
		Mechanisms:         []string{"X509"},
		AgentMechanism:     "X509",
		ClientCertificates: util.RequireClientCertificates,
		UserOptions: UserOptions{
			AutomationSubject: validSubject("automation"),
		},
	}

	if err := Configure(conn, opts, false, zap.S()); err != nil {
		t.Fatal(err)
	}

	ac, err := conn.ReadAutomationConfig()
	if err != nil {
		t.Fatal(err)
	}

	assertAuthenticationEnabled(t, ac.Auth)
	assertAuthenticationMechanism(t, ac.Auth, "MONGODB-X509")
}

func TestConfigureScramSha1(t *testing.T) {
	dep := om.NewDeployment()
	conn := om.NewMockedOmConnection(dep)

	opts := Options{
		AuthoritativeSet: true,
		ProcessNames:     []string{"process-1", "process-2", "process-3"},
		Mechanisms:       []string{"SCRAM-SHA-1"},
		AgentMechanism:   "SCRAM-SHA-1",
	}

	if err := Configure(conn, opts, false, zap.S()); err != nil {
		t.Fatal(err)
	}

	ac, err := conn.ReadAutomationConfig()
	assert.NoError(t, err)

	assertAuthenticationEnabled(t, ac.Auth)
	assertAuthenticationMechanism(t, ac.Auth, "SCRAM-SHA-1")
}

func TestConfigureMultipleAuthenticationMechanisms(t *testing.T) {
	dep := om.NewDeployment()
	conn := om.NewMockedOmConnection(dep)

	opts := Options{
		AuthoritativeSet: true,
		ProcessNames:     []string{"process-1", "process-2", "process-3"},
		Mechanisms:       []string{"X509", "SCRAM"},
		AgentMechanism:   "SCRAM",
		UserOptions: UserOptions{
			AutomationSubject: validSubject("automation"),
		},
	}

	if err := Configure(conn, opts, false, zap.S()); err != nil {
		t.Fatal(err)
	}

	ac, err := conn.ReadAutomationConfig()
	if err != nil {
		t.Fatal(err)
	}

	assertAuthenticationEnabled(t, ac.Auth)

	assert.Contains(t, ac.Auth.AutoAuthMechanisms, "SCRAM-SHA-256")

	assert.Len(t, ac.Auth.DeploymentAuthMechanisms, 2)
	assert.Len(t, ac.Auth.AutoAuthMechanisms, 1)
	assert.Contains(t, ac.Auth.DeploymentAuthMechanisms, "SCRAM-SHA-256")
	assert.Contains(t, ac.Auth.DeploymentAuthMechanisms, "MONGODB-X509")
}

func TestDisableAuthentication(t *testing.T) {
	dep := om.NewDeployment()
	conn := om.NewMockedOmConnection(dep)

	// enable authentication
	_ = conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		ac.Auth.Enable()
		return nil
	}, zap.S())

	if err := Disable(conn, Options{}, true, zap.S()); err != nil {
		t.Fatal(err)
	}

	ac, err := conn.ReadAutomationConfig()
	if err != nil {
		t.Fatal(err)
	}

	assertAuthenticationDisabled(t, ac.Auth)
}

func TestGetCorrectAuthMechanismFromVersion(t *testing.T) {
	conn := om.NewMockedOmConnection(om.NewDeployment())
	ac, err := conn.ReadAutomationConfig()
	require.NoError(t, err)

	mechanismNames := getMechanismNames(ac, []string{"X509"})

	assert.Len(t, mechanismNames, 1)
	assert.Contains(t, mechanismNames, MechanismName("MONGODB-X509"))

	mechanismNames = getMechanismNames(ac, []string{"SCRAM", "X509"})

	assert.Contains(t, mechanismNames, MechanismName("SCRAM-SHA-256"))
	assert.Contains(t, mechanismNames, MechanismName("MONGODB-X509"))

	// enable MONGODB-CR
	ac.Auth.AutoAuthMechanism = "MONGODB-CR"
	ac.Auth.Enable()

	mechanismNames = getMechanismNames(ac, []string{"SCRAM", "X509"})

	assert.Contains(t, mechanismNames, MechanismName("MONGODB-CR"))
	assert.Contains(t, mechanismNames, MechanismName("MONGODB-X509"))
}

func assertAuthenticationEnabled(t *testing.T, auth *om.Auth) {
	assertAuthenticationEnabledWithUsers(t, auth, 0)
}

func assertAuthenticationEnabledWithUsers(t *testing.T, auth *om.Auth, numUsers int) {
	assert.True(t, auth.AuthoritativeSet)
	assert.False(t, auth.Disabled)
	assert.NotEmpty(t, auth.Key)
	assert.NotEmpty(t, auth.KeyFileWindows)
	assert.NotEmpty(t, auth.KeyFile)
	assert.Len(t, auth.Users, numUsers)
	assert.True(t, noneNil(auth.Users))
}

func assertAuthenticationDisabled(t *testing.T, auth *om.Auth) {
	assert.True(t, auth.Disabled)
	assert.Empty(t, auth.DeploymentAuthMechanisms)
	assert.Empty(t, auth.AutoAuthMechanisms)
	assert.Equal(t, auth.AutoUser, util.AutomationAgentName)
	assert.NotEmpty(t, auth.Key)
	assert.NotEmpty(t, auth.AutoPwd)
	assert.True(t, len(auth.Users) == 0 || allNil(auth.Users))
}

func assertAuthenticationMechanism(t *testing.T, auth *om.Auth, mechanism string) {
	assert.Len(t, auth.DeploymentAuthMechanisms, 1)
	assert.Len(t, auth.AutoAuthMechanisms, 1)
	assert.Len(t, auth.Users, 0)
	assert.Contains(t, auth.DeploymentAuthMechanisms, mechanism)
	assert.Contains(t, auth.AutoAuthMechanisms, mechanism)
}

func assertDeploymentMechanismsConfigured(t *testing.T, authMechanism Mechanism, conn om.Connection, opts Options) {
	err := authMechanism.EnableDeploymentAuthentication(conn, opts, zap.S())
	require.NoError(t, err)

	ac, err := conn.ReadAutomationConfig()
	require.NoError(t, err)
	assert.True(t, authMechanism.IsDeploymentAuthenticationConfigured(ac, opts))
}

func assertAgentAuthenticationDisabled(t *testing.T, authMechanism Mechanism, conn om.Connection, opts Options) {
	err := authMechanism.EnableAgentAuthentication(conn, opts, zap.S())
	require.NoError(t, err)

	ac, err := conn.ReadAutomationConfig()
	require.NoError(t, err)
	assert.True(t, authMechanism.IsAgentAuthenticationConfigured(ac, opts))

	err = authMechanism.DisableAgentAuthentication(conn, zap.S())
	require.NoError(t, err)

	ac, err = conn.ReadAutomationConfig()
	require.NoError(t, err)
	assert.False(t, authMechanism.IsAgentAuthenticationConfigured(ac, opts))
}

func noneNil(users []*om.MongoDBUser) bool {
	for i := range users {
		if users[i] == nil {
			return false
		}
	}
	return true
}

func allNil(users []*om.MongoDBUser) bool {
	for i := range users {
		if users[i] != nil {
			return false
		}
	}
	return true
}
