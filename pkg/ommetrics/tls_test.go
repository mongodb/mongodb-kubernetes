package ommetrics

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

func TestTLSOptions(t *testing.T) {
	validCA := testCACertificate(t)
	tests := []struct {
		name         string
		options      TLSOptions
		wantNil      bool
		wantInsecure bool
		wantMin      uint16
		wantPool     bool
		wantErr      bool
	}{
		{name: "system roots", wantNil: true},
		{name: "skip verify", options: TLSOptions{AllowInvalidSSLCertificate: true}, wantInsecure: true},
		{name: "invalid CA", options: TLSOptions{CACertificate: "not pem"}, wantErr: true},
		{name: "CA bundle", options: TLSOptions{CACertificate: validCA}, wantMin: tls.VersionTLS12, wantPool: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg, err := test.options.TLSConfig()
			if (err != nil) != test.wantErr {
				t.Fatalf("TLSConfig() error = %v, wantErr %v", err, test.wantErr)
			}
			if err != nil {
				return
			}
			if (cfg == nil) != test.wantNil {
				t.Fatalf("TLSConfig() nil = %v, want %v", cfg == nil, test.wantNil)
			}
			if cfg == nil {
				return
			}
			if cfg.InsecureSkipVerify != test.wantInsecure {
				t.Fatalf("InsecureSkipVerify = %v, want %v", cfg.InsecureSkipVerify, test.wantInsecure)
			}
			if cfg.MinVersion != test.wantMin {
				t.Fatalf("MinVersion = %d, want %d", cfg.MinVersion, test.wantMin)
			}
			if (cfg.RootCAs != nil) != test.wantPool {
				t.Fatalf("RootCAs present = %v, want %v", cfg.RootCAs != nil, test.wantPool)
			}
		})
	}
}

func testCACertificate(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}
