package om

import (
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"sync"

	apierror "github.com/10gen/ops-manager-kubernetes/pkg/controller/om/apierror"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om/backup"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/versionutil"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om/host"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/controlledfeature"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om/api"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"go.uber.org/zap"
)

// TODO move it to 'api' package

// Connection is a client interacting with OpsManager API. Note, that all methods returning 'error' return the
// '*Error' in fact but it's error-prone to declare method as returning specific implementation of error
// (see https://golang.org/doc/faq#nil_error)
type Connection interface {
	UpdateDeployment(deployment Deployment) ([]byte, error)
	ReadDeployment() (Deployment, error)

	// ReadUpdateDeployment reads Deployment from Ops Manager, applies the update function to it and pushes it back
	// Note the mutex that must be passed to provide strict serializability for the read-write operations for the same group
	ReadUpdateDeployment(depFunc func(Deployment) error, log *zap.SugaredLogger) error

	//WaitForReadyState(processNames []string, log *zap.SugaredLogger) error
	ReadAutomationStatus() (*AutomationStatus, error)
	ReadAutomationAgents(page int) (Paginated, error)
	MarkProjectAsBackingDatabase(databaseType BackingDatabaseType) error

	ReadOrganizationsByName(name string) ([]*Organization, error)
	// ReadOrganizations returns all organizations at specified page
	ReadOrganizations(page int) (Paginated, error)
	ReadOrganization(orgID string) (*Organization, error)

	ReadProjectsInOrganizationByName(orgID string, name string) ([]*Project, error)
	// ReadProjectsInOrganization returns all projects in the organization at the specified page
	ReadProjectsInOrganization(orgID string, page int) (Paginated, error)
	CreateProject(project *Project) (*Project, error)
	UpdateProject(project *Project) (*Project, error)

	backup.HostClusterReader

	backup.ConfigReader
	backup.ConfigUpdater

	OpsManagerVersion() versionutil.OpsManagerVersion

	AgentKeyGenerator

	AutomationConfigConnection
	MonitoringConfigConnection
	BackupConfigConnection
	HasAgentAuthMode

	host.Adder
	host.GetRemover

	controlledfeature.Getter
	controlledfeature.Updater

	BaseURL() string
	GroupID() string
	GroupName() string
	OrgID() string
	User() string
	PublicAPIKey() string

	// ConfigureProject configures the OMContext to have the correct project and org ids
	ConfigureProject(project *Project)
}

type MonitoringConfigConnection interface {
	ReadMonitoringAgentConfig() (*MonitoringAgentConfig, error)
	UpdateMonitoringAgentConfig(mat *MonitoringAgentConfig, log *zap.SugaredLogger) ([]byte, error)
	ReadUpdateMonitoringAgentConfig(matFunc func(*MonitoringAgentConfig) error, log *zap.SugaredLogger) error
}

type BackupConfigConnection interface {
	ReadBackupAgentConfig() (*BackupAgentConfig, error)
	UpdateBackupAgentConfig(mat *BackupAgentConfig, log *zap.SugaredLogger) ([]byte, error)
	ReadUpdateBackupAgentConfig(matFunc func(*BackupAgentConfig) error, log *zap.SugaredLogger) error
}

type HasAgentAuthMode interface {
	GetAgentAuthMode() (string, error)
}

type AgentKeyGenerator interface {
	GenerateAgentKey() (string, error)
}

// AutomationConfigConnection is an interface that only deals with reading/updating of the AutomationConfig
type AutomationConfigConnection interface {
	// UpdateAutomationConfig updates the Automation Config in Ops Manager
	// Note, that this method calls *the same* api endpoint as the `OmConnection.UpdateDeployment` - just uses a
	// Deployment wrapper (AutomationConfig) as a parameter
	UpdateAutomationConfig(ac *AutomationConfig, log *zap.SugaredLogger) error
	ReadAutomationConfig() (*AutomationConfig, error)
	// ReadAutomationConfig reads the Automation Config from Ops Manager
	// Note, that this method calls *the same* api endpoint as the `OmConnection.ReadDeployment` - just wraps the answer
	// to the different object
	ReadUpdateAutomationConfig(acFunc func(ac *AutomationConfig) error, log *zap.SugaredLogger) error

	// Calls the API to update all the MongoDB Agents in the project to latest. Returns the new version
	UpgradeAgentsToLatest() (string, error)
}

// omMutexes is the synchronous map of mutexes that provide strict serializability for operations "read-modify-write"
// for Ops Manager. Keys are (group_name + org_id) and values are mutexes.
var omMutexes = sync.Map{}

