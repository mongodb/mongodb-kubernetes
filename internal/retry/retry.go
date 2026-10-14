// Package retry provides a small, dependency-free HTTP request retry loop.
//
// The retry loop can be driven either by a RoundTripper (for installation as
// an http.Client transport) or by a per-attempt send function (so callers can
// use http.Client.Do and retain its per-attempt timeout).
package retry

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"time"
)

// Policy decides whether a failed attempt should be retried.
type Policy interface {
	// ShouldRetry reports whether the attempt should be retried. Exactly one
	// of err or resp is non-nil.
	ShouldRetry(err error, resp *http.Response) bool
}

// PolicyFunc is a function implementing Policy.
type PolicyFunc func(err error, resp *http.Response) bool

// ShouldRetry implements Policy.
func (f PolicyFunc) ShouldRetry(err error, resp *http.Response) bool {
	return f(err, resp)
}

// DefaultPolicy retries transport errors (except context cancellation and
// deadline exceeded, including when wrapped), HTTP 429 and HTTP 5xx responses.
func DefaultPolicy(err error, resp *http.Response) bool {
	if err != nil {
		return !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
	}
	return resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
}

// Config configures the retry loop. All fields are optional; zero values
// yield sensible defaults.
type Config struct {
	// MaxRetries is the number of retries after the initial attempt.
	// Negative values are treated as 0. Defaults to 0.
	MaxRetries int
	// MinDelay is the delay before the first retry. Defaults to 1s, unless
	// both MinDelay and MaxDelay are 0, in which case retries happen
	// immediately with no delay (explicit zero-delay retries).
	MinDelay time.Duration
	// MaxDelay caps the computed delay. Defaults to 10 * MinDelay when
	// non-positive (and not both-zero zero-delay mode as described above).
	MaxDelay time.Duration
	// Policy decides which attempts are retryable. Defaults to DefaultPolicy.
	Policy Policy
	// Backoff computes the delay before the retry numbered retryNum (1-based).
	// Defaults to exponential backoff with Retry-After handling.
	Backoff func(attempt int, minDelay, maxDelay time.Duration, resp *http.Response) time.Duration
	// Hook is invoked after every retryable (non-final) attempt. err and resp
	// are mutually exclusive; exactly one is non-nil.
	Hook func(attempt int, err error, resp *http.Response)
}

func (c *Config) policy() Policy {
	if c.Policy == nil {
		return PolicyFunc(DefaultPolicy)
	}
	return c.Policy
}

func (c *Config) backoff() func(int, time.Duration, time.Duration, *http.Response) time.Duration {
	if c.Backoff == nil {
		return ExponentialBackoff
	}
	return c.Backoff
}

// delays normalizes the configured delay range. Explicit zero values are
// preserved, enabling immediate (zero-delay) retries as required by existing
// callers (e.g. WithRetryConfig(0, 0, ...)). Otherwise:
//   - MinDelay <= 0 defaults to 1s.
//   - MaxDelay <= 0 caps at 10 * MinDelay.
//   - MaxDelay < MinDelay is raised to MinDelay.
func (c *Config) delays() (time.Duration, time.Duration) {
	if c.MinDelay == 0 && c.MaxDelay == 0 {
		return 0, 0
	}
	minDelay := c.MinDelay
	if minDelay <= 0 {
		minDelay = time.Second
	}
	maxDelay := c.MaxDelay
	if maxDelay <= 0 {
		maxDelay = 10 * minDelay
	}
	if maxDelay < minDelay {
		maxDelay = minDelay
	}
	return minDelay, maxDelay
}

// ExponentialBackoff returns minDelay * 2^(attempt-1), capped at maxDelay,
// unless the response carries a valid Retry-After header, in which case that
// duration is used (still capped at maxDelay).
func ExponentialBackoff(attempt int, minDelay, maxDelay time.Duration, resp *http.Response) time.Duration {
	if resp != nil {
		if d, ok := retryAfter(resp); ok {
			return capDelay(d, maxDelay)
		}
	}
	d := minDelay
	for i := 1; i < attempt; i++ {
		d *= 2
		if d >= maxDelay {
			return maxDelay
		}
	}
	return capDelay(d, maxDelay)
}

