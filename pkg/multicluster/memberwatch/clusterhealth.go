package memberwatch

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"go.uber.org/zap"

	"github.com/mongodb/mongodb-kubernetes/internal/retry"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
)

type ClusterHealthChecker interface {
	IsClusterHealthy(log *zap.SugaredLogger) bool
}

type MemberHealthCheck struct {
	Server string
	Client *http.Client
	// retryConfig holds the retry policy for health checks.
	retryConfig retry.Config
	Token       string
}

var (
	DefaultRetryWaitMin = 1 * time.Second
	DefaultRetryWaitMax = 3 * time.Second
	DefaultRetryMax     = 10
)

// HealthCheckOption is a functional option for configuring MemberHealthCheck
type HealthCheckOption func(*retry.Config)

// WithRetryConfig configures the retry behavior of the health check client
func WithRetryConfig(retryWaitMin, retryWaitMax time.Duration, retryMax int) HealthCheckOption {
	return func(cfg *retry.Config) {
		cfg.MinDelay = retryWaitMin
		cfg.MaxDelay = retryWaitMax
		cfg.MaxRetries = retryMax
	}
}

func NewMemberHealthCheck(server string, ca []byte, token string, log *zap.SugaredLogger, opts ...HealthCheckOption) ClusterHealthChecker {
	certpool := x509.NewCertPool()
	certpool.AppendCertsFromPEM(ca)

	// MULTI_CLUSTER_HEALTHCHECK_PROXY (when set) routes the member-cluster
	// /readyz checks through an explicit HTTP CONNECT proxy. We can't use
	// http.ProxyFromEnvironment here because Go hardcodes a 127.0.0.1 /
	// localhost bypass that overrides NO_PROXY — and member-cluster API
	// server URLs in the local-dev kubeconfig are exactly 127.0.0.1:<port>
	// on the EVG host's loopback, reachable only via the SSH tunnel.
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			RootCAs:    certpool,
			MinVersion: tls.VersionTLS12,
		},
	}
	if proxyEnv := env.ReadOrDefault("MULTI_CLUSTER_HEALTHCHECK_PROXY", ""); proxyEnv != "" { // nolint:forbidigo
		if proxyURL, perr := url.Parse(proxyEnv); perr == nil && proxyURL.Host != "" {
			transport.Proxy = http.ProxyURL(proxyURL)
		}
	}

	retryConfig := retry.Config{
		MinDelay:   DefaultRetryWaitMin,
		MaxDelay:   DefaultRetryWaitMax,
		MaxRetries: DefaultRetryMax,
		// The default policy retries transport errors and 429/5xx responses;
		// the default backoff is exponential based on the attempt number and
		// limited by the provided minimum and maximum durations. We don't need
		// Jitter here as we're the only client to the OM, so there's no risk
		// of overwhelming it in a peek.
		Hook: func(attempt int, err error, resp *http.Response) {
			log.Warnf("Retrying (#%d) failed health check to %s", attempt, server)
		},
	}

	// Apply any custom options
	for _, opt := range opts {
		opt(&retryConfig)
	}

	return &MemberHealthCheck{
		Server: server,
		Client: &http.Client{
			Transport: transport,
			Timeout:   time.Duration(env.ReadIntOrDefault(multicluster.ClusterClientTimeoutEnv, 10)) * time.Second, // nolint:forbidigo
		},
		retryConfig: retryConfig,
		Token:       token,
	}
}

// IsMemberClusterHealthy checks if there are some member clusters that are not in a "healthy" state
// by curl "ing" the /readyz endpoint of the clusters.
func (m *MemberHealthCheck) IsClusterHealthy(log *zap.SugaredLogger) bool {
	statusCode, err := check(m.Client, m.retryConfig, m.Server, m.Token)
	if err != nil {
		log.Errorf("Error running healthcheck for server: %s, error: %v", m.Server, err)
	}

	if err != nil || statusCode != http.StatusOK {
		log.Debugf("Response code from cluster endpoint call: %d", statusCode)
		return false
	}

	return true
}

// check pings the "/readyz" endpoint of a cluster's API server and checks if it is healthy
func check(client *http.Client, retryConfig retry.Config, server string, token string) (int, error) {
	endPoint := fmt.Sprintf("%s/readyz", server)
	req, err := http.NewRequest(http.MethodGet, endPoint, nil)
	if err != nil {
		return 0, err
	}

	bearer := "Bearer " + token
	req.Header.Add("Authorization", bearer)

	resp, err := retry.Send(req.Context(), req, retryConfig, client.Do)
	if err != nil {
		return 0, err
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(resp.Body)
	return resp.StatusCode, nil
}
