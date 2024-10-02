package authentication

import (
	"crypto/sha1" //nolint //Part of the algorithm
	"crypto/sha256"
	"fmt"

	"go.uber.org/zap"
	"golang.org/x/xerrors"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/ldap"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
)

// AuthResource is an interface that a resources that can have authentication enabled should implement.
type AuthResource interface {
	GetName() string
	GetNamespace() string
	GetSecurity() *mdbv1.Security
	IsLDAPEnabled() bool
	GetLDAP(password, caContents string) *ldap.Ldap
	GetMinimumMajorVersion() uint64
}

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

	// AgentMechanism indicates which Agent Mechanism should be configured. This should be in the Operator format.
	// I.e. X509, SCRAM and not MONGODB-X509 or SCRAM-SHA-256
	AgentMechanism string

	// ClientCertificates configures whether or not Client Certificates are required or optional.
	// If X509 is the only mechanism, they must be Required, otherwise they should be Optional
	// so it is possible to use other auth mechanisms without needing to provide client certs.
	ClientCertificates string

	CAFilePath string

	// Use Agent Client Auth
	AgentsShouldUseClientAuthentication bool

	UserOptions

	// Ldap is the LDAP configuration that will be passed to the Automation Config.
	// Only required if LDAP is configured as an authentication mechanism
	Ldap *ldap.Ldap

	AutoUser string

	AutoPwd string

	AutoLdapGroupDN string
}

func Redact(o Options) Options {
	if o.Ldap != nil && o.Ldap.BindQueryPassword != "" {
		ldapCopy := *o.Ldap
		o.Ldap = &ldapCopy
		o.Ldap.BindQueryPassword = "<redacted>"
	}
	return o
}

// UserOptions is a struct that contains the different user names
// of the agents that should be added to the automation config.
type UserOptions struct {
	AutomationSubject string
}

// Configure will configure all the specified authentication Mechanisms. We need to ensure we wait for
// the agents to reach ready state after each operation as prematurely updating the automation config can cause the agents to get stuck.
func Configure(conn om.Connection, opts Options, isRecovering bool, log *zap.SugaredLogger) error {
	log.Infow("ensuring correct deployment mechanisms", "MinimumMajorVersion", opts.MinimumMajorVersion, "ProcessNames", opts.ProcessNames, "Mechanisms", opts.Mechanisms)

	// In case we're recovering, we can push all changes at once, because the mechanism is triggered after 20min by default.
	// Otherwise, we might unnecessarily enter this waiting loop 7 times, and waste >10 min
	waitForReadyStateIfNeeded := func() error {
		if isRecovering {
			return nil
		}
		return om.WaitForReadyState(conn, opts.ProcessNames, false, log)
	}

	// we need to make sure the desired authentication mechanism for the agent exists. If the desired agent
	// authentication mechanism does not exist in auth.deploymentAuthMechanisms, it is an invalid config
	if err := ensureDeploymentsMechanismsExist(conn, opts, log); err != nil {
		return xerrors.Errorf("error ensuring deployment mechanisms: %w", err)
	}
	if err := waitForReadyStateIfNeeded(); err != nil {
		return err
	}

	// we make sure that the AuthoritativeSet options in the AC is correct
	if err := ensureAuthoritativeSetIsConfigured(conn, opts.AuthoritativeSet, log); err != nil {
		return xerrors.Errorf("error ensuring that authoritative set is configured: %w", err)
	}
	if err := waitForReadyStateIfNeeded(); err != nil {
		return err
	}

	// once we have made sure that the deployment authentication mechanism array contains the desired auth mechanism
	// we can then configure the agent authentication.
	if err := enableAgentAuthentication(conn, opts, log); err != nil {
		return xerrors.Errorf("error enabling agent authentication: %w", err)
	}
	if err := waitForReadyStateIfNeeded(); err != nil {
		return err
	}

	// once we have successfully enabled auth for the agents, we need to remove mechanisms we don't need.
	// this ensures we don't have mechanisms enabled that have not been configured.
	if err := removeUnusedAuthenticationMechanisms(conn, opts, log); err != nil {
		return xerrors.Errorf("error removing unused authentication mechanisms %w", err)
	}
	if err := waitForReadyStateIfNeeded(); err != nil {
		return err
	}

	// we remove any unrequired deployment auth mechanisms. This will generally be mechanisms
	// that we are disabling.
	if err := removeUnrequiredDeploymentMechanisms(conn, opts, log); err != nil {
		return xerrors.Errorf("error removing unrequired deployment mechanisms: %w", err)
	}
	if err := waitForReadyStateIfNeeded(); err != nil {
		return err
	}

	// Adding a client certificate for agents
	if err := addOrRemoveAgentClientCertificate(conn, opts, log); err != nil {
		return xerrors.Errorf("error adding client certificates for the agents: %w", err)
	}
	if err := waitForReadyStateIfNeeded(); err != nil {
		return err
	}

	return nil
}

