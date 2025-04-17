package authentication

import (
	"slices"
	"strings"

	"go.uber.org/zap"
	"golang.org/x/xerrors"

	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
)

// Mechanism is an interface that needs to be implemented for any Ops Manager authentication mechanism
type Mechanism interface {
	EnableAgentAuthentication(conn om.Connection, opts Options, log *zap.SugaredLogger) error
	DisableAgentAuthentication(conn om.Connection, log *zap.SugaredLogger) error
	EnableDeploymentAuthentication(conn om.Connection, opts Options, log *zap.SugaredLogger) error
	DisableDeploymentAuthentication(conn om.Connection, log *zap.SugaredLogger) error
	// IsAgentAuthenticationConfigured should not rely on util.MergoDelete since the method is always
	// called directly after deserializing the response from OM which should not contain the util.MergoDelete value in any field.
	IsAgentAuthenticationConfigured(ac *om.AutomationConfig, opts Options) bool
	IsDeploymentAuthenticationConfigured(ac *om.AutomationConfig, opts Options) bool
	GetName() MechanismName
}

// MechanismName corresponds to the string used in the automation config representing
// a particular type of authentication
type MechanismName string

const (
	ScramSha256 MechanismName = "SCRAM-SHA-256"
	ScramSha1   MechanismName = "SCRAM-SHA-1"
	MongoDBX509 MechanismName = "MONGODB-X509"
	LDAPPlain   MechanismName = "PLAIN"
	MongoDBOIDC MechanismName = "MONGODB-OIDC"

	// MongoDBCR is an umbrella term for SCRAM-SHA-1 and MONGODB-CR for legacy reasons, once MONGODB-CR
	// is enabled, users can auth with SCRAM-SHA-1 credentials
	MongoDBCR MechanismName = "MONGODB-CR"
)

type MechanismList []Mechanism

func (m MechanismList) String() string {
	names := make([]string, 0)
	for _, mechanism := range m {
		names = append(names, string(mechanism.GetName()))
	}

	slices.Sort(names)

	return strings.Join(names, ", ")
}

func (m MechanismList) Contains(mechanism Mechanism) bool {
	for _, m := range m {
		if m.GetName() == mechanism.GetName() {
			return true
		}
	}

	return false
}

// supportedMechanisms returns a list of all supported authentication mechanisms
// that can be configured by the Operator
var supportedMechanisms = []Mechanism{ScramSha256Mechanism, MongoDBCRMechanism, MongoDBX509Mechanism, LDAPPlainMechanism}

// mechanismsToDisable returns mechanisms which need to be disabled
// based on the currently supported authentication mechanisms and the desiredMechanisms
func mechanismsToDisable(desiredMechanisms MechanismList) MechanismList {
	toDisable := make([]Mechanism, 0)
	for _, mechanism := range supportedMechanisms {
		if !desiredMechanisms.Contains(mechanism) {
			toDisable = append(toDisable, mechanism)
		}
	}

	return toDisable
}

func convertToMechanismList(mechanismModesInCR []string, ac *om.AutomationConfig) MechanismList {
	result := make([]Mechanism, len(mechanismModesInCR))
	for i, mechanismModeInCR := range mechanismModesInCR {
		result[i] = convertToMechanism(mechanismModeInCR, ac)
	}

	return result
}

// convertToMechanism returns an implementation of mechanism from the CR value
func convertToMechanism(mechanismModeInCR string, ac *om.AutomationConfig) Mechanism {
	switch mechanismModeInCR {
	case util.X509:
		return MongoDBX509Mechanism
	case util.LDAP:
		return LDAPPlainMechanism
	case util.SCRAMSHA1:
		return ScramSha1Mechanism
	case util.MONGODBCR:
		return MongoDBCRMechanism
	case util.SCRAMSHA256:
		return ScramSha256Mechanism
	case util.OIDC:
		return MongoDBOIDCMechanism
	case util.SCRAM:
		// if we have already configured authentication, and it has been set to MONGODB-CR/SCRAM-SHA-1
		// we can not transition. This needs to be done in the UI

		// if no authentication has been configured, the default value for "AutoAuthMechanism" is "MONGODB-CR"
		// even if authentication is disabled, so we need to ensure that auth has been enabled.
		if ac.Auth.AutoAuthMechanism == string(MongoDBCR) && ac.Auth.IsEnabled() {
			return MongoDBCRMechanism
		}
		return ScramSha256Mechanism
	}

	// this should never be reached as validation of this string happens at the CR level
	panic(xerrors.Errorf("unknown mechanism name %s", mechanismModeInCR))
}