// GetMutex creates or reuses the relevant mutex for the group + org
func GetMutex(projectName, orgId string) *sync.Mutex {
	lockName := projectName + orgId
	mutex, _ := omMutexes.LoadOrStore(lockName, &sync.Mutex{})
	return mutex.(*sync.Mutex)
}

type BackingDatabaseType string

const (
	AppDBDatabaseType      BackingDatabaseType = "APP_DB"
	BlockStoreDatabaseType BackingDatabaseType = "BLOCKSTORE_DB"
	OplogDatabaseType      BackingDatabaseType = "OPLOG_DB"
)

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
	Version      versionutil.OpsManagerVersion

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

func (oc *HTTPOmConnection) GetAgentAuthMode() (string, error) {
	ac, err := oc.ReadAutomationConfig()
	if err != nil {
		return "", err
	}
	if ac.Auth == nil {
		return "", nil
	}
	return ac.Auth.AutoAuthMechanism, nil
}

var _ Connection = &HTTPOmConnection{}

// NewOpsManagerConnection stores OpsManger api endpoint and authentication credentials.
// It makes it easy to call the API without having to explicitly provide connection details.
func NewOpsManagerConnection(context *OMContext) Connection {
	return &HTTPOmConnection{context: context}
}

func (oc *HTTPOmConnection) ConfigureProject(project *Project) {
	oc.context.GroupID = project.ID
	oc.context.OrgID = project.OrgID
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

// GetOpsManagerVersion returns the current Ops Manager version
func (oc *HTTPOmConnection) OpsManagerVersion() versionutil.OpsManagerVersion {
	return oc.context.Version
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
	return d, apierror.New(e)
}

func (oc *HTTPOmConnection) ReadAutomationConfig() (*AutomationConfig, error) {
	ans, err := oc.get(fmt.Sprintf("/api/public/v1.0/groups/%s/automationConfig", oc.GroupID()))

	if err != nil {
		return nil, err
	}

	ac, err := BuildAutomationConfigFromBytes(ans)

	return ac, apierror.New(err)
}

// ReadUpdateDeployment performs the "read-modify-update" operation on OpsManager Deployment.
// Note, that the mutex locks infinitely (there is no built-in support for timeouts for locks in Go) which seems to be
// ok as OM endpoints are not supposed to hang for long
func (oc *HTTPOmConnection) ReadUpdateDeployment(depFunc func(Deployment) error, log *zap.SugaredLogger) error {
	mutex := GetMutex(oc.GroupName(), oc.OrgID())
	mutex.Lock()
	defer mutex.Unlock()

	deployment, err := oc.ReadDeployment()
	if err != nil {
		return err
	}

	original, err := util.MapDeepCopy(deployment)
	if err != nil {
		return err
	}
	if err := depFunc(deployment); err != nil {
		return apierror.New(err)
	}

	if reflect.DeepEqual(original, deployment) {
		log.Debug("Deployment has not changed, not pushing changes to Ops Manager")
	} else {
		_, err = oc.UpdateDeployment(deployment)
		if err != nil {
			if util.ShouldLogAutomationConfigDiff() {
				var originalDeployment Deployment = original
				log.Debug("Current Automation Config")
				originalDeployment.Debug(log)
				log.Debug("Invalid Automation Config")
				deployment.Debug(log)
			}

			return err
		}
	}

	return nil
}

func (oc *HTTPOmConnection) UpdateAutomationConfig(ac *AutomationConfig, log *zap.SugaredLogger) error {
	original, err := util.MapDeepCopy(ac.Deployment)
	if err != nil {
		return err
	}
	err = ac.Apply()
	if err != nil {
		return err
	}

	if reflect.DeepEqual(original, ac.Deployment) {
		log.Debug("AutomationConfig has not changed, not pushing changes to Ops Manager")
	} else {
		_, err = oc.UpdateDeployment(ac.Deployment)
		if err != nil {
			return err
		}
	}
	return nil
}

func (oc *HTTPOmConnection) ReadUpdateAutomationConfig(acFunc func(ac *AutomationConfig) error, log *zap.SugaredLogger) error {
	mutex := GetMutex(oc.GroupName(), oc.OrgID())
	mutex.Lock()
	defer mutex.Unlock()

	ac, err := oc.ReadAutomationConfig()
	if err != nil {
		log.Errorf("error reading automation config. %s", err)
		return err
	}

	original, err := util.MapDeepCopy(ac.Deployment)
	if err != nil {
		return err
	}
	if err := acFunc(ac); err != nil {
		return apierror.New(err)
	}

	err = oc.UpdateAutomationConfig(ac, log)
	if err != nil {
		if util.ShouldLogAutomationConfigDiff() {
			var originalDeployment Deployment = original
			log.Debug("Current Automation Config")
			originalDeployment.Debug(log)
			log.Debug("Invalid Automation Config")
			ac.Deployment.Debug(log)
		}
		log.Errorf("error updating automation config. %s", err)
		return apierror.New(err)
	}
	return nil
}

func (oc *HTTPOmConnection) UpgradeAgentsToLatest() (string, error) {
	ans, err := oc.post(fmt.Sprintf("/api/public/v1.0/groups/%s/automationConfig/updateAgentVersions", oc.GroupID()), nil)

	if err != nil {
		return "", err
	}
	type updateAgentsVersionsResponse struct {
		AutomationAgentVersion string `json:"automationAgentVersion"`
	}
	var response updateAgentsVersionsResponse
	if err = json.Unmarshal(ans, &response); err != nil {
		return "", apierror.New(err)
	}
	return response.AutomationAgentVersion, nil
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
		return "", apierror.New(err)
	}
	return keyInfo["key"].(string), nil
}