// ConfigureScramCredentials creates both SCRAM-SHA-1 and SCRAM-SHA-256 credentials. This ensures
// that changes to the authentication settings on the MongoDB resources won't leave MongoDBUsers without
// the correct credentials.
func ConfigureScramCredentials(user *om.MongoDBUser, password string) error {
	scram256Salt, err := GenerateSalt(sha256.New)
	if err != nil {
		return xerrors.Errorf("error generating scramSha256 salt: %w", err)
	}

	scram1Salt, err := GenerateSalt(sha1.New)
	if err != nil {
		return xerrors.Errorf("error generating scramSha1 salt: %w", err)
	}

	scram256Creds, err := ComputeScramShaCreds(user.Username, password, scram256Salt, ScramSha256)
	if err != nil {
		return xerrors.Errorf("error generating scramSha256 creds: %w", err)
	}
	scram1Creds, err := ComputeScramShaCreds(user.Username, password, scram1Salt, MongoDBCR)
	if err != nil {
		return xerrors.Errorf("error generating scramSha1Creds: %w", err)
	}
	user.ScramSha256Creds = scram256Creds
	user.ScramSha1Creds = scram1Creds
	return nil
}

// Disable disables all authentication mechanisms, and waits for the agents to reach goal state. It is still required to provide
// automation agent username, password and keyfile contents to ensure a valid Automation Config.
func Disable(conn om.Connection, opts Options, deleteUsers bool, log *zap.SugaredLogger) error {
	ac, err := conn.ReadAutomationConfig()
	if err != nil {
		return xerrors.Errorf("error reading automation config: %w", err)
	}

	// Disabling auth must be done in two steps, otherwise the agents might not be able to transition.
	// From a Slack conversation with Agent team:
	// "First disable with leaving credentials and mechanisms and users in place. Wait for goal state.  Then remove the rest"
	// "assume the agent is stateless.  So if you remove the authentication information before it has transitioned then it won't be able to transition"
	if ac.Auth.IsEnabled() {
		log.Info("Disabling authentication")

		err := conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
			ac.Auth.Disabled = true
			return nil
		}, log)
		if err != nil {
			return xerrors.Errorf("error read/updating automation config: %w", err)
		}

		if err := om.WaitForReadyState(conn, opts.ProcessNames, false, log); err != nil {
			return xerrors.Errorf("error waiting for ready state: %w", err)
		}
	}

	err = conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		if err := ac.EnsureKeyFileContents(); err != nil {
			return xerrors.Errorf("error ensuring keyfile contents: %w", err)
		}
		if _, err := ac.EnsurePassword(); err != nil {
			return xerrors.Errorf("error ensuring agent password: %w", err)
		}

		// we don't always want to delete the users. This can result in the agents getting stuck
		// certain situations around auth transitions.
		if deleteUsers {
			ac.Auth.Users = []*om.MongoDBUser{}
		}
		ac.Auth.AutoAuthMechanisms = []string{}
		ac.Auth.DeploymentAuthMechanisms = []string{}
		ac.Auth.AutoUser = util.AutomationAgentName
		ac.Auth.KeyFile = util.AutomationAgentKeyFilePathInContainer
		ac.Auth.KeyFileWindows = util.AutomationAgentWindowsKeyFilePath
		ac.Auth.AuthoritativeSet = opts.AuthoritativeSet
		ac.AgentSSL.ClientCertificateMode = util.OptionalClientCertficates
		ac.AgentSSL.AutoPEMKeyFilePath = util.MergoDelete
		return nil
	}, log)
	if err != nil {
		return xerrors.Errorf("error read/updating automation config: %w", err)
	}

	// It is only required to update monitoring and backup agent configs in a 3 agent environment.
	// we should eventually be able to remove this.
	err = conn.ReadUpdateMonitoringAgentConfig(func(config *om.MonitoringAgentConfig) error {
		config.DisableX509Authentication()
		return nil
	}, log)
	if err != nil {
		return xerrors.Errorf("error read/updating monitoring config: %w", err)
	}

	err = conn.ReadUpdateBackupAgentConfig(func(config *om.BackupAgentConfig) error {
		config.DisableX509Authentication()
		return nil
	}, log)
	if err != nil {
		return xerrors.Errorf("error read/updating backup agent config: %w", err)
	}

	if err := om.WaitForReadyState(conn, opts.ProcessNames, false, log); err != nil {
		return xerrors.Errorf("error waiting for ready state: %w", err)
	}

	return nil
}

