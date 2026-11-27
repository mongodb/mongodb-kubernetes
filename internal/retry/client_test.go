package retry

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
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
)

func immediateClient(retries int) *Client {
	return &Client{
		RetryMax: retries,
		Backoff:  func(time.Duration, time.Duration, int, *http.Response) time.Duration { return 0 },
	}
}

func TestRequestConstructorsAndBodyReplay(t *testing.T) {
	tests := []struct {
		name string
		body any
	}{
		{"string", "payload"},
		{"bytes", []byte("payload")},
		{"byte reader", bytes.NewReader([]byte("payload"))},
		{"string reader", strings.NewReader("payload")},
		{"buffer", bytes.NewBufferString("payload")},
		{"generic reader", strings.NewReader("payload")},
		{"read seeker", &testReadSeeker{Reader: *bytes.NewReader([]byte("payload"))}},
		{"reader func", ReaderFunc(func() (io.Reader, error) { return strings.NewReader("payload"), nil })},
		{"plain reader func", func() (io.Reader, error) { return strings.NewReader("payload"), nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := NewRequest(http.MethodPost, "http://example.test", tt.body)
			if err != nil {
				t.Fatal(err)
			}
			if req.GetBody == nil {
				t.Fatal("constructor did not make body replayable")
			}
			if req.ContentLength != int64(len("payload")) {
				t.Fatalf("ContentLength = %d", req.ContentLength)
			}
			var bodies []string
			client := immediateClient(1)
			client.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				body, _ := io.ReadAll(r.Body)
				bodies = append(bodies, string(body))
				if len(bodies) == 1 {
					return response(503), nil
				}
				return response(200), nil
			})}
			resp, err := client.Do(req)
			if err != nil || resp.StatusCode != 200 {
				t.Fatalf("resp=%v err=%v", resp, err)
			}
			if len(bodies) != 2 || bodies[0] != "payload" || bodies[1] != "payload" {
				t.Fatalf("body attempts = %q", bodies)
			}
		})
	}
	// A factory is invoked once and snapshotted, then replayed byte-for-byte.
	factoryCalls := 0
	factoryReq, err := NewRequest(http.MethodPost, "http://example.test", ReaderFunc(func() (io.Reader, error) {
		factoryCalls++
		return strings.NewReader("snapshot"), nil
	}))
	if err != nil || factoryCalls != 1 {
		t.Fatalf("ReaderFunc construction calls=%d err=%v", factoryCalls, err)
	}
	if body, _ := io.ReadAll(factoryReq.Body); string(body) != "snapshot" {
		t.Fatalf("initial factory body = %q", body)
	}
	replay, err := factoryReq.GetBody()
	if err != nil {
		t.Fatal(err)
	}
	if body, _ := io.ReadAll(replay); string(body) != "snapshot" {
		t.Fatalf("replayed factory body = %q", body)
	}

	if _, err := NewRequest(http.MethodPost, "http://example.test", nil); err != nil {
		t.Fatal(err)
	}

	ctx := t.Context()
	withCtx, err := NewRequestWithContext(ctx, http.MethodGet, "http://example.test", nil)
	if err != nil || withCtx.Context() != ctx {
		t.Fatalf("context constructor: req=%v err=%v", withCtx, err)
	}
	original, _ := http.NewRequest(http.MethodGet, "http://example.test", nil)
	wrapped, err := FromRequest(original)
	if err != nil || wrapped.Request != original {
		t.Fatalf("FromRequest did not preserve request: %v", err)
	}
	if _, err = FromRequest(nil); err == nil {
		t.Fatal("FromRequest(nil) should fail")
	}
}

func TestClientHooksAttemptNumberAndRequestScopedHooks(t *testing.T) {
	var clientAttempts, requestAttempts, responses []int
	c := immediateClient(2)
	c.RequestLogHook = func(_ Logger, _ *http.Request, n int) { clientAttempts = append(clientAttempts, n) }
	c.ResponseLogHook = func(_ Logger, resp *http.Response) {
		if resp != nil {
			responses = append(responses, resp.StatusCode)
		}
	}
	c.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		if len(responses) < 2 {
			return response(500), nil
		}
		return response(200), nil
	})}
	req, _ := NewRequest(http.MethodGet, "http://example.test", nil)
	req.RequestLogHook = func(_ Logger, _ *http.Request, n int) { requestAttempts = append(requestAttempts, n) }
	resp, err := c.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("resp=%v err=%v", resp, err)
	}
	if fmt.Sprint(requestAttempts) != "[0 1 2]" {
		t.Fatalf("request scoped attempts = %v", requestAttempts)
	}
	if len(clientAttempts) != 0 {
		t.Fatalf("client hook should be overridden per request: %v", clientAttempts)
	}
	if fmt.Sprint(responses) != "[500 500 200]" {
		t.Fatalf("response hook statuses = %v", responses)
	}
}

