package operator

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base32"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"strings"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"

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

type pemCollection struct {
	pemFiles map[string]pemFile
}

// getHash returns a cryptographically hashed representation of the collection
// of PEM files.
func (p pemCollection) getHash() (string, error) {
	// this relies on the implementation detail that json.Marshal sorts the keys
	// in a map when performing the serialisation, thus resulting in a
	// deterministic representation of the struct
	jsonBytes, err := json.Marshal(p.pemFiles)
	if err != nil {
		// this should never happen
		return "", fmt.Errorf("could not marshal PEM files to JSON: %w", err)
	}
	hashBytes := sha256.Sum256(jsonBytes)

	// base32 encoding without padding (i.e. no '=' character) is used as this
	// guarantees a strictly alphanumeric output. Since the result is a hash, and
	// thus needs not be reversed, removing the padding is not an issue.
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(hashBytes[:]), nil
}

func newPemCollection() *pemCollection {
	return &pemCollection{
		pemFiles: make(map[string]pemFile),
	}
}

func (p pemCollection) addPrivateKey(hostname, key string) {
	if key == "" {
		return
	}
	pem, ok := p.pemFiles[hostname]
	if !ok {
		pem = pemFile{PrivateKey: key}
	} else {
		pem.PrivateKey = key
	}
	p.pemFiles[hostname] = pem
}

func (p pemCollection) addCertificate(hostname, cert string) {
	if cert == "" {
		return
	}
	pem, ok := p.pemFiles[hostname]
	if !ok {
		pem = pemFile{Certificate: cert}
	} else {
		pem.Certificate = cert
	}
	p.pemFiles[hostname] = pem
}

// mergeEntry merges a given PEM file into the collection of PEM files. If a
// file with the same hostname exists in the collection, then existing
// components will not be overridden.
func (p pemCollection) mergeEntry(hostname string, pem pemFile) {
	existingPem := p.pemFiles[hostname]
	if existingPem.PrivateKey == "" {
		p.addPrivateKey(hostname, pem.PrivateKey)
	}
	if existingPem.Certificate == "" {
		p.addCertificate(hostname, pem.Certificate)
	}
}

func (p *pemCollection) merge() map[string]string {
	result := make(map[string]string)

	for k, v := range p.pemFiles {
		result[k+"-pem"] = v.String()
	}

	return result
}

func (p *pemCollection) mergeWith(data map[string][]byte) map[string]string {
	for k, v := range data {
		hostname := removeSuffixFromHostname(k, "-pem")
		p.mergeEntry(hostname, newPemFileFrom(string(v)))
	}

	return p.merge()
}

func removeSuffixFromHostname(hostname, suffix string) string {
	if !strings.HasSuffix(hostname, suffix) {
		return hostname
	}

	return hostname[:len(hostname)-len(suffix)]
}

func (pf pemFile) parseCertificate() (*x509.Certificate, error) {
	block, _ := pem.Decode([]byte(pf.Certificate))
	return x509.ParseCertificate(block.Bytes)
}

type pemFile struct {
	PrivateKey  string `json:"privateKey"`
	Certificate string `json:"certificate"`
}

func newPemFileFrom(data string) pemFile {
	parts := separatePemFile(data)
	privateKey := ""
	certificate := ""

	for _, el := range parts {
		if strings.Contains(el, "BEGIN CERTIFICATE") {
			certificate = el
		} else if strings.Contains(el, "PRIVATE KEY") {
			privateKey = el
		}
	}

	return pemFile{
		PrivateKey:  privateKey,
		Certificate: certificate,
	}
}

func newPemFileFromData(data []byte) pemFile {
	return newPemFileFrom(string(data))
}

func (p *pemFile) isValid() bool {
	return p.PrivateKey != ""
}

func (p *pemFile) isComplete() bool {
	return p.isValid() && p.Certificate != ""
}

func (p *pemFile) String() string {
	return p.Certificate + p.PrivateKey
}

func separatePemFile(data string) []string {
	parts := strings.Split(data, "\n")
	certificates := make([]string, 0)
	certificatePart := ""

	for _, el := range parts {
		if strings.HasPrefix(el, "-----END") {
			certificates = append(certificates, certificatePart+el+"\n")
			certificatePart = ""
			continue
		}
		certificatePart += el + "\n"
	}

	return certificates
}

// ReadCSR will obtain a get a CSR object from the Kubernetes API
func (k *KubeHelper) readCSR(name, namespace string) (*certsv1.CertificateSigningRequest, error) {
	csr := &certsv1.CertificateSigningRequest{}
	err := k.client.Get(context.TODO(),
		types.NamespacedName{Namespace: "", Name: fmt.Sprintf("%s.%s", name, namespace)},
		csr)

	if err == nil {
		return csr, nil
	}

	return nil, err
}

// CreateTlsCsr creates a CertificateSigningRequest for Server certificates.
func (k *KubeHelper) createTlsCsr(name, namespace, clusterDomain string, hosts []string, commonName string) (key []byte, err error) {
	subject := pkix.Name{
		CommonName:         commonName,
		Organization:       []string{clusterDomain + "-server"},
		OrganizationalUnit: []string{namespace},
		Country:            []string{CertificateNameCountry},
		Province:           []string{CertificateNameState},
		Locality:           []string{CertificateNameLocation},
	}
	return k.createCSR(hosts, subject, keyUsages, name, namespace)
}

// createCSR creates a CertificateSigningRequest object and posting it into Kubernetes API.
func (k *KubeHelper) createCSR(hosts []string, subject pkix.Name, keyUsages []certsv1.KeyUsage, name, namespace string) ([]byte, error) {
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

	if err = k.client.Create(context.TODO(), &csr); err != nil {
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

// createInternalClusterAuthCSR creates CSRs for internal cluster authentication.
// The certs structure is very strict, more info in:
// https://docs.mongodb.com/manual/tutorial/configure-x509-member-authentication/index.html
// For instance, both O and OU need to match O and OU for the server TLS certs.
func (k *KubeHelper) createInternalClusterAuthCSR(name, namespace, clusterDomain string, hosts []string, commonName string) ([]byte, error) {
	subject := pkix.Name{
		CommonName:         commonName,
		Locality:           []string{CertificateNameLocation},
		Organization:       []string{clusterDomain + "-server"},
		Country:            []string{CertificateNameCountry},
		Province:           []string{CertificateNameState},
		OrganizationalUnit: []string{namespace},
	}
	return k.createCSR(hosts, subject, clientKeyUsages, name, namespace)
}

// createAgentCSR creates CSR for the agents.
// These is regular client based authentication so the requirements for the Subject are less
// strict than for internal cluster auth.
func (k *KubeHelper) createAgentCSR(name, namespace, clusterDomain string) ([]byte, error) {
	subject := pkix.Name{
		CommonName:         name,
		Organization:       []string{clusterDomain + "-agent"},
		Locality:           []string{CertificateNameLocation},
		Country:            []string{CertificateNameCountry},
		Province:           []string{CertificateNameState},
		OrganizationalUnit: []string{namespace},
	}
	return k.createCSR([]string{name}, subject, clientKeyUsages, name, namespace)
}

func checkCSRWasApproved(conditions []certsv1.CertificateSigningRequestCondition) bool {
	for _, condition := range conditions {
		if condition.Type == certsv1.CertificateApproved {
			return true
		}
	}

	return false
}

// checkCSRHasRequiredDomains checks that a given CSR is requesting a
// certificate valid for at least the provided domains.
func checkCSRHasRequiredDomains(csr *certsv1.CertificateSigningRequest, domains []string) bool {
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
