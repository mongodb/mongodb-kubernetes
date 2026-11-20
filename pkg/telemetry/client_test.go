package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/mongodb/mongodb-kubernetes/internal/retry"
	"github.com/stretchr/testify/assert"
)

const (
	// baseURLPath is a non-empty Client.BaseURL path to use during tests,
	// to ensure relative URLs are used for all endpoints.
	baseURLPath = "/api-v1"
)

// mockServerHandler simulates different HTTP responses for testing retry behavior.
type mockServerHandler struct {
	attempts    int
	maxAttempts int
	statusCode  int
	retryAfter  string // Used for simulating 429 Retry-After header
	wantRetry   bool
}

func (h *mockServerHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.attempts++

	// Simulate a successful response after maxAttempts has passed
	if h.attempts >= h.maxAttempts && h.wantRetry {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Simulate EOF by closing the connection immediately after hijacking
	if h.statusCode == -1 { // Use a special status code to indicate EOF
		if hj, ok := w.(http.Hijacker); ok {
			conn, _, err := hj.Hijack()
			if err != nil {
				return
			}
			conn.Close() // Close the connection to simulate EOF
			return
		}
	}

	// Simulate a retryable error (500, 502, 503, 504)
	if h.statusCode == 429 && h.retryAfter != "" {
		w.Header().Set("Retry-After", h.retryAfter)
	}
	w.WriteHeader(h.statusCode)
}

func TestSendEventWithRetry(t *testing.T) {
	// Override the sleep function to prevent actual delays
	ctx := context.Background()

	tests := []struct {
		name        string
		statusCode  int
		maxAttempts int
		retryAfter  string
		wantRetry   bool
		expectError bool
	}{
		{"Retry on 500, then success", 500, 3, "", true, false},
		{"Retry on 502, then success", 502, 3, "", true, false},
		{"Retry on 503, then success", 503, 3, "", true, false},
		{"Retry on 504, then success", 504, 3, "", true, false},
		{"Handle 429 with Retry-After", 429, 3, "0", true, false},
		{"No retry on 400", 400, 1, "", false, true},
		{"No retry on 403", 403, 1, "", false, true},
		{"No retry on 404", 404, 1, "", false, true},
		{"Retry on EOF, then success", -1, 3, "", true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := &mockServerHandler{
				attempts:    0,
				maxAttempts: tt.maxAttempts,
				statusCode:  tt.statusCode,
				retryAfter:  tt.retryAfter,
				wantRetry:   tt.wantRetry,
			}

			server := httptest.NewServer(handler)
			defer server.Close()
			retryClient := retry.NewClient()
			retryClient.RetryWaitMin = 0
			retryClient.RetryWaitMax = 0
			client, err := NewClient(retryClient)
			assert.NoError(t, err)
			u, _ := url.Parse(server.URL + baseURLPath + "/")
			client.atlasClient.BaseURL = u
			// Call the function under test
			err = client.SendEventWithRetry(ctx, []Event{})

			// Check expectations
			if tt.expectError && err == nil {
				t.Errorf("Expected an error but got nil")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Did not expect an error, but got: %v", err)
			}

			// Ensure retries were performed correctly
			if handler.attempts != tt.maxAttempts {
				t.Errorf("Expected %d attempts, but got %d", tt.maxAttempts, handler.attempts)
			}
		})
	}
}

func TestNewClientPreservesSuppliedHTTPClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	var transportCalls int
	retryClient := retry.NewClient()
	retryClient.RetryMax = 0
	retryClient.HTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		transportCalls++
		return http.DefaultTransport.RoundTrip(req)
	})}
	client, err := NewClient(retryClient)
	if err != nil {
		t.Fatal(err)
	}
	client.atlasClient.BaseURL, err = url.Parse(server.URL + baseURLPath + "/")
	if err != nil {
		t.Fatal(err)
	}

	if err := client.SendEventWithRetry(context.Background(), []Event{}); err != nil {
		t.Fatalf("SendEventWithRetry() error = %v", err)
	}
	if transportCalls != 1 {
		t.Fatalf("supplied transport calls = %d, want 1", transportCalls)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
