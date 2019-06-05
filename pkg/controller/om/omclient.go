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
	"strings"
	"sync"
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

	// ReadUpdateDeployment reads Deployment from Ops Manager, applies the update function to it and pushes it back
	// Note the mutex that must be passed to provide strict serializability for the read-write operations for the same group
	ReadUpdateDeployment(depFunc func(Deployment) error, mutex *sync.Mutex, log *zap.SugaredLogger) error

	//WaitForReadyState(processNames []string, log *zap.SugaredLogger) error
	GenerateAgentKey() (string, error)
	ReadAutomationStatus() (*AutomationStatus, error)
	ReadAutomationAgents() (*AgentState, error)
	GetHosts() (*Host, error)
	RemoveHost(hostID string) error
	ReadOrganizations(page int) (Paginated, error)
	ReadOrganization(orgID string) (*Organization, error)
	ReadProjectsInOrganization(orgID string, page int) (Paginated, error)
	CreateProject(project *Project) (*Project, error)
	UpdateProject(project *Project) (*Project, error)
	// ReadBackupConfigs returns all host clusters registered in OM. If there's no backup enabled the status is supposed
	// to be Inactive
	ReadBackupConfigs() (*BackupConfigsResponse, error)
	ReadBackupConfig(clusterID string) (*BackupConfig, error)
	ReadHostCluster(clusterID string) (*HostCluster, error)
	UpdateBackupStatus(clusterID string, status BackupStatus) error

	AutomationConfigConnection
	MonitoringConfigConnection
	BackupConfigConnection

	BaseURL() string
	GroupID() string
	GroupName() string
	OrgID() string
	User() string
	PublicAPIKey() string
}

type MonitoringConfigConnection interface {
	ReadMonitoringAgentConfig() (*MonitoringAgentConfig, error)
	UpdateMonitoringAgentConfig(mat *MonitoringAgentConfig) ([]byte, error)
	ReadUpdateMonitoringAgentConfig(matFunc func(*MonitoringAgentConfig) error, mutex *sync.Mutex, log *zap.SugaredLogger) error
}

type BackupConfigConnection interface {
	ReadBackupAgentConfig() (*BackupAgentConfig, error)
	UpdateBackupAgentConfig(mat *BackupAgentConfig) ([]byte, error)
	ReadUpdateBackupAgentConfig(matFunc func(*BackupAgentConfig) error, mutex *sync.Mutex, log *zap.SugaredLogger) error
}

// AutomationConfigConnection is an interface that only deals with reading/updating of the AutomationConfig
type AutomationConfigConnection interface {
	UpdateAutomationConfig(ac *AutomationConfig) error
	ReadAutomationConfig() (*AutomationConfig, error)
	ReadUpdateAutomationConfig(acFunc func(ac *AutomationConfig) error, mutex *sync.Mutex, log *zap.SugaredLogger) error
}

// ConnectionFactory type defines a function to create a connection to Ops Manager API.
// That's the way we implement some kind of Template Factory pattern to create connections using some incoming parameters
// (groupId, api key etc - all encapsulated into 'context'). The reconciler later uses this factory to build real
// connections and this allows us to mock out Ops Manager communication during unit testing
type ConnectionFactory func(context *OMContext) Connection

// OMContext is the convenient way of grouping all OM related information together
type OMContext struct {
	BaseURL      string
	GroupID      string
	GroupName    string
	OrgID        string
	User         string
	PublicAPIKey string

	// Will check that the SSL certificate provided by the Ops Manager Server is valid
	// I've decided to use a "AllowInvalid" instead of "RequireValid" as the Zero value
	// of bool is false.
	AllowInvalidSSLCertificate bool

	// CACertificate is the actual certificate as a string, as every "Project" could have
	// its own certificate.
	CACertificate string
}

// HTTPOmConnection
type HTTPOmConnection struct {
	context *OMContext
}

var _ Connection = &HTTPOmConnection{}

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
func NewOpsManagerConnection(context *OMContext) Connection {
	return &HTTPOmConnection{context: context}
}

// BaseURL returns BaseURL of HTTPOmConnection
func (oc *HTTPOmConnection) BaseURL() string {
	return oc.context.BaseURL
}

// GroupID returns GroupID of HTTPOmConnection
func (oc *HTTPOmConnection) GroupID() string {
	return oc.context.GroupID
}

// GroupName returns GroupName of HTTPOmConnection
func (oc *HTTPOmConnection) GroupName() string {
	return oc.context.GroupName
}

// OrgID returns OrgID of HTTPOmConnection
func (oc *HTTPOmConnection) OrgID() string {
	return oc.context.OrgID
}

