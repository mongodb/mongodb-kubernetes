// Package hsvault provides an API-shaped facade for the Vault operations used
// by the operator, with HTTP and KV-v2 adaptations kept behind the facade.
package hsvault

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const defaultHTTPTimeout = 60 * time.Second

// Config contains the Vault address and optional HTTP client.
type Config struct {
	Address    string
	HttpClient *http.Client
}

// DefaultConfig returns the default Vault client configuration.
func DefaultConfig() *Config {
	return &Config{Address: "https://127.0.0.1:8200", HttpClient: http.DefaultClient}
}

// Secret is a decoded logical response. Data holds the complete outer "data"
// object returned by Vault's logical read endpoint.
type Secret struct {
	Data map[string]any
	Auth *SecretAuth
}

// SecretAuth contains the typed authentication fields used by Kubernetes login.
type SecretAuth struct {
	ClientToken string `json:"client_token"`
}

// Client supports the Logical read/write subset used by the operator. It is
// safe for concurrent use.
type Client struct {
	httpClient *http.Client
	address    string
	mu         sync.RWMutex
	token      string
	logical    *Logical
}

// NewClient creates a Vault client from config.
func NewClient(config *Config) (*Client, error) {
	if config == nil {
		return nil, fmt.Errorf("vault config must not be nil")
	}
	address := config.Address
	if strings.TrimSpace(address) == "" {
		return nil, fmt.Errorf("vault address must not be empty")
	}
	if !strings.Contains(address, "://") {
		address = "https://" + address
	}
	parsed, err := url.Parse(address)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid vault address %q", address)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("invalid vault address scheme %q", parsed.Scheme)
	}
	httpClient := config.HttpClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	cloned := *httpClient
	if cloned.Timeout == 0 {
		cloned.Timeout = defaultHTTPTimeout
	}
	cloned.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	c := &Client{httpClient: &cloned, address: strings.TrimSuffix(address, "/")}
	c.logical = &Logical{client: c}
	return c, nil
}

// ConfigureTLS configures the client to trust the supplied CA certificate.
func (config *Config) ConfigureTLS(caData []byte) error {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caData) {
		return fmt.Errorf("CA data is not valid PEM")
	}
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return fmt.Errorf("default transport is not an *http.Transport")
	}
	t := transport.Clone()
	tlsConfig := t.TLSClientConfig.Clone()
	if tlsConfig == nil {
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	} else if tlsConfig.MinVersion < tls.VersionTLS12 {
		tlsConfig.MinVersion = tls.VersionTLS12
	}
	tlsConfig.RootCAs = pool
	t.TLSClientConfig = tlsConfig
	config.HttpClient = &http.Client{Transport: t, Timeout: defaultHTTPTimeout}
	return nil
}

// Logical returns the logical secret API facade.
func (c *Client) Logical() *Logical { return c.logical }

// SetToken atomically sets the token used for authenticated requests.
func (c *Client) SetToken(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token = token
}

// Token returns the current Vault token.
func (c *Client) Token() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.token
}

// Logical exposes Vault's logical Read and Write operations.
type Logical struct{ client *Client }

// Write writes a logical path, including KV-v2 data paths.
func (l *Logical) Write(path string, data map[string]any) (*Secret, error) {
	path = strings.TrimPrefix(path, "/")
	if after, ok := strings.CutPrefix(path, "secret/data/"); ok {
		path = "/v1/secret/data/" + after
	} else if path == "auth/kubernetes/login" {
		path = "/v1/" + path
	} else {
		return nil, fmt.Errorf("unsupported vault write path %q", path)
	}
	payload, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal write request: %w", err)
	}
	body, err := l.client.do(context.Background(), http.MethodPost, path, payload)
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return nil, nil
	}
	return decodeSecret(body, path)
}

// Read reads a logical path, including KV-v2 data and metadata paths. A 404
// response yields (nil, nil), matching the HashiCorp logical API.
func (l *Logical) Read(path string) (*Secret, error) {
	path = strings.TrimPrefix(path, "/")
	switch {
	case strings.HasPrefix(path, "secret/data/"):
		path = "/v1/secret/data/" + strings.TrimPrefix(path, "secret/data/")
	case strings.HasPrefix(path, "secret/metadata/"):
		path = "/v1/secret/metadata/" + strings.TrimPrefix(path, "secret/metadata/")
	default:
		return nil, fmt.Errorf("unsupported vault read path %q", path)
	}
	body, status, err := l.client.doAllowNotFound(context.Background(), http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound {
		return nil, nil
	}
	return decodeSecret(body, path)
}

func decodeSecret(body []byte, path string) (*Secret, error) {
	var decoded struct {
		Data map[string]any `json:"data"`
		Auth *SecretAuth    `json:"auth"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return nil, fmt.Errorf("failed to decode secret response for %s: %w", path, err)
	}
	return &Secret{Data: decoded.Data, Auth: decoded.Auth}, nil
}

func (c *Client) do(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	respBody, status, err := c.doAllowNotFound(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound {
		return nil, fmt.Errorf("vault request failed: %s %s: status %d: %s", method, path, status, string(respBody))
	}
	return respBody, nil
}

func (c *Client) doAllowNotFound(ctx context.Context, method, path string, body []byte) ([]byte, int, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.address+path, reader)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to build vault request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.mu.RLock()
	token := c.token
	c.mu.RUnlock()
	if token != "" {
		req.Header.Set("X-Vault-Token", token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("vault request failed: %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("failed to read vault response body: %s %s: %w", method, path, err)
	}
	if (resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices) && resp.StatusCode != http.StatusNotFound {
		return respBody, resp.StatusCode, fmt.Errorf("vault request failed: %s %s: status %d: %s", method, path, resp.StatusCode, string(respBody))
	}
	return respBody, resp.StatusCode, nil
}
