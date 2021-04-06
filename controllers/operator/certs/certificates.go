package certs

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"net/url"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/workflow"
	"go.uber.org/zap"

	enterprisepem "github.com/10gen/ops-manager-kubernetes/controllers/operator/pem"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/kube"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/secret"
	corev1 "k8s.io/api/core/v1"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/client"
	certsv1 "k8s.io/api/certificates/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

var keyUsages = []certsv1.KeyUsage{"digital signature", "key encipherment", "server auth", "client auth"}
var clientKeyUsages = []certsv1.KeyUsage{"digital signature", "key encipherment", "client auth"}

const (
	NumAgents                       = 3
	privateKeySize                  = 4096
	certificateNameCountry          = "US"
	certificateNameState            = "NY"
	certificateNameLocation         = "NY"
	clusterDomain                   = "cluster.local"
	TLSGenerationDeprecationWarning = "This feature has been DEPRECATED and should only be used in testing environments."
)

// createCSR creates a CertificateSigningRequest object and posting it into Kubernetes API.
func createCSR(client kubernetesClient.Client, hosts []string, subject pkix.Name, keyUsages []certsv1.KeyUsage, name, namespace string) ([]byte, error) {
	priv, err := rsa.GenerateKey(rand.Reader, privateKeySize)
	if err != nil {
		return nil, err
	}

	template := x509.CertificateRequest{
		Subject:  subject,
		DNSNames: hosts,
	}
	certRequestBytes, err := x509.CreateCertificateRequest(rand.Reader, &template, priv)
	if err != nil {
		return nil, err
	}

	certRequestPemBytes := &bytes.Buffer{}
	certRequestPemBlock := pem.Block{Type: "CERTIFICATE REQUEST", Bytes: certRequestBytes}
	if err := pem.Encode(certRequestPemBytes, &certRequestPemBlock); err != nil {
		return nil, err
	}

	csr := certsv1.CertificateSigningRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s.%s", name, namespace),
		},
		Spec: certsv1.CertificateSigningRequestSpec{
			Groups:  []string{"system:authenticated"},
			Usages:  keyUsages,
			Request: certRequestPemBytes.Bytes(),
		},
	}

	if err = client.Create(context.TODO(), &csr); err != nil {
		return nil, err
	}

	x509EncodedPrivKey, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, err
	}

	pemEncodedPrivKey := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: x509EncodedPrivKey,
	})
	return pemEncodedPrivKey, nil
}

// ReadCSR will obtain a get a CSR object from the Kubernetes API.
func ReadCSR(client kubernetesClient.Client, name, namespace string) (*certsv1.CertificateSigningRequest, error) {
	csr := &certsv1.CertificateSigningRequest{}
	err := client.Get(context.TODO(),
		types.NamespacedName{Namespace: "", Name: fmt.Sprintf("%s.%s", name, namespace)},
		csr)
	return csr, err
}

// CreateTlsCSR creates a CertificateSigningRequest for Server certificates.
func CreateTlsCSR(client kubernetesClient.Client, name, namespace, clusterDomain string, hosts []string, commonName string) (key []byte, err error) {
	subject := pkix.Name{
		CommonName:         commonName,
		Organization:       []string{clusterDomain + "-server"},
		OrganizationalUnit: []string{namespace},
		Country:            []string{certificateNameCountry},
		Province:           []string{certificateNameState},
		Locality:           []string{certificateNameLocation},
	}
	return createCSR(client, hosts, subject, keyUsages, name, namespace)
}

// CreateInternalClusterAuthCSR creates CSRs for internal cluster authentication.
// The certs structure is very strict, more info in:
// https://docs.mongodb.com/manual/tutorial/configure-x509-member-authentication/index.html
// For instance, both O and OU need to match O and OU for the server TLS certs.
func CreateInternalClusterAuthCSR(client kubernetesClient.Client, name, namespace, clusterDomain string, hosts []string, commonName string) ([]byte, error) {
	subject := pkix.Name{
		CommonName:         commonName,
		Locality:           []string{certificateNameLocation},
		Organization:       []string{clusterDomain + "-server"},
		Country:            []string{certificateNameCountry},
		Province:           []string{certificateNameState},
		OrganizationalUnit: []string{namespace},
	}
	return createCSR(client, hosts, subject, clientKeyUsages, name, namespace)
}

// CreateAgentCSR creates CSR for the agents.
// These is regular client based authentication so the requirements for the Subject are less
// strict than for internal cluster auth.
func CreateAgentCSR(client kubernetesClient.Client, name, namespace, clusterDomain string) ([]byte, error) {
	subject := pkix.Name{
		CommonName:         name,
		Organization:       []string{clusterDomain + "-agent"},
		Locality:           []string{certificateNameLocation},
		Country:            []string{certificateNameCountry},
		Province:           []string{certificateNameState},
		OrganizationalUnit: []string{namespace},
	}
	return createCSR(client, []string{name}, subject, clientKeyUsages, name, namespace)
}

