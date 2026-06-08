package memberwatch

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

func init() {
	logger, _ := zap.NewDevelopment()
	zap.ReplaceGlobals(logger)
}

func TestIsMemberClusterHealthy(t *testing.T) {
	// mark cluster as healthy because "200" status code
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.WriteHeader(200)
	}))

	memberHealthCheck := NewMemberHealthCheck(server.URL, []byte("ca-data"), "bhjkb", zap.S())
	healthy := memberHealthCheck.IsClusterHealthy(zap.S())
	assert.Equal(t, true, healthy)

	// mark cluster unhealthy because != "200" status code
	var requestCount int
	server = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		requestCount++
		rw.WriteHeader(500)
	}))

	memberHealthCheck = NewMemberHealthCheck(
		server.URL,
		[]byte("ca-data"),
		"hhfhj",
		zap.S(),
		WithRetryConfig(0, 0, 2), // No delay between retries, retry 2 times
	)
	healthy = memberHealthCheck.IsClusterHealthy(zap.S())

	assert.Equal(t, false, healthy)
	// Verify retries actually happened: initial request + 2 retries = 3 total
	assert.Equal(t, 3, requestCount, "Expected 3 requests (1 initial + 2 retries)")

	// mark cluster unhealthy because of error
	memberHealthCheck = NewMemberHealthCheck("", []byte("ca-data"), "bhdjbh", zap.S())
	healthy = memberHealthCheck.IsClusterHealthy(zap.S())
	assert.Equal(t, false, healthy)
}
