package operator

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base32"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"strings"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	certsv1 "k8s.io/api/certificates/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

var keyUsages = []certsv1.KeyUsage{"digital signature", "key encipherment", "server auth", "client auth"}
var clientKeyUsages = []certsv1.KeyUsage{"digital signature", "key encipherment", "client auth"}

const (
	PrivateKeyAlgo = "rsa"
	PrivateKeySize = 4096

	CertificateNameCountry            = "US"
	CertificateNameState              = "NY"
	CertificateNameLocation           = "NY"
	CertificateNameOrganization       = "mongodb"
	CertificateNameOrganizationalUnit = "MongoDB Kubernetes Operator"
)

// CertificateData is the object that encapsulates the json document that
// `cfssl` expects.
type certificateData struct {
	Hosts      []string           `json:"hosts"`
	CommonName string             `json:"CN"`
	Key        CertificateDataKey `json:"key"`
	Names      []CertificateNames `json:"names"`
}

type CertificateNames struct {
	Country            string `json:"C,omitempty"`
	State              string `json:"ST,omitempty"`
	Location           string `json:"L,omitempty"`
	Organization       string `json:"O,omitempty"`
	OrganizationalUnit string `json:"OU,omitempty"`
}

// CertificateDataKey key used for this CSR
type CertificateDataKey struct {
	Algo string `json:"algo"`
	Size int    `json:"size"`
}

// CSRFile structure of the file returned by cfssl
type certificateSigningRequestFile struct {
	CSR []byte `json:"csr"`
	Key []byte `json:"key"`
}

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

// canCreateCSR determines if the cfssl, and cfssljson exists.
// It is important to note that we expect those files to live in specific directories,
// and not to just only exist on $PATH.
func canCreateCSR() bool {
	requiredFiles := []string{"/usr/local/bin/cfssljson", "/usr/local/bin/cfssl"}

	for _, f := range requiredFiles {
		if _, err := os.Stat(f); os.IsNotExist(err) {
			return false
		}
	}

	return true
}

// NewCSR will create a CSR object (and server key).
func newCSR(certificate certificateData) (*certificateSigningRequestFile, error) {
	fileContents, err := json.Marshal(certificate)
	if err != nil {
		return nil, err
	}

	// First tmp file is the file from where we read the "hosts" struct
	tmpfile, err := ioutil.TempFile("", "inputdata")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmpfile.Name())

	// Second tmp file is the file where the results of cfssl is stored
	// In this implementation, this is a redirect from the stdout of this
	// command to an actual file in disk.
	tmpfileCfSSLOutput, err := ioutil.TempFile("", "csr")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmpfileCfSSLOutput.Name())

	// This is the output of the cfssljson command into files, we will only
	// use this command to figure out random names for these files, not to write
	// into them directly, as cfssljson will do that for us.
	tmpfileCfSSLJSONOutput, err := ioutil.TempFile("", "cfssljson")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmpfileCfSSLJSONOutput.Name())

	if _, err := tmpfile.Write(fileContents); err != nil {
		return nil, err
	}
	err = tmpfile.Close()
	if err != nil {
		return nil, err
	}

	cfsslCmd := exec.Command("cfssl", "genkey", tmpfile.Name())
	cfssljsonCmd := exec.Command("cfssljson", "-bare", tmpfileCfSSLJSONOutput.Name())

	r, w := io.Pipe()
	cfsslCmd.Stdout = w
	cfssljsonCmd.Stdin = r

	if err := cfsslCmd.Start(); err != nil {
		return nil, err
	}
	if err := cfssljsonCmd.Start(); err != nil {
		return nil, err
	}

	if err := cfsslCmd.Wait(); err != nil {
		return nil, err
	}
	err = w.Close()
	if err != nil {
		return nil, err
	}

	if err := cfssljsonCmd.Wait(); err != nil {
		return nil, err
	}

	csrObj := certificateSigningRequestFile{}
	csrFile, err := os.Open(tmpfileCfSSLJSONOutput.Name() + ".csr")
	if err != nil {
		return nil, err
	}

	keyFile, err := os.Open(tmpfileCfSSLJSONOutput.Name() + "-key.pem")
	if err != nil {
		return nil, err
	}

	_data, err := ioutil.ReadAll(csrFile)
	if err != nil {
		return nil, err
	}
	csrObj.CSR = _data

	_data, err = ioutil.ReadAll(keyFile)
	if err != nil {
		return nil, err
	}
	csrObj.Key = _data

	return &csrObj, err
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

// CreateCSR will send a new CSR to the Kubernetes API
func (k *KubeHelper) createTlsCsr(name, namespace string, hosts []string, commonName string) ([]byte, error) {
	return k.createCSR(certificateData{
		Hosts:      hosts,
		CommonName: commonName,
		Key: CertificateDataKey{
			Algo: PrivateKeyAlgo,
			Size: PrivateKeySize,
		},
		Names: []CertificateNames{{
			Country:            CertificateNameCountry,
			State:              CertificateNameState,
			Location:           CertificateNameLocation,
			Organization:       CertificateNameOrganization,
			OrganizationalUnit: CertificateNameOrganizationalUnit,
		}},
	}, keyUsages, name, namespace)
}

// createCSR creates a CertificateSigningRequest object and posting it into Kubernetes API.
func (k *KubeHelper) createCSR(certificate certificateData, keyUsages []certsv1.KeyUsage, name, namespace string) ([]byte, error) {
	if !canCreateCSR() {
		return nil, fmt.Errorf("cfssl or cfssljson binary could not be found")
	}

	serverCsr, err := newCSR(certificate)
	if err != nil {
		return nil, err
	}

	csr := &certsv1.CertificateSigningRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s.%s", name, namespace),
		},
		Spec: certsv1.CertificateSigningRequestSpec{
			Groups:  []string{"system:authenticated"},
			Usages:  keyUsages,
			Request: serverCsr.CSR,
		},
	}

	err = k.client.Create(context.TODO(), csr)
	return serverCsr.Key, err
}

func (k *KubeHelper) createInternalClusterAuthCSR(name, namespace string, hosts []string, commonName string) ([]byte, error) {
	return k.createCSR(certificateData{
		Hosts:      hosts,
		CommonName: commonName,
		Key: CertificateDataKey{
			Algo: PrivateKeyAlgo,
			Size: PrivateKeySize,
		},
		Names: []CertificateNames{{
			Country:            CertificateNameCountry,
			State:              CertificateNameState,
			Location:           CertificateNameLocation,
			Organization:       CertificateNameOrganization,
			OrganizationalUnit: CertificateNameOrganizationalUnit,
		}},
	}, clientKeyUsages, name, namespace)
}

func (k *KubeHelper) createAgentCSR(name, namespace string) ([]byte, error) {
	return k.createCSR(certificateData{
		Hosts:      []string{name},
		CommonName: name,
		Key: CertificateDataKey{
			Algo: PrivateKeyAlgo,
			Size: PrivateKeySize,
		},
		Names: []CertificateNames{{
			Country:            CertificateNameCountry,
			State:              CertificateNameState,
			Location:           CertificateNameLocation,
			OrganizationalUnit: CertificateNameOrganizationalUnit,
			Organization:       name,
		}},
	}, clientKeyUsages, name, namespace)
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
		if !util.ContainsString(csrX509.DNSNames, domain) {
			return false
		}
	}
	return true
}
