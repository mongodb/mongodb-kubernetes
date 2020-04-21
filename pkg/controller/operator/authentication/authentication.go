package authentication

import (
	"crypto/sha1"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"go.uber.org/zap"
)

// Options contains all the required values that are required to configure authentication
// for a set of processes
type Options struct {
	// MinimumMajorVersion is required in order to determine if we will be enabling SCRAM-SHA-1 or SCRAM-SHA-256
	MinimumMajorVersion uint64
	// Mechanisms is a list of strings coming from MongoDB.Spec.Security.Authentication.Modes, these strings
	// are mapped to the corresponding mechanisms in the Automation Config
	Mechanisms []string

	// ProcessNames is a list of the names of the processes which authentication will be configured for
	ProcessNames []string

	// AuthoritativeSet maps directly to auth.authoritativeSet
	AuthoritativeSet bool

	// OneAgent indicates whether or not authentication is being enabled in a One Agent environment.
	// default of false to indicate 3 agent environment
	OneAgent bool

	UserOptions
}

// UserOptions is a struct that contains the different user names
// of the agents that should be added to the automation config.
type UserOptions struct {
	AutomationSubject string
	MonitoringSubject string
	BackupSubject     string
}

// Configure will configure all of the specified authentication Mechanisms. We need to ensure we wait for
// the agents to reach ready state after each operation as prematurely updating the automation config can cause the agents to get stuck.
func Configure(conn om.Connection, opts Options, log *zap.SugaredLogger) error {
	log.Infow("ensuring correct deployment mechanisms", "MinimumMajorVersion", opts.MinimumMajorVersion, "ProcessNames", opts.ProcessNames, "Mechanisms", opts.Mechanisms)

	if stringutil.Contains(opts.Mechanisms, util.X509) && !canEnableX509(conn) {
		return errors.New("unable to configure X509 with this version of Ops Manager, 4.0.11 is the minimum required version to enable X509")
	}

	// we need to make sure the desired authentication mechanism for the agent exists. If the desired agent
	// authentication mechanism does not exist in auth.deploymentAuthMechanisms, it is an invalid config
	if err := ensureDeploymentsMechanismsExist(conn, opts, log); err != nil {
		return err
	}

	if err := om.WaitForReadyState(conn, opts.ProcessNames, log); err != nil {
		return err
	}

	// once we have made sure that the deployment authentication mechanism array contains the desired auth mechanism
	// we can then configure the agent authentication.
	if err := enableAgentAuthentication(conn, opts, log); err != nil {
		return err
	}

	if err := om.WaitForReadyState(conn, opts.ProcessNames, log); err != nil {
		return err
	}

	// once we have successfully enabled auth for the agents, we need to remove mechanisms we don't need.
	// this ensures we don't have mechanisms enabled that have not been configured.
	if err := removeUnusedAuthenticationMechanisms(conn, opts, log); err != nil {
		return err
	}

	if err := om.WaitForReadyState(conn, opts.ProcessNames, log); err != nil {
		return err
	}

	// we remove any unrequired deployment auth mechanisms. This will generally be mechanisms
	// that we are disabling.
	if err := removeUnrequiredDeploymentMechanisms(conn, opts, log); err != nil {
		return err
	}

	if err := removeUnusedAgentUsers(opts.UserOptions, conn, log); err != nil {
		return err
	}

	return om.WaitForReadyState(conn, opts.ProcessNames, log)
}

// removeUnusedAgentUsers ensures that the only agent users that exist in the automation config
// are the agent users for the currently enabled auth mechanism.
func removeUnusedAgentUsers(options UserOptions, conn om.Connection, log *zap.SugaredLogger) error {
	log.Info("Removing any unused agent users")
	return conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {

		autoAuthMechanism := mechanismName(ac.Auth.AutoAuthMechanism)
		if ac.Auth.Disabled { // it's possible for autoAuthMechanism to be populated even when disabled
			autoAuthMechanism = DisableAuth
		}

		for _, user := range usersToRemove(options, autoAuthMechanism) {
			if ac.Auth.EnsureUserRemoved(user.Username, user.Database) {
				log.Infow("removed user", "username", user.Username, "database", user.Database)
			}
		}
		return nil
	}, log)
}

