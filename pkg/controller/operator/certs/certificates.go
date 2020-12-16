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

	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/client"
	certsv1 "k8s.io/api/certificates/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

var keyUsages = []certsv1.KeyUsage{"digital signature", "key encipherment", "server auth", "client auth"}
var clientKeyUsages = []certsv1.KeyUsage{"digital signature", "key encipherment", "client auth"}

const (
	PrivateKeySize          = 4096
	CertificateNameCountry  = "US"
	CertificateNameState    = "NY"
	CertificateNameLocation = "NY"
)

// CreateCSR creates a CertificateSigningRequest object and posting it into Kubernetes API.
func CreateCSR(client kubernetesClient.Client, hosts []string, subject pkix.Name, keyUsages []certsv1.KeyUsage, name, namespace string) ([]byte, error) {
	priv, err := rsa.GenerateKey(rand.Reader, PrivateKeySize)
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
		Country:            []string{CertificateNameCountry},
		Province:           []string{CertificateNameState},
		Locality:           []string{CertificateNameLocation},
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
		Locality:           []string{CertificateNameLocation},
		Organization:       []string{clusterDomain + "-server"},
		Country:            []string{CertificateNameCountry},
		Province:           []string{CertificateNameState},
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
		Locality:           []string{CertificateNameLocation},
		Country:            []string{CertificateNameCountry},
		Province:           []string{CertificateNameState},
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
