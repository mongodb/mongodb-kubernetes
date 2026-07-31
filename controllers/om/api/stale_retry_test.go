package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStaleRetrySuccessNoRetry(t *testing.T) {
	totalRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		totalRequests++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := &http.Client{Transport: &staleRetryTransport{
		transport: http.DefaultTransport,
	}}
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if totalRequests != 1 {
		t.Errorf("expected 1 request, got %d", totalRequests)
	}
}

func TestStaleRetryRetriesOnStaleTrue(t *testing.T) {
	totalRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		totalRequests++
		if totalRequests == 1 {
			w.Header().Set("WWW-Authenticate", `Digest realm="test", nonce="n", qop="auth", stale=true`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := &http.Client{Transport: &staleRetryTransport{
		transport: http.DefaultTransport,
	}}
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if totalRequests != 2 {
		t.Errorf("expected 2 requests, got %d", totalRequests)
	}
}

func TestStaleRetryNoRetryOnNonStale401(t *testing.T) {
	totalRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		totalRequests++
		w.Header().Set("WWW-Authenticate", `Digest realm="test", nonce="n", qop="auth"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	client := &http.Client{Transport: &staleRetryTransport{
		transport: http.DefaultTransport,
	}}
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
	if totalRequests != 1 {
		t.Errorf("expected 1 request, got %d", totalRequests)
	}
}

func TestStaleRetryBodyReplayed(t *testing.T) {
	var lastBody string
	totalRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		totalRequests++
		body, _ := io.ReadAll(r.Body)
		lastBody = string(body)
		if totalRequests == 1 {
			w.Header().Set("WWW-Authenticate", `Digest realm="test", nonce="n", qop="auth", stale=true`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := &http.Client{Transport: &staleRetryTransport{
		transport: http.DefaultTransport,
	}}
	resp, err := client.Post(server.URL, "text/plain", strings.NewReader("test body"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if lastBody != "test body" {
		t.Errorf("expected body replayed, got %q", lastBody)
	}
	if totalRequests != 2 {
		t.Errorf("expected 2 requests, got %d", totalRequests)
	}
}

func TestStaleRetryMultipleStaleThenSuccess(t *testing.T) {
	totalRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		totalRequests++
		if totalRequests <= 3 {
			w.Header().Set("WWW-Authenticate", `Digest realm="test", nonce="n", qop="auth", stale=true`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := &http.Client{Transport: &staleRetryTransport{
		transport: http.DefaultTransport,
	}}
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if totalRequests != 4 {
		t.Errorf("expected 4 requests (3 stale + 1 success), got %d", totalRequests)
	}
}

func TestStaleRetryMaxBound(t *testing.T) {
	totalRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		totalRequests++
		w.Header().Set("WWW-Authenticate", `Digest realm="test", nonce="n", qop="auth", stale=true`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	client := &http.Client{Transport: &staleRetryTransport{
		transport: http.DefaultTransport,
	}}
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
	// 1 original + maxStaleRetries retries = maxStaleRetries+1 total
	if totalRequests != maxStaleRetries+1 {
		t.Errorf("expected %d requests, got %d", maxStaleRetries+1, totalRequests)
	}
}
