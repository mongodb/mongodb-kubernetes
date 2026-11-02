package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	staleChallenge = `Digest realm="test", nonce="n", qop="auth", stale=true`
	freshChallenge = `Digest realm="test", nonce="n", qop="auth"`
)

// readerWithoutGetBody hides the concrete reader type from http.NewRequest, so
// the request has no GetBody and the transport has to buffer the body itself.
type readerWithoutGetBody struct {
	io.Reader
}

// roundTripFunc adapts a function to http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// trackedBody records whether a response body was read to EOF and closed.
type trackedBody struct {
	reader  io.Reader
	drained bool
	closed  bool
}

func (b *trackedBody) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	if err == io.EOF {
		b.drained = true
	}
	return n, err
}

func (b *trackedBody) Close() error {
	b.closed = true
	return nil
}

func TestStaleRetryTransport(t *testing.T) {
	tests := []struct {
		name string
		// respond returns the status and the WWW-Authenticate header for request n (1-based).
		respond      func(n int) (int, string)
		method       string
		body         string
		noGetBody    bool
		wantStatus   int
		wantRequests int
		wantBody     string
	}{
		{
			name:         "success is not retried",
			respond:      alwaysStatus(http.StatusOK),
			wantStatus:   http.StatusOK,
			wantRequests: 1,
		},
		{
			name:         "stale nonce is retried",
			respond:      staleFirst(1),
			wantStatus:   http.StatusOK,
			wantRequests: 2,
		},
		{
			name:         "non-stale 401 is not retried",
			respond:      alwaysStatusWithChallenge(http.StatusUnauthorized, freshChallenge),
			wantStatus:   http.StatusUnauthorized,
			wantRequests: 1,
		},
		{
			name:         "401 without challenge is not retried",
			respond:      alwaysStatus(http.StatusUnauthorized),
			wantStatus:   http.StatusUnauthorized,
			wantRequests: 1,
		},
		{
			name:         "POST body is replayed",
			respond:      staleFirst(1),
			method:       http.MethodPost,
			body:         "test body",
			wantStatus:   http.StatusOK,
			wantRequests: 2,
			wantBody:     "test body",
		},
		{
			name:         "POST body is replayed without GetBody",
			respond:      staleFirst(1),
			method:       http.MethodPost,
			body:         "test body",
			noGetBody:    true,
			wantStatus:   http.StatusOK,
			wantRequests: 2,
			wantBody:     "test body",
		},
		{
			name:         "consecutive stale nonces are retried up to the bound",
			respond:      staleFirst(maxStaleRetries),
			wantStatus:   http.StatusOK,
			wantRequests: maxStaleRetries + 1,
		},
		{
			name:         "retries stop at the bound",
			respond:      alwaysStatusWithChallenge(http.StatusUnauthorized, staleChallenge),
			wantStatus:   http.StatusUnauthorized,
			wantRequests: maxStaleRetries + 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requests int
			var lastBody string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				lastBody = string(body)

				status, challenge := tt.respond(requests)
				if challenge != "" {
					w.Header().Set("WWW-Authenticate", challenge)
				}
				w.WriteHeader(status)
			}))
			defer server.Close()

			method := tt.method
			if method == "" {
				method = http.MethodGet
			}
			var bodyReader io.Reader
			if tt.body != "" {
				bodyReader = strings.NewReader(tt.body)
				if tt.noGetBody {
					bodyReader = readerWithoutGetBody{bodyReader}
				}
			}
			req, err := http.NewRequest(method, server.URL, bodyReader)
			require.NoError(t, err)

			client := &http.Client{Transport: &staleRetryTransport{transport: http.DefaultTransport}}
			resp, err := client.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, tt.wantStatus, resp.StatusCode)
			assert.Equal(t, tt.wantRequests, requests)
			if tt.wantBody != "" {
				assert.Equal(t, tt.wantBody, lastBody)
			}
		})
	}
}

// TestStaleRetryDrainsDiscardedResponse checks that the 401 response body is read
// to EOF before it is closed, so the transport can reuse the connection.
func TestStaleRetryDrainsDiscardedResponse(t *testing.T) {
	discarded := &trackedBody{reader: strings.NewReader("stale challenge body")}
	attempts := 0
	transport := &staleRetryTransport{transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			return &http.Response{
				StatusCode: http.StatusUnauthorized,
				Header:     http.Header{"Www-Authenticate": []string{staleChallenge}},
				Body:       discarded,
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("ok")),
		}, nil
	})}

	req, err := http.NewRequest(http.MethodGet, "http://om.example.com/api/public/v1.0/groups", nil)
	require.NoError(t, err)

	resp, err := transport.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 2, attempts)
	assert.True(t, discarded.drained, "the discarded stale response body must be read to EOF")
	assert.True(t, discarded.closed, "the discarded stale response body must be closed")
}

