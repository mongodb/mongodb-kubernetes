package telemetry

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hashicorp/go-retryablehttp"
	"go.uber.org/zap"

	atlas "go.mongodb.org/atlas/mongodbatlas"

	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/util/envvar"
)

type Client struct {
	atlasClient    *atlas.Client
	maxRetries     int
	initialBackoff time.Duration
}

const (
	telemetryTimeout      = 1 * time.Second
	keepAlive             = 30 * time.Second
	maxIdleConns          = 5
	maxIdleConnsPerHost   = 4
	idleConnTimeout       = 30 * time.Second
	expectContinueTimeout = 1 * time.Second
	initialBackoff        = 5 * time.Second
	maxRetries            = 5
	defaultRetryWaitMin   = 1 * time.Second
	defaultRetryWaitMax   = 10 * time.Second
)

// zapAdapter wraps zap.Logger to implement retryablehttp.Logger interface
type zapAdapter struct {
	logger *zap.SugaredLogger
}

func (z *zapAdapter) Printf(format string, v ...interface{}) {
	z.logger.Infof(format, v...)
}

var (
	telemetryTransport = newTelemetryTransport()
	defaultRetryClient = &retryablehttp.Client{
		HTTPClient:   &http.Client{Transport: telemetryTransport},
		RetryWaitMin: defaultRetryWaitMin,
		RetryWaitMax: defaultRetryWaitMax,
		RetryMax:     maxRetries,
		CheckRetry:   retryablehttp.DefaultRetryPolicy,
		Backoff:      retryablehttp.LinearJitterBackoff,
		Logger:       &zapAdapter{logger: Logger},
	}
)

func newTelemetryTransport() *http.Transport {
	return &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   telemetryTimeout,
			KeepAlive: keepAlive,
		}).DialContext,
		MaxIdleConns:          maxIdleConns,
		MaxIdleConnsPerHost:   maxIdleConnsPerHost,
		Proxy:                 http.ProxyFromEnvironment,
		IdleConnTimeout:       idleConnTimeout,
		ExpectContinueTimeout: expectContinueTimeout,
	}
}

func NewClient(retryClient *retryablehttp.Client) (*Client, error) {
	if retryClient == nil {
		retryClient = defaultRetryClient
	}

	c := atlas.NewClient(
		&http.Client{Transport: &retryablehttp.RoundTripper{Client: retryClient}},
	)

	if urlStr := envvar.GetEnvOrDefault(BaseUrl, ""); urlStr != "" { // nolint:forbidigo
		Logger.Debugf("Using different base url configured for atlasclient: %s", urlStr)
		parsed, err := url.Parse(urlStr)
		if err != nil {
			return nil, fmt.Errorf("invalid base URL for atlas client: %w", err)
		}
		c.BaseURL = parsed
	}

	c.OnResponseProcessed(func(resp *atlas.Response) {
		respHeaders := ""
		for key, value := range resp.Header {
			respHeaders += fmt.Sprintf("%v: %v\n", key, strings.Join(value, " "))
		}

		Logger.Debugf(`request:
%v %v
response:
%v %v
%v
%v
`, resp.Request.Method, resp.Request.URL.String(), resp.Proto, resp.Status, respHeaders, string(resp.Raw))
	})

	return &Client{
		atlasClient:    c,
		maxRetries:     maxRetries,
		initialBackoff: initialBackoff,
	}, nil
}

// SendEventWithRetry sends an HTTP request with retries on transient failures.
func (c *Client) SendEventWithRetry(ctx context.Context, body []Event) error {
	atlasClient := c.atlasClient
	request, err := atlasClient.NewRequest(ctx, http.MethodPost, "api/private/unauth/telemetry/events", body)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	_, err = atlasClient.Do(ctx, request, nil)
	return err
}