// ReadAutomationAgents returns the state of the automation agents registered in Ops Manager
func (oc *HTTPOmConnection) ReadAutomationAgents(pageNum int) (Paginated, error) {
	// TODO: Add proper testing to this pagination. In order to test it I just used `itemsPerPage=1`, which will make
	// the endpoint to be called 3 times in a 3 member replica set. The default itemsPerPage is 100
	ans, err := oc.get(fmt.Sprintf("/api/public/v1.0/groups/%s/agents/AUTOMATION?pageNum=%d", oc.GroupID(), pageNum))
	if err != nil {
		return nil, err
	}
	var resp automationAgentStatusResponse
	if err := json.Unmarshal(ans, &resp); err != nil {
		return nil, err
	}
	return resp, apierror.New(err)
}

// ReadAutomationStatus returns the state of the automation status, this includes if the agents
// have reached goal state.
func (oc *HTTPOmConnection) ReadAutomationStatus() (*AutomationStatus, error) {
	ans, err := oc.get(fmt.Sprintf("/api/public/v1.0/groups/%s/automationStatus", oc.GroupID()))
	if err != nil {
		return nil, err
	}
	status, e := buildAutomationStatusFromBytes(ans)
	return status, apierror.New(e)
}

// AddHost adds the given host to the project
func (oc *HTTPOmConnection) AddHost(host host.Host) error {
	_, err := oc.post(fmt.Sprintf("/api/public/v1.0/groups/%s/hosts", oc.GroupID()), host)
	return err
}

// GetHosts return the hosts in this group
func (oc *HTTPOmConnection) GetHosts() (*host.Result, error) {
	mPath := fmt.Sprintf("/api/public/v1.0/groups/%s/hosts/", oc.GroupID())
	res, err := oc.get(mPath)
	if err != nil {
		return nil, err
	}

	hosts := &host.Result{}
	if err := json.Unmarshal(res, hosts); err != nil {
		return nil, apierror.New(err)
	}

	return hosts, nil
}

// RemoveHost will remove host, identified by hostID from group
func (oc *HTTPOmConnection) RemoveHost(hostID string) error {
	mPath := fmt.Sprintf("/api/public/v1.0/groups/%s/hosts/%s", oc.GroupID(), hostID)
	return oc.delete(mPath)
}

// ReadOrganizationsByName finds the organizations by name. It uses the same endpoint as the 'ReadOrganizations' but
// 'name' and 'page' parameters are not supposed to be used together so having a separate endpoint allows
func (oc *HTTPOmConnection) ReadOrganizationsByName(name string) ([]*Organization, error) {
	mPath := fmt.Sprintf("/api/public/v1.0/orgs?name=%s", url.QueryEscape(name))
	res, err := oc.get(mPath)
	if err != nil {
		return nil, err
	}

	orgsResponse := &OrganizationsResponse{}
	if err = json.Unmarshal(res, orgsResponse); err != nil {
		return nil, apierror.New(err)
	}

	return orgsResponse.Organizations, nil
}