func getMechanismName(mongodbResourceMode string, ac *om.AutomationConfig, minimumMajorVersion uint64) MechanismName {
	switch mongodbResourceMode {
	case util.X509:
		return MongoDBX509
	case util.LDAP:
		return LDAPPlain
	case util.SCRAMSHA1:
		return ScramSha1
	case util.MONGODBCR:
		return MongoDBCR
	case util.SCRAMSHA256:
		return ScramSha256
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
	EnableDeploymentAuthentication(opts Options) error
	DisableDeploymentAuthentication() error
	// IsAgentAuthenticationConfigured should not rely on util.MergoDelete since the method is always
	// called directly after deserializing the response from OM which should not contain the util.MergoDelete value in any field.
	IsAgentAuthenticationConfigured() bool
	IsDeploymentAuthenticationConfigured() bool
}

var (
	_ Mechanism = ConnectionScramSha{}
	_ Mechanism = AutomationConfigScramSha{}
	_ Mechanism = ConnectionX509{}
	_ Mechanism = &ldapAuthMechanism{}
)

// removeUnusedAuthenticationMechanisms removes authentication mechanism that were previously enabled, or were required
// as part of the transition process.
func removeUnusedAuthenticationMechanisms(conn om.Connection, opts Options, log *zap.SugaredLogger) error {
	ac, err := conn.ReadAutomationConfig()
	if err != nil {
		return xerrors.Errorf("error reading automation config: %w", err)
	}

	automationConfigAuthMechanismNames := getMechanismNames(ac, opts.MinimumMajorVersion, opts.Mechanisms)

	unrequiredMechanisms := mechanismsToDisable(automationConfigAuthMechanismNames)

	log.Infow("configuring agent authentication mechanisms", "enabled", opts.AgentMechanism, "disabling", unrequiredMechanisms)
	for _, mn := range unrequiredMechanisms {
		m := fromName(mn, ac, conn, opts)
		if m.IsAgentAuthenticationConfigured() {
			log.Infof("disabling authentication mechanism %s", mn)
			if err := m.DisableAgentAuthentication(log); err != nil {
				return xerrors.Errorf("error disabling agent authentication: %w", err)
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
		return xerrors.Errorf("error reading automation config: %w", err)
	}

	// we then configure the agent authentication for that type
	agentAuthMechanism := getMechanismName(opts.AgentMechanism, ac, opts.MinimumMajorVersion)
	if err := ensureAgentAuthenticationIsConfigured(conn, opts, ac, agentAuthMechanism, log); err != nil {
		return xerrors.Errorf("error ensuring agent authentication is configured: %w", err)
	}

	return nil
}

// ensureAuthoritativeSetIsConfigured makes sure that the authoritativeSet options is correctly configured
// in Ops Manager
func ensureAuthoritativeSetIsConfigured(conn om.Connection, authoritativeSet bool, log *zap.SugaredLogger) error {
	ac, err := conn.ReadAutomationConfig()
	if err != nil {
		return xerrors.Errorf("error reading automation config: %w", err)
	}

	if ac.Auth.AuthoritativeSet == authoritativeSet {
		log.Debugf("Authoritative set %t is already configured", authoritativeSet)
		return nil
	}

	return conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		ac.Auth.AuthoritativeSet = authoritativeSet
		return nil
	}, log)
}

// ensureDeploymentsMechanismsExist makes sure that the corresponding deployment mechanisms which are required
// in order to enable the desired agent auth mechanisms are configured.
func ensureDeploymentsMechanismsExist(conn om.Connection, opts Options, log *zap.SugaredLogger) error {
	ac, err := conn.ReadAutomationConfig()
	if err != nil {
		return xerrors.Errorf("error reading automation config: %w", err)
	}

	// "opts.Mechanisms" is the list of mechanism names passed through from the MongoDB resource.
	// We need to convert this to the list of strings the automation config expects.
	automationConfigMechanismNames := getMechanismNames(ac, opts.MinimumMajorVersion, opts.Mechanisms)

	log.Debugf("Automation config authentication mechanisms: %+v", automationConfigMechanismNames)
	if err := ensureDeploymentMechanisms(conn, ac, automationConfigMechanismNames, opts, log); err != nil {
		return xerrors.Errorf("error ensuring deployment mechanisms: %w", err)
	}

	return nil
}

// removeUnrequiredDeploymentMechanisms updates the given AutomationConfig struct to enable all the given
// authentication mechanisms.
func removeUnrequiredDeploymentMechanisms(conn om.Connection, opts Options, log *zap.SugaredLogger) error {
	ac, err := conn.ReadAutomationConfig()
	if err != nil {
		return xerrors.Errorf("error reading automation config: %w", err)
	}

	// "opts.Mechanisms" is the list of mechanism names passed through from the MongoDB resource.
	// We need to convert this to the list of strings the automation config expects.
	automationConfigAuthMechanismNames := getMechanismNames(ac, opts.MinimumMajorVersion, opts.Mechanisms)

	toDisable := mechanismsToDisable(automationConfigAuthMechanismNames)
	log.Infow("Removing unrequired deployment authentication mechanisms", "Mechanisms", toDisable)
	if err := ensureDeploymentMechanismsAreDisabled(conn, ac, toDisable, opts, log); err != nil {
		return xerrors.Errorf("error ensuring deployment mechanisms are disabled: %w", err)
	}

	return nil
}

// addOrRemoveAgentClientCertificate changes the automation config so it enables or disables
// client TLS authentication.
// This function will not change the automation config if x509 agent authentication has been
// enabled already (by the x509 auth package).
func addOrRemoveAgentClientCertificate(conn om.Connection, opts Options, log *zap.SugaredLogger) error {
	// If x509 is not enabled but still Client Certificates are, this automation config update
	// will add the required configuration.
	return conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		if getMechanismName(opts.AgentMechanism, ac, opts.MinimumMajorVersion) == MongoDBX509 {
			// If TLS client authentication is managed by x509, we won't disable or enable it
			// in here.
			return nil
		}

		if opts.AgentsShouldUseClientAuthentication {
			ac.AgentSSL = &om.AgentSSL{
				AutoPEMKeyFilePath:    util.AutomationAgentPemFilePath,
				CAFilePath:            opts.CAFilePath,
				ClientCertificateMode: opts.ClientCertificates,
			}
		} else {
			ac.AgentSSL = &om.AgentSSL{
				AutoPEMKeyFilePath:    util.MergoDelete,
				ClientCertificateMode: util.OptionalClientCertficates,
			}
		}
		return nil
	}, log)
}

