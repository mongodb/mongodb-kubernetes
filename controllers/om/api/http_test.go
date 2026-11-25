package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mongodb/mongodb-kubernetes/controllers/om/apierror"
	"github.com/mongodb/mongodb-kubernetes/internal/retry"
)

func TestCreateHttpRequest(t *testing.T) {
	httpRequest, e := createHTTPRequest(context.Background(), "POST", "http://some.com", nil)
	u, _ := url.Parse("http://some.com")

	assert.NoError(t, e)
	assert.Equal(t, []string{"application/json; charset=UTF-8"}, httpRequest.Header["Content-Type"])
	assert.Equal(t, []string{"KUBERNETES"}, httpRequest.Header["Provider"])
	assert.Equal(t, "POST", httpRequest.Method)
	assert.Equal(t, u, httpRequest.URL)
	assert.Nil(t, httpRequest.Body)
}

func TestCreateHttpRequest_IsBoundToContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	httpRequest, err := createHTTPRequest(ctx, "GET", "http://some.com", nil)
	require.NoError(t, err)

	cancel()
	assert.ErrorIs(t, httpRequest.Context().Err(), context.Canceled)
}

func TestNewHTTPClient_SetsRequestTimeout(t *testing.T) {
	client, err := NewHTTPClient()
	require.NoError(t, err)

	assert.Equal(t, defaultRequestTimeout, client.HTTPClient.Timeout)
}

func TestClientDoRetriesAndReplaysRequestBody(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		assert.Equal(t, "request body", string(body))
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := NewHTTPClient()
	require.NoError(t, err)
	client.RetryMax = 1
	client.RetryWaitMin = 0
	client.RetryWaitMax = 0

	req, err := retry.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, strings.NewReader("request body"))
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, int32(2), requests.Load())
}

// hangingServer never answers; the handler returns only once the client has gone away so that srv.Close() completes.
// The server only notices the client going away once the request body has been consumed (see hangingAutomationConfig).
func hangingServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	return srv, &requests
}

func TestRequest_DeadlineAbortsHangingServer(t *testing.T) {
	tests := []struct {
		name    string
		options []func(*Client) error
		// only one request may reach the server: the aborted request itself, or the digest challenge
		requestsMsg string
	}{
		{
			name:        "without digest auth",
			requestsMsg: "a request aborted by its context must not be retried",
		},
		{
			name:        "with digest auth",
			options:     []func(*Client) error{OptionDigestAuth("user", "password")},
			requestsMsg: "the digest challenge is the only request that may reach the server",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, requests := hangingServer(t)

			client, err := NewHTTPClient(tt.options...)
			require.NoError(t, err)

			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()

			_, _, err = client.Request(ctx, "GET", srv.URL, "/api/public/v1.0/groups", nil)

			require.Error(t, err)
			assert.Contains(t, err.Error(), context.DeadlineExceeded.Error())
			assert.Equal(t, int32(1), requests.Load(), tt.requestsMsg)
		})
	}
}

// Retries that exhaust on an error status must return the response with its
// status: the cleanup path classifies the backup-anchor refusal by status, so
// the client must hand the last response to sendRequest instead of a generic
// retry error.
func TestRequest_ServerErrorAfterRetries_ReturnsStatus(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"detail":"Unexpected error.","error":500,"errorCode":"UNEXPECTED_ERROR","parameters":[],"reason":"Internal Server Error"}`))
	}))
	t.Cleanup(srv.Close)

	client, err := NewHTTPClient()
	require.NoError(t, err)
	// Retry without waiting, to keep the test fast.
	client.RetryMax = 1
	client.RetryWaitMin = 0
	client.RetryWaitMax = 0

	_, _, err = client.Request(context.Background(), "DELETE", srv.URL, "/api/public/v1.0/groups/group/hosts/host", nil)

	require.Error(t, err)
	assert.True(t, apierror.NewNonNil(err).IsServerError(), "the 500 status must reach the caller, got: %v", err)
	assert.Equal(t, int32(2), requests.Load())
}

// A transport error returns no response, so the retries keep the error path.
func TestRequest_TransportErrorAfterRetries_KeepsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	// Close the server before the request so every attempt is refused.
	srv.Close()

	client, err := NewHTTPClient()
	require.NoError(t, err)
	client.RetryMax = 0
	client.RetryWaitMin = 0
	client.RetryWaitMax = 0

	_, _, err = client.Request(context.Background(), "GET", srv.URL, "/api/public/v1.0/groups", nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection refused")
	assert.False(t, apierror.NewNonNil(err).IsServerError())
}
