package api

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCreateHttpRequest(t *testing.T) {
	httpRequest, e := createHTTPRequest("POST", "http://some.com", nil)
	u, _ := url.Parse("http://some.com")

	assert.NoError(t, e)
	assert.Equal(t, []string{"application/json; charset=UTF-8"}, httpRequest.Header["Content-Type"])
	assert.Equal(t, []string{"KUBERNETES"}, httpRequest.Header["Provider"])
	assert.Equal(t, "POST", httpRequest.Method)
	assert.Equal(t, u, httpRequest.URL)
	assert.Nil(t, httpRequest.Body)
}
