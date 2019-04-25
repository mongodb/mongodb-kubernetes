package util

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"time"
)

// NewHTTPClient is a functional options constructor, based on this blog post:
// https://dave.cheney.net/2014/10/17/functional-options-for-friendly-apis
func NewHTTPClient(options ...func(*http.Client) error) (*http.Client, error) {
	client := &http.Client{
		Transport: &http.Transport{
			ResponseHeaderTimeout: 10 * time.Minute,
			TLSHandshakeTimeout:   10 * time.Second,
		},
	}

	for _, op := range options {
		err := op(client)
		if err != nil {
			return nil, err
		}
	}

	return client, nil
}

// OptionSkipVerify will set the Insecure Skip which means that TLS certs will not be
// verified for validity.
func OptionSkipVerify(client *http.Client) error {
	TLSClientConfig := &tls.Config{InsecureSkipVerify: true}

	transport := client.Transport.(*http.Transport)
	transport.TLSClientConfig = TLSClientConfig
	client.Transport = transport

	return nil
}

// OptionCAValidate will use the CA certificate, passed as a string, to validate the
// certificates presented by Ops Manager.
func OptionCAValidate(ca string) func(client *http.Client) error {
	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM([]byte(ca))
	TLSClientConfig := &tls.Config{
		InsecureSkipVerify: false,
		RootCAs:            caCertPool,
	}

	return func(client *http.Client) error {
		transport := client.Transport.(*http.Transport)
		transport.TLSClientConfig = TLSClientConfig
		client.Transport = transport

		return nil
	}
}
