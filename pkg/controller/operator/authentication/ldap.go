package authentication

import (
	"fmt"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"
	"go.uber.org/zap"
)

type ldapAuthMechanism struct {
	AutomationConfig *om.AutomationConfig
	Conn             om.Connection
	Options          Options
}

func NewLdap(conn om.Connection, ac *om.AutomationConfig, opts Options) Mechanism {
	return &ldapAuthMechanism{
		AutomationConfig: ac,
		Conn:             conn,
		Options:          opts,
	}
}

func (l *ldapAuthMechanism) EnableAgentAuthentication(opts Options, log *zap.SugaredLogger) error {
	return fmt.Errorf("LDAP Agent authentication has not yet been implemented")
}

func (l *ldapAuthMechanism) DisableAgentAuthentication(log *zap.SugaredLogger) error {
	return nil
}

func (l *ldapAuthMechanism) EnableDeploymentAuthentication(opts Options) error {
	ac := l.AutomationConfig
	ac.Ldap = opts.Ldap
	if !stringutil.Contains(ac.Auth.DeploymentAuthMechanisms, string(LDAPPlain)) {
		ac.Auth.DeploymentAuthMechanisms = append(ac.Auth.DeploymentAuthMechanisms, string(LDAPPlain))
	}

	return nil
}

func (l *ldapAuthMechanism) DisableDeploymentAuthentication() error {
	ac := l.AutomationConfig
	ac.Ldap = nil
	ac.Auth.DeploymentAuthMechanisms = stringutil.Remove(ac.Auth.DeploymentAuthMechanisms, string(LDAPPlain))
	return nil
}

func (l *ldapAuthMechanism) IsAgentAuthenticationConfigured() bool {
	return false
}

func (l *ldapAuthMechanism) IsDeploymentAuthenticationConfigured() bool {
	ac := l.AutomationConfig
	return stringutil.Contains(ac.Auth.DeploymentAuthMechanisms, string(LDAPPlain)) && ac.Ldap != nil
}
