package certs

import (
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
)

// UnusedClientConfigured returns the warnings to report when a resource configures
// security.managedCertificate.client but the deployment issues no client certificates, so the
// configured client settings `managedCertificate.client` would not be used by operator.
//
// The client certificate category exists when the deployment uses x509 for internal cluster
// authentication or for the automation agent. With SCRAM neither is true: the operator signs no
// certificate with the configured client issuer, and it creates no client CA trust bundle. Nothing
// fails, which is why the settings are worth pointing out rather than rejecting.
func UnusedClientConfigured(res CertificateOwner) []status.Warning {
	sec := res.GetSecurity()
	if !sec.IsManagedCertificateEnabled() || sec.ManagedCertificate.Client == nil {
		return nil
	}
	if sec.RequiresX509ClientCerts() {
		return nil
	}
	return []status.Warning{
		"The field spec.managedCertificate.client is set even though the deployment doesn't use x509 for internal " +
			"cluster authentication or for the automation agent, so the operator doesn't use the field and it should not be set. " +
			"The presented client certificates will be validated against the server CA.",
	}
}
