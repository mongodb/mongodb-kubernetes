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

	// defaultRequestTimeout bounds a single HTTP attempt (connection, response headers and body) against Ops Manager.
	defaultRequestTimeout = 2 * time.Minute
)

type Client struct {
	// HTTPClient performs each attempt; its Timeout bounds a single attempt.
	HTTPClient *http.Client

	// RetryWaitMin is the minimum delay before a retry.
	RetryWaitMin time.Duration
	// RetryWaitMax is the maximum delay before a retry.
	RetryWaitMax time.Duration
	// RetryMax is the number of retries after the initial attempt.
	RetryMax int

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
	client := newDefaultHTTPClient()

	return applyOptions(client, options...)
}

func newDefaultHTTPClient() *Client {
	return &Client{
		HTTPClient:   &http.Client{Transport: http.DefaultTransport, Timeout: defaultRequestTimeout},
		RetryWaitMin: defaultRetryWaitMin,
		RetryWaitMax: defaultRetryWaitMax,
		RetryMax:     defaultRetryMax,
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

// retryConfig returns the retry.Config matching this client's retry settings.
// The default policy retries transport errors (except cancellation) and 429/5xx
// responses; the default backoff is exponential with Retry-After support.
func (client *Client) retryConfig() retry.Config {
	return retry.Config{
		MaxRetries: client.RetryMax,
		MinDelay:   client.RetryWaitMin,
		MaxDelay:   client.RetryWaitMax,
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

	if client.username != "" && client.password != "" {
		// Only add Digest auth when needed.
		err = client.authorizeRequest(ctx, method, hostname, path, req)
		if err != nil {
			return nil, nil, err
		}
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

// Do executes req with this client's retry policy and per-attempt timeout,
// without any logging hooks.
func (client *Client) Do(req *http.Request) (*http.Response, error) {
	return client.do(req, nil, nil)
}

// do executes req with this client's retry policy and per-attempt timeout.
// requestLogHook (if non-nil) runs before every attempt, and responseLogHook
// (if non-nil) runs for every response with no transport error. The hooks are
// request-scoped, so concurrent requests never share mutable state.
func (client *Client) do(req *http.Request, requestLogHook func(attempt int, req *http.Request), responseLogHook func(resp *http.Response)) (*http.Response, error) {
	attempt := 0
	return retry.Send(req.Context(), req, client.retryConfig(), func(r *http.Request) (*http.Response, error) {
		if requestLogHook != nil {
			requestLogHook(attempt, r)
		}
		attempt++
		resp, err := client.HTTPClient.Do(r)
		if responseLogHook != nil && err == nil && resp != nil {
			responseLogHook(resp)
		}
		return resp, err
	})
}

// authorizeRequest executes one request that's meant to be challenged by the
// server in order to build the next one. The `request` parameter is aggregated
// with the required `Authorization` header.
func (client *Client) authorizeRequest(ctx context.Context, method, hostname, path string, request *http.Request) error {
	url := hostname + path

	digestRequest, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(digestRequest)
	if err != nil {
		return err
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(resp.Body)

	_, err = io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode != http.StatusUnauthorized {
		return apierror.New(
			xerrors.Errorf(
				"Received status code '%v' (%v) but expected the '%d', requested url: %v",
				resp.StatusCode,
				resp.Status,
				http.StatusUnauthorized,
				digestRequest.URL,
			),
		)
	}
	digestParts := digestParts(resp)
	digestAuth := getDigestAuthorization(digestParts, method, path, client.username, client.password)

	request.Header.Set("Authorization", digestAuth)

	return nil
}

// createHTTPRequest builds a request bound to ctx, with 'v' serialized to JSON as the body.
func createHTTPRequest(ctx context.Context, method string, url string, v any) (*http.Request, error) {
	buffer, err := serializeToBuffer(v)
	if err != nil {
		return nil, err
	}

	var body io.Reader
	if buffer != nil {
		body = buffer
	}

	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}

	req.Header.Add("Content-Type", "application/json; charset=UTF-8")
	req.Header.Add("Provider", "KUBERNETES")

	return req, nil
}

func (client *Client) sendRequest(method, url, path string, req *http.Request) ([]byte, http.Header, error) {
	// we need to limit size of request/response dump, because automation config request can't have over 1MB size
	const maxDumpSize = 10000 // 1 MB
	requestLogHook := func(attempt int, request *http.Request) {
		if client.debug {
			dumpRequest, _ := httputil.DumpRequest(request, true)
			if len(dumpRequest) > maxDumpSize {
				dumpRequest = dumpRequest[:maxDumpSize]
			}
			zap.S().Debugf("Ops Manager request (%d): %s %s\n \n %s", attempt, method, path, dumpRequest)
		} else {
			zap.S().Debugf("Ops Manager request: %s %s", method, url)
		}
	}
	responseLogHook := func(response *http.Response) {
		if client.debug {
			dumpResponse, _ := httputil.DumpResponse(response, true)
			if len(dumpResponse) > maxDumpSize {
				dumpResponse = dumpResponse[:maxDumpSize]
			}
			zap.S().Debugf("Ops Manager response: %s %s\n \n %s", method, path, dumpResponse)
		}
	}

	resp, err := client.do(req, requestLogHook, responseLogHook)
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
func serializeToBuffer(v any) (*bytes.Buffer, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return bytes.NewBuffer(b), nil
}
