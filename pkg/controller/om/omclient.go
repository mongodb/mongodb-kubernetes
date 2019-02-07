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

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"go.uber.org/zap"
)

// Connection is a client interacting with OpsManager API. Note, that all methods returning 'error' return the
// '*APIError' in fact but it's error-prone to declare method as returning specific implementation of error
// (see https://golang.org/doc/faq#nil_error)
type Connection interface {
	UpdateDeployment(deployment Deployment) ([]byte, error)
	ReadDeployment() (Deployment, error)
	ReadUpdateDeployment(depFunc func(Deployment) error, log *zap.SugaredLogger) error
	WaitForReadyState(processNames []string, log *zap.SugaredLogger) error
	GenerateAgentKey() (string, error)
	ReadAutomationStatus() (*AutomationStatus, error)
	ReadAutomationAgents() (*AgentState, error)
	GetHosts() (*Host, error)
	RemoveHost(hostID string) error
	ReadOrganizations() ([]*Organization, error)
	ReadGroups() ([]*Group, error)
	CreateGroup(group *Group) (*Group, error)
	UpdateGroup(group *Group) (*Group, error)
	// ReadBackupConfigs returns all host clusters registered in OM. If there's no backup enabled the status is supposed
	// to be Inactive
	ReadBackupConfigs() (*BackupConfigsResponse, error)
	ReadBackupConfig(clusterID string) (*BackupConfig, error)
	ReadHostCluster(clusterID string) (*HostCluster, error)
	UpdateBackupStatus(clusterID string, status BackupStatus) error

	BaseURL() string
	GroupID() string
	User() string
	PublicAPIKey() string
}

// ConnectionFunc type defines a connection to Ops Manager API
type ConnectionFunc func(baseUrl, groupId, user, publicApiKey string) Connection

// HTTPOmConnection
type HTTPOmConnection struct {
	baseURL      string
	groupID      string
	user         string
	publicAPIKey string
}

// APIError is the error extension that contains the details of OM error if OM returned the error. This allows the
// code using Connection methods to do more fine-grained exception handling depending on exact error that happened.
// The class has to encapsulate the usual error (non-OM one) as well as the error may happen at any stage before/after
// OM request (failing to (de)serialize json object for example) so in this case all fields except for 'Detail' will be
// empty
type APIError struct {
	Status    *int   `json:"error"`
	Reason    string `json:"reason"`
	Detail    string `json:"detail"`
	ErrorCode string `json:"errorCode"`
}

// NewAPIError
func NewAPIError(err error) error {
	if err == nil {
		return nil
	}
	return &APIError{Detail: err.Error()}
}