// ReadOrganizations returns all organizations at the specified page.
func (oc *HTTPOmConnection) ReadOrganizations(page int) (Paginated, error) {
	mPath := fmt.Sprintf("/api/public/v1.0/orgs?itemsPerPage=500&pageNum=%d", page)
	res, err := oc.get(mPath)
	if err != nil {
		return nil, err
	}

	orgsResponse := &OrganizationsResponse{}
	if err := json.Unmarshal(res, orgsResponse); err != nil {
		return nil, apierror.New(err)
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
		return nil, apierror.New(err)
	}

	return organization, nil
}

func (oc *HTTPOmConnection) MarkProjectAsBackingDatabase(backingType BackingDatabaseType) error {
	_, err := oc.post(fmt.Sprintf("/api/private/v1.0/groups/%s/markAsBackingDatabase", oc.GroupID()), string(backingType))
	if err != nil {
		if apiErr, ok := err.(*apierror.Error); ok {
			if apiErr.Status != nil && *apiErr.Status == 400 && strings.Contains(apiErr.Detail, "INVALID_DOCUMENT") {
				return nil
			}
		}
		return err
	}
	return nil
}

func (oc *HTTPOmConnection) ReadProjectsInOrganizationByName(orgID string, name string) ([]*Project, error) {
	mPath := fmt.Sprintf("/api/public/v1.0/orgs/%s/groups?name=%s", orgID, url.QueryEscape(name))
	res, err := oc.get(mPath)
	if err != nil {
		return nil, err
	}

	projectsResponse := &ProjectsResponse{}
	if err := json.Unmarshal(res, projectsResponse); err != nil {
		return nil, apierror.New(err)
	}

	return projectsResponse.Groups, nil
}

// ReadProjectsInOrganization returns all projects inside organization
func (oc *HTTPOmConnection) ReadProjectsInOrganization(orgID string, page int) (Paginated, error) {
	mPath := fmt.Sprintf("/api/public/v1.0/orgs/%s/groups?itemsPerPage=500&pageNum=%d", orgID, page)
	res, err := oc.get(mPath)
	if err != nil {
		return nil, err
	}

	projectsResponse := &ProjectsResponse{}
	if err := json.Unmarshal(res, projectsResponse); err != nil {
		return nil, apierror.New(err)
	}

	return projectsResponse, nil
}

// CreateProject
func (oc *HTTPOmConnection) CreateProject(project *Project) (*Project, error) {
	res, err := oc.post("/api/public/v1.0/groups", project)

	if err != nil {
		return nil, err
	}

	g := &Project{}
	if err := json.Unmarshal(res, g); err != nil {
		return nil, apierror.New(err)
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
		return nil, apierror.New(err)
	}

	return project, nil
}

// ReadBackupConfigs
func (oc *HTTPOmConnection) ReadBackupConfigs() (*backup.ConfigsResponse, error) {
	mPath := fmt.Sprintf("/api/public/v1.0/groups/%s/backupConfigs", oc.GroupID())
	res, err := oc.get(mPath)
	if err != nil {
		return nil, err
	}

	response := &backup.ConfigsResponse{}
	if err := json.Unmarshal(res, response); err != nil {
		return nil, apierror.New(err)
	}

	return response, nil
}

// ReadBackupConfig
func (oc *HTTPOmConnection) ReadBackupConfig(clusterID string) (*backup.Config, error) {
	mPath := fmt.Sprintf("/api/public/v1.0/groups/%s/backupConfigs/%s", oc.GroupID(), clusterID)
	res, err := oc.get(mPath)
	if err != nil {
		return nil, err
	}

	response := &backup.Config{}
	if err := json.Unmarshal(res, response); err != nil {
		return nil, apierror.New(err)
	}

	return response, nil
}

// UpdateBackupConfig
func (oc *HTTPOmConnection) UpdateBackupConfig(config *backup.Config) (*backup.Config, error) {
	path := fmt.Sprintf("/api/public/v1.0/groups/%s/backupConfigs/%s", oc.GroupID(), config.ClusterId)
	res, err := oc.patch(path, config)
	if err != nil {
		return nil, err
	}

	response := &backup.Config{}
	if err := json.Unmarshal(res, response); err != nil {
		return nil, apierror.New(err)
	}
	return response, nil
}

// ReadHostCluster
func (oc *HTTPOmConnection) ReadHostCluster(clusterID string) (*backup.HostCluster, error) {
	mPath := fmt.Sprintf("/api/public/v1.0/groups/%s/clusters/%s", oc.GroupID(), clusterID)
	res, err := oc.get(mPath)
	if err != nil {
		return nil, err
	}

	cluster := &backup.HostCluster{}
	if err := json.Unmarshal(res, cluster); err != nil {
		return nil, apierror.New(err)
	}

	return cluster, nil
}

