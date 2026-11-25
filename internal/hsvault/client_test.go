package hsvault

import (
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLogicalLoginAndKVPaths(t *testing.T) {
	var gotPath, gotMethod, gotToken string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod, gotToken = r.URL.Path, r.Method, r.Header.Get("X-Vault-Token")
		if r.Method == http.MethodPost {
			if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
				t.Errorf("decode request body: %v", err)
			}
		}
		if gotPath == "/v1/auth/kubernetes/login" {
			_, _ = io.WriteString(w, `{"auth":{"client_token":"tok-123","lease_duration":3600}}`)
			return
		}
		if gotMethod == http.MethodGet {
			_, _ = io.WriteString(w, `{"data":{"data":{"user":"admin"},"metadata":{"version":2},"current_version":2}}`)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	c := newTestClient(t, srv)

	login, err := c.Logical().Write("auth/kubernetes/login", map[string]any{"jwt": "my-jwt", "role": "my-role"})
	if err != nil {
		t.Fatalf("login Write: %v", err)
	}
	if gotPath != "/v1/auth/kubernetes/login" || gotMethod != http.MethodPost || gotToken != "" {
		t.Errorf("login request path/method/token = %q/%q/%q", gotPath, gotMethod, gotToken)
	}
	if login.Auth == nil || login.Auth.ClientToken != "tok-123" || gotBody["jwt"] != "my-jwt" || gotBody["role"] != "my-role" {
		t.Errorf("login response/body = %+v / %v", login, gotBody)
	}

	c.SetToken(login.Auth.ClientToken)
	secret, err := c.Logical().Read("/secret/data/mongodb/creds")
	if err != nil {
		t.Fatalf("Logical.Read: %v", err)
	}
	data, ok := secret.Data["data"].(map[string]any)
	if gotPath != "/v1/secret/data/mongodb/creds" || gotToken != "tok-123" || !ok || data["user"] != "admin" {
		t.Errorf("read request/response = %q token %q data %v", gotPath, gotToken, secret.Data)
	}
	if version, ok := secret.Data["current_version"].(json.Number); !ok || version != "2" {
		t.Errorf("current_version = %T(%v), want json.Number(2)", secret.Data["current_version"], secret.Data["current_version"])
	}

	_, err = c.Logical().Write("/secret/data/mongodb/creds", map[string]any{"data": map[string]any{"user": "admin"}})
	if err != nil {
		t.Fatalf("Logical.Write: %v", err)
	}
	if gotPath != "/v1/secret/data/mongodb/creds" || gotMethod != http.MethodPost || gotToken != "tok-123" {
		t.Errorf("write request path/method/token = %q/%q/%q", gotPath, gotMethod, gotToken)
	}

	metadata, err := c.Logical().Read("secret/metadata/mongodb/creds")
	if err != nil {
		t.Fatalf("metadata Read: %v", err)
	}
	if gotPath != "/v1/secret/metadata/mongodb/creds" || metadata == nil {
		t.Errorf("metadata path/data = %q/%v", gotPath, metadata)
	}
}

func TestLogicalNotFoundAndErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "missing") {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"errors":["missing"]}`)
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"errors":["permission denied"]}`)
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	secret, err := c.Logical().Read("secret/data/missing")
	if err != nil || secret != nil {
		t.Errorf("Read 404 = (%v, %v), want (nil, nil)", secret, err)
	}
	_, err = c.Logical().Write("secret/data/missing", map[string]any{"data": map[string]any{}})
	assertErrorContains(t, err, "404", "missing")
	_, err = c.Logical().Read("secret/data/forbidden")
	assertErrorContains(t, err, "GET", "/v1/secret/data/forbidden", "403", "permission denied")
	_, err = c.Logical().Read("unsupported/path")
	assertErrorContains(t, err, "unsupported vault read path")
}

func TestCustomCAAndHTTPDefaults(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":{"data":{"ok":true}}}`)
	}))
	defer srv.Close()
	config := DefaultConfig()
	config.Address = strings.TrimPrefix(srv.URL, "https://")
	if err := config.ConfigureTLS(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})); err != nil {
		t.Fatalf("ConfigureTLS: %v", err)
	}
	c, err := NewClient(config)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c.httpClient.Timeout != 60*time.Second {
		t.Errorf("timeout = %s, want 60s", c.httpClient.Timeout)
	}
	if _, err := c.Logical().Read("secret/data/tls-test"); err != nil {
		t.Fatalf("Read with custom CA: %v", err)
	}
}

func TestTimeoutRedirectAndInvalidConfig(t *testing.T) {
	var redirected bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/target" {
			redirected = true
			return
		}
		http.Redirect(w, r, "/target", http.StatusFound)
	}))
	defer srv.Close()
	config := DefaultConfig()
	config.Address = srv.URL
	c, err := NewClient(config)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Logical().Read("secret/data/redirect")
	assertErrorContains(t, err, "302")
	if redirected {
		t.Error("Vault client followed redirect")
	}
	for _, address := range []string{"", "  ", "ftp://vault.example"} {
		invalid := DefaultConfig()
		invalid.Address = address
		if _, err := NewClient(invalid); err == nil {
			t.Errorf("NewClient(%q) succeeded, want error", address)
		}
	}
}

func newTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	config := DefaultConfig()
	config.Address, config.HttpClient = srv.URL, srv.Client()
	c, err := NewClient(config)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

func assertErrorContains(t *testing.T, err error, substrings ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error")
	}
	for _, substring := range substrings {
		if !strings.Contains(err.Error(), substring) {
			t.Errorf("error %q does not contain %q", err, substring)
		}
	}
}
