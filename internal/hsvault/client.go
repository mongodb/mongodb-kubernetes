// Package hsvault provides a minimal, dependency-free client for the subset
// of the HashiCorp Vault HTTP API needed by this operator: Kubernetes
// authentication and KV-v2 secret read/write operations.
package hsvault

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

const (
	// kubernetesLoginPath is the Vault endpoint for Kubernetes auth.
	kubernetesLoginPath = "/v1/auth/kubernetes/login"
	// kvDataPrefix is the path prefix for KV-v2 secret data operations.
	kvDataPrefix = "/v1/secret/data/"
	// kvMetadataPrefix is the path prefix for KV-v2 secret metadata operations.
	kvMetadataPrefix = "/v1/secret/metadata/"
)

// Secret is a decoded logical read response. It mirrors the shape of
// api.Logical().Read: Data holds the complete outer "data" object returned
// by the Vault API. For KV-v2 data reads that means Data["data"] holds the
// user secret and Data["metadata"] the version metadata; for metadata reads
// Data["current_version"] etc. is present directly.
type Secret struct {
	Data map[string]any
}

// Client is a minimal Vault client for Kubernetes login and KV-v2
// operations. It is safe for concurrent use.
type Client struct {
	httpClient *http.Client
	address    string

	mu    sync.RWMutex
	token string
}

// NewClient creates a Client targeting the given Vault address. If
// httpClient is nil, http.DefaultClient is used.
func NewClient(address string, httpClient *http.Client) (*Client, error) {
	if strings.TrimSpace(address) == "" {
		return nil, fmt.Errorf("vault address must not be empty")
	}
	if _, err := url.Parse(address); err != nil {
		return nil, fmt.Errorf("invalid vault address %q: %w", address, err)
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{
		httpClient: httpClient,
		address:    strings.TrimSuffix(address, "/"),
	}, nil
}

// SetToken atomically sets the token used for authenticated requests.
func (c *Client) SetToken(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token = token
}

// Token returns the current token.
func (c *Client) Token() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.token
}

// LoginRequest is the payload for Kubernetes authentication.
type LoginRequest struct {
	JWT  string `json:"jwt"`
	Role string `json:"role"`
}

// LoginResponse is the decoded response of a successful Kubernetes login.
type LoginResponse struct {
	// Auth contains the auth block, including the client token.
	Auth map[string]any
	// Raw is the full decoded response body.
	Raw map[string]any
}

// Login authenticates against Vault using the Kubernetes auth method and
// stores the returned client token on the client.
func (c *Client) Login(ctx context.Context, jwt, role string) (*LoginResponse, error) {
	body, err := json.Marshal(LoginRequest{JWT: jwt, Role: role})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal login request: %w", err)
	}

	respBody, err := c.do(ctx, http.MethodPost, kubernetesLoginPath, body)
	if err != nil {
		return nil, err
	}

	var decoded struct {
		Auth map[string]any `json:"auth"`
	}
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return nil, fmt.Errorf("failed to decode login response: %w", err)
	}

	if decoded.Auth != nil {
		if token, ok := decoded.Auth["client_token"].(string); ok {
			c.SetToken(token)
		}
	}
	return &LoginResponse{Auth: decoded.Auth, Raw: nil}, nil
}

// ReadKV reads a KV-v2 secret at the given mount-relative path
// (e.g. "mongodb/credentials"). A 404 response yields (nil, nil).
// Path segments are URL-escaped as provided.
func (c *Client) ReadKV(ctx context.Context, path string) (*Secret, error) {
	return c.read(ctx, kvDataPrefix+strings.TrimPrefix(path, "/"))
}

// ReadKVMetadata reads only the metadata of a KV-v2 secret. A 404 response
// yields (nil, nil).
func (c *Client) ReadKVMetadata(ctx context.Context, path string) (*Secret, error) {
	return c.read(ctx, kvMetadataPrefix+strings.TrimPrefix(path, "/"))
}

func (c *Client) read(ctx context.Context, fullPath string) (*Secret, error) {
	respBody, status, err := c.doAllowNotFound(ctx, http.MethodGet, fullPath, nil)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound {
		return nil, nil
	}

	var decoded struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return nil, fmt.Errorf("failed to decode secret response for %s: %w", fullPath, err)
	}

	// Preserve the complete outer "data" object, matching api.Secret.Data.
	return &Secret{Data: decoded.Data}, nil
}

// WriteKV writes a KV-v2 secret at the given mount-relative path. The
// caller provides the full request body envelope, which is sent unchanged
// (matching api.Logical().Write semantics; KV-v2 callers pass
// {"data": secretData}).
func (c *Client) WriteKV(ctx context.Context, path string, body map[string]any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("failed to marshal write request: %w", err)
	}
	_, err = c.do(ctx, http.MethodPost, kvDataPrefix+strings.TrimPrefix(path, "/"), payload)
	return err
}

// do performs a request and returns the response body on success (2xx).
// Non-2xx responses (including 404) produce an informative error including
// method, path, status and the Vault error body.
func (c *Client) do(ctx context.Context, method, fullPath string, body []byte) ([]byte, error) {
	respBody, status, err := c.doAllowNotFound(ctx, method, fullPath, body)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound {
		return nil, fmt.Errorf("vault request failed: %s %s: status %d: %s", method, fullPath, status, string(respBody))
	}
	return respBody, nil
}

// doAllowNotFound behaves like do but additionally reports the status code
// and does not treat 404 as an error, so callers can implement
// 404-tolerant semantics.
func (c *Client) doAllowNotFound(ctx context.Context, method, fullPath string, body []byte) ([]byte, int, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.address+fullPath, reader)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to build vault request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token := c.Token(); token != "" {
		req.Header.Set("X-Vault-Token", token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("vault request failed: %s %s: %w", method, fullPath, err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("failed to read vault response body: %s %s: %w", method, fullPath, err)
	}

	if (resp.StatusCode < 200 || resp.StatusCode > 299) && resp.StatusCode != http.StatusNotFound {
		return respBody, resp.StatusCode, fmt.Errorf("vault request failed: %s %s: status %d: %s", method, fullPath, resp.StatusCode, string(respBody))
	}
	return respBody, resp.StatusCode, nil
}
