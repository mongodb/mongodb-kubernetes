package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/hashicorp/go-retryablehttp"
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
			retryableClient := retryablehttp.NewClient()
			retryableClient.RetryWaitMin = 0
			retryableClient.RetryWaitMax = 0

			client, err := NewClient(retryableClient)
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