// TestStaleRetryWithDigestAuth reproduces the Ops Manager upgrade sequence:
// the nonce expires between the challenge and the authenticated request, and
// the request only succeeds if the stale challenge triggers a new
// challenge-response cycle. It exercises the full transport stack built by
// NewHTTPClient, not just the stale retry transport on its own.
func TestStaleRetryWithDigestAuth(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		authorization := r.Header.Get("Authorization")

		switch {
		case authorization == "":
			// Every challenge carries a new nonce, as Ops Manager issues one per call.
			nonce := fmt.Sprintf("nonce-%d", requests.Load())
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Digest realm="test", nonce=%q, qop="auth"`, nonce))
			w.WriteHeader(http.StatusUnauthorized)
		case strings.Contains(authorization, `nonce="nonce-1"`):
			// The nonce expired while the AppDB restarted.
			w.Header().Set("WWW-Authenticate", `Digest realm="test", nonce="nonce-2", qop="auth", stale=true`)
			w.WriteHeader(http.StatusUnauthorized)
		case strings.Contains(authorization, `nonce="nonce-3"`):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		default:
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	defer server.Close()

	client, err := NewHTTPClient(OptionDigestAuth("user", "password"))
	require.NoError(t, err)

	body, _, err := client.Request(context.Background(), http.MethodGet, server.URL, "/api/public/v1.0/groups", nil)
	require.NoError(t, err)
	assert.Equal(t, "ok", string(body))
	// challenge, authenticated 401 stale, fresh challenge, authenticated 200
	assert.Equal(t, int32(4), requests.Load())
}

// TestDigestClientRedirectPolicy checks the redirect policy installed with the
// digest transport, which exists so a redirect cannot carry OM credentials to
// another host.
func TestDigestClientRedirectPolicy(t *testing.T) {
	t.Run("cross-host redirect is not followed", func(t *testing.T) {
		var targetRequests atomic.Int32
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			targetRequests.Add(1)
			w.WriteHeader(http.StatusOK)
		}))
		defer target.Close()

		source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL+r.URL.Path, http.StatusTemporaryRedirect)
		}))
		defer source.Close()

		client, err := NewHTTPClient(OptionDigestAuth("user", "password"))
		require.NoError(t, err)

		_, _, err = client.Request(context.Background(), http.MethodGet, source.URL, "/api/public/v1.0/groups", nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "307")
		assert.Equal(t, int32(0), targetRequests.Load(), "the redirect target must receive neither the request nor the credentials")
	})

	t.Run("same-host redirect is followed", func(t *testing.T) {
		var requests atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			if r.URL.Path == "/old" {
				http.Redirect(w, r, "/new", http.StatusTemporaryRedirect)
				return
			}
			if r.Header.Get("Authorization") == "" {
				w.Header().Set("WWW-Authenticate", `Digest realm="test", nonce="nonce-1", qop="auth"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		}))
		defer server.Close()

		client, err := NewHTTPClient(OptionDigestAuth("user", "password"))
		require.NoError(t, err)

		body, _, err := client.Request(context.Background(), http.MethodGet, server.URL, "/old", nil)
		require.NoError(t, err)
		assert.Equal(t, "ok", string(body))
		// the redirect, then the challenge and the authenticated request on the new path
		assert.Equal(t, int32(3), requests.Load())
	})

	t.Run("redirect limit is kept", func(t *testing.T) {
		client, err := NewHTTPClient(OptionDigestAuth("user", "password"))
		require.NoError(t, err)

		via := make([]*http.Request, 0, maxRedirects)
		for range maxRedirects {
			req, err := http.NewRequest(http.MethodGet, "http://om.example.com/api", nil)
			require.NoError(t, err)
			via = append(via, req)
		}
		next, err := http.NewRequest(http.MethodGet, "http://om.example.com/api", nil)
		require.NoError(t, err)

		// Nine followed redirects are allowed, the tenth is not.
		assert.NoError(t, client.HTTPClient.CheckRedirect(next, via[:maxRedirects-1]))
		assert.Error(t, client.HTTPClient.CheckRedirect(next, via))
	})
}

// alwaysStatus answers every request with the given status and no challenge.
func alwaysStatus(status int) func(int) (int, string) {
	return func(int) (int, string) { return status, "" }
}

// alwaysStatusWithChallenge answers every request with the given status and challenge.
func alwaysStatusWithChallenge(status int, challenge string) func(int) (int, string) {
	return func(int) (int, string) { return status, challenge }
}

// staleFirst answers the first n requests with a stale nonce challenge and every
// later request with 200.
func staleFirst(n int) func(int) (int, string) {
	return func(request int) (int, string) {
		if request <= n {
			return http.StatusUnauthorized, staleChallenge
		}
		return http.StatusOK, ""
	}
}