func TestDefaultPolicyStatusAndErrorCases(t *testing.T) {
	for _, status := range []int{0, 429, 500, 502, 503, 599} {
		resp := &http.Response{StatusCode: status, Status: fmt.Sprintf("%d status", status)}
		retry, err := DefaultRetryPolicy(context.Background(), resp, nil)
		if !retry || ((status == 429) != (err == nil)) {
			t.Errorf("status %d: retry=%v err=%v", status, retry, err)
		}
	}
	for _, status := range []int{200, 400, 404, 501} {
		if retry, err := DefaultRetryPolicy(context.Background(), &http.Response{StatusCode: status}, nil); retry || err != nil {
			t.Errorf("status %d: retry=%v err=%v", status, retry, err)
		}
	}
	for _, err := range []error{
		context.Canceled,
		fmt.Errorf("wrapped: %w", context.DeadlineExceeded),
		&url.Error{Op: "Get", URL: "x://host", Err: errors.New("unsupported protocol scheme")},
		errors.New("net/http: invalid header field value"),
		&url.Error{Op: "Get", URL: "https://host", Err: errors.New("stopped after 10 redirects")},
		x509.UnknownAuthorityError{},
	} {
		if retry, _ := DefaultRetryPolicy(context.Background(), nil, err); retry {
			t.Errorf("should not retry %T: %v", err, err)
		}
	}
	if retry, _ := DefaultRetryPolicy(context.Background(), nil, errors.New("connection reset")); !retry {
		t.Fatal("transient transport error was not retried")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if retry, _ := DefaultRetryPolicy(ctx, nil, errors.New("connection reset")); retry {
		t.Fatal("canceled context should not retry")
	}
}

func TestDefaultBackoffConventionAndRetryAfter(t *testing.T) {
	if got := DefaultBackoff(time.Second, time.Minute, 0, nil); got != time.Second {
		t.Fatalf("attempt zero = %v", got)
	}
	if got := DefaultBackoff(time.Second, time.Minute, 2, nil); got != 4*time.Second {
		t.Fatalf("attempt two = %v", got)
	}
	if got := DefaultBackoff(0, 0, 2, nil); got != 0 {
		t.Fatalf("zero backoff = %v, want zero", got)
	}
	for _, status := range []int{429, 503} {
		resp := response(status)
		resp.Header.Set("Retry-After", "7")
		if got := DefaultBackoff(time.Second, time.Minute, 0, resp); got != 7*time.Second {
			t.Errorf("status %d ignored Retry-After: %v", status, got)
		}
	}
	resp := response(503)
	resp.Header.Set("Retry-After", "120")
	if got := DefaultBackoff(time.Second, time.Minute, 0, resp); got != 120*time.Second {
		t.Fatalf("Retry-After was capped, got %v", got)
	}
	resp = response(500)
	resp.Header.Set("Retry-After", "7")
	if got := DefaultBackoff(time.Second, time.Minute, 0, resp); got != time.Second {
		t.Fatalf("unexpected Retry-After for 500: %v", got)
	}
}

func TestClientExhaustionAndDrainsRetryResponse(t *testing.T) {
	var attempts atomic.Int32
	var closed atomic.Bool
	client := immediateClient(2)
	client.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		attempts.Add(1)
		return &http.Response{StatusCode: 503, Header: make(http.Header), Body: closeTracker{Reader: strings.NewReader("body"), closed: &closed}}, nil
	})}
	resp, err := client.Do(&Request{Request: mustRequest(t)})
	if resp != nil || err == nil || !strings.Contains(err.Error(), "giving up after 3 attempt(s)") || attempts.Load() != 3 {
		t.Fatalf("resp=%v err=%v attempts=%d", resp, err, attempts.Load())
	}
	if !closed.Load() {
		t.Fatal("response body was not closed on exhaustion")
	}
}

