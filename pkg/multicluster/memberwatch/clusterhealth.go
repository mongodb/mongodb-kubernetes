package memberwatch

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/hashicorp/go-retryablehttp"
	"go.uber.org/zap"

	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
)

type ClusterHealthChecker interface {
	IsClusterHealthy() bool
}

type MemberHeathCheck struct {
	Server string
	Client *retryablehttp.Client
	Token  string
}

var (
	DefaultRetryWaitMin = 1 * time.Second
	DefaultRetryWaitMax = 3 * time.Second
	DefaultRetryMax     = 10
)

func NewMemberHealthCheck(server string, ca []byte, token string, log *zap.SugaredLogger) *MemberHeathCheck {
	certpool := x509.NewCertPool()
	certpool.AppendCertsFromPEM(ca)

	return &MemberHeathCheck{
		Server: server,
		Client: &retryablehttp.Client{
			HTTPClient: &http.Client{
				Transport: &http.Transport{
					TLSClientConfig: &tls.Config{
						RootCAs:    certpool,
						MinVersion: tls.VersionTLS12,
					},
				},
				Timeout: time.Duration(env.ReadIntOrDefault(multicluster.ClusterClientTimeoutEnv, 10)) * time.Second, // nolint:forbidigo
			},
			RetryWaitMin: DefaultRetryWaitMin,
			RetryWaitMax: DefaultRetryWaitMax,
			RetryMax:     DefaultRetryMax,
			// Will retry on all errors
			CheckRetry: retryablehttp.DefaultRetryPolicy,
			// Exponential backoff based on the attempt number and limited by the provided minimum and maximum durations.
			// We don't need Jitter here as we're the only client to the OM, so there's no risk
			// of overwhelming it in a peek.
			Backoff: retryablehttp.DefaultBackoff,
			RequestLogHook: func(logger retryablehttp.Logger, request *http.Request, i int) {
				if i > 0 {
					log.Warnf("Retrying (#%d) failed health check to %s (%s)", i, server, request.URL)
				}
			},
		},
		Token: token,
	}
}

// IsMemberClusterHealthy checks if there are some member clusters that are not in a "healthy" state
// by curl "ing" the /readyz endpoint of the clusters.
func (m *MemberHeathCheck) IsClusterHealthy(log *zap.SugaredLogger) bool {
	statusCode, err := check(m.Client, m.Server, m.Token)
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
func check(client *retryablehttp.Client, server string, token string) (int, error) {
	endPoint := fmt.Sprintf("%s/readyz", server)
	req, err := retryablehttp.NewRequest("GET", endPoint, nil)
	if err != nil {
		return 0, err
	}

	bearer := "Bearer " + token
	req.Header.Add("Authorization", bearer)

	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(resp.Body)
	return resp.StatusCode, nil
}
