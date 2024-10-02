package pem

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/base32"
	"encoding/json"
	"encoding/pem"
	"strings"

	"golang.org/x/xerrors"
)

type Collection struct {
	PemFiles map[string]File
}

// GetHash returns a cryptographically hashed representation of the collection
// of PEM files.
func (p Collection) GetHash() (string, error) {
	// this relies on the implementation detail that json.Marshal sorts the keys
	// in a map when performing the serialisation, thus resulting in a
	// deterministic representation of the struct
	jsonBytes, err := json.Marshal(p.PemFiles)
	if err != nil {
		// this should never happen
		return "", xerrors.Errorf("could not marshal PEM files to JSON: %w", err)
	}
	hashBytes := sha256.Sum256(jsonBytes)

	// base32 encoding without padding (i.e. no '=' character) is used as this
	// guarantees a strictly alphanumeric output. Since the result is a hash, and
	// thus needs not be reversed, removing the padding is not an issue.
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(hashBytes[:]), nil
}

// NewCollection creates a new Pem Collection with an initialized empty map.
func NewCollection() *Collection {
	return &Collection{
		PemFiles: make(map[string]File),
	}
}

// AddPrivateKey ensures a Pem File exists for the given hostname and key.
func (p Collection) AddPrivateKey(hostname, key string) {
	if key == "" {
		return
	}
	pem, ok := p.PemFiles[hostname]
	if !ok {
		pem = File{PrivateKey: key}
	} else {
		pem.PrivateKey = key
	}
	p.PemFiles[hostname] = pem
}

// AddCertificate ensures a Pem File is added for the given hostname and cert.
func (p Collection) AddCertificate(hostname, cert string) {
	if cert == "" {
		return
	}
	pem, ok := p.PemFiles[hostname]
	if !ok {
		pem = File{Certificate: cert}
	} else {
		pem.Certificate = cert
	}
	p.PemFiles[hostname] = pem
}

// MergeEntry merges a given PEM file into the collection of PEM files. If a
// file with the same hostname exists in the collection, then existing
// components will not be overridden.
func (p Collection) MergeEntry(hostname string, pem File) {
	existingPem := p.PemFiles[hostname]
	if existingPem.PrivateKey == "" {
		p.AddPrivateKey(hostname, pem.PrivateKey)
	}
	if existingPem.Certificate == "" {
		p.AddCertificate(hostname, pem.Certificate)
	}
}

// Merge combines all Pem Files into a map[string]string.
func (p *Collection) Merge() map[string]string {
	result := make(map[string]string)

	for k, v := range p.PemFiles {
		result[k+"-pem"] = v.String()
	}

	return result
}

// MergeWith merges the provided entry into this Collection.
func (p *Collection) MergeWith(data map[string][]byte) map[string]string {
	for k, v := range data {
		hostname := strings.TrimSuffix(k, "-pem")
		p.MergeEntry(hostname, NewFileFrom(string(v)))
	}

	return p.Merge()
}

func (p File) ParseCertificate() ([]*x509.Certificate, error) {
	var certs []*x509.Certificate
	for block, rest := pem.Decode([]byte(p.Certificate)); block != nil; block, rest = pem.Decode(rest) {
		if block == nil {
			return []*x509.Certificate{}, xerrors.Errorf("failed to parse certificate PEM, please ensure validity of the file")
		}
		switch block.Type {
		case "CERTIFICATE":
			cert, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				return []*x509.Certificate{}, err
			}
			certs = append(certs, cert)
		default:
			return []*x509.Certificate{}, xerrors.Errorf("failed to parse certificate PEM, please ensure validity of the file")
		}

	}
	return certs, nil
}

type File struct {
	PrivateKey  string `json:"privateKey"`
	Certificate string `json:"certificate"`
}

func NewFileFrom(data string) File {
	parts := separatePemFile(data)
	privateKey := ""
	certificate := ""

	for _, el := range parts {
		if strings.Contains(el, "BEGIN CERTIFICATE") {
			certificate += el
		} else if strings.Contains(el, "PRIVATE KEY") {
			privateKey = el
		}
	}

	return File{
		PrivateKey:  privateKey,
		Certificate: certificate,
	}
}

func NewFileFromData(data []byte) File {
	return NewFileFrom(string(data))
}

func (p *File) IsValid() bool {
	return p.PrivateKey != ""
}

func (p *File) IsComplete() bool {
	return p.IsValid() && p.Certificate != ""
}

func (p *File) String() string {
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