func TestClientCheckRetryErrorAndFinalTransportError(t *testing.T) {
	policyErr := errors.New("policy failed")
	client := immediateClient(3)
	client.CheckRetry = func(context.Context, *http.Response, error) (bool, error) { return false, policyErr }
	client.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return response(503), nil })}
	resp, err := client.Do(&Request{Request: mustRequest(t)})
	if !errors.Is(err, policyErr) || resp != nil || !strings.Contains(err.Error(), "giving up after 1 attempt(s)") {
		t.Fatalf("policy error result: resp=%v err=%v", resp, err)
	}

	// A nonretryable status remains an ordinary response.
	client.CheckRetry = DefaultRetryPolicy
	client.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return response(404), nil })}
	resp, err = client.Do(&Request{Request: mustRequest(t)})
	if err != nil || resp == nil || resp.StatusCode != 404 {
		t.Fatalf("nonretryable status result: resp=%v err=%v", resp, err)
	}

	wantErr := errors.New("connection reset")
	client = immediateClient(1)
	client.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, wantErr })}
	resp, err = client.Do(&Request{Request: mustRequest(t)})
	if !errors.Is(err, wantErr) || resp != nil {
		t.Fatalf("transport exhaustion: resp=%v err=%v", resp, err)
	}
}

func TestClientNonretryableTransportErrorIsExhausted(t *testing.T) {
	wantErr := errors.New("connection reset")
	client := immediateClient(3)
	client.CheckRetry = func(context.Context, *http.Response, error) (bool, error) {
		return false, nil
	}
	client.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, wantErr
	})}

	resp, err := client.Do(&Request{Request: mustRequest(t)})
	if resp != nil || !errors.Is(err, wantErr) || !strings.Contains(err.Error(), "giving up after 1 attempt(s)") {
		t.Fatalf("resp=%v err=%v", resp, err)
	}
}

func TestExplicitZeroWaitsAndResponseHooksOnlyOnResponses(t *testing.T) {
	var minSeen, maxSeen time.Duration
	c := &Client{RetryMax: 1, CheckRetry: func(_ context.Context, resp *http.Response, err error) (bool, error) {
		return err != nil || (resp != nil && resp.StatusCode >= 500), nil
	}}
	c.Backoff = func(min, max time.Duration, _ int, _ *http.Response) time.Duration {
		minSeen, maxSeen = min, max
		return 0
	}
	var responseHooks atomic.Int32
	c.ResponseLogHook = func(Logger, *http.Response) { responseHooks.Add(1) }
	var calls atomic.Int32
	c.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			return nil, errors.New("connection reset")
		}
		return response(200), nil
	})}
	req, _ := NewRequest(http.MethodGet, "http://example.test", nil)
	if _, err := c.Do(req); err != nil {
		t.Fatal(err)
	}
	if minSeen != 0 || maxSeen != 0 {
		t.Fatalf("struct literal delays normalized: min=%v max=%v", minSeen, maxSeen)
	}
	if responseHooks.Load() != 1 {
		t.Fatalf("response hook calls=%d; transport errors must not invoke it", responseHooks.Load())
	}
	defaults := NewClient()
	if defaults.RetryWaitMin != time.Second || defaults.RetryWaitMax != 30*time.Second {
		t.Fatalf("NewClient delays = %v/%v", defaults.RetryWaitMin, defaults.RetryWaitMax)
	}
}

func TestRoundTripperAndClientConcurrentHooks(t *testing.T) {
	var hookCalls atomic.Int32
	var rounds atomic.Int32
	client := immediateClient(1)
	client.RequestLogHook = func(_ Logger, _ *http.Request, attempt int) {
		if attempt == 0 {
			hookCalls.Add(1)
		}
	}
	client.HTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		rounds.Add(1)
		if req.Header.Get("X-Retried") == "" {
			req.Header.Set("X-Retried", "yes")
			return response(500), nil
		}
		return response(200), nil
	})}
	rt := &RoundTripper{Client: client}
	const count = 8
	var wg sync.WaitGroup
	for range count {
		wg.Go(func() {
			r, _ := http.NewRequest(http.MethodGet, "http://example.test", nil)
			resp, err := rt.RoundTrip(r)
			if err != nil || resp.StatusCode != 200 {
				t.Errorf("round trip: resp=%v err=%v", resp, err)
			}
		})
	}
	wg.Wait()
	if hookCalls.Load() != count {
		t.Fatalf("initial hooks = %d, want %d", hookCalls.Load(), count)
	}
}

