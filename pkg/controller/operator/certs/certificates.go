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

	enterprisepem "github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/pem"
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
	numAgents               = 3
	privateKeySize          = 4096
	certificateNameCountry  = "US"
	certificateNameState    = "NY"
	certificateNameLocation = "NY"
)

// CreateCSR creates a CertificateSigningRequest object and posting it into Kubernetes API.
func CreateCSR(client kubernetesClient.Client, hosts []string, subject pkix.Name, keyUsages []certsv1.KeyUsage, name, namespace string) ([]byte, error) {
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
	return CreateCSR(client, hosts, subject, keyUsages, name, namespace)
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
	return CreateCSR(client, hosts, subject, clientKeyUsages, name, namespace)
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
	return CreateCSR(client, []string{name}, subject, clientKeyUsages, name, namespace)
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
	_, podnames := util.GetDNSNames(opts.Name, opts.ServiceName, opts.Namespace, opts.ClusterDomain, opts.Replicas)
	return podnames
}

func GetDNSNames(opts Options) (hostnames, podnames []string) {
	return util.GetDNSNames(opts.Name, opts.ServiceName, opts.Namespace, opts.ClusterDomain, opts.Replicas)
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
		return numAgents
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
