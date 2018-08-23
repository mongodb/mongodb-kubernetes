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
	"time"

	"errors"

	"github.com/10gen/ops-manager-kubernetes/util"
	"go.uber.org/zap"
)

type OmConnection interface {
	UpdateDeployment(deployment Deployment) ([]byte, error)
	ReadDeployment() (Deployment, error)
	ReadUpdateDeployment(wait bool, depFunc func(Deployment) error) error
	GenerateAgentKey() (string, error)
	ReadAutomationStatus() (*AutomationStatus, error)
	ReadAutomationAgents() (*AgentState, error)
	GetHosts() (*Host, error)
	RemoveHost(hostId string) error
	BaseUrl() string
	GroupId() string
	User() string
	PublicApiKey() string
}

type HttpOmConnection struct {
	baseUrl      string
	groupId      string
	user         string
	publicApiKey string
}

// NewOpsManagerConnection stores OpsManger api endpoint and authentication credentials.
// It makes it easy to call the API without having to explicitly provide connection details.
func NewOpsManagerConnection(baseUrl, groupId, user, publicApiKey string) OmConnection {
	return &HttpOmConnection{
		baseUrl:      strings.TrimSuffix(baseUrl, "/"),
		groupId:      groupId,
		user:         user,
		publicApiKey: publicApiKey,
	}
}

func (oc *HttpOmConnection) BaseUrl() string {
	return oc.baseUrl
}

func (oc *HttpOmConnection) GroupId() string {
	return oc.groupId
}

func (oc *HttpOmConnection) User() string {
	return oc.user
}

func (oc *HttpOmConnection) PublicApiKey() string {
	return oc.publicApiKey
}

func (oc *HttpOmConnection) UpdateDeployment(deployment Deployment) ([]byte, error) {
	return oc.put(fmt.Sprintf("/api/public/v1.0/groups/%s/automationConfig", oc.GroupId()), deployment)
}

func (oc *HttpOmConnection) ReadDeployment() (Deployment, error) {
	ans, err := oc.get(fmt.Sprintf("/api/public/v1.0/groups/%s/automationConfig", oc.GroupId()))

	if err != nil {
		return nil, err
	}
	//fmt.Println(string(ans))
	return BuildDeploymentFromBytes(ans)
}

// ReadUpdateDeployment performs the "read-modify-update" operation on OpsManager Deployment. It will wait for
// Automation agents to apply results if "wait" is set to true.
func (oc *HttpOmConnection) ReadUpdateDeployment(wait bool, depFunc func(Deployment) error) error {
	deployment, err := oc.ReadDeployment()
	if err != nil {
		return err
	}

	if err := depFunc(deployment); err != nil {
		return err
	}

	_, err = oc.UpdateDeployment(deployment)
	if err != nil {
		return err
	}

	if wait && !WaitUntilGoalState(oc) {
		return errors.New(fmt.Sprintf("Process didn't reach goal state"))
	}
	return nil
}

func (oc *HttpOmConnection) GenerateAgentKey() (string, error) {
	data := map[string]string{"desc": "Agent key for Kubernetes"}
	ans, err := oc.post(fmt.Sprintf("/api/public/v1.0/groups/%s/agentapikeys", oc.GroupId()), data)

	if err != nil {
		return "", err
	}

	zap.S().Debug(string(ans))

	var keyInfo map[string]interface{}
	if err := json.Unmarshal(ans, &keyInfo); err != nil {
		return "", err
	}
	return keyInfo["key"].(string), nil
}

func (oc *HttpOmConnection) ReadAutomationAgents() (*AgentState, error) {
	ans, err := oc.get(fmt.Sprintf("/api/public/v1.0/groups/%s/agents/AUTOMATION", oc.GroupId()))
	if err != nil {
		return nil, err
	}
	zap.S().Debug(string(ans))
	return BuildAgentStateFromBytes(ans)
}

func (oc *HttpOmConnection) ReadAutomationStatus() (*AutomationStatus, error) {
	ans, err := oc.get(fmt.Sprintf("/api/public/v1.0/groups/%s/automationStatus", oc.GroupId()))
	if err != nil {
		return nil, err
	}

	return buildAutomationStatusFromBytes(ans)
}

func (oc *HttpOmConnection) GetHosts() (*Host, error) {
	mPath := fmt.Sprintf("/api/public/v1.0/groups/%s/hosts/", oc.GroupId())
	res, err := oc.get(mPath)
	if err != nil {
		return nil, err
	}

	hosts := &Host{}
	if err := json.Unmarshal(res, hosts); err != nil {
		return nil, err
	}

	return hosts, nil
}

func (oc *HttpOmConnection) RemoveHost(hostId string) error {
	mPath := fmt.Sprintf("/api/public/v1.0/groups/%s/hosts/%s", oc.GroupId(), hostId)
	return oc.delete(mPath)
}

//********************************** Private methods *******************************************************************

func (oc *HttpOmConnection) get(path string) ([]byte, error) {
	return oc.httpVerb("GET", path, nil)
}

func (oc *HttpOmConnection) post(path string, v interface{}) ([]byte, error) {
	return oc.httpVerb("POST", path, v)
}

func (oc *HttpOmConnection) put(path string, v interface{}) ([]byte, error) {
	return oc.httpVerb("PUT", path, v)
}

func (oc *HttpOmConnection) delete(path string) error {
	res, err := oc.httpVerb("DELETE", path, nil)
	if err != nil {
		zap.S().Debugf(string(res))
	}
	return err
}

func (oc *HttpOmConnection) httpVerb(method, path string, v interface{}) ([]byte, error) {
	var buffer io.Reader
	if v != nil {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		buffer = bytes.NewBuffer(b)
	}

	return request(method, oc.BaseUrl(), path, buffer, oc.User(), oc.PublicApiKey(), "application/json; charset=UTF-8")
}

func request(method string, hostname string, path string, reader io.Reader, user string, token string, contentType string) (response []byte, err error) {
	url := hostname + path

	// First request is to get authorization information - we are not sending the body
	req, err := createHttpRequest(method, url, nil, contentType)
	if err != nil {
		return nil, err
	}

	resp, err := util.DefaultHttpClient.Do(req)
	var body []byte
	if err != nil {
		return nil, err
	}
	if resp != nil && resp.Body != nil {
		defer resp.Body.Close()
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return nil, fmt.Errorf("Recieved status code '%v' (%v) but expected the '%d', requested url: %v", resp.StatusCode, resp.Status, http.StatusUnauthorized, req.URL)
	}
	digestParts := digestParts(resp)

	// Second request is the real one - we send body as well as digest authorization header
	req, err = createHttpRequest(method, url, reader, contentType)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", getDigestAuthorization(digestParts, method, path, user, token))

	request, _ := httputil.DumpRequest(req, false)
	zap.S().Debugf("Request sending: \n %s", string(request))

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

func createHttpRequest(method string, url string, reader io.Reader, contentType string) (*http.Request, error) {
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		return nil, err
	}

	req.Header.Add("Content-Type", contentType)
	req.Header.Add("Provider", "KUBERNETES")

	return req, nil
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

func WaitFunction(count, interval int) func() bool {
	// return 10 * time.Second
	return func() bool {
		count--
		if count <= 0 {
			return false
		}
		time.Sleep(time.Duration(interval) * time.Second)
		return true
	}
}