// UpdateBackupStatus
func (oc *HTTPOmConnection) UpdateBackupStatus(clusterID string, status backup.Status) error {
	path := fmt.Sprintf("/api/public/v1.0/groups/%s/backupConfigs/%s", oc.GroupID(), clusterID)

	_, err := oc.patch(path, map[string]interface{}{"statusName": status})

	if err != nil {
		return apierror.New(err)
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

func (oc *HTTPOmConnection) UpdateMonitoringAgentConfig(mat *MonitoringAgentConfig, log *zap.SugaredLogger) ([]byte, error) {
	err := mat.Apply()
	if err != nil {
		return nil, err
	}
	return oc.put(fmt.Sprintf("/api/public/v1.0/groups/%s/automationConfig/monitoringAgentConfig", oc.GroupID()), mat.BackingMap)
}

func (oc *HTTPOmConnection) ReadUpdateMonitoringAgentConfig(matFunc func(*MonitoringAgentConfig) error, log *zap.SugaredLogger) error {
	if log == nil {
		log = zap.S()
	}
	mutex := GetMutex(oc.GroupName(), oc.OrgID())
	mutex.Lock()
	defer mutex.Unlock()

	mat, err := oc.ReadMonitoringAgentConfig()
	if err != nil {
		return err
	}

	if err := matFunc(mat); err != nil {
		return err
	}

	if _, err := oc.UpdateMonitoringAgentConfig(mat, log); err != nil {
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

func (oc *HTTPOmConnection) UpdateBackupAgentConfig(backup *BackupAgentConfig, log *zap.SugaredLogger) ([]byte, error) {
	original, _ := util.MapDeepCopy(backup.BackingMap)

	err := backup.Apply()
	if err != nil {
		return nil, err
	}

	if reflect.DeepEqual(original, backup.BackingMap) {
		log.Debug("Backup Configuration has not changed, not pushing changes to Ops Manager")
	} else {
		return oc.put(fmt.Sprintf("/api/public/v1.0/groups/%s/automationConfig/backupAgentConfig", oc.GroupID()), backup.BackingMap)
	}

	return nil, nil
}

func (oc *HTTPOmConnection) ReadUpdateBackupAgentConfig(backupFunc func(*BackupAgentConfig) error, log *zap.SugaredLogger) error {
	if log == nil {
		log = zap.S()
	}
	mutex := GetMutex(oc.GroupName(), oc.OrgID())
	mutex.Lock()
	defer mutex.Unlock()

	backup, err := oc.ReadBackupAgentConfig()
	if err != nil {
		return err
	}

	if err := backupFunc(backup); err != nil {
		return err
	}

	if _, err := oc.UpdateBackupAgentConfig(backup, log); err != nil {
		return err
	}

	return nil
}

func (oc *HTTPOmConnection) UpdateControlledFeature(cf *controlledfeature.ControlledFeature) error {
	_, err := oc.put(fmt.Sprintf("/api/public/v1.0/groups/%s/controlledFeature", oc.GroupID()), cf)
	return err
}

func (oc *HTTPOmConnection) GetControlledFeature() (*controlledfeature.ControlledFeature, error) {
	res, err := oc.get(fmt.Sprintf("/api/public/v1.0/groups/%s/controlledFeature", oc.GroupID()))
	if err != nil {
		return nil, err
	}
	cf := &controlledfeature.ControlledFeature{}
	if err := json.Unmarshal(res, cf); err != nil {
		return nil, apierror.New(err)
	}
	return cf, nil
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

	response, header, err := client.Request(method, oc.BaseURL(), path, v)
	if header != nil {
		oc.context.Version = versionutil.OpsManagerVersion{
			VersionString: versionutil.GetVersionFromOpsManagerApiHeader(header.Get("X-MongoDB-Service-Version")),
		}
	}

	return response, err
}

func (oc *HTTPOmConnection) getHTTPClient() (*api.Client, error) {
	opts := api.NewHTTPOptions()

	if oc.context.CACertificate != "" {
		zap.S().Debug("Using CA Certificate")
		opts = append(opts, api.OptionCAValidate(oc.context.CACertificate))
	}

	if oc.context.AllowInvalidSSLCertificate {
		zap.S().Debug("Allowing insecure certs")
		opts = append(opts, api.OptionSkipVerify)
	}

	opts = append(opts, api.OptionDigestAuth(oc.User(), oc.PublicAPIKey()))

	return api.NewHTTPClient(opts...)
}