// usersToRemove returns a list of the agent users that aren't required for the given mechanism.
// we want to agent users that we don't need.
func usersToRemove(subjects UserOptions, mn mechanismName) []om.MongoDBUser {
	switch mn {
	case ScramSha256, MongoDBCR:
		return buildX509AgentUsers(subjects)
	case MongoDBX509:
		// the password doesn't matter in this case as we're using the user to remove
		// based on the username/database
		return buildScramAgentUsers("")
	case DisableAuth: // authentication has been disabled, remove all users
		return allAgentUsers(subjects, "")
	}
	return []om.MongoDBUser{}
}

func allAgentUsers(options UserOptions, scramPassword string) []om.MongoDBUser {
	allAgentUsers := []om.MongoDBUser{}
	allAgentUsers = append(allAgentUsers, buildX509AgentUsers(options)...)
	allAgentUsers = append(allAgentUsers, buildScramAgentUsers(scramPassword)...)
	return allAgentUsers
}

// EnsureAgentUsers makes sure that the correct agent users are present in the
// provided AutomationConfig
func EnsureAgentUsers(userOpts UserOptions, ac *om.AutomationConfig, mn mechanismName) error {

	if !containsMechanismName(supportedMechanisms(), mn) {
		return fmt.Errorf("unknown mechanism name specified %s", mn)
	}

	if err := ac.EnsureKeyFileContents(); err != nil {
		return err
	}

	if _, err := ac.EnsurePassword(); err != nil {
		return err
	}

	if mn == MongoDBX509 {
		ac.Auth.AutoUser = userOpts.AutomationSubject
	} else {
		ac.Auth.AutoUser = util.AutomationAgentName
	}

	ac.Auth.KeyFile = util.AutomationAgentKeyFilePathInContainer
	ac.Auth.KeyFileWindows = util.AutomationAgentWindowsKeyFilePath

	switch mn {
	case MongoDBCR, ScramSha256:
		for _, user := range buildScramAgentUsers(ac.Auth.AutoPwd) {
			ac.Auth.EnsureUser(user)
		}
	case MongoDBX509:
		for _, user := range buildX509AgentUsers(userOpts) {
			ac.Auth.EnsureUser(user)
		}
	}
	return nil
}

// ConfigureScramCredentials creates both SCRAM-SHA-1 and SCRAM-SHA-256 credentials. This ensures
// that changes to the authentication settings on the MongoDB resources won't leave MongoDBUsers without
// the correct credentials.
func ConfigureScramCredentials(user *om.MongoDBUser, password string) error {

	scram256Salt, err := GenerateSalt(sha256.New)
	if err != nil {
		return err
	}

	scram1Salt, err := GenerateSalt(sha1.New)
	if err != nil {
		return err
	}

	scram256Creds, err := ComputeScramShaCreds(user.Username, password, scram256Salt, ScramSha256)
	if err != nil {
		return err
	}
	scram1Creds, err := ComputeScramShaCreds(user.Username, password, scram1Salt, MongoDBCR)
	if err != nil {
		return err
	}
	user.ScramSha256Creds = scram256Creds
	user.ScramSha1Creds = scram1Creds
	return nil
}

