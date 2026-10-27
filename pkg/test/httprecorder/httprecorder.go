// Package httprecorder provides a test HTTP server that records the raw request-target of the
// requests it receives, for tests asserting on how a client builds its URLs — typically that
// untrusted input spliced into a path or query stays escaped and cannot retarget the request.
package httprecorder

import (
	"net/http"
	"net/http/httptest"
	"sync"
)

// RequestInfo is what the recorder captured from a single request.
type RequestInfo struct {
	Method     string
	Path       string // r.URL.Path (percent-decoded by the server)
	RawQuery   string // r.URL.RawQuery
	RequestURI string // raw request-target exactly as received on the wire
	Count      int    // number of requests recorded so far
}

// Recorder captures the most recent request seen by a handler. It is safe for concurrent use.
type Recorder struct {
	mu   sync.Mutex
	last RequestInfo
}

// Record stores the request details, overwriting the previous ones and incrementing the count.
// It is exported so that tests needing their own handler can still reuse the recorder.
func (rec *Recorder) Record(r *http.Request) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.last.Count++
	rec.last.Method = r.Method
	rec.last.Path = r.URL.Path
	rec.last.RawQuery = r.URL.RawQuery
	rec.last.RequestURI = r.RequestURI
}

// Last returns the most recently recorded request.
func (rec *Recorder) Last() RequestInfo {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return rec.last
}

// NewServer starts a test server that records every request and replies with the given status and
// body. It deliberately uses a bare http.HandlerFunc (not http.ServeMux) so that path traversal
// ("..") and escaped separators are observed exactly as sent, without the mux's path-cleaning and
// redirects masking the injection. The caller is responsible for closing the server.
func NewServer(status int, body []byte) (*httptest.Server, *Recorder) {
	rec := &Recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.Record(r)
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	return srv, rec
}
