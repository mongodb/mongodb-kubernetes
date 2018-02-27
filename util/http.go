package util

import (
	"net/http"
	"time"
)

var HttpTransport *http.Transport = &http.Transport{
	ResponseHeaderTimeout: 10 * time.Minute,
	TLSHandshakeTimeout:   10 * time.Second, // Same as DefaultTransport
}

func HttpClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: HttpTransport,
		Timeout:   timeout,
	}
}

// default timeout of 5 minutes
var DefaultHttpClient = HttpClient(5 * time.Minute)
