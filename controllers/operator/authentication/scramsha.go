package authentication

import (
	"go.uber.org/zap"

	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/stringutil"
)

var (
	MongoDBCRMechanism   Mechanism = AutomationConfigScramSha{MechanismName: MongoDBCR}
	ScramSha1Mechanism   Mechanism = AutomationConfigScramSha{MechanismName: ScramSha1}
	ScramSha256Mechanism Mechanism = AutomationConfigScramSha{MechanismName: ScramSha256}
)

// AutomationConfigScramSha applies all the changes required to configure SCRAM-SHA authentication
// directly to an AutomationConfig struct. This implementation does not communicate with Ops Manager in any way.
type AutomationConfigScramSha struct {
	MechanismName MechanismName
}

func (s AutomationConfigScramSha) GetName() MechanismName {
	return s.MechanismName
}

func (s AutomationConfigScramSha) EnableAgentAuthentication(conn om.Connection, opts Options, log *zap.SugaredLogger) error {
	return conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		if err := configureScramAgentUsers(ac, opts); err != nil {
			return err
		}
		if err := ac.EnsureKeyFileContents(); err != nil {
			return err
		}

		auth := ac.Auth
		auth.Disabled = false
		auth.AuthoritativeSet = opts.AuthoritativeSet
		auth.KeyFile = util.AutomationAgentKeyFilePathInContainer
		auth.KeyFileWindows = util.AutomationAgentWindowsKeyFilePath

		// We can only have a single agent authentication mechanism specified at a given time
		auth.AutoAuthMechanisms = []string{string(s.MechanismName)}
		return nil
	}, log)
}

func (s AutomationConfigScramSha) DisableAgentAuthentication(conn om.Connection, log *zap.SugaredLogger) error {
	return conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		ac.Auth.AutoAuthMechanisms = stringutil.Remove(ac.Auth.AutoAuthMechanisms, string(s.MechanismName))
		return nil
	}, log)
}

func (s AutomationConfigScramSha) DisableDeploymentAuthentication(conn om.Connection, log *zap.SugaredLogger) error {
	return conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		ac.Auth.DeploymentAuthMechanisms = stringutil.Remove(ac.Auth.DeploymentAuthMechanisms, string(s.MechanismName))
		return nil
	}, log)
}

func (s AutomationConfigScramSha) EnableDeploymentAuthentication(conn om.Connection, _ Options, log *zap.SugaredLogger) error {
	return conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		if !stringutil.Contains(ac.Auth.DeploymentAuthMechanisms, string(s.MechanismName)) {
			ac.Auth.DeploymentAuthMechanisms = append(ac.Auth.DeploymentAuthMechanisms, string(s.MechanismName))
		}
		return nil
	}, log)
}

func (s AutomationConfigScramSha) IsAgentAuthenticationConfigured(ac *om.AutomationConfig, _ Options) bool {
	if ac.Auth.Disabled {
		return false
	}

	if !stringutil.Contains(ac.Auth.AutoAuthMechanisms, string(s.MechanismName)) {
		return false
	}

	if ac.Auth.AutoUser != util.AutomationAgentName || (ac.Auth.AutoPwd == "" || ac.Auth.AutoPwd == util.MergoDelete) {
		return false
	}

	if ac.Auth.Key == "" || ac.Auth.KeyFile == "" || ac.Auth.KeyFileWindows == "" {
		return false
	}

	return true
}

func (s AutomationConfigScramSha) IsDeploymentAuthenticationConfigured(ac *om.AutomationConfig, _ Options) bool {
	return stringutil.Contains(ac.Auth.DeploymentAuthMechanisms, string(s.MechanismName))
}

// configureScramAgentUsers makes sure that the given automation config always has the correct SCRAM-SHA users
func configureScramAgentUsers(ac *om.AutomationConfig, authOpts Options) error {
	agentPassword, err := ac.EnsurePassword()
	if err != nil {
		return err
	}
	auth := ac.Auth
	if auth.AutoUser == "" {
		auth.AutoUser = authOpts.AutoUser
	}
	auth.AutoPwd = agentPassword

	return nil
}
