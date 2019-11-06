package authentication

import (
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"go.uber.org/zap"
)

type x509 struct{}

func (x x509) enableAgentAuthentication(conn om.Connection, opts Options, log *zap.SugaredLogger) error {
	log.Info("configuring x509 authentication")
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
			AutoPEMKeyFilePath:    util.AutomationAgentPemFilePath,
			CAFilePath:            util.CAFilePathInContainer,
			ClientCertificateMode: util.RequireClientCertificates,
		}

		// we want to ensure we don't have any SCRAM-1/256 agent users
		// present. We want the final set of agent users to be the 2 agent
		// x509 users
		for _, user := range buildScramAgentUsers("") {
			auth.EnsureUserRemoved(user.Username, user.Database)
		}

		auth.AutoUser = util.AutomationAgentSubject
		for _, user := range buildX509AgentUsers() {
			auth.EnsureUser(user)
		}

		auth.AutoAuthMechanisms = []string{string(MongoDBX509)}

		return nil
	}, log)

	log.Info("configuring backup agent user")
	err = conn.ReadUpdateBackupAgentConfig(func(config *om.BackupAgentConfig) error {
		config.EnableX509Authentication()
		return nil
	}, log)

	if err != nil {
		return err
	}

	log.Info("configuring monitoring agent user")
	return conn.ReadUpdateMonitoringAgentConfig(func(config *om.MonitoringAgentConfig) error {
		config.EnableX509Authentication()
		return nil
	}, log)
}

func (x x509) disableAgentAuthentication(conn om.Connection, log *zap.SugaredLogger) error {
	err := conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {

		ac.AgentSSL = &om.AgentSSL{
			AutoPEMKeyFilePath:    util.MergoDelete,
			ClientCertificateMode: util.OptionalClientCertficates,
		}

		if util.ContainsString(ac.Auth.AutoAuthMechanisms, string(MongoDBX509)) {
			ac.Auth.AutoAuthMechanisms = util.RemoveString(ac.Auth.AutoAuthMechanisms, string(MongoDBX509))
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

func (x x509) enableDeploymentAuthentication(ac *om.AutomationConfig) error {
	if !util.ContainsString(ac.Auth.DeploymentAuthMechanisms, util.AutomationConfigX509Option) {
		ac.Auth.DeploymentAuthMechanisms = append(ac.Auth.DeploymentAuthMechanisms, string(MongoDBX509))
	}
	return nil
}

func (x x509) disableDeploymentAuthentication(ac *om.AutomationConfig) error {
	ac.Auth.DeploymentAuthMechanisms = util.RemoveString(ac.Auth.DeploymentAuthMechanisms, string(MongoDBX509))
	return nil
}

func (x x509) isAgentAuthenticationConfigured(ac *om.AutomationConfig) bool {
	if ac.Auth.Disabled {
		return false
	}

	if !util.ContainsString(ac.Auth.AutoAuthMechanisms, string(MongoDBX509)) {
		return false
	}

	if ac.Auth.AutoUser != util.AutomationAgentSubject || ac.Auth.AutoPwd != util.MergoDelete {
		return false
	}

	if ac.Auth.Key == "" || ac.Auth.KeyFile == "" || ac.Auth.KeyFileWindows == "" {
		return false
	}

	for _, user := range buildX509AgentUsers() {
		if !ac.Auth.HasUser(user.Username, user.Database) {
			return false
		}
	}

	return true
}

func (x x509) isDeploymentAuthenticationConfigured(ac *om.AutomationConfig) bool {
	return util.ContainsString(ac.Auth.DeploymentAuthMechanisms, string(MongoDBX509))
}

// buildX509AgentUsers returns the MongoDBUsers with all the required roles
// for the BackupAgent and the MonitoringAgent
func buildX509AgentUsers() []om.MongoDBUser {
	// required roles for Backup Agent
	// https://docs.opsmanager.mongodb.com/current/reference/required-access-backup-agent/
	return []om.MongoDBUser{
		{
			Username:                   util.BackupAgentSubject,
			Database:                   util.X509Db,
			AuthenticationRestrictions: []string{},
			Mechanisms:                 []string{},
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
			Username:                   util.MonitoringAgentSubject,
			Database:                   util.X509Db,
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
