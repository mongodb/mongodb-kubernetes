package certs

import (
	"fmt"
	"net/url"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/hashicorp/go-multierror"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/workflow"
	"go.uber.org/zap"

	enterprisepem "github.com/10gen/ops-manager-kubernetes/controllers/operator/pem"
	"github.com/10gen/ops-manager-kubernetes/pkg/dns"
	"github.com/10gen/ops-manager-kubernetes/pkg/multicluster"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/kube"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/secret"
	corev1 "k8s.io/api/core/v1"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/client"
)

const NumAgents = 3

// VerifyCertificatesForStatefulSet returns the number of certificates created by the operator which are not ready (approved and issued).
// If all the certificates and keys required for the MongoDB resource exist in the secret with name `secretName`.
// Note: the generation of certificates by the operator is now a deprecated feature to be used in test environments only.
func VerifyCertificatesForStatefulSet(secretGetter secret.Getter, secretName string, opts Options) error {
	s, err := secretGetter.GetSecret(kube.ObjectKey(opts.Namespace, secretName))
	if err != nil {
		return err
	}

	var errs error

	// For multi-cluster mode ....
	if opts.ClusterMode == multi {
		// get the pod names and get the service FQDN for each of the service hostnames
		mdbmName, clusterNum := multicluster.GetRsNamefromMultiStsName(opts.ResourceName), multicluster.MustGetClusterNumFromMultiStsName(opts.ResourceName)
		for podNum := 0; podNum < opts.Replicas; podNum++ {
			podName, serviceFQDN := dns.GetMultiPodName(mdbmName, clusterNum, podNum), dns.GetMultiServiceFQDN(mdbmName, opts.Namespace, clusterNum, podNum)
			pem := fmt.Sprintf("%s-pem", podName)
			if err := validatePemSecret(s, pem, []string{serviceFQDN}); err != nil {
				errs = multierror.Append(errs, err)
			}
		}

		return errs
	}

	for i, pod := range getPodNames(opts) {
		pem := fmt.Sprintf("%s-pem", pod)
		additionalDomains := GetAdditionalCertDomainsForMember(opts, i)
		if err := validatePemSecret(s, pem, additionalDomains); err != nil {
			errs = multierror.Append(errs, err)
		}
	}

	return errs
}

// getPodNames returns the pod names based on the Cert Options provided.
func getPodNames(opts Options) []string {
	_, podnames := dns.GetDNSNames(opts.ResourceName, opts.ServiceName, opts.Namespace, opts.ClusterDomain, opts.Replicas)
	return podnames
}

func GetDNSNames(opts Options) (hostnames, podnames []string) {
	return dns.GetDNSNames(opts.ResourceName, opts.ServiceName, opts.Namespace, opts.ClusterDomain, opts.Replicas)
}

// GetAdditionalCertDomainsForMember gets any additional domains that the
// certificate for the given member of the stateful set should be signed for.
func GetAdditionalCertDomainsForMember(opts Options, member int) (hostnames []string) {
	_, podnames := GetDNSNames(opts)
	for _, certDomain := range opts.additionalCertificateDomains {
		hostnames = append(hostnames, podnames[member]+"."+certDomain)
	}
	if len(opts.horizons) > 0 {
		//at this point len(ss.ReplicaSetHorizons) should be equal to the number
		//of members in the replica set
		for _, externalHost := range opts.horizons[member] {
			//need to use the URL struct directly instead of url.Parse as
			//Parse expects the URL to have a scheme.
			hostURL := url.URL{Host: externalHost}
			hostnames = append(hostnames, hostURL.Hostname())
		}
	}
	return hostnames
}