// CSRWasApproved returns true if the given CSR has been approved.
func CSRWasApproved(csr *certsv1.CertificateSigningRequest) bool {
	for _, condition := range csr.Status.Conditions {
		if condition.Type == certsv1.CertificateApproved {
			return true
		}
	}
	return false
}

// CSRHasRequiredDomains checks that a given CSR is requesting a
// certificate valid for at least the provided domains.
func CSRHasRequiredDomains(csr *certsv1.CertificateSigningRequest, domains []string) bool {
	block, _ := pem.Decode(csr.Spec.Request)
	if block == nil {
		return false
	}

	csrX509, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return false
	}

	for _, domain := range domains {
		if !stringutil.Contains(csrX509.DNSNames, domain) {
			return false
		}
	}
	return true
}

// VerifyCertificatesForStatefulSet returns the number of certificates created by the operator which are not ready (approved and issued).
// If all the certificates and keys required for the MongoDB resource exist in the secret with name `secretName`.
// Note: the generation of certificates by the operator is now a deprecated feature to be used in test environments only.
func VerifyCertificatesForStatefulSet(secretGetter secret.Getter, secretName string, opts Options) int {
	s, err := secretGetter.GetSecret(kube.ObjectKey(opts.Namespace, secretName))
	if err != nil {
		return opts.Replicas
	}

	certsNotReady := 0
	for i, pod := range getPodNames(opts) {
		pem := fmt.Sprintf("%s-pem", pod)
		additionalDomains := GetAdditionalCertDomainsForMember(opts, i)
		if !isValidPemSecret(s, pem, additionalDomains) {
			certsNotReady++
		}
	}

	return certsNotReady
}

// getPodNames returns the pod names based on the Cert Options provided.
func getPodNames(opts Options) []string {
	_, podnames := util.GetDNSNames(opts.ResourceName, opts.ServiceName, opts.Namespace, opts.ClusterDomain, opts.Replicas)
	return podnames
}

