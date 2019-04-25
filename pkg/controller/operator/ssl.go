package operator

import (
	"context"
	"encoding/json"
	"io"
	"strings"

	"fmt"
	"io/ioutil"
	"os"
	"os/exec"

	certsv1 "k8s.io/api/certificates/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

var keyUsages = []certsv1.KeyUsage{"digital signature", "key encipherment", "server auth", "client auth"}

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
		pem = pemFile{privateKey: key}
	} else {
		pem.privateKey = key
	}
	p.pemFiles[hostname] = pem
}

func (p pemCollection) addCertificate(hostname, cert string) {
	if cert == "" {
		return
	}
	pem, ok := p.pemFiles[hostname]
	if !ok {
		pem = pemFile{certificate: cert}
	} else {
		pem.certificate = cert
	}
	p.pemFiles[hostname] = pem
}

func (p pemCollection) addEntry(hostname string, pem pemFile) {
	p.addPrivateKey(hostname, pem.privateKey)
	p.addCertificate(hostname, pem.certificate)
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
		p.addEntry(hostname, newPemFileFrom(string(v)))
	}

	return p.merge()
}

func removeSuffixFromHostname(hostname, suffix string) string {
	if !strings.HasSuffix(hostname, suffix) {
		return hostname
	}

	return hostname[:len(hostname)-len(suffix)]
}

type pemFile struct {
	privateKey  string
	certificate string
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
		privateKey:  privateKey,
		certificate: certificate,
	}
}

func newPemFileFromData(data []byte) pemFile {
	return newPemFileFrom(string(data))
}

func (p *pemFile) validate() bool {
	return !(p.privateKey == "" && p.certificate != "")
}

func (p *pemFile) String() string {
	return p.certificate + p.privateKey
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

// ssl.go provides a mechanism to obtain certificates programmatically.

// NewCSR will create a CSR object (and server key).
func newCSR(name string, hosts []string, commonName string) (*certificateSigningRequestFile, error) {
	data := certificateData{
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
	}

	fileContents, err := json.Marshal(data)
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
func (k *KubeHelper) createCSR(name, namespace string, hosts []string, commonName string) ([]byte, error) {
	serverCsr, err := newCSR(name, hosts, commonName)
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

func checkCSRWasApproved(conditions []certsv1.CertificateSigningRequestCondition) bool {
	for _, condition := range conditions {
		if condition.Type == certsv1.CertificateApproved {
			return true
		}
	}

	return false
}
