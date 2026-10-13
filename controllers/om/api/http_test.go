package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
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

func TestRequestWithContextInto_DecodesResponseBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-MongoDB-Service-Version", "8.0.0")
		_, _ = w.Write([]byte(`{"version":"1","processes":[]}`))
	}))
	t.Cleanup(srv.Close)

	client, err := NewHTTPClient()
	require.NoError(t, err)

	var out map[string]any
	header, err := client.RequestWithContextInto(context.Background(), "GET", srv.URL, "/api/public/v1.0/groups", nil, &out)

	require.NoError(t, err)
	assert.Equal(t, "1", out["version"])
	assert.Equal(t, "8.0.0", header.Get("X-MongoDB-Service-Version"))
}

func TestRequestWithContextInto_ReturnsAPIErrorWithBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"detail":"invalid automation config"}`))
	}))
	t.Cleanup(srv.Close)

	client, err := NewHTTPClient()
	require.NoError(t, err)

	var out map[string]any
	_, err = client.RequestWithContextInto(context.Background(), "GET", srv.URL, "/api/public/v1.0/groups", nil, &out)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid automation config")
}

func TestRequestWithContextInto_EmptyBodyIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	client, err := NewHTTPClient()
	require.NoError(t, err)

	var out map[string]any
	_, err = client.RequestWithContextInto(context.Background(), "GET", srv.URL, "/api/public/v1.0/groups", nil, &out)

	require.Error(t, err)
}

func TestRequestWithContextInto_DropsOversizedBufferStorage(t *testing.T) {
	var buffers []*bytes.Buffer
	responseBuffers = sync.Pool{New: func() any {
		buffer := &bytes.Buffer{}
		buffers = append(buffers, buffer)
		return buffer
	}}
	t.Cleanup(func() { responseBuffers = sync.Pool{New: func() any { return &bytes.Buffer{} }} })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `{"ignored":%q}`, strings.Repeat("x", 4<<20))
	}))
	t.Cleanup(srv.Close)
	client, err := NewHTTPClient()
	require.NoError(t, err)
	var out struct{}
	_, err = client.RequestWithContextInto(t.Context(), "GET", srv.URL, "/", nil, &out)
	require.NoError(t, err, "large responses must still be accepted")
	require.NotEmpty(t, buffers)
	for _, buffer := range buffers {
		assert.LessOrEqual(t, buffer.Cap(), 2<<20, "a large response must not retain its backing storage")
	}
}

// benchmarkAutomationConfigPayload builds an Automation Config shaped response body,
// comparable to what Ops Manager returns in e2e runs.
func benchmarkAutomationConfigPayload(b *testing.B) []byte {
	b.Helper()

	processes := make([]map[string]any, 0, 3000)
	for i := range 3000 {
		processes = append(processes, map[string]any{
			"name":        fmt.Sprintf("pod-%04d", i),
			"hostname":    fmt.Sprintf("pod-%04d.example.mongodb.svc.cluster.local", i),
			"processType": "mongod",
			"version":     "8.0.0",
			"args2_6": map[string]any{
				"net":         map[string]any{"bindIp": "0.0.0.0", "port": 27017},
				"storage":     map[string]any{"dbPath": "/data/db"},
				"replication": map[string]any{"replSetName": "rs"},
				"systemLog":   map[string]any{"destination": "file", "path": "/var/log/mongodb/mongodb.log", "logAppend": true},
			},
		})
	}

	payload := map[string]any{
		"version":   "1",
		"processes": processes,
		"replicaSets": []map[string]any{{
			"id":      "rs",
			"members": []map[string]any{{"_id": 0, "host": "pod-0000:27017", "priority": 1, "votes": 1}},
		}},
		"auth":     map[string]any{"authoritativeSet": false, "autoAuthMechanism": "SCRAM-SHA-256"},
		"tls":      map[string]any{"CAFilePath": "/etc/mongodb/tls/ca.crt"},
		"mongoURI": "mongodb://pod-0000:27017/",
	}

	data, err := json.Marshal(payload)
	require.NoError(b, err)
	b.Logf("payload size: %d bytes", len(data))
	return data
}

// BenchmarkOMResponseRead compares the old read path (raw body returned by io.ReadAll and decoded
// by the caller) with the pooled direct-decode path used by RequestWithContextInto.
func BenchmarkOMResponseRead(b *testing.B) {
	payload := benchmarkAutomationConfigPayload(b)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	b.Cleanup(srv.Close)

	client, err := NewHTTPClient()
	require.NoError(b, err)

	b.Run("ReadAll+Unmarshal", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(payload)))
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			body, _, err := client.Request(context.Background(), "GET", srv.URL, "/api/public/v1.0/groups/g/automationConfig", nil)
			require.NoError(b, err)
			var out map[string]any
			require.NoError(b, json.Unmarshal(body, &out))
		}
	})

	b.Run("Pooled+Unmarshal", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(payload)))
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			var out map[string]any
			_, err := client.RequestWithContextInto(context.Background(), "GET", srv.URL, "/api/public/v1.0/groups/g/automationConfig", nil, &out)
			require.NoError(b, err)
		}
	})
}
