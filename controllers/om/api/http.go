package api

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httputil"
	"os"
	"strconv"
	"time"

	// mongodb-forks/digest implements RFC 7616 digest auth (MD5 and SHA-256) and
	// replays request bodies. It replaces the hand-rolled MD5 client that ignored
	// the stale=true challenge parameter.
	"github.com/mongodb-forks/digest"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	"github.com/mongodb/mongodb-kubernetes/controllers/om/apierror"
	"github.com/mongodb/mongodb-kubernetes/internal/retry"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

const (
	OMClientSubsystem = "om_client"
	ResultKey         = "requests_total"
)

var omClient = prometheus.NewCounterVec(prometheus.CounterOpts{
	Subsystem: OMClientSubsystem,
	Name:      ResultKey,
	Help:      "Number of HTTP requests, partitioned by status code, method, and path.",
}, []string{"code", "method", "path"})

func init() {
	// Register custom metrics with the global prometheus registry
	if util.OperatorEnvironment(os.Getenv(util.OmOperatorEnv)) != util.OperatorEnvironmentProd { // nolint:forbidigo
		zap.S().Debugf("collecting operator specific debug metrics")
		registerClientMetrics()
	}
}

// registerClientMetrics sets up the operator om client metrics.
func registerClientMetrics() {
	// register the metrics with our registry
	metrics.Registry.MustRegister(omClient)
}

const (
	defaultRetryWaitMin = 1 * time.Second
	defaultRetryWaitMax = 10 * time.Second
	defaultRetryMax     = 3

	// maxRedirects is the net/http default redirect limit, kept because
	// CheckRedirect replaces the default policy.
	maxRedirects = 10

	// defaultRequestTimeout bounds a single HTTP attempt (connection, response headers and body) against Ops Manager.
	defaultRequestTimeout = 2 * time.Minute
)

type Client struct {
	*retry.Client

	// Digest username and password
	username string
	password string

	// Enable debugging information on this client
	debug bool
}

// NewHTTPClient is a functional options constructor, based on this blog post:
// https://dave.cheney.net/2014/10/17/functional-options-for-friendly-apis
// The default client uses http.DefaultTransport and bounds every attempt with defaultRequestTimeout. Failed
// requests are retried up to defaultRetryMax times, so an overall deadline can only be enforced by the context
// passed to Request.
func NewHTTPClient(options ...func(*Client) error) (*Client, error) {
	client := &Client{
		Client: newDefaultHTTPClient(),
	}

	client, err := applyOptions(client, options...)
	if err != nil {
		return nil, err
	}

	// If digest auth is configured, wrap the HTTP transport with the digest
	// client, then wrap that with staleRetryTransport to retry on stale nonces
	// (RFC 7616 §3.2). This runs after TLS options so they modify the
	// *http.Transport first.
	if client.username != "" && client.password != "" {
		// The digest transport answers a challenge with credentials on whatever
		// host sent it, so a cross-host redirect would send the OM credentials to
		// an unrelated server. net/http only strips an existing Authorization
		// header, and this transport adds one after the redirect.
		client.HTTPClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			// CheckRedirect replaces the net/http default policy, so keep its limit.
			if len(via) >= maxRedirects {
				return xerrors.Errorf("stopped after %d redirects", maxRedirects)
			}
			if req.URL.Scheme != via[0].URL.Scheme || req.URL.Host != via[0].URL.Host {
				// Hand the 3xx response to sendRequest instead of failing the
				// request: an error here could cause the retry loop to replay a
				// request that may not be idempotent.
				return http.ErrUseLastResponse
			}
			return nil
		}
		client.HTTPClient.Transport = &staleRetryTransport{
			transport: &digest.Transport{
				Username:  client.username,
				Password:  client.password,
				Transport: client.HTTPClient.Transport,
				// Avoid broken pipes when Ops Manager rejects unauthenticated
				// requests before reading their bodies.
				ChallengeWithoutBody: true,
			},
		}
	}

	return client, nil
}