func getMechanismNames(ac *om.AutomationConfig, minimumMajorVersion uint64, mechanisms []string) []MechanismName {
	automationConfigMechanismNames := make([]MechanismName, 0)
	for _, m := range mechanisms {
		automationConfigMechanismNames = append(automationConfigMechanismNames, getMechanismName(m, ac, minimumMajorVersion))
	}
	return automationConfigMechanismNames
}

// MechanismName corresponds to the string used in the automation config representing
// a particular type of authentication
type MechanismName string

const (
	ScramSha256 MechanismName = "SCRAM-SHA-256"
	ScramSha1   MechanismName = "SCRAM-SHA-1"
	MongoDBX509 MechanismName = "MONGODB-X509"
	LDAPPlain   MechanismName = "PLAIN"

	// MongoDBCR is an umbrella term for SCRAM-SHA-1 and MONGODB-CR for legacy reasons, once MONGODB-CR
	// is enabled, users can auth with SCRAM-SHA-1 credentials
	MongoDBCR MechanismName = "MONGODB-CR"
)

// supportedMechanisms returns a list of all the authentication mechanisms
// that can be configured by the Operator
func supportedMechanisms() []MechanismName {
	return []MechanismName{ScramSha256, MongoDBCR, MongoDBX509, LDAPPlain}
}

