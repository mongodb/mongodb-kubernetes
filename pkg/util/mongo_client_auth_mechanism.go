package util

// WireAuthMechanismForCRAuthMode converts a CR authMode to the corresponding wire mechanism.
// The util.SCRAM umbrella does not name a single wire mechanism;
// the deployed mechanism depends on automation config. Ops Manager also reports autoAuthMechanism
// MONGODB-CR as a default even when authentication is disabled, so without an explicit “auth is on”
// check we would mis-classify disabled clusters as legacy MONGODB-CR.
func WireAuthMechanismForCRAuthMode(mode string, autoAuthMechanism string, authEnabled bool) string {
	if mode == SCRAM {
		if autoAuthMechanism == AutomationConfigScramSha1Option && authEnabled {
			return AutomationConfigScramSha1Option
		}
		return AutomationConfigScramSha256Option
	}
	switch mode {
	case X509:
		return AutomationConfigX509Option
	case LDAP:
		return AutomationConfigLDAPOption
	case SCRAMSHA1:
		return SCRAMSHA1
	case MONGODBCR:
		return AutomationConfigScramSha1Option
	case SCRAMSHA256:
		return AutomationConfigScramSha256Option
	case OIDC:
		return AutomationConfigOIDCOption
	default:
		return ""
	}
}
