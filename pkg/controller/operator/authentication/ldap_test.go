package authentication

import (
	"testing"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/ldap"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/stretchr/testify/assert"
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
