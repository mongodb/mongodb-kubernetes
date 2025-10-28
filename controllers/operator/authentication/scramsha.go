package authentication

import (
	"context"

	"go.uber.org/zap"

	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/stringutil"
)

// automationConfigScramSha applies all the changes required to configure SCRAM-SHA authentication
// directly to an AutomationConfig struct. This implementation does not communicate with Ops Manager in any way.
type automationConfigScramSha struct {
	MechanismName MechanismName
}

func (s *automationConfigScramSha) GetName() MechanismName {
	return s.MechanismName
}

func (s *automationConfigScramSha) EnableAgentAuthentication(client kubernetesClient.Client, ctx context.Context, conn om.Connection, opts Options, log *zap.SugaredLogger) error {
	return conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		if err := configureScramAgentUsers(client, ctx, ac, opts); err != nil {
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

func (s *automationConfigScramSha) DisableAgentAuthentication(conn om.Connection, log *zap.SugaredLogger) error {
	return conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		ac.Auth.AutoAuthMechanisms = stringutil.Remove(ac.Auth.AutoAuthMechanisms, string(s.MechanismName))
		return nil
	}, log)
}

func (s *automationConfigScramSha) DisableDeploymentAuthentication(conn om.Connection, log *zap.SugaredLogger) error {
	return conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		ac.Auth.DeploymentAuthMechanisms = stringutil.Remove(ac.Auth.DeploymentAuthMechanisms, string(s.MechanismName))
		return nil
	}, log)
}

func (s *automationConfigScramSha) EnableDeploymentAuthentication(conn om.Connection, _ Options, log *zap.SugaredLogger) error {
	return conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		if !stringutil.Contains(ac.Auth.DeploymentAuthMechanisms, string(s.MechanismName)) {
			ac.Auth.DeploymentAuthMechanisms = append(ac.Auth.DeploymentAuthMechanisms, string(s.MechanismName))
		}
		return nil
	}, log)
}

func (s *automationConfigScramSha) IsAgentAuthenticationConfigured(ac *om.AutomationConfig, _ Options) bool {
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

func (s *automationConfigScramSha) IsDeploymentAuthenticationConfigured(ac *om.AutomationConfig, _ Options) bool {
	return s.IsDeploymentAuthenticationEnabled(ac)
}

func (s *automationConfigScramSha) IsDeploymentAuthenticationEnabled(ac *om.AutomationConfig) bool {
	return stringutil.Contains(ac.Auth.DeploymentAuthMechanisms, string(s.MechanismName))
}

// configureScramAgentUsers makes sure that the given automation config always has the correct SCRAM-SHA users
func configureScramAgentUsers(client kubernetesClient.Client, ctx context.Context, ac *om.AutomationConfig, authOpts Options) error {
	agentPassword, err := ac.EnsurePassword(client, ctx)
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