// Disable disables all authentication mechanisms, and waits for the agents to reach goal state. It is still required to provide
// automation agent user name, password and keyfile contents to ensure a valid Automation Config.
func Disable(conn om.Connection, opts Options, log *zap.SugaredLogger) error {
	ac, err := conn.ReadAutomationConfig()
	if err != nil {
		return err
	}

	if ac.Auth.IsEnabled() {
		log.Info("Disabling authentication")
		err := conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
			if err := ac.EnsureKeyFileContents(); err != nil {
				return err
			}
			if _, err := ac.EnsurePassword(); err != nil {
				return err
			}

			ac.Auth.AutoAuthMechanisms = []string{}
			ac.Auth.DeploymentAuthMechanisms = []string{}
			ac.Auth.Disabled = true
			ac.Auth.AutoUser = util.AutomationAgentName
			ac.Auth.KeyFile = util.AutomationAgentKeyFilePathInContainer
			ac.Auth.KeyFileWindows = util.AutomationAgentWindowsKeyFilePath
			ac.Auth.AuthoritativeSet = opts.AuthoritativeSet
			ac.AgentSSL.ClientCertificateMode = util.OptionalClientCertficates
			ac.AgentSSL.AutoPEMKeyFilePath = util.MergoDelete
			return nil
		}, log)

		if err != nil {
			return err
		}
	}

	// It is only required to update monitoring and backup agent configs in a 3 agent environment.
	// we should eventually be able to remove this.
	err = conn.ReadUpdateMonitoringAgentConfig(func(config *om.MonitoringAgentConfig) error {
		config.DisableX509Authentication()
		return nil
	}, log)

	if err != nil {
		return err
	}

	err = conn.ReadUpdateBackupAgentConfig(func(config *om.BackupAgentConfig) error {
		config.DisableX509Authentication()
		return nil
	}, log)

	if err != nil {
		return err
	}

	if err := om.WaitForReadyState(conn, opts.ProcessNames, log); err != nil {
		return err
	}

	// we want to remove all agent users if we're disabling authentication completely
	if err := removeUnusedAgentUsers(opts.UserOptions, conn, log); err != nil {
		return err
	}

	return om.WaitForReadyState(conn, opts.ProcessNames, log)
}
func getMechanismName(mongodbResourceMode string, ac *om.AutomationConfig, minimumMajorVersion uint64) mechanismName {
	switch mongodbResourceMode {
	case util.X509:
		return MongoDBX509
	case util.SCRAM:
		// if we have already configured authentication and it has been set to MONGODB-CR/SCRAM-SHA-1
		// we can not transition. This needs to be done in the UI

		// if no authentication has been configured, the default value for "AutoAuthMechanism" is "MONGODB-CR"
		// even if authentication is disabled, so we need to ensure that auth has been enabled.
		if ac.Auth.AutoAuthMechanism == string(MongoDBCR) && ac.Auth.IsEnabled() {
			return MongoDBCR
		}

		if minimumMajorVersion < 4 {
			return MongoDBCR
		} else {
			return ScramSha256
		}
	}
	// this should never be reached as validation of this string happens at the CR level
	panic(fmt.Sprintf("unknown mechanism name %s", mongodbResourceMode))
}

// mechanism is an interface that needs to be implemented for any Ops Manager authentication mechanism
type Mechanism interface {
	EnableAgentAuthentication(opts Options, log *zap.SugaredLogger) error
	DisableAgentAuthentication(log *zap.SugaredLogger) error
	EnableDeploymentAuthentication() error
	DisableDeploymentAuthentication() error
	IsAgentAuthenticationConfigured() bool
	IsDeploymentAuthenticationConfigured() bool
}

var _ Mechanism = ConnectionScramSha{}
var _ Mechanism = AutomationConfigScramSha{}
var _ Mechanism = ConnectionX509{}

// removeUnusedAuthenticationMechanisms removes authentication mechanism that were previously enabled, or were required
// as part of the transition process.
func removeUnusedAuthenticationMechanisms(conn om.Connection, opts Options, log *zap.SugaredLogger) error {
	ac, err := conn.ReadAutomationConfig()
	if err != nil {
		return err
	}

	automationConfigAuthMechanismNames := getMechanismNames(ac, opts.MinimumMajorVersion, opts.Mechanisms)

	agentAuthMechanismName := getAgentAuthenticationMechanism(automationConfigAuthMechanismNames)
	unrequiredMechanisms := mechanismsToDisable(automationConfigAuthMechanismNames)

	log.Infow("configuring agent authentication mechanisms", "enabled", agentAuthMechanismName, "disabling", unrequiredMechanisms)
	for _, mn := range unrequiredMechanisms {
		m := fromName(mn, ac, conn, opts)
		if m.IsAgentAuthenticationConfigured() {
			log.Infof("disabling authentication mechanism %s", mn)
			if err := m.DisableAgentAuthentication(log); err != nil {
				return err
			}
		} else {
			log.Infof("mechanism %s is already disabled", mn)
		}
	}
	return nil
}

// enableAgentAuthentication determines which agent authentication mechanism should be configured
// and enables it in Ops Manager
func enableAgentAuthentication(conn om.Connection, opts Options, log *zap.SugaredLogger) error {

	ac, err := conn.ReadAutomationConfig()
	if err != nil {
		return err
	}

	// "opts.Mechanisms" is the list of mechanism names passed through from the MongoDB resource.
	// We need to convert this to the list of strings the automation config expects.
	automationConfigAuthMechanismNames := getMechanismNames(ac, opts.MinimumMajorVersion, opts.Mechanisms)

	// depending on the selected authentication mechanisms, we determine which mechanism
	// will be used by the agent(s)
	agentAuthMechanism := getAgentAuthenticationMechanism(automationConfigAuthMechanismNames)

	// we then configure the agent authentication for that type
	if err := ensureAgentAuthenticationIsConfigured(conn, opts, agentAuthMechanism, log); err != nil {
		return err
	}

	return nil
}