// validatePemSecret returns true if the given Secret contains a parsable certificate and contains all required domains.
func validatePemSecret(secret corev1.Secret, key string, additionalDomains []string) error {
	data, ok := secret.Data[key]
	if !ok {
		return fmt.Errorf("the secret %s does not contain the expected key %s\n", secret.Name, key)
	}

	pemFile := enterprisepem.NewFileFromData(data)
	if !pemFile.IsComplete() {
		return fmt.Errorf("the certificate is not complete\n")
	}

	cert, err := pemFile.ParseCertificate()
	if err != nil {
		return fmt.Errorf("can't parse certificate: %s\n", err)
	}

	for _, domain := range additionalDomains {
		if !stringutil.Contains(cert.DNSNames, domain) {
			return fmt.Errorf("domain %s is not contained in the list of DNSNames %v\n", domain, cert.DNSNames)
		}
	}
	return nil
}

// ValidateCertificates verifies the Secret containing the certificates and the keys is valid.
func ValidateCertificates(secretGetter secret.Getter, name, namespace string) error {
	byteData, err := secret.ReadByteData(secretGetter, kube.ObjectKey(namespace, name))
	if err == nil {
		// Validate that the secret contains the keys, if it contains the certs.
		for _, value := range byteData {
			pemFile := enterprisepem.NewFileFromData(value)
			if !pemFile.IsValid() {
				return fmt.Errorf(fmt.Sprintf("The Secret %s containing certificates is not valid. Entries must contain a certificate and a private key.", name))
			}
		}
	}
	return nil
}

// VerifyClientCertificatesForAgents returns the number of agent certs that are not yet ready.
func VerifyClientCertificatesForAgents(secretGetter secret.Getter, namespace string) error {
	s, err := secretGetter.GetSecret(kube.ObjectKey(namespace, util.AgentSecretName))
	if err != nil {
		return err
	}

	var errs error
	for _, agentSecretKey := range []string{util.AutomationAgentPemSecretKey, util.MonitoringAgentPemSecretKey, util.BackupAgentPemSecretKey} {
		additionalDomains := []string{} // agents have no additional domains
		if err := validatePemSecret(s, agentSecretKey, additionalDomains); err != nil {
			errs = multierror.Append(errs, err)
		}
	}

	return errs
}

// EnsureSSLCertsForStatefulSet contains logic to ensure that all of the
// required SSL certs for a StatefulSet object exist.
func EnsureSSLCertsForStatefulSet(client kubernetesClient.Client, ms mdbv1.Security, opts Options, log *zap.SugaredLogger) workflow.Status {
	if !ms.IsTLSEnabled() {
		// if there's no SSL certs to generate, return
		return workflow.OK()
	}

	secretName := opts.CertSecretName
	if !ms.TLSConfig.IsSelfManaged() {
		return workflow.Failed("Operator-generated certs are not supported. You must create your own certificates.")
	}
	return validateSelfManagedSSLCertsForStatefulSet(client, secretName, opts)

}

// validateSelfManagedSSLCertsForStatefulSet ensures that a stateful set using
// user-provided certificates has all of the relevant certificates in place.
func validateSelfManagedSSLCertsForStatefulSet(client kubernetesClient.Client, secretName string, opts Options) workflow.Status {
	// A "Certs" attribute has been provided
	// This means that the customer has provided with a secret name they have
	// already populated with the certs and keys for this deployment.
	// Because of the async nature of Kubernetes, this object might not be ready yet,
	// in which case, we'll keep reconciling until the object is created and is correct.
	if err := VerifyCertificatesForStatefulSet(client, secretName, opts); err != nil {
		return workflow.Failed("The secret object '%s' does not contain all the valid certificates needed: %s", secretName, err)
	}

	if err := ValidateCertificates(client, secretName, opts.Namespace); err != nil {
		return workflow.Failed(err.Error())
	}

	return workflow.OK()
}

// ToInternalClusterAuthName takes a hostname e.g. my-replica-set and converts
// it into the name of the secret which will hold the internal clusterFile
func ToInternalClusterAuthName(hostname string) string {
	return fmt.Sprintf("%s-%s", hostname, util.ClusterFileName)
}