func GetDNSNames(opts Options) (hostnames, podnames []string) {
	return util.GetDNSNames(opts.ResourceName, opts.ServiceName, opts.Namespace, opts.ClusterDomain, opts.Replicas)
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

// isValidPemSecret returns true if the given Secret contains a parsable certificate and contains all required domains.
func isValidPemSecret(secret corev1.Secret, key string, additionalDomains []string) bool {
	data, ok := secret.Data[key]
	if !ok {
		return false
	}

	pemFile := enterprisepem.NewFileFromData(data)
	if !pemFile.IsComplete() {
		return false
	}

	cert, err := pemFile.ParseCertificate()
	if err != nil {
		return false
	}

	for _, domain := range additionalDomains {
		if !stringutil.Contains(cert.DNSNames, domain) {
			return false
		}
	}
	return true
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
func VerifyClientCertificatesForAgents(secretGetter secret.Getter, namespace string) int {
	s, err := secretGetter.GetSecret(kube.ObjectKey(namespace, util.AgentSecretName))
	if err != nil {
		return NumAgents
	}

	certsNotReady := 0
	for _, agentSecretKey := range []string{util.AutomationAgentPemSecretKey, util.MonitoringAgentPemSecretKey, util.BackupAgentPemSecretKey} {
		additionalDomains := []string{} // agents have no additional domains
		if !isValidPemSecret(s, agentSecretKey, additionalDomains) {
			certsNotReady++
		}
	}

	return certsNotReady
}

// EnsureSSLCertsForStatefulSet contains logic to ensure that all of the
// required SSL certs for a StatefulSet object exist.
func EnsureSSLCertsForStatefulSet(client kubernetesClient.Client, mdb mdbv1.MongoDB, opts Options, log *zap.SugaredLogger) workflow.Status {
	if !mdb.Spec.IsTLSEnabled() {
		// if there's no SSL certs to generate, return
		return workflow.OK()
	}

	secretName := opts.CertSecretName
	if mdb.Spec.Security.TLSConfig.IsSelfManaged() {
		return validateSelfManagedSSLCertsForStatefulSet(client, secretName, opts)
	}
	return ensureOperatorManagedSSLCertsForStatefulSet(client, secretName, opts, log)
}

// validateSelfManagedSSLCertsForStatefulSet ensures that a stateful set using
// user-provided certificates has all of the relevant certificates in place.
func validateSelfManagedSSLCertsForStatefulSet(client kubernetesClient.Client, secretName string, opts Options) workflow.Status {
	// A "Certs" attribute has been provided
	// This means that the customer has provided with a secret name they have
	// already populated with the certs and keys for this deployment.
	// Because of the async nature of Kubernetes, this object might not be ready yet,
	// in which case, we'll keep reconciling until the object is created and is correct.
	if notReadyCerts := VerifyCertificatesForStatefulSet(client, secretName, opts); notReadyCerts > 0 {
		return workflow.Failed("The secret object '%s' does not contain all the certificates needed."+
			"Required: %d, contains: %d", secretName,
			opts.Replicas,
			opts.Replicas-notReadyCerts,
		)
	}

	if err := ValidateCertificates(client, secretName, opts.Namespace); err != nil {
		return workflow.Failed(err.Error())
	}

	return workflow.OK()
}

// ensureOperatorManagedSSLCertsForStatefulSet ensures that a stateful set
// using operator-managed certificates has all of the relevant certificates in
// place.
func ensureOperatorManagedSSLCertsForStatefulSet(client kubernetesClient.Client, secretName string, opts Options, log *zap.SugaredLogger) workflow.Status {
	certsNeedApproval := false

	if err := ValidateCertificates(client, secretName, opts.Namespace); err != nil {
		return workflow.Failed(err.Error())
	}

	if notReadyCerts := VerifyCertificatesForStatefulSet(client, secretName, opts); notReadyCerts > 0 {
		// If the Kube CA and the operator are responsible for the certificates to be
		// ready and correctly stored in the secret object, and this secret is not "complete"
		// we'll go through the process of creating the CSR, wait for certs approval and then
		// creating a correct secret with the certificates and keys.

		// For replica set we need to create rs.Spec.Replicas certificates, one per each Pod
		fqdns, podnames := GetDNSNames(opts)

		// pemFiles will store every key (during the CSR creation phase) and certificate
		// both can happen on different reconciliation stages (CSR and keys are created, then
		// reconciliation, then certs are obtained from the CA). If this happens we need to
		// store the keys in the final secret, that will be updated with the certs, once they
		// are issued by the CA.
		pemFiles := enterprisepem.NewCollection()

		for idx, host := range fqdns {
			csr, err := ReadCSR(client, podnames[idx], opts.Namespace)
			additionalCertDomains := GetAdditionalCertDomainsForMember(opts, idx)
			if err != nil {
				certsNeedApproval = true
				hostnames := []string{host, podnames[idx]}
				hostnames = append(hostnames, additionalCertDomains...)
				key, err := CreateTlsCSR(client, podnames[idx], opts.Namespace, clusterDomainOrDefault(opts.ClusterDomain), hostnames, host)
				if err != nil {
					return workflow.Failed("Failed to create CSR, %s", err)
				}

				// This note was added on Release 1.5.1 of the Operator.
				log.Warn("The Operator is generating TLS certificates for server authentication. " + TLSGenerationDeprecationWarning)

				pemFiles.AddPrivateKey(podnames[idx], string(key))
			} else if !CSRHasRequiredDomains(csr, additionalCertDomains) {
				log.Infow(
					"Certificate request does not have all required domains",
					"requiredDomains", additionalCertDomains,
					"host", host,
				)
				return workflow.Pending("Certificate request for " + host + " doesn't have all required domains. Please manually remove the CSR in order to proceed.")
			} else if CSRWasApproved(csr) {
				log.Infof("Certificate for Pod %s -> Approved", host)
				pemFiles.AddCertificate(podnames[idx], string(csr.Status.Certificate))
			} else {
				log.Infof("Certificate for Pod %s -> Waiting for Approval", host)
				certsNeedApproval = true
			}
		}

		// once we are here we know we have built everything we needed
		// This "secret" object corresponds to the certificates for this statefulset
		labels := make(map[string]string)
		labels["mongodb/secure"] = "certs"
		labels["mongodb/operator"] = "certs." + secretName

		// note that CreateOrUpdateSecret modifies pemFiles in place by merging
		// in the existing values in the secret
		err := enterprisepem.CreateOrUpdateSecret(client, secretName, opts.Namespace, pemFiles)
		if err != nil {
			// If we have an error creating or updating the secret, we might lose
			// the keys, in which case we return an error, to make it clear what
			// the error was to customers -- this should end up in the status
			// message.
			return workflow.Failed("Failed to create or update the secret: %s", err)
		}
	}

	if certsNeedApproval {
		return workflow.Pending("Not all certificates have been approved by Kubernetes CA for %s", opts.ResourceName)
	}
	return workflow.OK()
}

func clusterDomainOrDefault(domain string) string {
	if domain == "" {
		return clusterDomain
	}

	return domain
}

// ToInternalClusterAuthName takes a hostname e.g. my-replica-set and converts
// it into the name of the secret which will hold the internal clusterFile
func ToInternalClusterAuthName(hostname string) string {
	return fmt.Sprintf("%s-%s", hostname, util.ClusterFileName)
}
