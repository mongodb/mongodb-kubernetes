package authentication

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/ldap"
)

func TestLdapDeploymentMechanism(t *testing.T) {
	conn := om.NewMockedOmConnection(om.NewDeployment())

	opts := Options{
		Ldap: &ldap.Ldap{
			BindMethod:    "BindMethod",
			BindQueryUser: "BindQueryUser",
			Servers:       "Servers",
		},
	}

	err := LDAPPlainMechanism.EnableDeploymentAuthentication(conn, opts, zap.S())
	require.NoError(t, err)

	ac, err := conn.ReadAutomationConfig()
	require.NoError(t, err)
	assert.Contains(t, ac.Auth.DeploymentAuthMechanisms, string(LDAPPlain))
	assert.Equal(t, "BindQueryUser", ac.Ldap.BindQueryUser)
	assert.Equal(t, "Servers", ac.Ldap.Servers)
	assert.Equal(t, "BindMethod", ac.Ldap.BindMethod)

	err = LDAPPlainMechanism.DisableDeploymentAuthentication(conn, zap.S())
	require.NoError(t, err)

	ac, err = conn.ReadAutomationConfig()
	require.NoError(t, err)

	assert.NotContains(t, ac.Auth.DeploymentAuthMechanisms, string(LDAPPlain))
	assert.Nil(t, ac.Ldap)
}

func TestLdapEnableAgentAuthentication(t *testing.T) {
	conn := om.NewMockedOmConnection(om.NewDeployment())
	opts := Options{
		AgentMechanism: "LDAP",
		UserOptions: UserOptions{
			AutomationSubject: "mms-automation",
		},
		AuthoritativeSet: true,
		AutoPwd:          "LDAPPassword.",
	}

	err := LDAPPlainMechanism.EnableAgentAuthentication(conn, opts, zap.S())
	require.NoError(t, err)

	ac, err := conn.ReadAutomationConfig()
	require.NoError(t, err)

	assert.Equal(t, ac.Auth.AutoUser, opts.AutomationSubject)
	assert.Len(t, ac.Auth.AutoAuthMechanisms, 1)
	assert.Contains(t, ac.Auth.AutoAuthMechanisms, string(LDAPPlain))
	assert.Equal(t, "LDAPPassword.", ac.Auth.AutoPwd)
	assert.False(t, ac.Auth.Disabled)

	assert.True(t, ac.Auth.AuthoritativeSet)
}

func TestLDAP_DisableAgentAuthentication(t *testing.T) {
	conn := om.NewMockedOmConnection(om.NewDeployment())

	opts := Options{
		AutoPwd: "LDAPPassword.",
		UserOptions: UserOptions{
			AutomationSubject: validSubject("automation"),
		},
	}
	assertAgentAuthenticationDisabled(t, LDAPPlainMechanism, conn, opts)
}
