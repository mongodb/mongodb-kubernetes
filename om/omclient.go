package om

import (
	"bytes"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"strings"
	"github.com/10gen/ops-manager-kubernetes/util"
)

type OmConnection struct {
	BaseUrl      string
	GroupId      string
	User         string
	PublicApiKey string
}

// NewOpsManagerConnection stores OpsManger api endpoint and authentication credentials.
// It makes it easy to call the API without having to explicitly provide connection details.
func NewOpsManagerConnection(baseUrl, groupId, user, publicApiKey string) *OmConnection {
	return &OmConnection{
		BaseUrl:      baseUrl,
		GroupId:      groupId,
		User:         user,
		PublicApiKey: publicApiKey,
	}
}

func (oc *OmConnection) UpdateDeployment(deployment *Deployment) ([]byte, error) {
	return oc.put(fmt.Sprintf("/api/public/v1.0/groups/%s/automationConfig", oc.GroupId), deployment)
}

func (oc *OmConnection) ReadDeployment() (*Deployment, error) {
	ans, err := oc.Get(fmt.Sprintf("/api/public/v1.0/groups/%s/automationConfig", oc.GroupId))

	if err != nil {
		return nil, err
	}
	fmt.Println(string(ans))
	return BuildDeploymentFromBytes(ans)
}

func(oc *OmConnection) GenerateAgentKey() (string, error) {
	data := map[string]string{"desc": "Agent key for Kubernetes"}
	ans, err := oc.post(fmt.Sprintf("/api/public/v1.0/groups/%s/agentapikeys", oc.GroupId), data)

	if err != nil {
		return "", err
	}

	fmt.Println(string(ans))

	var keyInfo map[string]interface{}
	if err := json.Unmarshal(ans, &keyInfo); err != nil {
		return "", err
	}
	return keyInfo["key"].(string), nil
}

// TODO uncomment code to read agents here
/*func (oc *OmConnection) ReadAutomationAgents() (*AgentState, error) {
	return request("GET", oc.BaseUrl, path, nil, oc.User, oc.PublicApiKey, "application/json; charset=UTF-8")
}*/

// TODO make Get method private (refactor agents code for this)
func (oc *OmConnection) Get(path string) ([]byte, error) {
	return request("GET", oc.BaseUrl, path, nil, oc.User, oc.PublicApiKey, "application/json; charset=UTF-8")
}

//********************************** Private methods *******************************************************************

func (oc *OmConnection) post(path string, v interface{}) (response []byte, err error) {
	postBytes, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("Error while encoding to json: %v", err)
	}
	return request("POST", oc.BaseUrl, path, bytes.NewBuffer(postBytes), oc.User, oc.PublicApiKey, "application/json; charset=UTF-8")
}

func (oc *OmConnection) put(path string, v interface{}) (response []byte, err error) {
	postBytes, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("Error while encoding to json: %v", err)
	}
	return request("PUT", oc.BaseUrl, path, bytes.NewBuffer(postBytes), oc.User, oc.PublicApiKey, "application/json; charset=UTF-8")
}

func request(method string, hostname string, path string, reader io.Reader, user string, token string, contentType string) (response []byte, err error) {
	url := hostname + path

	// First request is to get authorization information - we are not sending the body
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		return nil, err
	}

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

	// Second request is the real one - we send bosy as well as digest authorization header
	req, err = http.NewRequest(method, url, reader)

	req.Header.Set("Authorization", getDigestAuthorization(digestParts, method, path, user, token))
	req.Header.Add("Content-Type", contentType)

	request, _ := httputil.DumpRequest(req, true)
	fmt.Printf("Request: %s\n", request)

	resp, err = util.DefaultHttpClient.Do(req)

	if resp != nil {
		if resp.Body != nil {
			defer resp.Body.Close()
			// limit size of response body read to 16MB
			body, err = util.ReadAtMost(resp.Body, 16*1024*1024)
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
