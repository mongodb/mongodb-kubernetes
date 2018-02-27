package om

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"github.com/10gen/ops-manager-kubernetes/util"
	ioutil "com.tengen/cm/util"
	"strings"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
)


func ApplyDeployment(hostname string, group string, v *Deployment, user string, token string) (response []byte, err error) {
	return Put(hostname, fmt.Sprintf("/api/public/v1.0/groups/%s/automationConfig", group), v, user, token)
}

func ReadDeployment(hostname string, group string, user string, token string) (response *Deployment, err error) {
	ans, err := Get(hostname, fmt.Sprintf("/api/public/v1.0/groups/%s/automationConfig", group), user, token)

	if err != nil {
		return nil, err
	}
	fmt.Println(string(ans))
	return BuildDeploymentFromBytes(ans)
}

func Post(hostname string, path string, v interface{}, user string, token string) (response []byte, err error) {
	postBytes, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("Error while encoding to json: %v", err)
	}
	return request("POST", hostname, path, bytes.NewBuffer(postBytes), user, token, "application/json; charset=UTF-8")
}
func Put(hostname string, path string, v interface{}, user string, token string) (response []byte, err error) {
	postBytes, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("Error while encoding to json: %v", err)
	}
	return request("PUT", hostname, path, bytes.NewBuffer(postBytes), user, token, "application/json; charset=UTF-8")
}

func Get(hostname string, path string, user string, token string) (response []byte, err error) {
	return request("GET", hostname, path, nil, user, token, "application/json; charset=UTF-8")
}

func request(method string, hostname string, path string, reader io.Reader, user string, token string, contentType string) (response []byte, err error) {
	url := hostname + path
	req, err := http.NewRequest(method, url, reader)
	req.Header.Add("Content-Type", contentType)

	resp, err := util.DefaultHttpClient.Do(req)
	var body []byte
	if err != nil {
		return nil, err
	}
	if resp != nil && resp.Body != nil {
		defer resp.Body.Close()
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return nil, fmt.Errorf("Recieved status code '%v' but expected the '%d'", resp.StatusCode, http.StatusUnauthorized)
	}
	digestParts := digestParts(resp)

	req, err = http.NewRequest(method, url, reader)
	req.Header.Set("Authorization", getDigestAuthorization(digestParts, method, path, user, token))
	req.Header.Add("Content-Type", contentType)

	resp, err = util.DefaultHttpClient.Do(req)

	if resp != nil {
		if resp.Body != nil {
			defer resp.Body.Close()
			// limit size of response body read to 16MB
			body, err = ioutil.ReadAtMost(resp.Body, 16*1024*1024)
			if err != nil {
				return nil, fmt.Errorf("Error reading response body from %s to %v status=%v", method, url, resp.StatusCode)
			}
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			if resp.Body == nil {
				return nil, fmt.Errorf("%s %v failed with status %d with no response body", method, url, resp.StatusCode)
			} else {
				return nil, fmt.Errorf("%s %v failed with status %d with response body:\n%s", method, url, resp.StatusCode, string(body))
			}
		}
	}

	if err != nil {
		return body, fmt.Errorf("Error sending %s request to %s: %v", method, url, err)
	}

	return body, nil
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
func getMD5(text string) string {
	hasher := md5.New()
	hasher.Write([]byte(text))
	return hex.EncodeToString(hasher.Sum(nil))
}

func getCnonce() string {
	b := make([]byte, 8)
	io.ReadFull(rand.Reader, b)
	return fmt.Sprintf("%x", b)[:16]
}

func getDigestAuthorization(digestParts map[string]string, method string, url string, user string, token string) string {
	d := digestParts
	ha1 := getMD5(user + ":" + d["realm"] + ":" + token)
	ha2 := getMD5(method + ":" + url)
	nonceCount := 00000001
	cnonce := getCnonce()
	response := getMD5(fmt.Sprintf("%s:%s:%v:%s:%s:%s", ha1, d["nonce"], nonceCount, cnonce, d["qop"], ha2))
	authorization := fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", cnonce="%s", nc=%v, qop=%s, response="%s", algorithm="MD5"`,
		user, d["realm"], d["nonce"], url, cnonce, nonceCount, d["qop"], response)
	return authorization
}
