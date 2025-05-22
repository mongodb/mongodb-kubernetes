package authentication

import (
	"go.uber.org/zap"
	"golang.org/x/xerrors"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/ldap"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/oidc"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

// AuthResource is an interface that a resources that can have authentication enabled should implement.
type AuthResource interface {
	GetName() string
	GetNamespace() string
	GetSecurity() *mdbv1.Security
	IsLDAPEnabled() bool
	IsOIDCEnabled() bool
	GetLDAP(password, caContents string) *ldap.Ldap
}

// Options contains all the required values that are required to configure authentication
// for a set of processes
type Options struct {
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

	OIDCProviderConfigs []oidc.ProviderConfig

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
	log.Infow("ensuring correct deployment mechanisms", "ProcessNames", opts.ProcessNames, "Mechanisms", opts.Mechanisms)

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
	if err := removeUnsupportedAgentMechanisms(conn, opts, log); err != nil {
		return xerrors.Errorf("error removing unused authentication mechanisms %w", err)
	}
	if err := waitForReadyStateIfNeeded(); err != nil {
		return err
	}

	// we remove any unsupported deployment auth mechanisms. This will generally be mechanisms
	// that we are disabling.
	if err := removeUnsupportedDeploymentMechanisms(conn, opts, log); err != nil {
		return xerrors.Errorf("error removing unsupported deployment mechanisms: %w", err)
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

// removeUnsupportedAgentMechanisms removes authentication mechanism that were previously enabled, or were required
// as part of the transition process.
func removeUnsupportedAgentMechanisms(conn om.Connection, opts Options, log *zap.SugaredLogger) error {
	ac, err := conn.ReadAutomationConfig()
	if err != nil {
		return xerrors.Errorf("error reading automation config: %w", err)
	}

	automationConfigAuthMechanismNames := convertToMechanismList(opts.Mechanisms, ac)

	unsupportedMechanisms := mechanismsToDisable(automationConfigAuthMechanismNames)

	log.Infow("configuring agent authentication mechanisms", "enabled", opts.AgentMechanism, "disabling", unsupportedMechanisms)
	for _, mechanism := range unsupportedMechanisms {
		if mechanism.IsAgentAuthenticationConfigured(ac, opts) {
			log.Infof("disabling authentication mechanism %s", mechanism.GetName())
			if err := mechanism.DisableAgentAuthentication(conn, log); err != nil {
				return xerrors.Errorf("error disabling agent authentication: %w", err)
			}
		} else {
			log.Infof("mechanism %s is already disabled", mechanism.GetName())
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
	mechanism := convertToMechanismOrPanic(opts.AgentMechanism, ac)
	if err := ensureAgentAuthenticationIsConfigured(conn, opts, ac, mechanism, log); err != nil {
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
	automationConfigMechanisms := convertToMechanismList(opts.Mechanisms, ac)

	log.Debugf("Automation config authentication mechanisms: %+v", automationConfigMechanisms)
	if err := ensureDeploymentMechanisms(conn, ac, automationConfigMechanisms, opts, log); err != nil {
		return xerrors.Errorf("error ensuring deployment mechanisms: %w", err)
	}

	return nil
}

// removeUnsupportedDeploymentMechanisms updates the given AutomationConfig struct to enable all the given
// authentication mechanisms.
func removeUnsupportedDeploymentMechanisms(conn om.Connection, opts Options, log *zap.SugaredLogger) error {
	ac, err := conn.ReadAutomationConfig()
	if err != nil {
		return xerrors.Errorf("error reading automation config: %w", err)
	}

	// "opts.Mechanisms" is the list of mechanism names passed through from the MongoDB resource.
	automationConfigAuthMechanisms := convertToMechanismList(opts.Mechanisms, ac)

	unsupportedMechanisms := mechanismsToDisable(automationConfigAuthMechanisms)
  
	log.Infow("Removing unsupported deployment authentication mechanisms", "Mechanisms", unsupportedMechanisms)
	if err := ensureDeploymentMechanismsAreDisabled(conn, ac, unsupportedMechanisms, log); err != nil {
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
		if convertToMechanismOrPanic(opts.AgentMechanism, ac).GetName() == MongoDBX509 {
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

// ensureAgentAuthenticationIsConfigured will configure the agent authentication settings based on the desiredAgentAuthMechanism
func ensureAgentAuthenticationIsConfigured(conn om.Connection, opts Options, ac *om.AutomationConfig, mechanism Mechanism, log *zap.SugaredLogger) error {
	if mechanism.IsAgentAuthenticationConfigured(ac, opts) {
		log.Infof("Agent authentication mechanism %s is already configured", mechanism.GetName())
		return nil
	}

	log.Infof("Enabling %s agent authentication", mechanism.GetName())
	return mechanism.EnableAgentAuthentication(conn, opts, log)
}

// ensureDeploymentMechanisms configures the given AutomationConfig to allow deployments to
// authenticate using the specified mechanisms
func ensureDeploymentMechanisms(conn om.Connection, ac *om.AutomationConfig, mechanisms MechanismList, opts Options, log *zap.SugaredLogger) error {
	mechanismsToEnable := make([]Mechanism, 0)
	for _, mechanism := range mechanisms {
		if !mechanism.IsDeploymentAuthenticationConfigured(ac, opts) {
			mechanismsToEnable = append(mechanismsToEnable, mechanism)
		} else {
			log.Debugf("Deployment mechanism %s is already configured", mechanism.GetName())
		}
	}

	if len(mechanismsToEnable) == 0 {
		log.Info("All required deployment authentication mechanisms are configured")
		return nil
	}

	for _, mechanism := range mechanismsToEnable {
		log.Debugf("Enabling deployment mechanism %s", mechanism.GetName())
		if err := mechanism.EnableDeploymentAuthentication(conn, opts, log); err != nil {
			return xerrors.Errorf("error enabling deployment authentication: %w", err)
		}
	}

	return nil
}

// ensureDeploymentMechanismsAreDisabled configures the given AutomationConfig to allow deployments to
// authenticate using the specified mechanisms
func ensureDeploymentMechanismsAreDisabled(conn om.Connection, ac *om.AutomationConfig, mechanismsToDisable MechanismList, log *zap.SugaredLogger) error {
	deploymentMechanismsToDisable := make([]Mechanism, 0)
	for _, mechanism := range mechanismsToDisable {
		if mechanism.IsDeploymentAuthenticationEnabled(ac) {
			deploymentMechanismsToDisable = append(deploymentMechanismsToDisable, mechanism)
		}
	}

	if len(deploymentMechanismsToDisable) == 0 {
		log.Infof("Mechanisms [%s] are all already disabled", mechanismsToDisable)
		return nil
	}

	for _, mechanism := range deploymentMechanismsToDisable {
		log.Debugf("disabling deployment mechanism %s", mechanism.GetName())
		if err := mechanism.DisableDeploymentAuthentication(conn, log); err != nil {
			return xerrors.Errorf("error disabling deployment authentication: %w", err)
		}
	}

	return nil
}