// ensureDeploymentsMechanismsExist makes sure that the corresponding deployment mechanisms which are required
// in order to enable the desired agent auth mechanisms are configured.
func ensureDeploymentsMechanismsExist(conn om.Connection, opts Options, log *zap.SugaredLogger) error {
	ac, err := conn.ReadAutomationConfig()
	if err != nil {
		return err
	}

	// "opts.Mechanisms" is the list of mechanism names passed through from the MongoDB resource.
	// We need to convert this to the list of strings the automation config expects.
	automationConfigMechanismNames := getMechanismNames(ac, opts.MinimumMajorVersion, opts.Mechanisms)

	log.Debugf("automation config authentication mechanisms %+v", automationConfigMechanismNames)
	if err := ensureDeploymentMechanisms(conn, automationConfigMechanismNames, opts, log); err != nil {
		return err
	}

	return nil
}

// removeUnrequiredDeploymentMechanisms updates the given AutomationConfig struct to enable all the given
// authentication mechanisms.
func removeUnrequiredDeploymentMechanisms(conn om.Connection, opts Options, log *zap.SugaredLogger) error {
	ac, err := conn.ReadAutomationConfig()
	if err != nil {
		return err
	}

	// "opts.Mechanisms" is the list of mechanism names passed through from the MongoDB resource.
	// We need to convert this to the list of strings the automation config expects.
	automationConfigAuthMechanismNames := getMechanismNames(ac, opts.MinimumMajorVersion, opts.Mechanisms)

	toDisable := mechanismsToDisable(automationConfigAuthMechanismNames)
	log.Infow("Removing unrequired deployment authentication mechanisms", "Mechanisms", toDisable)
	if err := ensureDeploymentMechanismsAreDisabled(conn, toDisable, opts, log); err != nil {
		return err
	}

	return nil
}

func getMechanismNames(ac *om.AutomationConfig, minimumMajorVersion uint64, mechanisms []string) []mechanismName {
	automationConfigMechanismNames := make([]mechanismName, 0)
	for _, m := range mechanisms {
		automationConfigMechanismNames = append(automationConfigMechanismNames, getMechanismName(m, ac, minimumMajorVersion))
	}
	return automationConfigMechanismNames
}

// mechanismName corresponds to the string used in the automation config representing
// a particular type of authentication
type mechanismName string

const (
	ScramSha256 mechanismName = "SCRAM-SHA-256"
	MongoDBX509 mechanismName = "MONGODB-X509"

	// MONGODB-CR is an umbrella term for SCRAM-SHA-1 and MONGODB-CR for legacy reasons, once MONGODB-CR
	// is enabled, users can auth with SCRAM-SHA-1 credentials
	MongoDBCR mechanismName = "MONGODB-CR"

	// Sentinel value indicating auth is being disabled, this is exclusive to the Operator
	DisableAuth mechanismName = "DISABLE-AUTH"
)

// supportedMechanisms returns a list of all the authentication mechanisms
// that can be configured by the Operator
func supportedMechanisms() []mechanismName {
	return []mechanismName{ScramSha256, MongoDBCR, MongoDBX509}
}

// fromName returns an implementation of mechanism from the string value
// used in the AutomationConfig. All supported fields are in supportedMechanisms
func fromName(name mechanismName, ac *om.AutomationConfig, conn om.Connection, opts Options) Mechanism {
	switch name {
	case MongoDBCR:
		return NewConnectionScramSha1(conn, ac)
	case ScramSha256:
		return NewConnectionScramSha256(conn, ac)
	case MongoDBX509:
		return NewConnectionX509(conn, ac, opts)
	}
	panic(fmt.Errorf("unknown authentication mechanism %s. Supported mechanisms are %+v", name, supportedMechanisms()))
}

