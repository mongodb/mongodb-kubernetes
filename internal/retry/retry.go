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
	if resp == nil {
		return true
	}
	return resp.StatusCode == 0 || resp.StatusCode == http.StatusTooManyRequests || (resp.StatusCode >= 500 && resp.StatusCode != http.StatusNotImplemented)
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

// LinearJitterBackoff samples a base delay in [min,max) and multiplies it by
// attemptNum+1. attemptNum is zero-based, matching Client.Backoff. When the
// range is empty or reversed, min is used without jitter. Retry-After is
// ignored. Jitter is drawn from crypto/rand; if entropy is unavailable, min
// is used as the base delay.
func LinearJitterBackoff(min, max time.Duration, attemptNum int, _ *http.Response) time.Duration {
	base := int64(min)
	if max > min {
		span := new(big.Int).Sub(big.NewInt(int64(max)), big.NewInt(int64(min)))
		n, err := rand.Int(rand.Reader, span)
		if err == nil {
			base = new(big.Int).Add(big.NewInt(int64(min)), n).Int64()
		}
	}

	factor := new(big.Int).SetInt64(int64(attemptNum))
	factor.Add(factor, big.NewInt(1))
	product := new(big.Int).Mul(big.NewInt(base), factor)
	maxDuration := big.NewInt(int64(^uint64(0) >> 1))
	minDuration := new(big.Int).Neg(new(big.Int).Add(maxDuration, big.NewInt(1)))
	if product.Cmp(maxDuration) > 0 {
		return time.Duration(maxDuration.Int64())
	}
	if product.Cmp(minDuration) < 0 {
		return time.Duration(minDuration.Int64())
	}
	return time.Duration(product.Int64())
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
	if secs, ok := new(big.Int).SetString(v, 10); ok && secs.Sign() >= 0 {
		maxDuration := time.Duration(1<<63 - 1)
		maxSeconds := new(big.Int).SetInt64(int64(maxDuration / time.Second))
		if secs.Cmp(maxSeconds) > 0 {
			return maxDuration, true
		}
		return time.Duration(secs.Int64()) * time.Second, true
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

// RoundTripper is an http.RoundTripper that retries requests through its
// Client. It can be installed as an http.Client's Transport.
type RoundTripper struct {
	// Client configures the retry behavior. If nil, the default retry client is
	// used.
	Client *Client
}

// defaultRoundTripperClient is shared by RoundTrippers without an explicit
// client. Its configuration is immutable after initialization, and Client.Do
// only reads these fields, making it safe for concurrent requests.
var defaultRoundTripperClient = NewClient()

// RoundTrip implements http.RoundTripper.
func (rt *RoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	client := rt.Client
	if client == nil {
		client = defaultRoundTripperClient
	}
	wrapped, err := FromRequest(req)
	if err != nil {
		return nil, err
	}
	return client.Do(wrapped)
}

// IsReplayable reports whether req's body can be replayed on retry: requests
// without a body are always replayable, requests with a body are replayable
// only if Request.GetBody is set (matching net/http's own semantics).
func IsReplayable(req *http.Request) bool {
	return req.Body == nil || req.Body == http.NoBody || req.GetBody != nil
}

// rewind recreates req.Body via Request.GetBody when possible. It returns
// false if the body cannot be replayed.
func rewind(req *http.Request) bool {
	if req.Body == nil || req.Body == http.NoBody {
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
