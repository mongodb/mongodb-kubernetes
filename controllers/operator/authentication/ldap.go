package authentication

import (
	"context"

	"go.uber.org/zap"

	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/ldap"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/stringutil"
)

type ldapAuthMechanism struct{}

func (l *ldapAuthMechanism) GetName() MechanismName {
	return LDAPPlain
}

func (l *ldapAuthMechanism) EnableAgentAuthentication(ctx context.Context, client kubernetesClient.Client, conn om.Connection, opts Options, log *zap.SugaredLogger) error {
	log.Info("Configuring LDAP authentication")
	err := conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		if err := ac.EnsureKeyFileContents(); err != nil {
			return err
		}
		auth := ac.Auth
		auth.AutoPwd = opts.AutoPwd
		auth.Disabled = false
		auth.AuthoritativeSet = opts.AuthoritativeSet
		auth.KeyFile = util.AutomationAgentKeyFilePathInContainer
		auth.KeyFileWindows = util.AutomationAgentWindowsKeyFilePath

		auth.AutoUser = opts.AutomationSubject
		auth.LdapGroupDN = opts.AutoLdapGroupDN
		auth.AutoAuthMechanisms = []string{string(LDAPPlain)}
		return nil
	}, log)
	if err != nil {
		return err
	}

	log.Info("Configuring backup agent user")
	err = conn.ReadUpdateBackupAgentConfig(func(config *om.BackupAgentConfig) error {
		config.EnableLdapAuthentication(opts.AutomationSubject, opts.AutoPwd)
		config.SetLdapGroupDN(opts.AutoLdapGroupDN)
		return nil
	}, log)
	if err != nil {
		return err
	}

	log.Info("Configuring monitoring agent user")
	return conn.ReadUpdateMonitoringAgentConfig(func(config *om.MonitoringAgentConfig) error {
		config.EnableLdapAuthentication(opts.AutomationSubject, opts.AutoPwd)
		config.SetLdapGroupDN(opts.AutoLdapGroupDN)
		return nil
	}, log)
}

func (l *ldapAuthMechanism) DisableAgentAuthentication(conn om.Connection, log *zap.SugaredLogger) error {
	err := conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		if stringutil.Contains(ac.Auth.AutoAuthMechanisms, string(LDAPPlain)) {
			ac.Auth.AutoAuthMechanisms = stringutil.Remove(ac.Auth.AutoAuthMechanisms, string(LDAPPlain))
		}
		return nil
	}, log)
	if err != nil {
		return err
	}

	err = conn.ReadUpdateMonitoringAgentConfig(func(config *om.MonitoringAgentConfig) error {
		config.DisableLdapAuthentication()
		return nil
	}, log)
	if err != nil {
		return err
	}

	return conn.ReadUpdateBackupAgentConfig(func(config *om.BackupAgentConfig) error {
		config.DisableLdapAuthentication()
		return nil
	}, log)
}

func (l *ldapAuthMechanism) EnableDeploymentAuthentication(conn om.Connection, opts Options, log *zap.SugaredLogger) error {
	return conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		ac.Ldap = opts.Ldap
		if !stringutil.Contains(ac.Auth.DeploymentAuthMechanisms, string(LDAPPlain)) {
			ac.Auth.DeploymentAuthMechanisms = append(ac.Auth.DeploymentAuthMechanisms, string(LDAPPlain))
		}

		return nil
	}, log)
}

func (l *ldapAuthMechanism) DisableDeploymentAuthentication(conn om.Connection, log *zap.SugaredLogger) error {
	return conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		ac.Ldap = nil
		ac.Auth.DeploymentAuthMechanisms = stringutil.Remove(ac.Auth.DeploymentAuthMechanisms, string(LDAPPlain))
		return nil
	}, log)
}

func (l *ldapAuthMechanism) IsAgentAuthenticationConfigured(ac *om.AutomationConfig, _ Options) bool {
	if ac.Auth.Disabled {
		return false
	}

	if !stringutil.Contains(ac.Auth.AutoAuthMechanisms, string(LDAPPlain)) {
		return false
	}

	if ac.Auth.AutoUser == "" || ac.Auth.AutoPwd == "" {
		return false
	}

	return true
}

func (l *ldapAuthMechanism) IsDeploymentAuthenticationConfigured(ac *om.AutomationConfig, opts Options) bool {
	return l.IsDeploymentAuthenticationEnabled(ac) && ldapObjectsEqual(ac.Ldap, opts.Ldap)
}

func (l *ldapAuthMechanism) IsDeploymentAuthenticationEnabled(ac *om.AutomationConfig) bool {
	return stringutil.Contains(ac.Auth.DeploymentAuthMechanisms, string(LDAPPlain))
}

func ldapObjectsEqual(lhs *ldap.Ldap, rhs *ldap.Ldap) bool {
	return lhs != nil && rhs != nil && *lhs == *rhs
}
