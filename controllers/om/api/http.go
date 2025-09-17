package api

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httputil"
	"os"
	"strconv"
	"time"

	"github.com/hashicorp/go-retryablehttp"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	"github.com/mongodb/mongodb-kubernetes/controllers/om/apierror"
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
)

type Client struct {
	*retryablehttp.Client

	// Digest username and password
	username string
	password string

	// Enable debugging information on this client
	debug bool
}

// NewHTTPClient is a functional options constructor, based on this blog post:
// https://dave.cheney.net/2014/10/17/functional-options-for-friendly-apis
// The default clients specifies some important timeouts (some of them are synced with AA one):
// 10 seconds for connection (TLS/non TLS)
// 10 minutes for requests (time to get the first response headers)
func NewHTTPClient(options ...func(*Client) error) (*Client, error) {
	client := &Client{
		Client: newDefaultHTTPClient(),
	}

	return applyOptions(client, options...)
}

func newDefaultHTTPClient() *retryablehttp.Client {
	return &retryablehttp.Client{
		HTTPClient:   &http.Client{Transport: http.DefaultTransport},
		RetryWaitMin: defaultRetryWaitMin,
		RetryWaitMax: defaultRetryWaitMax,
		RetryMax:     defaultRetryMax,
		// Will retry on all errors
		CheckRetry: retryablehttp.DefaultRetryPolicy,
		// Exponential backoff based on the attempt number and limited by the provided minimum and maximum durations.
		// We don't need Jitter here as we're the only client to the OM, so there's no risk
		// of overwhelming it in a peek.
		Backoff: retryablehttp.DefaultBackoff,
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
func (client *Client) Request(method, hostname, path string, v interface{}) ([]byte, http.Header, error) {
	url := hostname + path

	req, err := createHTTPRequest(method, url, v)
	if err != nil {
		return nil, nil, apierror.New(err)
	}

	if client.username != "" && client.password != "" {
		// Only add Digest auth when needed.
		err = client.authorizeRequest(method, hostname, path, req)
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
func (client *Client) RequestWithAgentAuth(method, hostname, path string, agentAuth string, v interface{}) ([]byte, http.Header, error) {
	url := hostname + path

	req, err := createHTTPRequest(method, url, v)
	if err != nil {
		return nil, nil, apierror.New(err)
	}

	req.Header.Set("Authorization", agentAuth)

	return client.sendRequest(method, url, path, req)
}

// authorizeRequest executes one request that's meant to be challenged by the
// server in order to build the next one. The `request` parameter is aggregated
// with the required `Authorization` header.
func (client *Client) authorizeRequest(method, hostname, path string, request *retryablehttp.Request) error {
	url := hostname + path

	digestRequest, err := retryablehttp.NewRequest(method, url, nil)
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

	if resp.StatusCode == http.StatusOK {
		// No need to authorize, server didn't challenge us
		return nil
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

// createHTTPRequest
func createHTTPRequest(method string, url string, v interface{}) (*retryablehttp.Request, error) {
	buffer, err := serializeToBuffer(v)
	if err != nil {
		return nil, err
	}

	req, err := retryablehttp.NewRequest(method, url, buffer)
	if err != nil {
		return nil, err
	}

	req.Header.Add("Content-Type", "application/json; charset=UTF-8")
	req.Header.Add("Provider", "KUBERNETES")

	return req, nil
}

func (client *Client) sendRequest(method, url, path string, req *retryablehttp.Request) ([]byte, http.Header, error) {
	// we need to limit size of request/response dump, because automation config request can't have over 1MB size
	const maxDumpSize = 10000 // 1 MB
	client.RequestLogHook = func(logger retryablehttp.Logger, request *http.Request, i int) {
		if client.debug {
			dumpRequest, _ := httputil.DumpRequest(request, true)
			if len(dumpRequest) > maxDumpSize {
				dumpRequest = dumpRequest[:maxDumpSize]
			}
			zap.S().Debugf("Ops Manager request (%d): %s %s\n \n %s", i, method, path, dumpRequest)
		} else {
			zap.S().Debugf("Ops Manager request: %s %s", method, url)
		}
	}

	client.ResponseLogHook = func(logger retryablehttp.Logger, response *http.Response) {
		if client.debug {
			dumpResponse, _ := httputil.DumpResponse(response, true)
			if len(dumpResponse) > maxDumpSize {
				dumpResponse = dumpResponse[:maxDumpSize]
			}
			zap.S().Debugf("Ops Manager response: %s %s\n \n %s", method, path, dumpResponse)
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, apierror.New(xerrors.Errorf("error sending %s request to %s: %w", method, url, err))
	}

	omClient.WithLabelValues(strconv.Itoa(resp.StatusCode), method, path).Inc()

	// need to clear hooks, because otherwise they will be persisted for the subsequent calls
	// resulting in logging authorizeRequest
	client.RequestLogHook = nil
	client.ResponseLogHook = nil

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
func serializeToBuffer(v interface{}) (io.Reader, error) {
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
