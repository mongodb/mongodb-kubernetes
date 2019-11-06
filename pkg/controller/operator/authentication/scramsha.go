package authentication

import (
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"go.uber.org/zap"
)

func newScramSha256() scramSha {
	return scramSha{
		mechanismName: ScramSha256,
	}
}

func newScramSha1() scramSha {
	return scramSha{
		mechanismName: MongoDBCR,
	}
}

type scramSha struct {
	mechanismName mechanismName
}

func (s scramSha) enableAgentAuthentication(conn om.Connection, opts Options, log *zap.SugaredLogger) error {
	return conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		if err := configureScramAgentUsers(ac); err != nil {
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
		auth.AutoAuthMechanisms = []string{string(s.mechanismName)}
		return nil
	}, log)
}

func (s scramSha) disableAgentAuthentication(conn om.Connection, log *zap.SugaredLogger) error {
	return conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		ac.Auth.AutoAuthMechanisms = util.RemoveString(ac.Auth.AutoAuthMechanisms, string(s.mechanismName))
		return nil
	}, log)
}

func (s scramSha) disableDeploymentAuthentication(ac *om.AutomationConfig) error {
	ac.Auth.DeploymentAuthMechanisms = util.RemoveString(ac.Auth.DeploymentAuthMechanisms, string(s.mechanismName))
	return nil
}

func (s scramSha) enableDeploymentAuthentication(ac *om.AutomationConfig) error {
	auth := ac.Auth
	if !util.ContainsString(auth.DeploymentAuthMechanisms, string(s.mechanismName)) {
		auth.DeploymentAuthMechanisms = append(auth.DeploymentAuthMechanisms, string(s.mechanismName))
	}
	return nil
}

func (s scramSha) isAgentAuthenticationConfigured(ac *om.AutomationConfig) bool {
	if ac.Auth.Disabled {
		return false
	}

	if !util.ContainsString(ac.Auth.AutoAuthMechanisms, string(s.mechanismName)) {
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

func (s scramSha) isDeploymentAuthenticationConfigured(ac *om.AutomationConfig) bool {
	return util.ContainsString(ac.Auth.DeploymentAuthMechanisms, string(s.mechanismName))
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
func configureScramAgentUsers(ac *om.AutomationConfig) error {
	agentPassword, err := ac.EnsurePassword()
	if err != nil {
		return err
	}
	auth := ac.Auth
	auth.AutoUser = util.AutomationAgentUserName
	auth.AutoPwd = agentPassword
	for _, agentUser := range buildScramAgentUsers(agentPassword) {
		auth.EnsureUser(agentUser)
	}
	return nil
}
