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

// OmConnection is a client interacting with OpsManager API. Note, that all methods returning 'error' return the
// '*OmApiError' in fact but it's error-prone to declare method as returning specific implementation of error
// (see https://golang.org/doc/faq#nil_error)
type OmConnection interface {
	UpdateDeployment(deployment Deployment) ([]byte, error)
	ReadDeployment() (Deployment, error)
	ReadUpdateDeployment(wait bool, depFunc func(Deployment) error) error
	GenerateAgentKey() (string, error)
	ReadAutomationStatus() (*AutomationStatus, error)
	ReadAutomationAgents() (*AgentState, error)
	GetHosts() (*Host, error)
	RemoveHost(hostId string) error
	ReadGroup(name string) (*Group, error)
	CreateGroup(group *Group) (*Group, error)
	UpdateGroup(group *Group) (*Group, error)
	// ReadBackupConfigs returns all host clusters registered in OM. If there's no backup enabled the status is supposed
	// to be Inactive
	ReadBackupConfigs() (*BackupConfigsResponse, error)
	ReadBackupConfig(clusterId string) (*BackupConfig, error)
	ReadHostCluster(clusterId string) (*HostCluster, error)
	UpdateBackupStatus(clusterId string, status BackupStatus) error

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

// OmApiError is the error extension that contains the details of OM error if OM returned the error. This allows the
// code using OmConnection methods to do more fine-grained exception handling depending on exact error that happened.
// The class has to encapsulate the usual error (non-OM one) as well as the error may happen at any stage before/after
// OM request (failing to (de)serialize json object for example) so in this case all fields except for 'Detail' will be
// empty
type OmApiError struct {
	Status    *int   `json:"error"`
	Reason    string `json:"reason"`
	Detail    string `json:"detail"`
	ErrorCode string `json:"errorCode"`
}

func NewApiError(err error) error {
	if err == nil {
		return nil
	}
	return &OmApiError{Detail: err.Error()}
}

func (e *OmApiError) Error() string {
	if e.Status != nil {
		msg := fmt.Sprintf("Status: %d", *e.Status)
		if e.Reason != "" {
			msg += fmt.Sprintf(" (%s)", e.Reason)
		}
		if e.ErrorCode != "" {
			msg += fmt.Sprintf(", ErrorCode: %s", e.ErrorCode)
		}
		if e.Detail != "" {
			msg += fmt.Sprintf(", Detail: %s", e.Detail)
		}
		return msg
	}
	return e.Detail
}

func (e *OmApiError) ErrorCodeIn(errorCodes ...string) bool {
	for _, c := range errorCodes {
		if e.ErrorCode == c {
			return true
		}
	}
	return false
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
	d, e := BuildDeploymentFromBytes(ans)
	return d, NewApiError(e)
}

// ReadUpdateDeployment performs the "read-modify-update" operation on OpsManager Deployment. It will wait for
// Automation agents to apply results if "wait" is set to true.
func (oc *HttpOmConnection) ReadUpdateDeployment(wait bool, depFunc func(Deployment) error) error {
	deployment, err := oc.ReadDeployment()
	if err != nil {
		return err
	}

	if err := depFunc(deployment); err != nil {
		return NewApiError(err)
	}

	_, err = oc.UpdateDeployment(deployment)
	if err != nil {
		return err
	}

	if wait && !WaitUntilGoalState(oc) {
		return NewApiError(errors.New(fmt.Sprintf("Process didn't reach goal state")))
	}
	return nil
}

func (oc *HttpOmConnection) GenerateAgentKey() (string, error) {
	data := map[string]string{"desc": "Agent key for Kubernetes"}
	ans, err := oc.post(fmt.Sprintf("/api/public/v1.0/groups/%s/agentapikeys", oc.GroupId()), data)

	if err != nil {
		return "", err
	}

	var keyInfo map[string]interface{}
	if err := json.Unmarshal(ans, &keyInfo); err != nil {
		return "", NewApiError(err)
	}
	return keyInfo["key"].(string), nil
}

func (oc *HttpOmConnection) ReadAutomationAgents() (*AgentState, error) {
	ans, err := oc.get(fmt.Sprintf("/api/public/v1.0/groups/%s/agents/AUTOMATION", oc.GroupId()))
	if err != nil {
		return nil, err
	}
	state, e := BuildAgentStateFromBytes(ans)
	return state, NewApiError(e)
}

func (oc *HttpOmConnection) ReadAutomationStatus() (*AutomationStatus, error) {
	ans, err := oc.get(fmt.Sprintf("/api/public/v1.0/groups/%s/automationStatus", oc.GroupId()))
	if err != nil {
		return nil, err
	}

	status, e := buildAutomationStatusFromBytes(ans)
	return status, NewApiError(e)
}

func (oc *HttpOmConnection) GetHosts() (*Host, error) {
	mPath := fmt.Sprintf("/api/public/v1.0/groups/%s/hosts/", oc.GroupId())
	res, err := oc.get(mPath)
	if err != nil {
		return nil, err
	}

	hosts := &Host{}
	if err := json.Unmarshal(res, hosts); err != nil {
		return nil, NewApiError(err)
	}

	return hosts, nil
}

func (oc *HttpOmConnection) RemoveHost(hostId string) error {
	mPath := fmt.Sprintf("/api/public/v1.0/groups/%s/hosts/%s", oc.GroupId(), hostId)
	return oc.delete(mPath)
}

func (oc *HttpOmConnection) ReadGroup(name string) (*Group, error) {
	mPath := fmt.Sprintf("/api/public/v1.0/groups/byName/%s", name)
	res, err := oc.get(mPath)
	if err != nil {
		return nil, err
	}

	group := &Group{}
	if err := json.Unmarshal(res, group); err != nil {
		return nil, NewApiError(err)
	}

	return group, nil
}
func (oc *HttpOmConnection) CreateGroup(group *Group) (*Group, error) {
	res, err := oc.post("/api/public/v1.0/groups", group)

	if err != nil {
		return nil, err
	}

	g := &Group{}
	if err := json.Unmarshal(res, g); err != nil {
		return nil, NewApiError(err)
	}

	return g, nil
}

func (oc *HttpOmConnection) UpdateGroup(group *Group) (*Group, error) {
	path := fmt.Sprintf("/api/public/v1.0/groups/%s", group.Id)
	res, err := oc.patch(path, group)

	if err != nil {
		return nil, err
	}

	g := &Group{}
	if err := json.Unmarshal(res, g); err != nil {
		return nil, NewApiError(err)
	}

	return group, nil
}
func (oc *HttpOmConnection) ReadBackupConfigs() (*BackupConfigsResponse, error) {
	mPath := fmt.Sprintf("/api/public/v1.0/groups/%s/backupConfigs", oc.GroupId())
	res, err := oc.get(mPath)
	if err != nil {
		return nil, err
	}

	response := &BackupConfigsResponse{}
	if err := json.Unmarshal(res, response); err != nil {
		return nil, NewApiError(err)
	}

	return response, nil
}

func (oc *HttpOmConnection) ReadBackupConfig(clusterId string) (*BackupConfig, error) {
	mPath := fmt.Sprintf("/api/public/v1.0/groups/%s/backupConfigs/%s", oc.GroupId(), clusterId)
	res, err := oc.get(mPath)
	if err != nil {
		return nil, err
	}

	response := &BackupConfig{}
	if err := json.Unmarshal(res, response); err != nil {
		return nil, NewApiError(err)
	}

	return response, nil
}

func (oc *HttpOmConnection) ReadHostCluster(clusterId string) (*HostCluster, error) {
	mPath := fmt.Sprintf("/api/public/v1.0/groups/%s/clusters/%s", oc.GroupId(), clusterId)
	res, err := oc.get(mPath)
	if err != nil {
		return nil, err
	}

	cluster := &HostCluster{}
	if err := json.Unmarshal(res, cluster); err != nil {
		return nil, NewApiError(err)
	}

	return cluster, nil
}
func (oc *HttpOmConnection) UpdateBackupStatus(clusterId string, status BackupStatus) error {
	path := fmt.Sprintf("/api/public/v1.0/groups/%s/backupConfigs/%s", oc.GroupId(), clusterId)

	_, err := oc.patch(path, map[string]interface{}{"statusName": status})

	if err != nil {
		return NewApiError(err)
	}

	return nil
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

func (oc *HttpOmConnection) patch(path string, v interface{}) ([]byte, error) {
	return oc.httpVerb("PATCH", path, v)
}

func (oc *HttpOmConnection) delete(path string) error {
	_, err := oc.httpVerb("DELETE", path, nil)
	return err
}

func (oc *HttpOmConnection) httpVerb(method, path string, v interface{}) ([]byte, error) {
	response, err := request(method, oc.BaseUrl(), path, v, oc.User(), oc.PublicApiKey())
	return response, err
}

func request(method, hostname, path string, v interface{}, user string, token string) ([]byte, error) {
	url := hostname + path

	buffer, err := serialize(v)
	if err != nil {
		return nil, NewApiError(err)
	}

	// First request is to get authorization information - we are not sending the body
	req, err := createHttpRequest(method, url, nil)
	if err != nil {
		return nil, NewApiError(err)
	}

	resp, err := util.DefaultHttpClient.Do(req)
	var body []byte
	if err != nil {
		return nil, NewApiError(err)
	}
	if resp != nil && resp.Body != nil {
		defer resp.Body.Close()
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return nil, NewApiError(fmt.Errorf("Recieved status code '%v' (%v) but expected the '%d', requested url: %v", resp.StatusCode, resp.Status, http.StatusUnauthorized, req.URL))
	}
	digestParts := digestParts(resp)

	// Second request is the real one - we send body as well as digest authorization header
	req, err = createHttpRequest(method, url, buffer)

	req.Header.Set("Authorization", getDigestAuthorization(digestParts, method, path, user, token))

	request, _ := httputil.DumpRequest(req, false) // DEV: change this to true to see the request body sent
	zap.S().Debugf("Request sending: \n %s", string(request))

	resp, err = util.DefaultHttpClient.Do(req)

	if resp != nil {
		if resp.Body != nil {
			defer resp.Body.Close()
			// limit size of response body read to 16MB
			body, err = util.ReadAtMost(resp.Body, 16*1024*1024)
			if err != nil {
				return nil, NewApiError(fmt.Errorf("Error reading response body from %s to %v status=%v", method, url, resp.StatusCode))
			}
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			apiError := parseApiError(resp.StatusCode, method, url, body)
			return nil, apiError
		}
	}

	if err != nil {
		return body, NewApiError(fmt.Errorf("Error sending %s request to %s: %v", method, url, err))
	}

	return body, nil
}

func createHttpRequest(method string, url string, reader io.Reader) (*http.Request, error) {
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		return nil, err
	}

	req.Header.Add("Content-Type", "application/json; charset=UTF-8")
	req.Header.Add("Provider", "KUBERNETES")

	return req, nil
}

func parseApiError(statusCode int, method, url string, body []byte) *OmApiError {
	// If no body - returning the error object with only HTTP status
	if body == nil {
		return &OmApiError{
			Status: &statusCode,
			Detail: fmt.Sprintf("%s %v failed with status %d with no response body", method, url, statusCode),
		}
	}
	// If response body exists - trying to parse it
	errorObject := &OmApiError{}
	if err := json.Unmarshal(body, errorObject); err != nil {
		// If parsing has failed - returning just the general error with status code
		return &OmApiError{
			Status: &statusCode,
			Detail: fmt.Sprintf("%s %v failed with status %d with response body: %s", method, url, statusCode, string(body)),
		}
	}

	return errorObject
}

func serialize(v interface{}) (io.Reader, error) {
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