// LinearJitterBackoff returns minDelay * attempt with additive jitter of up
// to 50% of the computed delay, capped at maxDelay. It ignores Retry-After.
// Jitter is drawn from crypto/rand; on entropy failure the unjittered,
// capped delay is used.
func LinearJitterBackoff(attempt int, minDelay, maxDelay time.Duration, _ *http.Response) time.Duration {
	d := time.Duration(attempt) * minDelay
	// Non-positive range (e.g. zero-delay retries or negative attempt
	// products): no jitter to add.
	if d <= 0 {
		return capDelay(d, maxDelay)
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(d/2)+1))
	if err != nil {
		// Fall back to the base delay rather than failing a retry.
		return capDelay(d, maxDelay)
	}
	return capDelay(d+time.Duration(n.Int64()), maxDelay)
}

func capDelay(d, max time.Duration) time.Duration {
	if d < 0 {
		return 0
	}
	if d > max {
		return max
	}
	return d
}

func retryAfter(resp *http.Response) (time.Duration, bool) {
	v := resp.Header.Get("Retry-After")
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d, true
		}
		return 0, true
	}
	return 0, false
}

// SendFunc performs a single attempt. It must not modify the request.
type SendFunc func(req *http.Request) (*http.Response, error)

// Send runs req through the configured retry loop using send for each
// attempt. send may be req's transport's RoundTrip (via RoundTripperSend) or
// a wrapper around http.Client.Do (which retains its per-attempt timeout).
//
// On exhausting retries for a retryable HTTP response, that response is
// returned as (resp, nil) so the caller can handle the status. On a final
// transport error, the error is returned.
func Send(ctx context.Context, req *http.Request, cfg Config, send SendFunc) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("retry: nil request")
	}
	if send == nil {
		return nil, fmt.Errorf("retry: nil send function")
	}

	policy := cfg.policy()
	backoff := cfg.backoff()
	minDelay, maxDelay := cfg.delays()

	attempt := 0
	for {
		attempt++
		resp, err := send(req)
		if !policy.ShouldRetry(err, resp) {
			return resp, err
		}
		if attempt > cfg.MaxRetries {
			return resp, err
		}
		if cfg.Hook != nil {
			cfg.Hook(attempt, err, resp)
		}
		if !rewind(req) {
			// Non-replayable body: do not attempt again.
			return resp, err
		}
		if err == nil {
			// Drain and close so the connection can be reused and the
			// response body does not leak before retrying.
			drain(resp)
		}
		delay := backoff(attempt, minDelay, maxDelay, resp)
		if err := wait(ctx, delay); err != nil {
			return nil, err
		}
	}
}

// RoundTripperSend returns a SendFunc that uses rt for each attempt.
func RoundTripperSend(rt http.RoundTripper) SendFunc {
	return func(req *http.Request) (*http.Response, error) {
		return rt.RoundTrip(req)
	}
}

// RoundTripper is an http.RoundTripper that retries requests through the
// wrapped Transport according to Config. It can be installed as an
// http.Client's Transport.
type RoundTripper struct {
	// Transport performs each attempt. If nil, http.DefaultTransport is used.
	Transport http.RoundTripper
	// Config configures the retry loop.
	Config Config
}

// RoundTrip implements http.RoundTripper.
func (rt *RoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	transport := rt.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	return Send(req.Context(), req, rt.Config, RoundTripperSend(transport))
}

// IsReplayable reports whether req's body can be replayed on retry: requests
// without a body are always replayable, requests with a body are replayable
// only if Request.GetBody is set (matching net/http's own semantics).
func IsReplayable(req *http.Request) bool {
	return req.Body == nil || req.GetBody != nil
}

// rewind recreates req.Body via Request.GetBody when possible. It returns
// false if the body cannot be replayed.
func rewind(req *http.Request) bool {
	if req.Body == nil {
		return true
	}
	if req.GetBody == nil {
		return false
	}
	body, err := req.GetBody()
	if err != nil {
		return false
	}
	req.Body = body
	return true
}

// drain consumes and closes resp.Body.
func drain(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<20))
	_ = resp.Body.Close()
}

func wait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