func newDefaultHTTPClient() *retry.Client {
	return &retry.Client{
		HTTPClient:   &http.Client{Transport: http.DefaultTransport, Timeout: defaultRequestTimeout},
		RetryMax:     defaultRetryMax,
		RetryWaitMin: defaultRetryWaitMin,
		RetryWaitMax: defaultRetryWaitMax,
		CheckRetry:   retry.DefaultRetryPolicy,
		Backoff:      retry.DefaultBackoff,
	}
}

func applyOptions(client *Client, options ...func(*Client) error) (*Client, error) {
	for _, op := range options {
		err := op(client)
		if err != nil {
			return nil, err
		}
	}

	return client, nil
}

// NewHTTPOptions returns a list of options that can be used to construct an
// *http.Client using `NewHTTPClient`.
func NewHTTPOptions() []func(*Client) error {
	return make([]func(*Client) error, 0)
}

// OptionsDigestAuth enables Digest authentication.
func OptionDigestAuth(username, password string) func(client *Client) error {
	return func(client *Client) error {
		client.username = username
		client.password = password

		return nil
	}
}

// OptionDebug enables debug on the http client.
func OptionDebug(client *Client) error {
	client.debug = true

	return nil
}

// OptionSkipVerify will set the Insecure Skip which means that TLS certs will not be
// verified for validity.
func OptionSkipVerify(client *Client) error {
	TLSClientConfig := &tls.Config{InsecureSkipVerify: true} //nolint //Options for switching this on/off are at the CR level.

	transport := client.HTTPClient.Transport.(*http.Transport).Clone()
	transport.TLSClientConfig = TLSClientConfig
	client.HTTPClient.Transport = transport

	return nil
}

// OptionCAValidate will use the CA certificate, passed as a string, to validate the
// certificates presented by Ops Manager.
func OptionCAValidate(ca string) func(client *Client) error {
	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM([]byte(ca))
	TLSClientConfig := &tls.Config{
		InsecureSkipVerify: false,
		RootCAs:            caCertPool,
		MinVersion:         tls.VersionTLS12,
	}

	return func(client *Client) error {
		transport := client.HTTPClient.Transport.(*http.Transport).Clone()
		transport.TLSClientConfig = TLSClientConfig
		client.HTTPClient.Transport = transport

		return nil
	}
}

// Request executes an HTTP request, given a series of parameters, over this *Client object.
// It handles Digest when needed and json marshaling of the `v` struct.
// The whole exchange, including retries and backoff waits, is aborted as soon as ctx is done.
func (client *Client) Request(ctx context.Context, method, hostname, path string, v any) ([]byte, http.Header, error) {
	url := hostname + path

	req, err := createHTTPRequest(ctx, method, url, v)
	if err != nil {
		return nil, nil, apierror.New(err)
	}
	return client.sendRequest(method, url, path, req)
}

// RequestWithAgentAuth executes an HTTP request using Basic authorization with the agent API key
// This is used for specific endpoints created for the agent.
// We use it for /group/v2/info and /group/v2/addPreferredHostname to manage preferred hostnames
// We have created a ticket for the EA team to add a public or private endpoint
// whilst maintaining the other 2 https://jira.mongodb.org/browse/CLOUDP-308115
func (client *Client) RequestWithAgentAuth(ctx context.Context, method, hostname, path string, agentAuth string, v any) ([]byte, http.Header, error) {
	url := hostname + path

	req, err := createHTTPRequest(ctx, method, url, v)
	if err != nil {
		return nil, nil, apierror.New(err)
	}

	req.Header.Set("Authorization", agentAuth)

	return client.sendRequest(method, url, path, req)
}

// createHTTPRequest builds a request bound to ctx, with 'v' serialized to JSON as the body.
func createHTTPRequest(ctx context.Context, method string, url string, v any) (*retry.Request, error) {
	buffer, err := serializeToBuffer(v)
	if err != nil {
		return nil, err
	}

	req, err := retry.NewRequestWithContext(ctx, method, url, buffer)
	if err != nil {
		return nil, err
	}
	// Preserve http.NewRequestWithContext's nil-body representation for callers
	// that inspect the request before it is sent.
	if buffer == nil {
		req.Body = nil
	}

	req.Header.Add("Content-Type", "application/json; charset=UTF-8")
	req.Header.Add("Provider", "KUBERNETES")

	return req, nil
}

