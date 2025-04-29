package authentication

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"

	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/ldap"
)

func TestLdapDeploymentMechanism(t *testing.T) {
	conn := om.NewMockedOmConnection(om.NewDeployment())
	ac, _ := conn.ReadAutomationConfig()
	opts := Options{
		Ldap: &ldap.Ldap{
			BindMethod:    "BindMethod",
			BindQueryUser: "BindQueryUser",
			Servers:       "Servers",
		},
	}
	l := NewLdap(conn, ac, opts)

	if err := l.EnableDeploymentAuthentication(opts); err != nil {
		t.Fatal(err)
	}
	assert.Contains(t, ac.Auth.DeploymentAuthMechanisms, string(LDAPPlain))
	assert.Equal(t, "BindQueryUser", ac.Ldap.BindQueryUser)
	assert.Equal(t, "Servers", ac.Ldap.Servers)
	assert.Equal(t, "BindMethod", ac.Ldap.BindMethod)

	if err := l.DisableDeploymentAuthentication(); err != nil {
		t.Fatal(err)
	}

	assert.NotContains(t, ac.Auth.DeploymentAuthMechanisms, string(LDAPPlain))
	assert.Nil(t, ac.Ldap)
}

func TestLdapEnableAgentAuthentication(t *testing.T) {
	conn, ac := createConnectionAndAutomationConfig()
	options := Options{
		AgentMechanism: "LDAP",
		UserOptions: UserOptions{
			AutomationSubject: ("mms-automation"),
		},
	}

	l := NewLdap(conn, ac, options)

	if err := l.EnableAgentAuthentication(Options{AuthoritativeSet: true, AutoPwd: "LDAPPassword."}, zap.S()); err != nil {
		t.Fatal(err)
	}

	ac, err := conn.ReadAutomationConfig()
	if err != nil {
		t.Fatal(err)
	}

	assert.Equal(t, ac.Auth.AutoUser, options.AutomationSubject)
	assert.Len(t, ac.Auth.AutoAuthMechanisms, 1)
	assert.Contains(t, ac.Auth.AutoAuthMechanisms, string(LDAPPlain))
	assert.Equal(t, "LDAPPassword.", ac.Auth.AutoPwd)
	assert.False(t, ac.Auth.Disabled)

	assert.True(t, ac.Auth.AuthoritativeSet)
}

func TestLDAP_DisableAgentAuthentication(t *testing.T) {
	conn, ac := createConnectionAndAutomationConfig()
	opts := Options{
		AutoPwd: "LDAPPassword.",
		UserOptions: UserOptions{
			AutomationSubject: validSubject("automation"),
		},
	}
	ldap := NewLdap(conn, ac, opts)
	assertAgentAuthenticationDisabled(t, ldap, opts)
}
