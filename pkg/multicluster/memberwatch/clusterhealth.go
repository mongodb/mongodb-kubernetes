package memberwatch

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"

	"go.uber.org/zap"
)

type ClusterHealthChecker interface {
	IsClusterHealthy() bool
}

type MemberHeathCheck struct {
	Server string
	Client *http.Client
	Token  string
}

func NewMemberHealthCheck(server string, ca []byte, token string) *MemberHeathCheck {
	certpool := x509.NewCertPool()
	certpool.AppendCertsFromPEM(ca)

	return &MemberHeathCheck{
		Server: server,
		Client: &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					RootCAs: certpool,
				},
			},
		},
		Token: token,
	}
}

// IsMemberClusterHealthy checks if there are some member clusters that are not in a "healthy" state
// by curl "ing" the healthz endpoint of the clusters.
func (m *MemberHeathCheck) IsClusterHealthy(log *zap.SugaredLogger) bool {
	statusCode, err := check(m.Client, m.Server, m.Token)
	if err != nil {
		log.Error("error running healthcheck for server: %s, error: %w", m.Server, err)
	}

	if err != nil || statusCode != http.StatusOK {
		return false
	}

	return true
}

// check pings the "/readyz" endpoint of a cluster's API server and checks if it is healthy
func check(client *http.Client, server string, token string) (int, error) {
	endPoint := fmt.Sprintf("%s/readyz", server)
	req, err := http.NewRequest("GET", endPoint, nil)
	if err != nil {
		return 0, err
	}

	bearer := "Bearer " + token
	req.Header.Add("Authorization", bearer)

	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}
