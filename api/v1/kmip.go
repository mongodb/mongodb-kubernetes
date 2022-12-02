package v1

import (
	"fmt"
)

// KmipServerConfig contains the relevant configuration for KMIP integration.
type KmipServerConfig struct {
	// KMIP Server url in the following format: hostname:port
	// Valid examples are:
	//   10.10.10.3:5696
	//   my-kmip-server.mycorp.com:5696
	//   kmip-svc.svc.cluster.local:5696
	// +kubebuilder:validation:Pattern=`[^\:]+:[0-9]{0,5}`
	URL string `json:"url"`

	// CA corresponds to a ConfigMap containing an entry for the CA certificate (ca.pem)
	// used for KMIP authentication
	CA string `json:"ca"`
}

// KmipClientConfig contains the relevant configuration for KMIP integration.
type KmipClientConfig struct {
	// A prefix used to construct KMIP client certificate (and corresponding password) Secret names.
	// The names are generated using the following pattern:
	// KMIP Client Certificate (TLS Secret):
	//   <clientCertificatePrefix>-<CR Name>-kmip-client
	// KMIP Client Certificate Password:
	//   <clientCertificatePrefix>-<CR Name>-kmip-client-password
	//   The expected key inside is called "password".
	// +optional
	ClientCertificatePrefix string `json:"clientCertificatePrefix"`
}

func (k *KmipClientConfig) ClientCertificateSecretName(crName string) string {
	if len(k.ClientCertificatePrefix) == 0 {
		return fmt.Sprintf("%s-kmip-client", crName)
	}
	return fmt.Sprintf("%s-%s-kmip-client", k.ClientCertificatePrefix, crName)
}

func (k *KmipClientConfig) ClientCertificatePasswordSecretName(crName string) string {
	if len(k.ClientCertificatePrefix) == 0 {
		return fmt.Sprintf("%s-kmip-client-password", crName)
	}
	return fmt.Sprintf("%s-%s-kmip-client-password", k.ClientCertificatePrefix, crName)
}

func (k *KmipClientConfig) ClientCertificateSecretKeyName() string {
	return "tls.crt"
}

func (k *KmipClientConfig) ClientCertificatePasswordKeyName() string {
	return "password"
}
