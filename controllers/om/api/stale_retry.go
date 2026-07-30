package api

import (
	"bytes"
	"io"
	"net/http"
	"strings"
)

// maxStaleRetries is the maximum number of times RoundTrip will retry after
// receiving a 401 with stale=true (RFC 7616 §3.2). A bound prevents infinite
// loops against a misconfigured server that always returns stale=true.
const maxStaleRetries = 3

// staleRetryTransport wraps an http.RoundTripper and retries requests when
// the server returns 401 with stale=true in the WWW-Authenticate header
// (RFC 7616 §3.2). A stale nonce means the credentials are valid but the
// nonce has expired; the client should retry with a fresh challenge.
type staleRetryTransport struct {
	transport  http.RoundTripper
	maxRetries int
}

func (t *staleRetryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Buffer the request body so it can be replayed on each retry.
	// ponytail: triple body buffering (retryablehttp + here + digest.Transport),
	// bodies are <10KB JSON to OM API, not worth optimizing.
	var bodyBytes []byte
	if req.Body != nil {
		var err error
		bodyBytes, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
	}

	for attempt := 0; ; attempt++ {
		if bodyBytes != nil {
			req.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
		}

		resp, err := t.transport.RoundTrip(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusUnauthorized {
			return resp, nil
		}
		if !isStaleNonce(resp.Header.Get("WWW-Authenticate")) {
			return resp, nil
		}
		if attempt >= t.maxRetries {
			return resp, nil
		}
		_ = resp.Body.Close()
	}
}

// isStaleNonce checks whether the WWW-Authenticate header indicates a
// stale nonce (RFC 7616 §3.2).
func isStaleNonce(wwwAuth string) bool {
	return strings.Contains(strings.ToLower(wwwAuth), "stale=true")
}
