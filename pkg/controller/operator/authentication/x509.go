package authentication

import (
	"regexp"
	"strings"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"go.uber.org/zap"
)

func NewConnectionX509(conn om.Connection, ac *om.AutomationConfig, opts Options) ConnectionX509 {
	return ConnectionX509{
		AutomationConfig: ac,
		Conn:             conn,
		Options:          opts,
	}
}

type ConnectionX509 struct {
	AutomationConfig *om.AutomationConfig
	Conn             om.Connection
	Options          Options
}

func (x ConnectionX509) EnableAgentAuthentication(opts Options, log *zap.SugaredLogger) error {
	log.Info("Configuring x509 authentication")
	err := x.Conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
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
			ClientCertificateMode: opts.ClientCertificates,
		}

		// we want to ensure we don't have any SCRAM-1/256 agent users
		// present. We want the final set of agent users to be the 2 agent
		// x509 users
		for _, user := range buildScramAgentUsers("") {
			auth.EnsureUserRemoved(user.Username, user.Database)
		}

		auth.AutoUser = x.Options.AutomationSubject
		for _, user := range buildX509AgentUsers(x.Options.UserOptions) {
			auth.EnsureUser(user)
		}

		auth.AutoAuthMechanisms = []string{string(MongoDBX509)}

		return nil
	}, log)

	if err != nil {
		return err
	}

	log.Info("Configuring backup agent user")
	err = x.Conn.ReadUpdateBackupAgentConfig(func(config *om.BackupAgentConfig) error {
		config.EnableX509Authentication(opts.BackupSubject)
		return nil
	}, log)

	if err != nil {
		return err
	}

	log.Info("Configuring monitoring agent user")
	return x.Conn.ReadUpdateMonitoringAgentConfig(func(config *om.MonitoringAgentConfig) error {
		config.EnableX509Authentication(opts.MonitoringSubject)
		return nil
	}, log)
}

func (x ConnectionX509) DisableAgentAuthentication(log *zap.SugaredLogger) error {
	err := x.Conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {

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
	err = x.Conn.ReadUpdateMonitoringAgentConfig(func(config *om.MonitoringAgentConfig) error {
		config.DisableX509Authentication()
		return nil
	}, log)

	if err != nil {
		return err
	}

	return x.Conn.ReadUpdateBackupAgentConfig(func(config *om.BackupAgentConfig) error {
		config.DisableX509Authentication()
		return nil
	}, log)
}

func (x ConnectionX509) EnableDeploymentAuthentication(Options) error {
	ac := x.AutomationConfig
	if !stringutil.Contains(ac.Auth.DeploymentAuthMechanisms, util.AutomationConfigX509Option) {
		ac.Auth.DeploymentAuthMechanisms = append(ac.Auth.DeploymentAuthMechanisms, string(MongoDBX509))
	}
	// AutomationConfig validation requires the CAFile path to be specified in the case of multiple auth
	// mechanisms enabled. This is not required if only X509 is being configured
	ac.AgentSSL.CAFilePath = util.CAFilePathInContainer
	return nil
}

func (x ConnectionX509) DisableDeploymentAuthentication() error {
	ac := x.AutomationConfig
	ac.Auth.DeploymentAuthMechanisms = stringutil.Remove(ac.Auth.DeploymentAuthMechanisms, string(MongoDBX509))
	return nil
}

func (x ConnectionX509) IsAgentAuthenticationConfigured() bool {
	ac := x.AutomationConfig
	if ac.Auth.Disabled {
		return false
	}

	if !stringutil.Contains(ac.Auth.AutoAuthMechanisms, string(MongoDBX509)) {
		return false
	}

	if !isValidX509Subject(ac.Auth.AutoUser) || ac.Auth.AutoPwd != util.MergoDelete {
		return false
	}

	if ac.Auth.Key == "" || ac.Auth.KeyFile == "" || ac.Auth.KeyFileWindows == "" {
		return false
	}

	for _, user := range buildX509AgentUsers(x.Options.UserOptions) {
		if !ac.Auth.HasUser(user.Username, user.Database) {
			return false
		}
	}

	return true
}

func (x ConnectionX509) IsDeploymentAuthenticationConfigured() bool {
	return stringutil.Contains(x.AutomationConfig.Auth.DeploymentAuthMechanisms, string(MongoDBX509))
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

// buildX509AgentUsers returns the MongoDBUsers with all the required roles
// for the BackupAgent and the MonitoringAgent
func buildX509AgentUsers(options UserOptions) []om.MongoDBUser {
	// in the case of one agent, we don't need to add these additional agent users
	if options.AutomationSubject != "" && (options.BackupSubject == options.AutomationSubject && options.MonitoringSubject == options.AutomationSubject) {
		return []om.MongoDBUser{}
	}
	// required roles for Backup Agent
	// https://docs.opsmanager.mongodb.com/current/reference/required-access-backup-agent/
	return []om.MongoDBUser{
		{
			Username:                   options.BackupSubject,
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
			Username:                   options.MonitoringSubject,
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

//canEnableX509 determines if it's possible to enable/disable x509 configuration options in the current
// version of Ops Manager
func canEnableX509(conn om.Connection) bool {
	err := conn.ReadUpdateMonitoringAgentConfig(func(config *om.MonitoringAgentConfig) error {
		return nil
	}, nil)
	if err != nil && strings.Contains(err.Error(), util.MethodNotAllowed) {
		return false
	}
	return true
}
