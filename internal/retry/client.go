package retry

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Request wraps an HTTP request and carries optional hooks scoped to that
// request. The hooks are useful when a Client is shared between goroutines.
type Request struct {
	*http.Request
	RequestLogHook  func(Logger, *http.Request, int)
	ResponseLogHook func(Logger, *http.Response)
}

// ReaderFunc provides a fresh request body reader on each invocation.
type ReaderFunc func() (io.Reader, error)

// NewRequest creates a retryable request with a background context.
func NewRequest(method, rawURL string, body interface{}) (*Request, error) {
	return NewRequestWithContext(context.Background(), method, rawURL, body)
}

// NewRequestWithContext creates a retryable request. Body values are
// snapshotted at construction so GetBody can safely recreate them on retries.
func NewRequestWithContext(ctx context.Context, method, rawURL string, body interface{}) (*Request, error) {
	r, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if err := setRequestBody(r, body); err != nil {
		return nil, err
	}
	return &Request{Request: r}, nil
}

// FromRequest wraps an existing request without changing it.
func FromRequest(req *http.Request) (*Request, error) {
	if req == nil {
		return nil, fmt.Errorf("retry: nil request")
	}
	wrapped := &Request{Request: req}
	if req.Body != nil && req.Body != http.NoBody {
		body, err := snapshotBody(req.Body)
		if err != nil {
			return nil, err
		}
		setReaderBody(req, body, int64(len(body)))
	}
	return wrapped, nil
}

// requestBody returns a replayable snapshot and its known length. ReaderFunc
// values are invoked once at construction: snapshotting makes even one-shot
// factories safe for subsequent retries.
func requestBody(body interface{}) ([]byte, error) {
	switch b := body.(type) {
	case nil:
		return nil, nil
	case []byte:
		return append([]byte(nil), b...), nil
	case string:
		return []byte(b), nil
	case *bytes.Buffer:
		if b == nil {
			return nil, nil
		}
		return append([]byte(nil), b.Bytes()...), nil
	case *bytes.Reader:
		if b == nil {
			return nil, nil
		}
		copyReader := *b
		data, err := io.ReadAll(&copyReader)
		return data, err
	case ReaderFunc:
		if b == nil {
			return nil, nil
		}
		r, err := b()
		if err != nil {
			return nil, err
		}
		return snapshotBody(r)
	case func() (io.Reader, error):
		if b == nil {
			return nil, nil
		}
		r, err := b()
		if err != nil {
			return nil, err
		}
		return snapshotBody(r)
	case io.ReadSeeker:
		return snapshotSeeker(b)
	case io.Reader:
		return snapshotBody(b)
	default:
		return nil, fmt.Errorf("cannot handle type %T", body)
	}
}

func snapshotBody(r io.Reader) ([]byte, error) {
	if r == nil {
		return nil, nil
	}
	if c, ok := r.(io.Closer); ok {
		defer c.Close()
	}
	return io.ReadAll(r)
}

func snapshotSeeker(r io.ReadSeeker) ([]byte, error) {
	if r == nil {
		return nil, nil
	}
	position, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(r)
	_, seekErr := r.Seek(position, io.SeekStart)
	if readErr != nil {
		return nil, readErr
	}
	if seekErr != nil {
		return nil, seekErr
	}
	return data, nil
}

func setRequestBody(req *http.Request, body interface{}) error {
	data, err := requestBody(body)
	if err != nil {
		return err
	}
	setReaderBody(req, data, int64(len(data)))
	return nil
}

func setReaderBody(req *http.Request, data []byte, length int64) {
	if len(data) == 0 {
		req.Body = http.NoBody
		req.ContentLength = 0
	} else {
		req.Body = io.NopCloser(bytes.NewReader(data))
		req.ContentLength = length
	}
	req.GetBody = func() (io.ReadCloser, error) {
		if len(data) == 0 {
			return http.NoBody, nil
		}
		return io.NopCloser(bytes.NewReader(data)), nil
	}
}

// Logger is the small logging interface used by retry clients.
type Logger interface {
	Printf(string, ...interface{})
}

// CheckRetry decides whether the last attempt should be retried. An error is
// returned from Client.Do if retries stop or are exhausted.
type CheckRetry func(context.Context, *http.Response, error) (bool, error)

// Backoff computes the delay before a retry. attemptNum is zero for the first
// failed attempt, matching the retryable HTTP client convention.
type Backoff func(min, max time.Duration, attemptNum int, resp *http.Response) time.Duration

// Client performs retryable HTTP requests.
type Client struct {
	HTTPClient      *http.Client
	RetryMax        int
	RetryWaitMin    time.Duration
	RetryWaitMax    time.Duration
	CheckRetry      CheckRetry
	Backoff         Backoff
	Logger          Logger
	RequestLogHook  func(Logger, *http.Request, int)
	ResponseLogHook func(Logger, *http.Response)
}

// NewClient creates a client with retryablehttp-compatible defaults. A
// struct-literal Client intentionally retains zero delays for callers that
// explicitly configure immediate retries.
func NewClient() *Client {
	return &Client{
		HTTPClient:   http.DefaultClient,
		RetryMax:     4,
		RetryWaitMin: time.Second,
		RetryWaitMax: 30 * time.Second,
		Logger:       log.Default(),
		CheckRetry:   DefaultRetryPolicy,
		Backoff:      DefaultBackoff,
	}
}

