package retry

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

type attemptTracker struct {
	attempts      int
	responses     []*http.Response // response to return per attempt (last repeats)
	errs          []error          // error to return per attempt (last repeats)
	requestBodies []string         // body seen by each attempt
}

func (t *attemptTracker) send(req *http.Request) (*http.Response, error) {
	i := t.attempts
	t.attempts++
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		_ = req.Body.Close()
		t.requestBodies = append(t.requestBodies, string(b))
	} else {
		t.requestBodies = append(t.requestBodies, "")
	}
	var resp *http.Response
	var err error
	if i < len(t.responses) {
		resp = t.responses[i]
	} else if len(t.responses) > 0 {
		resp = t.responses[len(t.responses)-1]
	}
	if i < len(t.errs) {
		err = t.errs[i]
	} else if len(t.errs) > 0 {
		err = t.errs[len(t.errs)-1]
	}
	return resp, err
}

func textResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func newRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "http://example.com", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(body)), nil
	}
	return req
}

func TestSendRetriesStatusThenSucceeds(t *testing.T) {
	tr := &attemptTracker{responses: []*http.Response{
		textResponse(500, "boom"),
		textResponse(429, "slow down"),
		textResponse(200, "ok"),
	}}
	var hooks []int
	cfg := Config{MaxRetries: 3, MinDelay: time.Millisecond, Hook: func(a int, err error, resp *http.Response) {
		hooks = append(hooks, a)
	}}

	resp, err := Send(context.Background(), newRequest(t, "hi"), cfg, tr.send)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != 200 || tr.attempts != 3 {
		t.Fatalf("got status %d after %d attempts", resp.StatusCode, tr.attempts)
	}
	if len(hooks) != 2 || hooks[0] != 1 || hooks[1] != 2 {
		t.Fatalf("hooks = %v", hooks)
	}
	if got := tr.requestBodies[2]; got != "hi" {
		t.Fatalf("body not replayed: %q", got)
	}
}

func TestSendNoRetryOnOther4xx(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404} {
		tr := &attemptTracker{responses: []*http.Response{textResponse(status, "nope")}}
		resp, err := Send(context.Background(), newRequest(t, ""), Config{MinDelay: time.Millisecond}, tr.send)
		if err != nil || resp.StatusCode != status || tr.attempts != 1 {
			t.Fatalf("status %d: attempts=%d err=%v", status, tr.attempts, err)
		}
	}
}

func TestSendExhaustedReturnsResponseNotError(t *testing.T) {
	tr := &attemptTracker{responses: []*http.Response{textResponse(503, "down")}}
	resp, err := Send(context.Background(), newRequest(t, ""), Config{MaxRetries: 2, MinDelay: time.Millisecond}, tr.send)
	if err != nil {
		t.Fatalf("want response, got error: %v", err)
	}
	if resp.StatusCode != 503 || tr.attempts != 3 {
		t.Fatalf("status %d attempts %d", resp.StatusCode, tr.attempts)
	}
}

func TestSendFinalTransportErrorReturned(t *testing.T) {
	wantErr := errors.New("connection refused")
	tr := &attemptTracker{errs: []error{wantErr}}
	resp, err := Send(context.Background(), newRequest(t, ""), Config{MaxRetries: 2, MinDelay: time.Millisecond}, tr.send)
	if !errors.Is(err, wantErr) || resp != nil {
		t.Fatalf("got resp=%v err=%v", resp, err)
	}
	if tr.attempts != 3 {
		t.Fatalf("attempts = %d", tr.attempts)
	}
}

func TestSendReplayableBody(t *testing.T) {
	tr := &attemptTracker{responses: []*http.Response{
		textResponse(500, "boom"),
		textResponse(200, "ok"),
	}}
	req := newRequest(t, "payload")
	resp, err := Send(context.Background(), req, Config{MaxRetries: 3, MinDelay: time.Millisecond}, tr.send)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("resp=%v err=%v", resp, err)
	}
	if len(tr.requestBodies) != 2 || tr.requestBodies[0] != "payload" || tr.requestBodies[1] != "payload" {
		t.Fatalf("bodies = %v", tr.requestBodies)
	}
}

