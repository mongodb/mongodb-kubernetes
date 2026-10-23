package telemetry

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"

	atlas "go.mongodb.org/atlas/mongodbatlas"

	"github.com/mongodb/mongodb-kubernetes/internal/retry"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
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

var (
	telemetryTransport = newTelemetryTransport()
	// defaultRetryConfig retries transport errors and 429/5xx responses using
	// linear jittered backoff between defaultRetryWaitMin and defaultRetryWaitMax.
	defaultRetryConfig = retry.Config{
		MaxRetries: maxRetries,
		MinDelay:   defaultRetryWaitMin,
		MaxDelay:   defaultRetryWaitMax,
		Backoff:    retry.LinearJitterBackoff,
		Hook: func(attempt int, err error, resp *http.Response) {
			if err != nil {
				Logger.Debugf("Telemetry request failed, retrying (attempt %d): %v", attempt, err)
			} else {
				Logger.Debugf("Telemetry request got status %d, retrying (attempt %d)", resp.StatusCode, attempt)
			}
		},
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

// NewClient creates a telemetry client. Optional configOpts tweak the retry
// configuration of the default retrying RoundTripper (e.g. to disable delays
// in tests).
func NewClient(configOpts ...func(*retry.Config)) (*Client, error) {
	retryConfig := defaultRetryConfig
	for _, opt := range configOpts {
		opt(&retryConfig)
	}

	c := atlas.NewClient(
		&http.Client{Transport: &retry.RoundTripper{Transport: telemetryTransport, Config: retryConfig}},
	)

	if urlStr := env.ReadOrDefault(BaseUrl, ""); urlStr != "" { // nolint:forbidigo
		Logger.Debugf("Using different base url configured for atlasclient: %s", urlStr)
		parsed, err := url.Parse(urlStr)
		if err != nil {
			return nil, fmt.Errorf("invalid base URL for atlas client: %w", err)
		}
		c.BaseURL = parsed
	}

	c.OnResponseProcessed(func(resp *atlas.Response) {
		var respHeaders strings.Builder
		for key, value := range resp.Header {
			respHeaders.WriteString(fmt.Sprintf("%v: %v\n", key, strings.Join(value, " ")))
		}

		Logger.Debugf(`request:
%v %v
response:
%v %v
%v
%v
`, resp.Request.Method, resp.Request.URL.String(), resp.Proto, resp.Status, respHeaders.String(), string(resp.Raw))
	})

	return &Client{
		atlasClient:    c,
		maxRetries:     maxRetries,
		initialBackoff: initialBackoff,
	}, nil
}

// SendEventWithRetry sends an HTTP request with retries on transient failures.
func (c *Client) SendEventWithRetry(ctx context.Context, body []Event) error {
	_, span := TRACER.Start(ctx, "SendEventWithRetry")
	baseUrl := c.atlasClient.BaseURL
	span.SetAttributes(attribute.KeyValue{Key: "mck.atlas.base_url", Value: attribute.StringValue(baseUrl.String())})
	defer span.End()

	atlasClient := c.atlasClient
	request, err := atlasClient.NewRequest(ctx, http.MethodPost, "api/private/unauth/telemetry/events", body)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	_, err = atlasClient.Do(ctx, request, nil)
	return err
}
