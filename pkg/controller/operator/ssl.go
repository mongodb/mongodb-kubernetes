package operator

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/base32"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"strings"
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
