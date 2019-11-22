package api

import (
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"go.uber.org/zap"
)

// NewHTTPClient is a functional options constructor, based on this blog post:
// https://dave.cheney.net/2014/10/17/functional-options-for-friendly-apis
// The default clients specifies some important timeouts (some of them are synced with AA one):
// 10 seconds for connection (TLS/non TLS)
// 10 minutes for requests (time to get the first response headers)
func NewHTTPClient(options ...func(*http.Client) error) (*http.Client, error) {
	client := &http.Client{
		Transport: &http.Transport{
			ResponseHeaderTimeout: 10 * time.Minute,
			TLSHandshakeTimeout:   10 * time.Second,
			DialContext: (&net.Dialer{
				Timeout: 10 * time.Second,
			}).DialContext,
		},
	}

	for _, op := range options {
		err := op(client)
		if err != nil {
			return nil, err
		}
	}

	return client, nil
}

// OptionSkipVerify will set the Insecure Skip which means that TLS certs will not be
// verified for validity.
func OptionSkipVerify(client *http.Client) error {
	TLSClientConfig := &tls.Config{InsecureSkipVerify: true}

	transport := client.Transport.(*http.Transport)
	transport.TLSClientConfig = TLSClientConfig
	client.Transport = transport

	return nil
}

// OptionCAValidate will use the CA certificate, passed as a string, to validate the
// certificates presented by Ops Manager.
func OptionCAValidate(ca string) func(client *http.Client) error {
	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM([]byte(ca))
	TLSClientConfig := &tls.Config{
		InsecureSkipVerify: false,
		RootCAs:            caCertPool,
	}

	return func(client *http.Client) error {
		transport := client.Transport.(*http.Transport)
		transport.TLSClientConfig = TLSClientConfig
		client.Transport = transport

		return nil
	}
}

// serializeToBuffer takes any object and tries to serialize it to the buffer
func serializeToBuffer(v interface{}) (io.Reader, error) {
	var buffer io.Reader
	if v != nil {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		buffer = bytes.NewBuffer(b)
	}
	return buffer, nil
}

// Request is a generic method allowing to make all types of HTTP digest requests using specific 'client'
// Note, that it's currently coupled with Ops Manager specific functionality (ApiError) that's why it's put into 'om'
// package - this can be decoupled to a 'util' package if 'operator' package needs this in future
func Request(method, hostname, path string, v interface{}, user string, token string, client *http.Client) ([]byte, error) {
	url := hostname + path

	buffer, err := serializeToBuffer(v)
	if err != nil {
		return nil, NewError(err)
	}

	// First request is to get authorization information - we are not sending the body
	req, err := createHTTPRequest(method, url, nil)
	if err != nil {
		return nil, NewError(err)
	}

	var body []byte
	// Change this to a more flexible solution, depending on the SSL configuration
	resp, err := client.Do(req)
	if err != nil {
		return nil, NewError(err)
	}
	if resp != nil && resp.Body != nil {
		defer resp.Body.Close()
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return nil, NewError(
			fmt.Errorf(
				"Recieved status code '%v' (%v) but expected the '%d', requested url: %v",
				resp.StatusCode,
				resp.Status,
				http.StatusUnauthorized,
				req.URL,
			),
		)

	}
	digestParts := digestParts(resp)

	// Second request is the real one - we send body as well as digest authorization header
	req, _ = createHTTPRequest(method, url, buffer)

	req.Header.Set("Authorization", getDigestAuthorization(digestParts, method, path, user, token))

	// DEV: uncomment this to see full http request. Set to 'true' to to see the request body
	//dumpRequest, _ := httputil.DumpRequest(req, false)
	//zap.S().Debugf("Ops Manager request: \n %s", dumpRequest)
	zap.S().Debugf("Ops Manager request: %s %s", method, url) // pass string(request) to see full http request

	resp, err = client.Do(req)

	if resp != nil {
		if resp.Body != nil {
			defer resp.Body.Close()
			// limit size of response body read to 16MB
			body, err = util.ReadAtMost(resp.Body, 16*1024*1024)
			if err != nil {
				return nil, NewError(fmt.Errorf("Error reading response body from %s to %v status=%v", method, url, resp.StatusCode))
			}
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			apiError := parseAPIError(resp.StatusCode, method, url, body)
			return nil, apiError
		}
	}

	if err != nil {
		return body, NewError(fmt.Errorf("Error sending %s request to %s: %v", method, url, err))
	}

	return body, nil
}

// createHTTPRequest
func createHTTPRequest(method string, url string, reader io.Reader) (*http.Request, error) {
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		return nil, err
	}

	req.Header.Add("Content-Type", "application/json; charset=UTF-8")
	req.Header.Add("Provider", "KUBERNETES")

	return req, nil
}

// parseAPIError
func parseAPIError(statusCode int, method, url string, body []byte) *Error {
	// If no body - returning the error object with only HTTP status
	if body == nil {
		return &Error{
			Status: &statusCode,
			Detail: fmt.Sprintf("%s %v failed with status %d with no response body", method, url, statusCode),
		}
	}
	// If response body exists - trying to parse it
	errorObject := &Error{}
	if err := json.Unmarshal(body, errorObject); err != nil {
		// If parsing has failed - returning just the general error with status code
		return &Error{
			Status: &statusCode,
			Detail: fmt.Sprintf("%s %v failed with status %d with response body: %s", method, url, statusCode, string(body)),
		}
	}

	return errorObject
}

func digestParts(resp *http.Response) map[string]string {
	result := map[string]string{}
	if len(resp.Header["Www-Authenticate"]) > 0 {
		wantedHeaders := []string{"nonce", "realm", "qop"}
		responseHeaders := strings.Split(resp.Header["Www-Authenticate"][0], ",")
		for _, r := range responseHeaders {
			for _, w := range wantedHeaders {
				if strings.Contains(r, w) {
					result[w] = strings.Split(r, `"`)[1]
					break
				}
			}
		}
	}
	return result
}

func getCnonce() string {
	b := make([]byte, 8)
	io.ReadFull(rand.Reader, b)
	return fmt.Sprintf("%x", b)[:16]
}

func getDigestAuthorization(digestParts map[string]string, method string, url string, user string, token string) string {
	d := digestParts
	ha1 := util.MD5Hex(user + ":" + d["realm"] + ":" + token)
	ha2 := util.MD5Hex(method + ":" + url)
	nonceCount := 00000001
	cnonce := getCnonce()
	response := util.MD5Hex(fmt.Sprintf("%s:%s:%v:%s:%s:%s", ha1, d["nonce"], nonceCount, cnonce, d["qop"], ha2))
	authorization := fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", cnonce="%s", nc=%v, qop=%s, response="%s", algorithm="MD5"`,
		user, d["realm"], d["nonce"], url, cnonce, nonceCount, d["qop"], response)
	return authorization
}