// Error
func (e *APIError) Error() string {
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

// ErrorCodeIn
func (e *APIError) ErrorCodeIn(errorCodes ...string) bool {
	for _, c := range errorCodes {
		if e.ErrorCode == c {
			return true
		}
	}
	return false
}

// NewOpsManagerConnection stores OpsManger api endpoint and authentication credentials.
// It makes it easy to call the API without having to explicitly provide connection details.
func NewOpsManagerConnection(baseURL, groupID, user, publicAPIKey string) Connection {
	return &HTTPOmConnection{
		baseURL:      strings.TrimSuffix(baseURL, "/"),
		groupID:      groupID,
		user:         user,
		publicAPIKey: publicAPIKey,
	}
}

// BaseURL returns BaseURL of HTTPOmConnection
func (oc *HTTPOmConnection) BaseURL() string {
	return oc.baseURL
}

// GroupID returns GroupID of HTTPOmConnection
func (oc *HTTPOmConnection) GroupID() string {
	return oc.groupID
}

// User returns User of HTTPOmConnection
func (oc *HTTPOmConnection) User() string {
	return oc.user

}

// PublicAPIKey returns PublicAPIKey of HTTPOmConnection
func (oc *HTTPOmConnection) PublicAPIKey() string {
	return oc.publicAPIKey
}

// UpdateDeployment updates a given deployment to the new deployment object passed as parameter.
func (oc *HTTPOmConnection) UpdateDeployment(deployment Deployment) ([]byte, error) {
	return oc.put(fmt.Sprintf("/api/public/v1.0/groups/%s/automationConfig", oc.GroupID()), deployment)
}

// ReadDeployment returns a Deployment object for this group
func (oc *HTTPOmConnection) ReadDeployment() (Deployment, error) {
	ans, err := oc.get(fmt.Sprintf("/api/public/v1.0/groups/%s/automationConfig", oc.GroupID()))

	if err != nil {
		return nil, err
	}
	d, e := BuildDeploymentFromBytes(ans)
	return d, NewAPIError(e)
}

// ReadUpdateDeployment performs the "read-modify-update" operation on OpsManager Deployment.
func (oc *HTTPOmConnection) ReadUpdateDeployment(depFunc func(Deployment) error, log *zap.SugaredLogger) error {
	deployment, err := oc.ReadDeployment()
	if err != nil {
		return err
	}

	if err := depFunc(deployment); err != nil {
		return NewAPIError(err)
	}

	_, err = oc.UpdateDeployment(deployment)
	return err
}

func (oc *HTTPOmConnection) WaitForReadyState(processNames []string, log *zap.SugaredLogger) error {
	log.Infow("Waiting for automation config to be applied by Automation Agents...", "processes", processNames)
	reachStateFunc := func() (string, bool) {

		as, lastErr := oc.ReadAutomationStatus()
		if lastErr != nil {
			return fmt.Sprintf("Error reading Automation Agents status: %s", lastErr), false
		}

		if checkAutomationStatusIsGoal(as, processNames) {
			return "Automation agents haven't reached READY state", true
		}

		return "Automation agents haven't reached READY state", false
	}
	if !util.DoAndRetry(reachStateFunc, log, 30, 3) {
		return NewAPIError(fmt.Errorf("Failed to start databases during defined interval"))
	}
	log.Info("Automation config has been successfully updated in Ops Manager and Automation Agents reached READY state")
	return nil
}

// GenerateAgentKey
func (oc *HTTPOmConnection) GenerateAgentKey() (string, error) {
	data := map[string]string{"desc": "Agent key for Kubernetes"}
	ans, err := oc.post(fmt.Sprintf("/api/public/v1.0/groups/%s/agentapikeys", oc.GroupID()), data)

	if err != nil {
		return "", err
	}

	var keyInfo map[string]interface{}
	if err := json.Unmarshal(ans, &keyInfo); err != nil {
		return "", NewAPIError(err)
	}
	return keyInfo["key"].(string), nil
}

// ReadAutomationAgents returns the state of the automation agents registered in Ops Manager
func (oc *HTTPOmConnection) ReadAutomationAgents() (*AgentState, error) {
	ans, err := oc.get(fmt.Sprintf("/api/public/v1.0/groups/%s/agents/AUTOMATION", oc.GroupID()))
	if err != nil {
		return nil, err
	}
	state, e := BuildAgentStateFromBytes(ans)
	return state, NewAPIError(e)
}

// ReadAutomationStatus returns the state of the automation status, this includes if the agents
// have reached goal state.
func (oc *HTTPOmConnection) ReadAutomationStatus() (*AutomationStatus, error) {
	ans, err := oc.get(fmt.Sprintf("/api/public/v1.0/groups/%s/automationStatus", oc.GroupID()))
	if err != nil {
		return nil, err
	}
	status, e := buildAutomationStatusFromBytes(ans)
	return status, NewAPIError(e)
}

// GetHosts return the hosts in this group
func (oc *HTTPOmConnection) GetHosts() (*Host, error) {
	mPath := fmt.Sprintf("/api/public/v1.0/groups/%s/hosts/", oc.GroupID())
	res, err := oc.get(mPath)
	if err != nil {
		return nil, err
	}

	hosts := &Host{}
	if err := json.Unmarshal(res, hosts); err != nil {
		return nil, NewAPIError(err)
	}

	return hosts, nil
}

// RemoveHost will remove host, identified by hostID from group
func (oc *HTTPOmConnection) RemoveHost(hostID string) error {
	mPath := fmt.Sprintf("/api/public/v1.0/groups/%s/hosts/%s", oc.GroupID(), hostID)
	return oc.delete(mPath)
}

// ReadOrganizations
func (oc *HTTPOmConnection) ReadOrganizations() ([]*Organization, error) {
	mPath := fmt.Sprintf("/api/public/v1.0/orgs?itemsPerPage=1000")
	res, err := oc.get(mPath)
	if err != nil {
		return nil, err
	}

	orgsResponse := &OrganizationsResponse{}
	if err := json.Unmarshal(res, orgsResponse); err != nil {
		return nil, NewAPIError(err)
	}

	return orgsResponse.Organizations, nil
}

// ReadGroups
func (oc *HTTPOmConnection) ReadGroups() ([]*Group, error) {
	mPath := fmt.Sprintf("/api/public/v1.0/groups?itemsPerPage=1000")
	res, err := oc.get(mPath)
	if err != nil {
		return nil, err
	}

	groupsResponse := &GroupsResponse{}
	if err := json.Unmarshal(res, groupsResponse); err != nil {
		return nil, NewAPIError(err)
	}

	return groupsResponse.Groups, nil
}

// CreateGroup
func (oc *HTTPOmConnection) CreateGroup(group *Group) (*Group, error) {
	res, err := oc.post("/api/public/v1.0/groups", group)

	if err != nil {
		return nil, err
	}

	g := &Group{}
	if err := json.Unmarshal(res, g); err != nil {
		return nil, NewAPIError(err)
	}

	return g, nil
}

// UpdateGroup
func (oc *HTTPOmConnection) UpdateGroup(group *Group) (*Group, error) {
	path := fmt.Sprintf("/api/public/v1.0/groups/%s", group.ID)
	res, err := oc.patch(path, group)

	if err != nil {
		return nil, err
	}

	g := &Group{}
	if err := json.Unmarshal(res, g); err != nil {
		return nil, NewAPIError(err)
	}

	return group, nil
}

// ReadBackupConfigs
func (oc *HTTPOmConnection) ReadBackupConfigs() (*BackupConfigsResponse, error) {
	mPath := fmt.Sprintf("/api/public/v1.0/groups/%s/backupConfigs", oc.GroupID())
	res, err := oc.get(mPath)
	if err != nil {
		return nil, err
	}

	response := &BackupConfigsResponse{}
	if err := json.Unmarshal(res, response); err != nil {
		return nil, NewAPIError(err)
	}

	return response, nil
}

// ReadBackupConfig
func (oc *HTTPOmConnection) ReadBackupConfig(clusterID string) (*BackupConfig, error) {
	mPath := fmt.Sprintf("/api/public/v1.0/groups/%s/backupConfigs/%s", oc.GroupID(), clusterID)
	res, err := oc.get(mPath)
	if err != nil {
		return nil, err
	}

	response := &BackupConfig{}
	if err := json.Unmarshal(res, response); err != nil {
		return nil, NewAPIError(err)
	}

	return response, nil
}

// ReadHostCluster
func (oc *HTTPOmConnection) ReadHostCluster(clusterID string) (*HostCluster, error) {
	mPath := fmt.Sprintf("/api/public/v1.0/groups/%s/clusters/%s", oc.GroupID(), clusterID)
	res, err := oc.get(mPath)
	if err != nil {
		return nil, err
	}

	cluster := &HostCluster{}
	if err := json.Unmarshal(res, cluster); err != nil {
		return nil, NewAPIError(err)
	}

	return cluster, nil
}

// UpdateBackupStatus
func (oc *HTTPOmConnection) UpdateBackupStatus(clusterID string, status BackupStatus) error {
	path := fmt.Sprintf("/api/public/v1.0/groups/%s/backupConfigs/%s", oc.GroupID(), clusterID)

	_, err := oc.patch(path, map[string]interface{}{"statusName": status})

	if err != nil {
		return NewAPIError(err)
	}

	return nil
}

//********************************** Private methods *******************************************************************

func (oc *HTTPOmConnection) get(path string) ([]byte, error) {
	return oc.httpVerb("GET", path, nil)
}

func (oc *HTTPOmConnection) post(path string, v interface{}) ([]byte, error) {
	return oc.httpVerb("POST", path, v)
}

func (oc *HTTPOmConnection) put(path string, v interface{}) ([]byte, error) {
	return oc.httpVerb("PUT", path, v)
}

func (oc *HTTPOmConnection) patch(path string, v interface{}) ([]byte, error) {
	return oc.httpVerb("PATCH", path, v)
}

func (oc *HTTPOmConnection) delete(path string) error {
	_, err := oc.httpVerb("DELETE", path, nil)
	return err
}

func (oc *HTTPOmConnection) httpVerb(method, path string, v interface{}) ([]byte, error) {
	response, err := request(method, oc.BaseURL(), path, v, oc.User(), oc.PublicAPIKey())
	return response, err
}

func request(method, hostname, path string, v interface{}, user string, token string) ([]byte, error) {
	url := hostname + path

	buffer, err := serialize(v)
	if err != nil {
		return nil, NewAPIError(err)
	}

	// First request is to get authorization information - we are not sending the body
	req, err := createHTTPRequest(method, url, nil)
	if err != nil {
		return nil, NewAPIError(err)
	}

	resp, err := util.DefaultHttpClient.Do(req)
	var body []byte
	if err != nil {
		return nil, NewAPIError(err)
	}
	if resp != nil && resp.Body != nil {
		defer resp.Body.Close()
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return nil, NewAPIError(fmt.Errorf("Recieved status code '%v' (%v) but expected the '%d', requested url: %v", resp.StatusCode, resp.Status, http.StatusUnauthorized, req.URL))
	}
	digestParts := digestParts(resp)

	// Second request is the real one - we send body as well as digest authorization header
	req, err = createHTTPRequest(method, url, buffer)

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
				return nil, NewAPIError(fmt.Errorf("Error reading response body from %s to %v status=%v", method, url, resp.StatusCode))
			}
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			apiError := parseAPIError(resp.StatusCode, method, url, body)
			return nil, apiError
		}
	}

	if err != nil {
		return body, NewAPIError(fmt.Errorf("Error sending %s request to %s: %v", method, url, err))
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
func parseAPIError(statusCode int, method, url string, body []byte) *APIError {
	// If no body - returning the error object with only HTTP status
	if body == nil {
		return &APIError{
			Status: &statusCode,
			Detail: fmt.Sprintf("%s %v failed with status %d with no response body", method, url, statusCode),
		}
	}
	// If response body exists - trying to parse it
	errorObject := &APIError{}
	if err := json.Unmarshal(body, errorObject); err != nil {
		// If parsing has failed - returning just the general error with status code
		return &APIError{
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

// WaitFunction
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