func (client *Client) sendRequest(method, url, path string, req *retry.Request) ([]byte, http.Header, error) {
	// we need to limit size of request/response dump, because automation config request can't have over 1MB size
	const maxDumpSize = 10000 // 1 MB

	// retry.Send runs the same retry loop as Client.Do but returns the last
	// response when the retries exhaust on an error status. Client.Do drains
	// that response and returns a generic error instead, which would hide the
	// status that the deletion cleanup classifies.
	ctx := req.Context()
	checkRetry := client.CheckRetry
	if checkRetry == nil {
		checkRetry = retry.DefaultRetryPolicy
	}
	backoff := client.Backoff
	if backoff == nil {
		backoff = retry.DefaultBackoff
	}
	minDelay, maxDelay := client.RetryWaitMin, client.RetryWaitMax
	if maxDelay < minDelay {
		maxDelay = minDelay
	}

	cfg := retry.Config{
		MaxRetries: client.RetryMax,
		Policy: retry.PolicyFunc(func(err error, resp *http.Response) bool {
			shouldRetry, _ := checkRetry(ctx, resp, err)
			return shouldRetry
		}),
		// Config.Backoff numbers retries from 1, DefaultBackoff from 0.
		Backoff: func(attempt int, _, _ time.Duration, resp *http.Response) time.Duration {
			return backoff(minDelay, maxDelay, attempt-1, resp)
		},
	}

	httpClient := client.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	attempt := 0
	resp, err := retry.Send(ctx, req.Request, cfg, func(request *http.Request) (*http.Response, error) {
		if client.debug {
			dumpRequest, _ := httputil.DumpRequest(request, true)
			if len(dumpRequest) > maxDumpSize {
				dumpRequest = dumpRequest[:maxDumpSize]
			}
			zap.S().Debugf("Ops Manager request (%d): %s %s\n \n %s", attempt, method, path, dumpRequest)
		} else {
			zap.S().Debugf("Ops Manager request: %s %s", method, url)
		}
		attempt++

		response, err := httpClient.Do(request)
		if client.debug && response != nil {
			dumpResponse, _ := httputil.DumpResponse(response, true)
			if len(dumpResponse) > maxDumpSize {
				dumpResponse = dumpResponse[:maxDumpSize]
			}
			zap.S().Debugf("Ops Manager response: %s %s\n \n %s", method, path, dumpResponse)
		}
		return response, err
	})
	if ctxErr := ctx.Err(); ctxErr != nil {
		// A response that arrives after the context is canceled is
		// discarded, so the caller observes the cancellation.
		if resp != nil && resp.Body != nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
			_ = resp.Body.Close()
		}
		return nil, nil, apierror.New(xerrors.Errorf("error sending %s request to %s: %w", method, url, ctxErr))
	}
	if err != nil {
		return nil, nil, apierror.New(xerrors.Errorf("error sending %s request to %s: %w", method, url, err))
	}

	omClient.WithLabelValues(strconv.Itoa(resp.StatusCode), method, path).Inc()

	// It is required for the body to be read completely for the connection to be reused.
	// https://stackoverflow.com/a/17953506/75928
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return nil, nil, apierror.New(xerrors.Errorf("Error reading response body from %s to %v status=%v", method, url, resp.StatusCode))
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		apiError := parseAPIError(resp.StatusCode, method, url, body)
		return nil, nil, apiError
	}

	return body, resp.Header, nil
}

// serializeToBuffer takes any object and tries to serialize it to the buffer
func serializeToBuffer(v any) (io.Reader, error) {
	var buffer io.Reader
	if v != nil {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		buffer = bytes.NewBuffer(b)
	}
	return buffer, nil
}
