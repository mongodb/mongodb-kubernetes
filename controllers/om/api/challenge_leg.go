package api

import "net/http"

// challengeLegTransport drops the request body from the unauthenticated request
// that digest.Transport sends to obtain a 401 challenge.
//
// Ops Manager rejects an unauthenticated request before it reads the body and
// closes the connection. A body still in flight then fails with
// "write: broken pipe". digest.Transport marks the challenge leg only by the
// absence of an Authorization header, so that header identifies the leg to
// rewrite. The authenticated request keeps its body.
type challengeLegTransport struct {
	transport http.RoundTripper
}

func (t *challengeLegTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Header.Get("Authorization") != "" || req.Body == nil || req.Body == http.NoBody {
		return t.transport.RoundTrip(req)
	}

	challenge := req.Clone(req.Context())
	challenge.Body = http.NoBody
	challenge.ContentLength = 0
	challenge.GetBody = nil
	return t.transport.RoundTrip(challenge)
}
