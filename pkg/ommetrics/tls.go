package ommetrics

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
)

// TLSOptions are per-OM TLS settings; projects on the same OM must not use different ones.
type TLSOptions struct {
	// CACertificate is a PEM bundle; empty means system roots.
	CACertificate              string
	AllowInvalidSSLCertificate bool
}

// TLSConfig returns nil for the defaults: system roots with verification on.
func (o TLSOptions) TLSConfig() (*tls.Config, error) {
	if o.AllowInvalidSSLCertificate {
		return &tls.Config{InsecureSkipVerify: true}, nil //nolint:gosec // explicit CR-level opt-out
	}
	if o.CACertificate == "" {
		return nil, nil
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(o.CACertificate)) {
		return nil, fmt.Errorf("ommetrics: failed to parse OM CA certificate PEM")
	}
	return &tls.Config{
		RootCAs:    pool,
		MinVersion: tls.VersionTLS12,
	}, nil
}
