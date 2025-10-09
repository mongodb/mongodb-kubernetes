package authentication

import (
	"regexp"

	"go.uber.org/zap"

	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/stringutil"
)

type connectionX509 struct{}

func (x *connectionX509) GetName() MechanismName {
	return MongoDBX509
}

func (x *connectionX509) EnableAgentAuthentication(conn om.Connection, opts Options, log *zap.SugaredLogger) error {
	log.Info("Configuring x509 authentication")
	err := conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		if err := ac.EnsureKeyFileContents(); err != nil {
			return err
		}
		auth := ac.Auth
		auth.AutoPwd = util.MergoDelete
		auth.Disabled = false
		auth.AuthoritativeSet = opts.AuthoritativeSet
		auth.KeyFile = util.AutomationAgentKeyFilePathInContainer
		auth.KeyFileWindows = util.AutomationAgentWindowsKeyFilePath
		ac.AgentSSL = &om.AgentSSL{
			AutoPEMKeyFilePath:    opts.AutoPEMKeyFilePath,
			CAFilePath:            opts.CAFilePath,
			ClientCertificateMode: opts.ClientCertificates,
		}

		auth.AutoUser = opts.AutomationSubject
		auth.LdapGroupDN = opts.AutoLdapGroupDN
		auth.AutoAuthMechanisms = []string{string(MongoDBX509)}

		return nil
	}, log)
	if err != nil {
		return err
	}

	log.Info("Configuring backup agent user")
	err = conn.ReadUpdateBackupAgentConfig(func(config *om.BackupAgentConfig) error {
		config.EnableX509Authentication(opts.AutomationSubject, opts.AutoPEMKeyFilePath)
		config.SetLdapGroupDN(opts.AutoLdapGroupDN)
		return nil
	}, log)
	if err != nil {
		return err
	}

	log.Info("Configuring monitoring agent user")
	return conn.ReadUpdateMonitoringAgentConfig(func(config *om.MonitoringAgentConfig) error {
		config.EnableX509Authentication(opts.AutomationSubject, opts.AutoPEMKeyFilePath)
		config.SetLdapGroupDN(opts.AutoLdapGroupDN)
		return nil
	}, log)
}

func (x *connectionX509) DisableAgentAuthentication(conn om.Connection, log *zap.SugaredLogger) error {
	err := conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		ac.AgentSSL = &om.AgentSSL{
			AutoPEMKeyFilePath:    util.MergoDelete,
			ClientCertificateMode: util.OptionalClientCertficates,
		}

		if stringutil.Contains(ac.Auth.AutoAuthMechanisms, string(MongoDBX509)) {
			ac.Auth.AutoAuthMechanisms = stringutil.Remove(ac.Auth.AutoAuthMechanisms, string(MongoDBX509))
		}
		return nil
	}, log)
	if err != nil {
		return err
	}
	err = conn.ReadUpdateMonitoringAgentConfig(func(config *om.MonitoringAgentConfig) error {
		config.DisableX509Authentication()
		return nil
	}, log)
	if err != nil {
		return err
	}

	return conn.ReadUpdateBackupAgentConfig(func(config *om.BackupAgentConfig) error {
		config.DisableX509Authentication()
		return nil
	}, log)
}

func (x *connectionX509) EnableDeploymentAuthentication(conn om.Connection, opts Options, log *zap.SugaredLogger) error {
	return conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		if !stringutil.Contains(ac.Auth.DeploymentAuthMechanisms, util.AutomationConfigX509Option) {
			ac.Auth.DeploymentAuthMechanisms = append(ac.Auth.DeploymentAuthMechanisms, string(MongoDBX509))
		}
		// AutomationConfig validation requires the CAFile path to be specified in the case of multiple auth
		// mechanisms enabled. This is not required if only X509 is being configured
		ac.AgentSSL.CAFilePath = opts.CAFilePath
		return nil
	}, log)
}

func (x *connectionX509) DisableDeploymentAuthentication(conn om.Connection, log *zap.SugaredLogger) error {
	return conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		ac.Auth.DeploymentAuthMechanisms = stringutil.Remove(ac.Auth.DeploymentAuthMechanisms, string(MongoDBX509))
		return nil
	}, log)
}

func (x *connectionX509) IsAgentAuthenticationConfigured(ac *om.AutomationConfig, opts Options) bool {
	if ac.Auth.Disabled {
		return false
	}

	if !stringutil.Contains(ac.Auth.AutoAuthMechanisms, string(MongoDBX509)) {
		return false
	}

	if !isValidX509Subject(ac.Auth.AutoUser) {
		return false
	}

	if ac.Auth.Key == "" || ac.Auth.KeyFile == "" || ac.Auth.KeyFileWindows == "" {
		return false
	}

	if ac.AgentSSL != nil && ac.AgentSSL.AutoPEMKeyFilePath != opts.AutoPEMKeyFilePath {
		return false
	}

	return true
}

func (x *connectionX509) IsDeploymentAuthenticationConfigured(ac *om.AutomationConfig, _ Options) bool {
	return x.IsDeploymentAuthenticationEnabled(ac)
}

func (x *connectionX509) IsDeploymentAuthenticationEnabled(ac *om.AutomationConfig) bool {
	return stringutil.Contains(ac.Auth.DeploymentAuthMechanisms, string(MongoDBX509))
}

// isValidX509Subject checks the subject contains CommonName, Country and Organizational Unit, Location and State.
func isValidX509Subject(subject string) bool {
	expected := []string{"CN", "C", "OU"}
	for _, name := range expected {
		matched, err := regexp.MatchString(name+`=\w+`, subject)
		if err != nil {
			continue
		}
		if !matched {
			return false
		}
	}
	return true
}
