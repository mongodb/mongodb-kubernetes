package memberwatch

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

func TestIsMemberClusterHealthy(t *testing.T) {
	// mark cluster as healthy because "200" status code
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.WriteHeader(200)
	}))

	memberHealthCheck := NewMemberHealthCheck(server.URL, []byte("ca-data"), "bhjkb")
	healthy := memberHealthCheck.IsClusterHealthy(zap.S())
	assert.Equal(t, true, healthy)

	// mark cluster unhealthy because != "200" status code
	server = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.WriteHeader(500)
	}))

	memberHealthCheck = NewMemberHealthCheck(server.URL, []byte("ca-data"), "hhfhj")
	healthy = memberHealthCheck.IsClusterHealthy(zap.S())
	assert.Equal(t, false, healthy)

	// mark cluster unhealthy because of error
	memberHealthCheck = NewMemberHealthCheck("", []byte("ca-data"), "bhdjbh")
	healthy = memberHealthCheck.IsClusterHealthy(zap.S())
	assert.Equal(t, false, healthy)
}
