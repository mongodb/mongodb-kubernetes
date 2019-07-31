package util

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"time"
)

// NewHTTPClient is a functional options constructor, based on this blog post:
// https://dave.cheney.net/2014/10/17/functional-options-for-friendly-apis
// The default clients specifies some important timeouts (some of them are synced with AA one):
// 10 seconds for connection (TLS/non TLS)
// 10 minutes for requests (time to get the first response headers)
func NewHTTPClient(options ...func(*http.Client) error) (*http.Client, error) {
	client := &http.Client{
		Transport: &http.Transport{
			ResponseHeaderTimeout: 10 * time.Minute,
			TLSHandshakeTimeout:   10 * time.Second,
			DialContext: (&net.Dialer{
				Timeout: 10 * time.Second,
			}).DialContext,
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

// SerializeToBuffer takes any object and tries to serialize it to the buffer
func SerializeToBuffer(v interface{}) (io.Reader, error) {
	var buffer io.Reader
	if v != nil {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		buffer = bytes.NewBuffer(b)
	}
	return buffer, nil
}