func TestSendNonReplayableBodyNotRetried(t *testing.T) {
	tr := &attemptTracker{responses: []*http.Response{
		textResponse(500, "boom"),
		textResponse(200, "ok"),
	}}
	// io.NopCloser prevents http.NewRequest from auto-setting GetBody.
	req, _ := http.NewRequest(http.MethodPost, "http://example.com", io.NopCloser(strings.NewReader("once")))
	resp, err := Send(context.Background(), req, Config{MaxRetries: 3, MinDelay: time.Millisecond}, tr.send)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != 500 || tr.attempts != 1 {
		t.Fatalf("status %d attempts %d", resp.StatusCode, tr.attempts)
	}
}

func TestSendGetBodyErrorNotRetried(t *testing.T) {
	tr := &attemptTracker{responses: []*http.Response{textResponse(500, "boom")}}
	req, _ := http.NewRequest(http.MethodPost, "http://example.com", strings.NewReader("x"))
	req.GetBody = func() (io.ReadCloser, error) { return nil, errors.New("cannot rewind") }
	resp, err := Send(context.Background(), req, Config{MaxRetries: 3, MinDelay: time.Millisecond}, tr.send)
	if err != nil || resp.StatusCode != 500 || tr.attempts != 1 {
		t.Fatalf("resp=%v err=%v attempts=%d", resp, err, tr.attempts)
	}
}

func TestSendRetryAfterHeader(t *testing.T) {
	tr := &attemptTracker{responses: []*http.Response{
		func() *http.Response {
			r := textResponse(429, "later")
			r.Header.Set("Retry-After", "0")
			return r
		}(),
		textResponse(200, "ok"),
	}}
	// A large Retry-After would normally take long; with value 0 the wait is 0.
	start := time.Now()
	resp, err := Send(context.Background(), newRequest(t, ""), Config{MaxRetries: 3, MinDelay: time.Hour, MaxDelay: time.Hour}, tr.send)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("resp=%v err=%v", resp, err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("Retry-After not respected (waited too long)")
	}
}

func TestExponentialBackoffRetryAfter(t *testing.T) {
	resp := textResponse(503, "")
	resp.Header.Set("Retry-After", "7")
	if got := ExponentialBackoff(1, time.Second, time.Minute, resp); got != 7*time.Second {
		t.Fatalf("Retry-After ignored: %v", got)
	}
	if got := ExponentialBackoff(3, time.Second, time.Minute, nil); got != 4*time.Second {
		t.Fatalf("exponential: %v", got)
	}
	if got := ExponentialBackoff(10, time.Second, time.Minute, nil); got != time.Minute {
		t.Fatalf("cap: %v", got)
	}
}

func TestLinearJitterBackoff(t *testing.T) {
	resp := textResponse(503, "")
	resp.Header.Set("Retry-After", "100")
	for attempt, wantMax := range map[int]time.Duration{1: 1500 * time.Millisecond, 3: 4500 * time.Millisecond} {
		got := LinearJitterBackoff(attempt, time.Second, time.Hour, resp)
		if got <= 0 || got > wantMax {
			t.Fatalf("attempt %d: got %v, want (0,%v]", attempt, got, wantMax)
		}
	}
	if got := LinearJitterBackoff(100, time.Second, 3*time.Second, nil); got != 3*time.Second {
		t.Fatalf("cap: %v", got)
	}
	// Non-positive delay range: no jitter, capped as usual.
	if got := LinearJitterBackoff(1, 0, time.Hour, nil); got != 0 {
		t.Fatalf("zero base delay: %v", got)
	}
	if got := LinearJitterBackoff(2, -time.Second, time.Hour, nil); got != 0 {
		t.Fatalf("negative base delay: %v", got)
	}
}

func TestSendContextCanceledDuringWait(t *testing.T) {
	tr := &attemptTracker{responses: []*http.Response{textResponse(500, "boom")}}
	ctx, cancel := context.WithCancel(context.Background())
	cfg := Config{MaxRetries: 5, MinDelay: time.Hour, Hook: func(_ int, _ error, _ *http.Response) {
		cancel()
	}}
	start := time.Now()
	_, err := Send(ctx, newRequest(t, ""), cfg, tr.send)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("wait did not stop promptly")
	}
}

