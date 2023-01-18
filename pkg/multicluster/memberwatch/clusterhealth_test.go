package memberwatch

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
	server = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.WriteHeader(500)
	}))

	// check retry mechanism
	DefaultRetryWaitMin = time.Second * 1
	DefaultRetryWaitMax = time.Second * 1
	DefaultRetryMax = 2

	startTime := time.Now()
	memberHealthCheck = NewMemberHealthCheck(server.URL, []byte("ca-data"), "hhfhj", zap.S())
	healthy = memberHealthCheck.IsClusterHealthy(zap.S())
	endTime := time.Since(startTime)

	assert.Equal(t, false, healthy)
	assert.GreaterOrEqual(t, endTime, DefaultRetryWaitMin*2)
	assert.LessOrEqual(t, endTime, DefaultRetryWaitMax*2+time.Second)

	// mark cluster unhealthy because of error
	memberHealthCheck = NewMemberHealthCheck("", []byte("ca-data"), "bhdjbh", zap.S())
	healthy = memberHealthCheck.IsClusterHealthy(zap.S())
	assert.Equal(t, false, healthy)
}
