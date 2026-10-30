package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestParseAPIError_LicenseEnforcedBody(t *testing.T) {
	body := []byte(`{"error":403,"reason":"Forbidden","detail":"This Ops Manager is not licensed for this operation.","errorCode":"LICENSE_ENFORCED"}`)
	err := parseAPIError(http.StatusForbidden, "PUT", "http://om/api/public/v1.0/groups/1/automationConfig", body)
	require.NotNil(t, err)
	require.NotNil(t, err.Status)
	assert.Equal(t, http.StatusForbidden, *err.Status)
	assert.Equal(t, "LICENSE_ENFORCED", err.ErrorCode)
	assert.Contains(t, err.Detail, "not licensed for this operation")
}

func TestParseAPIError_StandardBodyNotOverwritten(t *testing.T) {
	body := []byte(`{"error":403,"reason":"Forbidden","errorCode":"SOME_CODE","detail":"canonical detail","message":"must not replace detail"}`)
	err := parseAPIError(http.StatusForbidden, "PUT", "http://om/x", body)
	require.NotNil(t, err)
	assert.Equal(t, "SOME_CODE", err.ErrorCode)
	assert.Equal(t, "canonical detail", err.Detail)
}

func TestParseAPIError_ArtifactOutdatedBody(t *testing.T) {
	body := []byte(`{"error":409,"reason":"Conflict","errorCode":"ARTIFACT_OUTDATED","detail":"artifact is not newer than the installed one"}`)
	err := parseAPIError(http.StatusConflict, "PUT", "http://om/api/public/v1.0/admin/licenses", body)
	require.NotNil(t, err)
	assert.True(t, err.IsConflict())
	assert.Equal(t, "ARTIFACT_OUTDATED", err.ErrorCode)
	assert.Equal(t, "Status: 409 (Conflict), ErrorCode: ARTIFACT_OUTDATED, Detail: artifact is not newer than the installed one", err.Error())
}

func TestRequestWithContext_DeadlineAbortsHangingServer(t *testing.T) {
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
