package api

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/10gen/ops-manager-kubernetes/controllers/om/apierror"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
)

// parseAPIError
func parseAPIError(statusCode int, method, url string, body []byte) *apierror.Error {
	// If nobody - returning the error object with only HTTP status
	if body == nil {
		return &apierror.Error{
			Status: &statusCode,
			Detail: fmt.Sprintf("%s %v failed with status %d with no response body", method, url, statusCode),
		}
	}
	// If response body exists - trying to parse it
	errorObject := &apierror.Error{}
	if err := json.Unmarshal(body, errorObject); err != nil {
		// If parsing has failed - returning just the general error with status code
		return &apierror.Error{
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
	_, _ = io.ReadFull(rand.Reader, b)
	return fmt.Sprintf("%x", b)[:16]
}

func getDigestAuthorization(digestParts map[string]string, method string, url string, user string, token string) string {
	d := digestParts
	ha1 := util.MD5Hex(user + ":" + d["realm"] + ":" + token)
	ha2 := util.MD5Hex(method + ":" + url)
	nonceCount := 1
	cnonce := getCnonce()
	response := util.MD5Hex(fmt.Sprintf("%s:%s:%v:%s:%s:%s", ha1, d["nonce"], nonceCount, cnonce, d["qop"], ha2))
	authorization := fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", cnonce="%s", nc=%v, qop=%s, response="%s", algorithm="MD5"`,
		user, d["realm"], d["nonce"], url, cnonce, nonceCount, d["qop"], response)
	return authorization
}
