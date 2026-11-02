package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// challengeLegBodySize exceeds the loopback socket buffers, so an unread body
// cannot be absorbed by the kernel before the server closes the connection.
const challengeLegBodySize = 4 << 20

// challengeLegResponse is a complete 401 challenge reply, written on a hijacked
// connection so the handler can close it without reading the request body.
const challengeLegResponse = "HTTP/1.1 401 Unauthorized\r\n" +
	`WWW-Authenticate: Digest realm="test", nonce="abc", qop="auth", algorithm=MD5` + "\r\n" +
	"Content-Length: 0\r\n" +
	"Connection: close\r\n" +
	"\r\n"

// challengeLegServer records what each leg of the digest exchange carried.
type challengeLegServer struct {
	srv *httptest.Server

	mu                 sync.Mutex
	challengeRequests  int
	challengeLength    int64
	authedRequests     int
	authenticatedBytes int64
}

func newChallengeLegServer(t *testing.T) *challengeLegServer {
	t.Helper()

	s := &challengeLegServer{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			s.mu.Lock()
			s.challengeRequests++
			s.challengeLength = r.ContentLength
			s.mu.Unlock()

			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Errorf("response writer does not support hijacking")
				return
			}
			conn, buf, err := hijacker.Hijack()
			if err != nil {
				t.Errorf("hijack connection: %v", err)
				return
			}
			if _, err = buf.WriteString(challengeLegResponse); err == nil {
				err = buf.Flush()
			}
			if err != nil {
				t.Errorf("write challenge: %v", err)
			}
			_ = conn.Close()
			return
		}

		n, err := io.Copy(io.Discard, r.Body)
		if err != nil {
			t.Errorf("read authenticated body: %v", err)
		}
		s.mu.Lock()
		s.authedRequests++
		s.authenticatedBytes = n
		s.mu.Unlock()

		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(s.srv.Close)

	return s
}

func TestChallengeLegDropsBody(t *testing.T) {
	s := newChallengeLegServer(t)

	client, err := NewHTTPClient(OptionDigestAuth("u", "p"))
	require.NoError(t, err)

	payload := struct {
		Data string `json:"data"`
	}{Data: strings.Repeat("x", challengeLegBodySize)}
	serialized, err := json.Marshal(payload)
	require.NoError(t, err)

	_, _, err = client.Request(context.Background(), http.MethodPut, s.srv.URL, "/x", payload)
	require.NoError(t, err)

	s.mu.Lock()
	defer s.mu.Unlock()
	require.Equal(t, 1, s.challengeRequests, "expected exactly one challenge request")
	require.Equal(t, 1, s.authedRequests, "expected exactly one authenticated request")
	assert.Zero(t, s.challengeLength, "the challenge request must not announce a body")
	assert.Equal(t, int64(len(serialized)), s.authenticatedBytes, "the authenticated request must carry the full body")
}
