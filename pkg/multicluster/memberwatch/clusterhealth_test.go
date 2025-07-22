package memberwatch

import (
	"go.uber.org/zap/zaptest"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestIsMemberClusterHealthy(t *testing.T) {
	// mark cluster as healthy because "200" status code
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.WriteHeader(200)
	}))

	memberHealthCheck := NewMemberHealthCheck(server.URL, []byte("ca-data"), "bhjkb", zaptest.NewLogger(t).Sugar())
	healthy := memberHealthCheck.IsClusterHealthy(zaptest.NewLogger(t).Sugar())
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
	memberHealthCheck = NewMemberHealthCheck(server.URL, []byte("ca-data"), "hhfhj", zaptest.NewLogger(t).Sugar())
	healthy = memberHealthCheck.IsClusterHealthy(zaptest.NewLogger(t).Sugar())
	endTime := time.Since(startTime)

	assert.Equal(t, false, healthy)
	assert.GreaterOrEqual(t, endTime, DefaultRetryWaitMin*2)
	assert.LessOrEqual(t, endTime, DefaultRetryWaitMax*2+time.Second)

	// mark cluster unhealthy because of error
	memberHealthCheck = NewMemberHealthCheck("", []byte("ca-data"), "bhdjbh", zaptest.NewLogger(t).Sugar())
	healthy = memberHealthCheck.IsClusterHealthy(zaptest.NewLogger(t).Sugar())
	assert.Equal(t, false, healthy)
}