// Do sends req, retrying according to the Client policy. The first attempt is
// numbered zero in RequestLogHook; RetryMax counts retries after that attempt.
func (c *Client) Do(req *Request) (*http.Response, error) {
	if req == nil || req.Request == nil {
		return nil, fmt.Errorf("retry: nil request")
	}
	hc := c.httpClient()
	return c.do(req, func(r *http.Request) (*http.Response, error) { return hc.Do(r) })
}

func (c *Client) do(req *Request, send SendFunc) (*http.Response, error) {
	check := c.CheckRetry
	if check == nil {
		check = DefaultRetryPolicy
	}
	backoff := c.Backoff
	if backoff == nil {
		backoff = DefaultBackoff
	}
	min, max := c.RetryWaitMin, c.RetryWaitMax
	if max < min {
		max = min
	}

	for attempt := 0; ; attempt++ {
		if err := req.Context().Err(); err != nil {
			return nil, err
		}
		if hook := req.RequestLogHook; hook != nil {
			hook(c.Logger, req.Request, attempt)
		} else if c.RequestLogHook != nil {
			c.RequestLogHook(c.Logger, req.Request, attempt)
		}
		attemptReq := req.Request
		if attempt > 0 {
			copyReq := *req.Request
			attemptReq = &copyReq
			if !rewind(attemptReq) {
				return nil, fmt.Errorf("%s %s giving up after %d attempt(s): request body is not replayable", req.Method, req.URL, attempt)
			}
			req.Request = attemptReq
		}
		resp, err := send(attemptReq)
		if err == nil {
			if hook := req.ResponseLogHook; hook != nil {
				hook(c.Logger, resp)
			} else if c.ResponseLogHook != nil {
				c.ResponseLogHook(c.Logger, resp)
			}
		}
		retry, checkErr := check(req.Context(), resp, err)
		if req.Context().Err() != nil {
			if resp != nil {
				drainFully(resp)
			}
			return nil, req.Context().Err()
		}
		if !retry && checkErr == nil && err == nil {
			return resp, err
		}
		if !retry || attempt >= c.RetryMax {
			return c.exhausted(req, resp, err, checkErr, attempt+1)
		}
		if err == nil && resp != nil {
			drainFully(resp)
		}
		delay := backoff(min, max, attempt, resp)
		if waitErr := wait(req.Context(), delay); waitErr != nil {
			return nil, waitErr
		}
	}
}

func (c *Client) exhausted(req *Request, resp *http.Response, err, checkErr error, attempts int) (*http.Response, error) {
	if checkErr != nil {
		err = checkErr
	}
	if resp != nil {
		drainFully(resp)
	}
	if err == nil {
		return nil, fmt.Errorf("%s %s giving up after %d attempt(s)", req.Method, req.URL, attempts)
	}
	return nil, fmt.Errorf("%s %s giving up after %d attempt(s): %w", req.Method, req.URL, attempts, err)
}

func (c *Client) httpClient() *http.Client {
	if c != nil && c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

// DefaultRetryPolicy retries transient transport errors and HTTP 429/5xx
// responses except 501. It excludes request/configuration and certificate
// verification errors, as well as canceled requests.
func DefaultRetryPolicy(ctx context.Context, resp *http.Response, err error) (bool, error) {
	if ctx != nil && ctx.Err() != nil {
		return false, ctx.Err()
	}
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return false, err
		}
		if nonRetryableError(err) {
			return false, err
		}
		return true, nil
	}
	if resp == nil {
		return true, fmt.Errorf("unexpected HTTP status %s", "")
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return true, nil
	}
	if resp.StatusCode == 0 || (resp.StatusCode >= 500 && resp.StatusCode != http.StatusNotImplemented) {
		return true, fmt.Errorf("unexpected HTTP status %s", resp.Status)
	}
	return false, nil
}

func nonRetryableError(err error) bool {
	var certErr x509.UnknownAuthorityError
	if errors.As(err, &certErr) {
		return true
	}
	var invalidCert x509.CertificateInvalidError
	if errors.As(err, &invalidCert) {
		return true
	}
	var hostnameErr x509.HostnameError
	if errors.As(err, &hostnameErr) {
		return true
	}
	var tlsVerify *tls.CertificateVerificationError
	if errors.As(err, &tlsVerify) {
		return true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		msg := strings.ToLower(urlErr.Error())
		if strings.Contains(msg, "unsupported protocol scheme") || strings.Contains(msg, "stopped after") && strings.Contains(msg, "redirect") || strings.Contains(msg, "invalid header") {
			return true
		}
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "invalid header") || strings.Contains(msg, "unsupported protocol scheme") || (strings.Contains(msg, "stopped after") && strings.Contains(msg, "redirect"))
}

// DefaultBackoff returns exponential backoff beginning at min for attempt 0.
// Retry-After is honored for 429 and 503 responses.
func DefaultBackoff(min, max time.Duration, attemptNum int, resp *http.Response) time.Duration {
	if resp != nil && (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable) {
		if d, ok := retryAfter(resp); ok {
			return d
		}
	}
	d := min
	for i := 0; i < attemptNum; i++ {
		if d > max/2 || d < 0 {
			return max
		}
		d *= 2
	}
	return capDelay(d, max)
}

func drainFully(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	_ = resp.Body.Close()
}