// User returns User of HTTPOmConnection
func (oc *HTTPOmConnection) User() string {
	return oc.context.User

}

// PublicAPIKey returns PublicAPIKey of HTTPOmConnection
func (oc *HTTPOmConnection) PublicAPIKey() string {
	return oc.context.PublicAPIKey
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

func (oc *HTTPOmConnection) ReadAutomationConfig() (*AutomationConfig, error) {
	ans, err := oc.get(fmt.Sprintf("/api/public/v1.0/groups/%s/automationConfig", oc.GroupID()))

	if err != nil {
		return nil, err
	}

	ac, err := BuildAutomationConfigFromBytes(ans)

	return ac, NewAPIError(err)
}

// ReadUpdateDeployment performs the "read-modify-update" operation on OpsManager Deployment.
// Note, that the mutex locks infinitely (there is no built-in support for timeouts for locks in Go) which seems to be
// ok as OM endpoints are not supposed to hang for long
func (oc *HTTPOmConnection) ReadUpdateDeployment(depFunc func(Deployment) error, mutex *sync.Mutex, log *zap.SugaredLogger) error {
	mutex.Lock()
	defer mutex.Unlock()

	deployment, err := oc.ReadDeployment()
	if err != nil {
		return err
	}

	if err := depFunc(deployment); err != nil {
		return NewAPIError(err)
	}

	_, err = oc.UpdateDeployment(deployment)
	if err != nil {
		return err
	}
	return nil
}

func (oc *HTTPOmConnection) UpdateAutomationConfig(ac *AutomationConfig) error {
	err := ac.Apply()
	if err != nil {
		return err
	}
	_, err = oc.UpdateDeployment(ac.Deployment)
	if err != nil {
		return err
	}
	return nil
}

func (oc *HTTPOmConnection) ReadUpdateAutomationConfig(acFunc func(ac *AutomationConfig) error, mutex *sync.Mutex, log *zap.SugaredLogger) error {
	mutex.Lock()
	defer mutex.Unlock()

	ac, err := oc.ReadAutomationConfig()
	if err != nil {
		log.Errorf("error reading automation config. %s", err)
		return err
	}

	if err := acFunc(ac); err != nil {
		return NewAPIError(err)
	}

	err = oc.UpdateAutomationConfig(ac)
	if err != nil {
		log.Errorf("error updating automation config. %s", err)
		return NewAPIError(err)
	}
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
func (oc *HTTPOmConnection) ReadOrganizations(page int) (Paginated, error) {
	mPath := fmt.Sprintf("/api/public/v1.0/orgs?itemsPerPage=500&pageNum=%d", page)
	res, err := oc.get(mPath)
	if err != nil {
		return nil, err
	}

	orgsResponse := &OrganizationsResponse{}
	if err := json.Unmarshal(res, orgsResponse); err != nil {
		return nil, NewAPIError(err)
	}

	return orgsResponse, nil
}

func (oc *HTTPOmConnection) ReadOrganization(orgID string) (*Organization, error) {
	ans, err := oc.get(fmt.Sprintf("/api/public/v1.0/orgs/%s", orgID))
	if err != nil {
		return nil, err
	}
	organization := &Organization{}
	if err := json.Unmarshal(ans, organization); err != nil {
		return nil, NewAPIError(err)
	}

	return organization, nil
}

// ReadProjectsInOrganization returns all projects inside organization
func (oc *HTTPOmConnection) ReadProjectsInOrganization(orgID string, page int) (Paginated, error) {
	mPath := fmt.Sprintf("/api/public/v1.0/orgs/%s/groups?itemsPerPage=500&pageNum=%d", orgID, page)
	res, err := oc.get(mPath)
	if err != nil {
		return nil, err
	}

	groupsResponse := &ProjectsResponse{}
	if err := json.Unmarshal(res, groupsResponse); err != nil {
		return nil, NewAPIError(err)
	}

	return groupsResponse, nil
}

// CreateProject
func (oc *HTTPOmConnection) CreateProject(project *Project) (*Project, error) {
	res, err := oc.post("/api/public/v1.0/groups", project)

	if err != nil {
		return nil, err
	}

	g := &Project{}
	if err := json.Unmarshal(res, g); err != nil {
		return nil, NewAPIError(err)
	}

	return g, nil
}

// UpdateProject
func (oc *HTTPOmConnection) UpdateProject(project *Project) (*Project, error) {
	path := fmt.Sprintf("/api/public/v1.0/groups/%s", project.ID)
	res, err := oc.patch(path, project)

	if err != nil {
		return nil, err
	}

	g := &Project{}
	if err := json.Unmarshal(res, g); err != nil {
		return nil, NewAPIError(err)
	}

	return project, nil
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

func (oc *HTTPOmConnection) ReadMonitoringAgentConfig() (*MonitoringAgentConfig, error) {
	ans, err := oc.get(fmt.Sprintf("/api/public/v1.0/groups/%s/automationConfig/monitoringAgentConfig", oc.GroupID()))
	if err != nil {
		return nil, err
	}

	mat, err := BuildMonitoringAgentConfigFromBytes(ans)

	if err != nil {
		return nil, err
	}
	return mat, nil
}

func (oc *HTTPOmConnection) UpdateMonitoringAgentConfig(mat *MonitoringAgentConfig) ([]byte, error) {
	err := mat.Apply()
	if err != nil {
		return nil, err
	}
	return oc.put(fmt.Sprintf("/api/public/v1.0/groups/%s/automationConfig/monitoringAgentConfig", oc.GroupID()), mat.BackingMap)
}

func (oc *HTTPOmConnection) ReadUpdateMonitoringAgentConfig(matFunc func(*MonitoringAgentConfig) error, mutex *sync.Mutex, log *zap.SugaredLogger) error {
	mutex.Lock()
	defer mutex.Unlock()

	mat, err := oc.ReadMonitoringAgentConfig()
	if err != nil {
		return err
	}

	if err := matFunc(mat); err != nil {
		return err
	}

	if _, err := oc.UpdateMonitoringAgentConfig(mat); err != nil {
		return err
	}

	return nil
}

func (oc *HTTPOmConnection) ReadBackupAgentConfig() (*BackupAgentConfig, error) {
	ans, err := oc.get(fmt.Sprintf("/api/public/v1.0/groups/%s/automationConfig/backupAgentConfig", oc.GroupID()))
	if err != nil {
		return nil, err
	}

	backup, err := BuildBackupAgentConfigFromBytes(ans)

	if err != nil {
		return nil, err
	}

	return backup, nil
}

func (oc *HTTPOmConnection) UpdateBackupAgentConfig(backup *BackupAgentConfig) ([]byte, error) {
	err := backup.Apply()
	if err != nil {
		return nil, err
	}
	return oc.put(fmt.Sprintf("/api/public/v1.0/groups/%s/automationConfig/backupAgentConfig", oc.GroupID()), backup.BackingMap)

}

func (oc *HTTPOmConnection) ReadUpdateBackupAgentConfig(backupFunc func(*BackupAgentConfig) error, mutex *sync.Mutex, log *zap.SugaredLogger) error {
	mutex.Lock()
	defer mutex.Unlock()

	backup, err := oc.ReadBackupAgentConfig()
	if err != nil {
		return err
	}

	if err := backupFunc(backup); err != nil {
		return err
	}

	if _, err := oc.UpdateBackupAgentConfig(backup); err != nil {
		return err
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
	client, err := oc.getHTTPClient()
	if err != nil {
		return nil, err
	}

	response, err := request(method, oc.BaseURL(), path, v, oc.User(), oc.PublicAPIKey(), client)
	return response, err
}

func (oc *HTTPOmConnection) getHTTPClient() (*http.Client, error) {
	if oc.context.CACertificate != "" {
		zap.S().Debug("Using CA Certificate ")
		return util.NewHTTPClient(util.OptionCAValidate(oc.context.CACertificate))
	}

	if oc.context.AllowInvalidSSLCertificate {
		zap.S().Debug("Allowing insecure certs")
		return util.NewHTTPClient(util.OptionSkipVerify)
	}

	return util.NewHTTPClient()
}

func request(method, hostname, path string, v interface{}, user string, token string, client *http.Client) ([]byte, error) {
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

	var body []byte
	// Change this to a more flexible solution, depending on the SSL configuration
	resp, err := client.Do(req)
	if err != nil {
		return nil, NewAPIError(err)
	}
	if resp != nil && resp.Body != nil {
		defer resp.Body.Close()
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return nil, NewAPIError(
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
	req, err = createHTTPRequest(method, url, buffer)

	req.Header.Set("Authorization", getDigestAuthorization(digestParts, method, path, user, token))

	// DEV: uncomment this to see full http request. Set to 'true' to to see the request body
	//zap.S().Debugf("Ops Manager request: \n %s", httputil.DumpRequest(req, false))
	zap.S().Debugf("Ops Manager request: %s %s", method, url) // pass string(request) to see full http request

	resp, err = client.Do(req)

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