func TestSendDefaultPolicyDoesNotRetryContextCancellation(t *testing.T) {
	tr := &attemptTracker{errs: []error{context.Canceled}}
	_, err := Send(context.Background(), newRequest(t, ""), Config{MaxRetries: 3, MinDelay: time.Millisecond}, tr.send)
	if !errors.Is(err, context.Canceled) || tr.attempts != 1 {
		t.Fatalf("attempts=%d err=%v", tr.attempts, err)
	}
}

func TestDefaultPolicyDoesNotRetryWrappedContextErrors(t *testing.T) {
	wrappedCanceled := fmt.Errorf("send failed: %w", context.Canceled)
	wrappedDeadline := fmt.Errorf("send failed: %w", context.DeadlineExceeded)
	if DefaultPolicy(wrappedCanceled, nil) {
		t.Fatal("wrapped context.Canceled should not be retried")
	}
	if DefaultPolicy(wrappedDeadline, nil) {
		t.Fatal("wrapped context.DeadlineExceeded should not be retried")
	}
	if !DefaultPolicy(errors.New("connection refused"), nil) {
		t.Fatal("plain transport error should be retried")
	}
}

func TestSendWrappedContextCancellationNotRetried(t *testing.T) {
	tr := &attemptTracker{errs: []error{fmt.Errorf("do failed: %w", context.Canceled)}}
	_, err := Send(context.Background(), newRequest(t, ""), Config{MaxRetries: 3, MinDelay: time.Millisecond}, tr.send)
	if !errors.Is(err, context.Canceled) || tr.attempts != 1 {
		t.Fatalf("attempts=%d err=%v", tr.attempts, err)
	}
}

func TestDelaysZeroConfigStaysZero(t *testing.T) {
	minDelay, maxDelay := (&Config{}).delays()
	if minDelay != 0 || maxDelay != 0 {
		t.Fatalf("want zero delays, got min=%v max=%v", minDelay, maxDelay)
	}
}

func TestSendZeroDelayRetriesImmediately(t *testing.T) {
	tr := &attemptTracker{responses: []*http.Response{
		textResponse(500, "boom"),
		textResponse(200, "ok"),
	}}
	start := time.Now()
	resp, err := Send(context.Background(), newRequest(t, ""), Config{MaxRetries: 2}, tr.send)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("resp=%v err=%v", resp, err)
	}
	if tr.attempts != 2 {
		t.Fatalf("attempts = %d", tr.attempts)
	}
	if time.Since(start) > time.Second {
		t.Fatal("zero-delay retry waited")
	}
}

func TestDelaysMinDelayPositiveMaxDelayZeroCaps(t *testing.T) {
	minDelay, maxDelay := (&Config{MinDelay: 500 * time.Millisecond}).delays()
	if minDelay != 500*time.Millisecond || maxDelay != 5*time.Second {
		t.Fatalf("got min=%v max=%v", minDelay, maxDelay)
	}
}

func TestSendViaRoundTripper(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Attempt") == "" {
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	attempt := 0
	rt := RoundTripperSend(http.DefaultTransport)
	send := func(req *http.Request) (*http.Response, error) {
		attempt++
		req.Header.Set("X-Attempt", strconv.Itoa(attempt))
		return rt(req)
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := Send(context.Background(), req, Config{MinDelay: time.Millisecond}, send)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("resp=%v err=%v", resp, err)
	}
}

func TestIsReplayable(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "http://example.com", nil)
	if !IsReplayable(req) {
		t.Fatal("bodyless request should be replayable")
	}
	req.Body = io.NopCloser(bytes.NewReader([]byte("x")))
	if IsReplayable(req) {
		t.Fatal("body without GetBody should not be replayable")
	}
	req.GetBody = func() (io.ReadCloser, error) { return nil, nil }
	if !IsReplayable(req) {
		t.Fatal("body with GetBody should be replayable")
	}
}