// fromName returns an implementation of mechanism from the string value
// used in the AutomationConfig. All supported fields are in supportedMechanisms
func fromName(name MechanismName, ac *om.AutomationConfig, conn om.Connection, opts Options) Mechanism {
	switch name {
	case MongoDBCR:
		return NewConnectionCR(conn, ac)
	case ScramSha1:
		return NewConnectionScramSha1(conn, ac)
	case ScramSha256:
		return NewConnectionScramSha256(conn, ac)
	case MongoDBX509:
		return NewConnectionX509(conn, ac, opts)
	case LDAPPlain:
		return NewLdap(conn, ac, opts)
	}
	panic(xerrors.Errorf("unknown authentication mechanism %s. Supported mechanisms are %+v", name, supportedMechanisms()))
}

// mechanismsToDisable returns a list of mechanisms which need to be disabled
// based on the currently supported authentication mechanisms and the desiredMechanisms
func mechanismsToDisable(desiredMechanisms []MechanismName) []MechanismName {
	toDisable := make([]MechanismName, 0)
	for _, m := range supportedMechanisms() {
		if !containsMechanismName(desiredMechanisms, m) {
			toDisable = append(toDisable, m)
		}
	}
	return toDisable
}

// ensureAgentAuthenticationIsConfigured will configure the agent authentication settings based on the desiredAgentAuthMechanism
func ensureAgentAuthenticationIsConfigured(conn om.Connection, opts Options, ac *om.AutomationConfig, desiredAgentAuthMechanismName MechanismName, log *zap.SugaredLogger) error {
	m := fromName(desiredAgentAuthMechanismName, ac, conn, opts)
	if m.IsAgentAuthenticationConfigured() {
		log.Infof("Agent authentication mechanism %s is already configured", desiredAgentAuthMechanismName)
		return nil
	}

	log.Infof("Enabling %s agent authentication", desiredAgentAuthMechanismName)
	return m.EnableAgentAuthentication(opts, log)
}

// ensureDeploymentMechanisms configures the given AutomationConfig to allow deployments to
// authenticate using the specified mechanisms
func ensureDeploymentMechanisms(conn om.Connection, ac *om.AutomationConfig, desiredDeploymentAuthMechanisms []MechanismName, opts Options, log *zap.SugaredLogger) error {
	allRequiredDeploymentMechanismsAreConfigured := true
	for _, mn := range desiredDeploymentAuthMechanisms {
		if !fromName(mn, ac, conn, opts).IsDeploymentAuthenticationConfigured() {
			allRequiredDeploymentMechanismsAreConfigured = false
		} else {
			log.Debugf("Deployment mechanism %s is already configured", mn)
		}
	}

	if allRequiredDeploymentMechanismsAreConfigured {
		log.Info("All required deployment authentication mechanisms are configured")
		return nil
	}

	return conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		for _, mechanismName := range desiredDeploymentAuthMechanisms {
			log.Debugf("Enabling deployment mechanism %s", mechanismName)
			if err := fromName(mechanismName, ac, conn, opts).EnableDeploymentAuthentication(opts); err != nil {
				return xerrors.Errorf("error enabling deployment authentication: %w", err)
			}
		}
		return nil
	}, log)
}

// ensureDeploymentMechanismsAreDisabled configures the given AutomationConfig to allow deployments to
// authenticate using the specified mechanisms
func ensureDeploymentMechanismsAreDisabled(conn om.Connection, ac *om.AutomationConfig, mechanismsToDisable []MechanismName, opts Options, log *zap.SugaredLogger) error {
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
				return xerrors.Errorf("error disabling deployment authentication: %w", err)
			}
		}
		return nil
	}, log)
}

// containsMechanismName returns true if there is at least one MechanismName in `slice`
// that is equal to `mn`.
func containsMechanismName(slice []MechanismName, mn MechanismName) bool {
	for _, item := range slice {
		if item == mn {
			return true
		}
	}
	return false
}
