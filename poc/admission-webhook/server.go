package admissionwebhook

import (
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"github.com/mongodb/mongodb-kubernetes/pkg/webhook"
)

// Start starts a TLS HTTP server on addr (use ":0" for a random free port).
// hosts is the list of IP addresses or hostnames to include in the cert's SAN —
// must include the IP the Kubernetes API server will use to reach this process.
// Returns the PEM-encoded CA cert, the actual bound address (host:port), and any error.
func Start(addr string, hosts []string, mux *http.ServeMux) (certPEM []byte, actualAddr string, err error) {
	certDir, err := os.MkdirTemp("", "webhook-certs-*")
	if err != nil {
		return nil, "", fmt.Errorf("creating temp cert dir: %w", err)
	}

	if err := webhook.CreateCertFiles(hosts, certDir); err != nil {
		return nil, "", fmt.Errorf("creating cert files: %w", err)
	}

	certPath := filepath.Join(certDir, "tls.crt")
	keyPath := filepath.Join(certDir, "tls.key")

	certPEM, err = os.ReadFile(certPath)
	if err != nil {
		return nil, "", fmt.Errorf("reading cert PEM: %w", err)
	}

	tlsCert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, "", fmt.Errorf("loading TLS key pair: %w", err)
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, "", fmt.Errorf("listening on %s: %w", addr, err)
	}

	srv := &http.Server{
		Handler: mux,
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{tlsCert},
			MinVersion:   tls.VersionTLS12,
		},
	}

	go func() {
		if err := srv.ServeTLS(ln, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("webhook server exited unexpectedly: %v", err)
		}
	}()

	return certPEM, ln.Addr().String(), nil
}
