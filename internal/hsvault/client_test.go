package hsvault

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestLogin(t *testing.T) {
	var gotPath, gotMethod, gotToken string
	var gotBody LoginRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotToken = r.Header.Get("X-Vault-Token")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"auth":{"client_token":"tok-123","lease_duration":3600}}`))
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, srv.Client())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	resp, err := c.Login(context.Background(), "my-jwt", "my-role")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/v1/auth/kubernetes/login" {
		t.Errorf("path = %q, want /v1/auth/kubernetes/login", gotPath)
	}
	if gotBody.JWT != "my-jwt" || gotBody.Role != "my-role" {
		t.Errorf("body = %+v, want jwt=my-jwt role=my-role", gotBody)
	}
	if gotToken != "" {
		t.Errorf("login should not send X-Vault-Token, got %q", gotToken)
	}
	if resp.Auth["client_token"] != "tok-123" {
		t.Errorf("auth.client_token = %v, want tok-123", resp.Auth["client_token"])
	}
	if c.Token() != "tok-123" {
		t.Errorf("client token = %q, want tok-123", c.Token())
	}
}

func TestReadKV(t *testing.T) {
	var gotPath, gotToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotToken = r.URL.Path, r.Header.Get("X-Vault-Token")
		_, _ = w.Write([]byte(`{"data":{"data":{"user":"admin","pass":"s3cret"},"metadata":{"version":2}}}`))
	}))
	defer srv.Close()

	c, _ := NewClient(srv.URL, srv.Client())
	c.SetToken("tok-abc")

	secret, err := c.ReadKV(context.Background(), "mongodb/creds")
	if err != nil {
		t.Fatalf("ReadKV: %v", err)
	}
	if gotPath != "/v1/secret/data/mongodb/creds" {
		t.Errorf("path = %q, want /v1/secret/data/mongodb/creds", gotPath)
	}
	if gotToken != "tok-abc" {
		t.Errorf("X-Vault-Token = %q, want tok-abc", gotToken)
	}
	data, ok := secret.Data["data"].(map[string]any)
	if !ok || data["user"] != "admin" || data["pass"] != "s3cret" {
		t.Errorf("data.data = %v", secret.Data["data"])
	}
	meta, ok := secret.Data["metadata"].(map[string]any)
	if !ok || meta["version"] != float64(2) {
		t.Errorf("data.metadata = %v", secret.Data["metadata"])
	}
}

func TestReadKVMetadata(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"data":{"created_time":"2026-01-01T00:00:00Z","current_version":3,"max_versions":0}}`))
	}))
	defer srv.Close()

	c, _ := NewClient(srv.URL, srv.Client())

	secret, err := c.ReadKVMetadata(context.Background(), "mongodb/creds")
	if err != nil {
		t.Fatalf("ReadKVMetadata: %v", err)
	}
	if gotPath != "/v1/secret/metadata/mongodb/creds" {
		t.Errorf("path = %q, want /v1/secret/metadata/mongodb/creds", gotPath)
	}
	if secret.Data["current_version"] != float64(3) {
		t.Errorf("data = %v", secret.Data)
	}
}

func TestWriteKV(t *testing.T) {
	var gotMethod, gotPath, gotToken, gotContentType string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotToken = r.Method, r.URL.Path, r.Header.Get("X-Vault-Token")
		gotContentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"version":5}}`))
	}))
	defer srv.Close()

	c, _ := NewClient(srv.URL, srv.Client())
	c.SetToken("tok-w")

	err := c.WriteKV(context.Background(), "mongodb/creds", map[string]any{
		"data": map[string]any{"user": "admin"},
	})
	if err != nil {
		t.Fatalf("WriteKV: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/v1/secret/data/mongodb/creds" {
		t.Errorf("path = %q, want /v1/secret/data/mongodb/creds", gotPath)
	}
	if gotToken != "tok-w" {
		t.Errorf("X-Vault-Token = %q, want tok-w", gotToken)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	data, ok := gotBody["data"].(map[string]any)
	if !ok {
		t.Fatalf("body = %v, want single top-level \"data\" wrapper", gotBody)
	}
	if data["user"] != "admin" {
		t.Errorf("body data = %v, want user=admin", data)
	}
	if _, ok := data["data"]; ok {
		t.Errorf("body data = %v, want no nested double \"data\" wrapper", data)
	}
}

func TestReadKVNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[]}`))
	}))
	defer srv.Close()

	c, _ := NewClient(srv.URL, srv.Client())

	secret, err := c.ReadKV(context.Background(), "missing/path")
	if err != nil {
		t.Fatalf("ReadKV 404: expected (nil, nil), got err %v", err)
	}
	if secret != nil {
		t.Errorf("ReadKV 404: expected nil secret, got %v", secret)
	}
}

func TestErrorResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"errors":["permission denied"]}`))
	}))
	defer srv.Close()

	c, _ := NewClient(srv.URL, srv.Client())
	c.SetToken("tok-bad")

	_, err := c.ReadKV(context.Background(), "secret/path")
	if err == nil {
		t.Fatal("expected error for 403 response")
	}
	wantSubstrings := []string{"GET", "/v1/secret/data/secret/path", "403", "permission denied"}
	for _, s := range wantSubstrings {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("error %q does not contain %q", err.Error(), s)
		}
	}

	err = c.WriteKV(context.Background(), "secret/path", map[string]any{"data": map[string]any{}})
	if err == nil || !strings.Contains(err.Error(), "POST") {
		t.Errorf("write error = %v, want POST + status in message", err)
	}
}

func TestInvalidConfig(t *testing.T) {
	if _, err := NewClient("", nil); err == nil {
		t.Error("expected error for empty address")
	}
	if _, err := NewClient("  ", nil); err == nil {
		t.Error("expected error for whitespace address")
	}
	// Default client injection works.
	c, err := NewClient("http://127.0.0.1:8200", nil)
	if err != nil {
		t.Fatalf("NewClient with nil http client: %v", err)
	}
	if c.httpClient != http.DefaultClient {
		t.Error("expected http.DefaultClient when nil is provided")
	}
}

func TestURLPathEscapingPreserved(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/v1/secret/data/my/secret%20key" {
			t.Errorf("path = %q, want escaped segment preserved", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":{"data":{},"metadata":{}}}`))
	}))
	defer srv.Close()

	c, _ := NewClient(srv.URL, srv.Client())
	if _, err := c.ReadKV(context.Background(), "my/secret%20key"); err != nil {
		t.Fatalf("ReadKV escaped path: %v", err)
	}
}

func TestTokenSetterGetterThreadSafety(t *testing.T) {
	c, _ := NewClient("http://127.0.0.1:8200", nil)
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c.SetToken("tok")
			_ = c.Token()
		}(i)
	}
	wg.Wait()
	if c.Token() != "tok" {
		t.Errorf("token = %q, want tok", c.Token())
	}
}
