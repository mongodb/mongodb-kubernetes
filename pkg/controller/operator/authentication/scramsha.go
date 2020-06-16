package authentication

import (
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"
	"go.uber.org/zap"
)

func NewConnectionScramSha256(conn om.Connection, ac *om.AutomationConfig) ConnectionScramSha {
	return ConnectionScramSha{
		Conn: conn,
		AutomationConfigScramSha: AutomationConfigScramSha{
			automationConfig: ac,
			mechanismName:    ScramSha256,
		},
	}
}

func NewConnectionScramSha1(conn om.Connection, ac *om.AutomationConfig) ConnectionScramSha {
	return ConnectionScramSha{
		Conn: conn,
		AutomationConfigScramSha: AutomationConfigScramSha{
			automationConfig: ac,
			mechanismName:    MongoDBCR,
		},
	}
}

func NewAutomationConfigScramSha1(ac *om.AutomationConfig) AutomationConfigScramSha {
	return AutomationConfigScramSha{
		automationConfig: ac,
		mechanismName:    MongoDBCR,
	}
}

// AutomationConfigScramSha applies all the changes required to configure SCRAM-SHA authentication
// directly to an AutomationConfig struct. This implementation does not communicate with Ops Manager in any way.
type AutomationConfigScramSha struct {
	mechanismName    MechanismName
	automationConfig *om.AutomationConfig
}

func (s AutomationConfigScramSha) EnableAgentAuthentication(opts Options, log *zap.SugaredLogger) error {
	if err := configureScramAgentUsers(s.automationConfig, opts); err != nil {
		return err
	}
	if err := s.automationConfig.EnsureKeyFileContents(); err != nil {
		return err
	}

	auth := s.automationConfig.Auth
	auth.Disabled = false
	auth.AuthoritativeSet = opts.AuthoritativeSet
	auth.KeyFile = util.AutomationAgentKeyFilePathInContainer
	auth.KeyFileWindows = util.AutomationAgentWindowsKeyFilePath

	// We can only have a single agent authentication mechanism specified at a given time
	auth.AutoAuthMechanisms = []string{string(s.mechanismName)}
	return nil
}

func (s AutomationConfigScramSha) DisableAgentAuthentication(log *zap.SugaredLogger) error {
	s.automationConfig.Auth.AutoAuthMechanisms = stringutil.Remove(s.automationConfig.Auth.AutoAuthMechanisms, string(s.mechanismName))
	return nil
}

func (s AutomationConfigScramSha) DisableDeploymentAuthentication() error {
	s.automationConfig.Auth.DeploymentAuthMechanisms = stringutil.Remove(s.automationConfig.Auth.DeploymentAuthMechanisms, string(s.mechanismName))
	return nil
}

func (s AutomationConfigScramSha) EnableDeploymentAuthentication() error {
	auth := s.automationConfig.Auth
	if !stringutil.Contains(auth.DeploymentAuthMechanisms, string(s.mechanismName)) {
		auth.DeploymentAuthMechanisms = append(auth.DeploymentAuthMechanisms, string(s.mechanismName))
	}
	return nil
}

func (s AutomationConfigScramSha) IsAgentAuthenticationConfigured() bool {
	ac := s.automationConfig
	if ac.Auth.Disabled {
		return false
	}

	if !stringutil.Contains(ac.Auth.AutoAuthMechanisms, string(s.mechanismName)) {
		return false
	}

	if ac.Auth.AutoUser != util.AutomationAgentName || (ac.Auth.AutoPwd == "" || ac.Auth.AutoPwd == util.MergoDelete) {
		return false
	}

	if ac.Auth.Key == "" || ac.Auth.KeyFile == "" || ac.Auth.KeyFileWindows == "" {
		return false
	}

	for _, user := range buildScramAgentUsers(ac.Auth.AutoPwd) {
		if !ac.Auth.HasUser(user.Username, user.Database) {
			return false
		}
	}

	return true
}

func (s AutomationConfigScramSha) IsDeploymentAuthenticationConfigured() bool {
	return stringutil.Contains(s.automationConfig.Auth.DeploymentAuthMechanisms, string(s.mechanismName))
}

// ConnectionScramSha is a wrapper around AutomationConfigScramSha which pulls the AutomationConfig
// from Ops Manager and sends back the AutomationConfig which has been configured for to enabled SCRAM-SHA
type ConnectionScramSha struct {
	AutomationConfigScramSha
	Conn om.Connection
}

func (s ConnectionScramSha) EnableAgentAuthentication(opts Options, log *zap.SugaredLogger) error {
	return s.Conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		s.automationConfig = ac
		return s.AutomationConfigScramSha.EnableAgentAuthentication(opts, log)
	}, log)
}

func (s ConnectionScramSha) DisableAgentAuthentication(log *zap.SugaredLogger) error {
	return s.Conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		s.automationConfig = ac
		return s.AutomationConfigScramSha.DisableAgentAuthentication(log)
	}, log)
}

// buildScramAgentUsers returns the MongoDBUsers with all the required roles
// for the BackupAgent and the MonitoringAgent
func buildScramAgentUsers(password string) []om.MongoDBUser {
	// required roles for Backup Agent
	// https://docs.opsmanager.mongodb.com/current/reference/required-access-backup-agent/
	return []om.MongoDBUser{
		{
			Username:                   util.BackupAgentName,
			Database:                   util.DefaultUserDatabase,
			AuthenticationRestrictions: []string{},
			Mechanisms:                 []string{},
			InitPassword:               password,
			Roles: []*om.Role{
				{
					Database: "admin",
					Role:     "clusterAdmin",
				},
				{
					Database: "admin",
					Role:     "readAnyDatabase",
				},
				{
					Database: "admin",
					Role:     "userAdminAnyDatabase",
				},
				{
					Database: "local",
					Role:     "readWrite",
				},
				{
					Database: "admin",
					Role:     "readWrite",
				},
			},
		},
		// roles for Monitoring Agent
		// https://docs.opsmanager.mongodb.com/current/reference/required-access-monitoring-agent/
		{
			Username:                   util.MonitoringAgentName,
			Database:                   util.DefaultUserDatabase,
			InitPassword:               password,
			AuthenticationRestrictions: []string{},
			Mechanisms:                 []string{},
			Roles: []*om.Role{
				{
					Database: "admin",
					Role:     "clusterMonitor",
				},
			},
		},
	}
}

// configureScramAgentUsers makes sure that the given automation config always has the correct SCRAM-SHA users
func configureScramAgentUsers(ac *om.AutomationConfig, authOpts Options) error {
	agentPassword, err := ac.EnsurePassword()
	if err != nil {
		return err
	}
	auth := ac.Auth
	auth.AutoUser = util.AutomationAgentUserName
	auth.AutoPwd = agentPassword

	threeAgentEnv := !authOpts.OneAgent
	if threeAgentEnv {
		for _, agentUser := range buildScramAgentUsers(agentPassword) {
			auth.EnsureUser(agentUser)
		}
	}

	return nil
}