func TestRoundTripperWithoutClientUsesDefaultRetryBehavior(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	req, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&RoundTripper{}).RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("request attempts = %d, want 2", got)
	}
}

func TestNoBodyWithoutGetBodyIsRetried(t *testing.T) {
	for _, useRoundTripper := range []bool{false, true} {
		name := "Client"
		if useRoundTripper {
			name = "RoundTripper"
		}
		t.Run(name, func(t *testing.T) {
			var attempts atomic.Int32
			client := immediateClient(1)
			client.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				if attempts.Add(1) == 1 {
					return response(http.StatusServiceUnavailable), nil
				}
				return response(http.StatusOK), nil
			})}
			req := mustRequest(t)
			req.Body = http.NoBody
			req.GetBody = nil

			var resp *http.Response
			var err error
			if useRoundTripper {
				resp, err = (&RoundTripper{Client: client}).RoundTrip(req)
			} else {
				var wrapped *Request
				wrapped, err = FromRequest(req)
				if err == nil {
					resp, err = client.Do(wrapped)
				}
			}
			if err != nil || resp == nil || resp.StatusCode != http.StatusOK {
				t.Fatalf("resp=%v err=%v", resp, err)
			}
			_ = resp.Body.Close()
			if got := attempts.Load(); got != 2 {
				t.Fatalf("request attempts = %d, want 2", got)
			}
		})
	}
}

func TestClientContextCancellationDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := immediateClient(2)
	client.Backoff = func(time.Duration, time.Duration, int, *http.Response) time.Duration {
		cancel()
		return time.Hour
	}
	client.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return response(500), nil })}
	req, _ := NewRequestWithContext(ctx, http.MethodGet, "http://example.test", nil)
	_, err := client.Do(req)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}

func TestRoundTripperClientHonorsCheckRedirect(t *testing.T) {
	redirectErr := errors.New("redirect rejected by retry client")
	var attempts atomic.Int32
	client := immediateClient(0)
	client.HTTPClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			attempts.Add(1)
			return &http.Response{
				StatusCode: http.StatusFound,
				Header:     http.Header{"Location": []string{"/redirect"}},
				Body:       io.NopCloser(strings.NewReader("")),
				Request:    req,
			}, nil
		}),
		CheckRedirect: func(*http.Request, []*http.Request) error { return redirectErr },
	}
	outerClient := &http.Client{Transport: &RoundTripper{Client: client}}

	_, err := outerClient.Get("http://example.test/start")
	if !errors.Is(err, redirectErr) {
		t.Fatalf("RoundTripper error = %v, want wrapped redirect error", err)
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("transport attempts = %d, want 1", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func response(status int) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("body"))}
}

func TestDrainFullyBoundsBodyAndCloses(t *testing.T) {
	const bodySize = 16 << 10
	reader := strings.NewReader(strings.Repeat("x", bodySize))
	closed := &atomic.Bool{}
	resp := &http.Response{Body: closeTracker{Reader: reader, closed: closed}}

	drainFully(resp)

	if consumed := bodySize - reader.Len(); consumed != 4<<10 {
		t.Fatalf("consumed %d bytes, want %d", consumed, 4<<10)
	}
	if !closed.Load() {
		t.Fatal("response body was not closed")
	}
}

func TestDrainFullyNilBodyIsSafe(t *testing.T) {
	drainFully(nil)
	drainFully(&http.Response{})
}

func mustRequest(t *testing.T) *http.Request {
	t.Helper()
	r, err := http.NewRequest(http.MethodGet, "http://example.test", nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

type closeTracker struct {
	io.Reader
	closed *atomic.Bool
}

func (c closeTracker) Close() error {
	c.closed.Store(true)
	return nil
}

type testReadSeeker struct{ bytes.Reader }

func (r *testReadSeeker) Seek(offset int64, whence int) (int64, error) {
	return r.Reader.Seek(offset, whence)
}