// mechanismsToDisable returns a list of mechanisms which need to be disabled
// based on the currently supported authentication mechanisms and the desiredMechanisms
func mechanismsToDisable(desiredMechanisms []mechanismName) []mechanismName {
	toDisable := make([]mechanismName, 0)
	for _, m := range supportedMechanisms() {
		if !containsMechanismName(desiredMechanisms, m) {
			toDisable = append(toDisable, m)
		}
	}
	return toDisable
}

// getAgentAuthenticationMechanism returns the authentication mechanism that the agents will
// use given the set of desired mechanisms
func getAgentAuthenticationMechanism(mechanisms []mechanismName) mechanismName {
	// x509 is only used for agent auth if it is the only authentication mechanism specified
	if len(mechanisms) == 1 && containsMechanismName(mechanisms, MongoDBX509) {
		return MongoDBX509
	} else if containsMechanismName(mechanisms, MongoDBCR) {
		return MongoDBCR
	} else {
		return ScramSha256
	}
}

// ensureAgentAuthenticationIsConfigured will configure the agent authentication settings based on the desiredAgentAuthMechanism
func ensureAgentAuthenticationIsConfigured(conn om.Connection, opts Options, desiredAgentAuthMechanismName mechanismName, log *zap.SugaredLogger) error {
	ac, err := conn.ReadAutomationConfig()
	if err != nil {
		return err
	}

	m := fromName(desiredAgentAuthMechanismName, ac, conn, opts)
	if m.IsAgentAuthenticationConfigured() {
		log.Infof("agent authentication mechanism %s is already configured", desiredAgentAuthMechanismName)
		return nil
	}

	log.Infof("enabling %s agent authentication", desiredAgentAuthMechanismName)
	return m.EnableAgentAuthentication(opts, log)
}

// ensureDeploymentMechanisms configures the given AutomationConfig to allow deployments to
// authenticate using the specified mechanisms
func ensureDeploymentMechanisms(conn om.Connection, desiredDeploymentAuthMechanisms []mechanismName, opts Options, log *zap.SugaredLogger) error {
	ac, err := conn.ReadAutomationConfig()
	if err != nil {
		return err
	}

	allRequiredDeploymentMechanismsAreConfigured := true
	for _, mn := range desiredDeploymentAuthMechanisms {
		if !fromName(mn, ac, conn, opts).IsDeploymentAuthenticationConfigured() {
			allRequiredDeploymentMechanismsAreConfigured = false
		} else {
			log.Debugf("deployment mechanism %s is already configured", mn)
		}
	}

	if allRequiredDeploymentMechanismsAreConfigured {
		log.Info("all required deployment authentication mechanisms are configured")
		return nil
	}

	return conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		for _, mechanismName := range desiredDeploymentAuthMechanisms {
			log.Debugf("enabling deployment mechanism %s", mechanismName)
			if err := fromName(mechanismName, ac, conn, opts).EnableDeploymentAuthentication(); err != nil {
				return err
			}
		}
		return nil
	}, log)
}

// ensureDeploymentMechanismsAreDisabled configures the given AutomationConfig to allow deployments to
// authenticate using the specified mechanisms
func ensureDeploymentMechanismsAreDisabled(conn om.Connection, mechanismsToDisable []mechanismName, opts Options, log *zap.SugaredLogger) error {
	ac, err := conn.ReadAutomationConfig()
	if err != nil {
		return err
	}

	allDeploymentMechanismsAreDisabled := true
	for _, mn := range mechanismsToDisable {
		if fromName(mn, ac, conn, opts).IsDeploymentAuthenticationConfigured() {
			allDeploymentMechanismsAreDisabled = false
		}
	}

	if allDeploymentMechanismsAreDisabled {
		log.Infof("Mechanisms %+v are all already disabled", mechanismsToDisable)
		return nil
	}
	return conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		for _, mechanismName := range mechanismsToDisable {
			log.Debugf("disabling deployment mechanism %s", mechanismName)
			if err := fromName(mechanismName, ac, conn, opts).DisableDeploymentAuthentication(); err != nil {
				return err
			}
		}
		return nil
	}, log)
}

// containsMechanismName returns true if there is at least one mechanismName in `slice`
// that is equal to `mn`.
func containsMechanismName(slice []mechanismName, mn mechanismName) bool {
	for _, item := range slice {
		if item == mn {
			return true
		}
	}
	return false
}
