package om

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"

	"github.com/blang/semver"
	"github.com/r3labs/diff/v3"
	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/utils/ptr"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/automationconfig"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/controllers/om/api"
	"github.com/10gen/ops-manager-kubernetes/controllers/om/apierror"
	"github.com/10gen/ops-manager-kubernetes/controllers/om/backup"
	"github.com/10gen/ops-manager-kubernetes/controllers/om/host"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/controlledfeature"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/maputil"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/versionutil"
)

// TODO move it to 'api' package

// Connection is a client interacting with OpsManager API. Note, that all methods returning 'error' return the
// '*Error' in fact, but it's error-prone to declare method as returning specific implementation of error
// (see https://golang.org/doc/faq#nil_error)
type Connection interface {
	UpdateDeployment(deployment Deployment) ([]byte, error)
	ReadDeployment() (Deployment, error)

	// ReadUpdateDeployment reads Deployment from Ops Manager, applies the update function to it and pushes it back
	ReadUpdateDeployment(depFunc func(Deployment) error, log *zap.SugaredLogger) error
	ReadUpdateAgentsLogRotation(logRotateSetting mdbv1.AgentConfig, log *zap.SugaredLogger) error
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

	ReadAgentVersion() (AgentsVersionsResponse, error)

	GetPreferredHostnames(agentApiKey string) ([]PreferredHostname, error)
	AddPreferredHostname(agentApiKey string, value string, isRegexp bool) error

	backup.GroupConfigReader
	backup.GroupConfigUpdater

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
	host.Updater

	controlledfeature.Getter
	controlledfeature.Updater

	BaseURL() string
	GroupID() string
	GroupName() string
	OrgID() string
	PublicKey() string
	PrivateKey() string

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

	// Calls the API to update all the MongoDB Agents in the project to the latest. Returns the new version
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
	AppDBDatabaseType BackingDatabaseType = "APP_DB"
)

// ConnectionFactory type defines a function to create a connection to Ops Manager API.
// That's the way we implement some kind of Template Factory pattern to create connections using some incoming parameters
// (groupId, api key etc. - all encapsulated into 'context'). The reconciler later uses this factory to build real
// connections and this allows us to mock out Ops Manager communication during unit testing
type ConnectionFactory func(context *OMContext) Connection

// OMContext is the convenient way of grouping all OM related information together
type OMContext struct {
	BaseURL    string
	GroupID    string
	GroupName  string
	OrgID      string
	PrivateKey string
	PublicKey  string
	Version    versionutil.OpsManagerVersion

	// Will check that the SSL certificate provided by the Ops Manager Server is valid
	// I've decided to use a "AllowInvalid" instead of "RequireValid" as the Zero value
	// of bool is false.
	AllowInvalidSSLCertificate bool

	// CACertificate is the actual certificate as a string, as every "Project" could have
	// its own certificate.
	CACertificate string
}

type HTTPOmConnection struct {
	context *OMContext
	once    sync.Once
	client  *api.Client
}

func (oc *HTTPOmConnection) ReadUpdateAgentsLogRotation(logRotateSetting mdbv1.AgentConfig, log *zap.SugaredLogger) error {
	// We don't have to wait for each step for the agent to reach goal state as setting logrotation does not require order
	if logRotateSetting.Mongod.LogRotate == nil && logRotateSetting.MonitoringAgent.LogRotate == nil &&
		logRotateSetting.BackupAgent.LogRotate == nil && logRotateSetting.Mongod.AuditLogRotate == nil {
		return nil
	}

	automationConfig, err := oc.ReadAutomationConfig()
	if err != nil {
		return err
	}

	if len(automationConfig.Deployment.getProcesses()) > 0 && logRotateSetting.Mongod.LogRotate != nil {
		omVersion, err := oc.OpsManagerVersion().Semver()
		if err != nil {
			log.Debugw("Failed to fetch OpsManager version: %s", err)
			return nil
		}

		// We only support process configuration for OM larger than 7.0.4 or 6.0.24
		if !oc.OpsManagerVersion().IsCloudManager() && !omVersion.GTE(semver.MustParse("7.0.4")) && !omVersion.GTE(semver.MustParse("6.0.24")) {
			return xerrors.Errorf("configuring log rotation for mongod processes is supported only with Cloud Manager or Ops Manager with versions >= 7.0.4 or >= 6.0.24")
		}

		// We only retrieve the first process, since logRotation is configured the same for all processes
		process := automationConfig.Deployment.getProcesses()[0]
		if err = updateProcessLogRotateIfChanged(logRotateSetting.Mongod.LogRotate, process.GetLogRotate(), oc.UpdateProcessLogRotation); err != nil {
			return err
		}
		if err = updateProcessLogRotateIfChanged(logRotateSetting.Mongod.AuditLogRotate, process.GetAuditLogRotate(), oc.UpdateAuditLogRotation); err != nil {
			return err
		}
	}

	if len(automationConfig.Deployment.getBackupVersions()) > 0 && logRotateSetting.BackupAgent.LogRotate != nil {
		err = oc.ReadUpdateBackupAgentConfig(func(config *BackupAgentConfig) error {
			config.SetLogRotate(*logRotateSetting.BackupAgent.LogRotate)
			return nil
		}, log)
	}

	if len(automationConfig.Deployment.getMonitoringVersions()) > 0 && logRotateSetting.MonitoringAgent.LogRotate != nil {
		err = oc.ReadUpdateMonitoringAgentConfig(func(config *MonitoringAgentConfig) error {
			config.SetLogRotate(*logRotateSetting.MonitoringAgent.LogRotate)
			return nil
		}, log)
	}

	return err
}

func updateProcessLogRotateIfChanged(logRotateSettingFromCRD *automationconfig.CrdLogRotate, logRotationSettingFromWire map[string]interface{}, updateLogRotationSetting func(logRotateSetting automationconfig.AcLogRotate) ([]byte, error)) error {
	logRotationToSetInAC := automationconfig.ConvertCrdLogRotateToAC(logRotateSettingFromCRD)
	if logRotationToSetInAC == nil {
		return nil
	}
	toMap, err := maputil.StructToMap(logRotationToSetInAC)
	if err != nil {
		return err
	}

	// We only support setting the same log rotation for all agents for the same type the same rotation config
	if equality.Semantic.DeepEqual(logRotationSettingFromWire, toMap) {
		return nil
	}

	_, err = updateLogRotationSetting(*logRotationToSetInAC)
	return err
}

func (oc *HTTPOmConnection) UpdateProcessLogRotation(logRotateSetting automationconfig.AcLogRotate) ([]byte, error) {
	return oc.put(fmt.Sprintf("/api/public/v1.0/groups/%s/automationConfig/systemLogRotateConfig", oc.GroupID()), logRotateSetting)
}

func (oc *HTTPOmConnection) UpdateAuditLogRotation(logRotateSetting automationconfig.AcLogRotate) ([]byte, error) {
	return oc.put(fmt.Sprintf("/api/public/v1.0/groups/%s/automationConfig/auditLogRotateConfig", oc.GroupID()), logRotateSetting)
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
	return &HTTPOmConnection{
		context: context,
	}
}

func (oc *HTTPOmConnection) ReadGroupBackupConfig() (backup.GroupBackupConfig, error) {
	ans, apiErr := oc.get(fmt.Sprintf("/api/public/v1.0/admin/backup/groups/%s", oc.GroupID()))

	if apiErr != nil {
		// This API provides very inconsistent way for obtaining values and authorization. In certain Ops Manager versions
		// when there's no Group Backup Config we get 404 (which is inconsistent with the UI, as the endpoints for UI
		// always return values). In Ops Manager 6, if this object doesn't yet exist, we get 401. So the only reasonable
		// thing to do here is to check whether the error comes from the Ops Manager (if we can parse it), and if we do,
		// we just ignore it.
		var err *apierror.Error
		ok := errors.As(apiErr, &err)
		if ok {
			return backup.GroupBackupConfig{
				Id: ptr.To(oc.GroupID()),
			}, nil
		}
		return backup.GroupBackupConfig{}, apiErr
	}
	groupBackupConfig := &backup.GroupBackupConfig{}
	if err := json.Unmarshal(ans, groupBackupConfig); err != nil {
		return backup.GroupBackupConfig{}, apierror.New(err)
	}

	return *groupBackupConfig, nil
}

func (oc *HTTPOmConnection) UpdateGroupBackupConfig(config backup.GroupBackupConfig) ([]byte, error) {
	return oc.put(fmt.Sprintf("/api/public/v1.0/admin/backup/groups/%s", *config.Id), config)
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

// PublicKey returns PublicKey of HTTPOmConnection
func (oc *HTTPOmConnection) PublicKey() string {
	return oc.context.PublicKey
}

// PrivateKey returns PrivateKey of HTTPOmConnection
func (oc *HTTPOmConnection) PrivateKey() string {
	return oc.context.PrivateKey
}

// OpsManagerVersion returns the current Ops Manager version
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
func (oc *HTTPOmConnection) ReadUpdateDeployment(changeDeploymentFunc func(Deployment) error, log *zap.SugaredLogger) error {
	mutex := GetMutex(oc.GroupName(), oc.OrgID())
	mutex.Lock()
	defer mutex.Unlock()
	deployment, err := oc.ReadDeployment()
	if err != nil {
		return err
	}

	isEqual, err := isEqualAfterModification(changeDeploymentFunc, deployment)
	if err != nil {
		return err
	}
	if isEqual {
		log.Debug("AutomationConfig has not changed, not pushing changes to Ops Manager")
		return nil
	}

	_, err = oc.UpdateDeployment(deployment)
	if util.ShouldLogAutomationConfigDiff() {
		originalDeployment, err := oc.ReadDeployment()
		if err != nil {
			return apierror.New(err)
		}

		changelog, err := diff.Diff(originalDeployment, deployment, diff.AllowTypeMismatch(true))
		if err != nil {
			return apierror.New(err)
		}

		log.Debug("Deployment diff (%d changes): %+v", len(changelog), changelog)
	}
	if err != nil {
		return apierror.New(err)
	}
	return nil
}

func (oc *HTTPOmConnection) UpdateAutomationConfig(ac *AutomationConfig, log *zap.SugaredLogger) error {
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

func (oc *HTTPOmConnection) ReadUpdateAutomationConfig(modifyACFunc func(ac *AutomationConfig) error, log *zap.SugaredLogger) error {
	mutex := GetMutex(oc.GroupName(), oc.OrgID())
	mutex.Lock()
	defer mutex.Unlock()

	ac, err := oc.ReadAutomationConfig()
	if err != nil {
		log.Errorf("error reading automation config. %s", err)
		return err
	}

	original, err := BuildAutomationConfigFromDeployment(ac.Deployment)
	if err != nil {
		return err
	}

	if err := modifyACFunc(ac); err != nil {
		return apierror.New(err)
	}

	if !reflect.DeepEqual(original.Deployment, ac.Deployment) {
		panic("It seems you modified the deployment directly. This is not allowed. Please use helper objects instead.")
	}

	areEqual := original.EqualsWithoutDeployment(*ac)
	if areEqual {
		log.Debug("AutomationConfig has not changed, not pushing changes to Ops Manager")
		return nil
	}

	// we are using UpdateAutomationConfig since we need to apply our changes.
	err = oc.UpdateAutomationConfig(ac, log)
	if util.ShouldLogAutomationConfigDiff() {
		changelog, err := diff.Diff(original.Deployment, ac.Deployment, diff.AllowTypeMismatch(true))
		if err != nil {
			return apierror.New(err)
		}

		log.Debug("Deployment diff (%d changes): %+v", len(changelog), changelog)
	}
	if err != nil {
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
	var resp AutomationAgentStatusResponse
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

// UpdateHost adds the given host.
func (oc *HTTPOmConnection) UpdateHost(host host.Host) error {
	_, err := oc.patch(fmt.Sprintf("/api/public/v1.0/groups/%s/hosts/%s", oc.GroupID(), host.Id), host)
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
		var apiErr *apierror.Error
		if errors.As(err, &apiErr) {
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
	original, _ := util.MapDeepCopy(mat.BackingMap)

	err := mat.Apply()
	if err != nil {
		return nil, err
	}

	if reflect.DeepEqual(original, mat.BackingMap) {
		log.Debug("Monitoring Configuration has not changed, not pushing changes to Ops Manager")
	} else {
		return oc.put(fmt.Sprintf("/api/public/v1.0/groups/%s/automationConfig/monitoringAgentConfig", oc.GroupID()), mat.BackingMap)
	}
	return nil, nil
}

func (oc *HTTPOmConnection) ReadUpdateMonitoringAgentConfig(modifyMonitoringAgentFunction func(*MonitoringAgentConfig) error, log *zap.SugaredLogger) error {
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

	if err := modifyMonitoringAgentFunction(mat); err != nil {
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

	backupAgentConfig, err := BuildBackupAgentConfigFromBytes(ans)
	if err != nil {
		return nil, err
	}

	return backupAgentConfig, nil
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

	backupAgentConfig, err := oc.ReadBackupAgentConfig()
	if err != nil {
		return err
	}

	if err := backupFunc(backupAgentConfig); err != nil {
		return err
	}

	if _, err := oc.UpdateBackupAgentConfig(backupAgentConfig, log); err != nil {
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

func (oc *HTTPOmConnection) ReadSnapshotSchedule(clusterID string) (*backup.SnapshotSchedule, error) {
	mPath := fmt.Sprintf("/api/public/v1.0/groups/%s/backupConfigs/%s/snapshotSchedule", oc.GroupID(), clusterID)
	res, err := oc.get(mPath)
	if err != nil {
		return nil, err
	}

	response := &backup.SnapshotSchedule{}
	if err := json.Unmarshal(res, response); err != nil {
		return nil, apierror.New(err)
	}

	// OM returns 0 if not set instead of null or omitted field
	if response.ClusterCheckpointIntervalMin != nil && *response.ClusterCheckpointIntervalMin == 0 {
		response.ClusterCheckpointIntervalMin = nil
	}

	return response, nil
}

func (oc *HTTPOmConnection) UpdateSnapshotSchedule(clusterID string, snapshotSchedule *backup.SnapshotSchedule) error {
	path := fmt.Sprintf("/api/public/v1.0/groups/%s/backupConfigs/%s/snapshotSchedule", oc.GroupID(), clusterID)
	res, err := oc.patch(path, snapshotSchedule)
	if err != nil {
		return err
	}

	if err := json.Unmarshal(res, &backup.SnapshotSchedule{}); err != nil {
		return apierror.New(err)
	}
	return nil
}

type AgentsVersionsResponse struct {
	AutomationVersion        string `json:"automationVersion"`
	AutomationMinimumVersion string `json:"automationMinimumVersion"`
}

// ReadAgentVersion reads the versions from OM API
func (oc *HTTPOmConnection) ReadAgentVersion() (AgentsVersionsResponse, error) {
	body, err := oc.get("/api/public/v1.0/softwareComponents/versions/")
	if err != nil {
		return AgentsVersionsResponse{}, err
	}
	agentsVersions := &AgentsVersionsResponse{}
	if err := json.Unmarshal(body, agentsVersions); err != nil {
		return AgentsVersionsResponse{}, err
	}
	return *agentsVersions, nil
}

type PreferredHostname struct {
	Regexp   bool   `json:"regexp"`
	EndsWith bool   `json:"endsWith"`
	Id       string `json:"id"`
	Value    string `json:"value"`
}

type GroupInfoResponse struct {
	PreferredHostnames []PreferredHostname `json:"preferredHostnames"`
}

// GetPreferredHostnames will call the info endpoint with the agent API key.
// We extract only the preferred hostnames from the response.
func (oc *HTTPOmConnection) GetPreferredHostnames(agentApiKey string) ([]PreferredHostname, error) {
	infoPath := fmt.Sprintf("/group/v2/info/%s", oc.GroupID())
	body, err := oc.getWithAgentAuth(infoPath, agentApiKey)
	if err != nil {
		return nil, err
	}

	groupInfo := &GroupInfoResponse{}
	if err := json.Unmarshal(body, groupInfo); err != nil {
		return nil, err
	}

	return groupInfo.PreferredHostnames, nil
}

// AddPreferredHostname will add a new preferred hostname.
// This does not check for duplicates. That needs to be checked in the consumer of this method.
// Here we also use the agent API key, so we need to configure basic auth.
// isRegex is true if the preferred hostnames is a regex, and false if it is "endsWith".
// We pass only "isRegex" to eliminate edge cases where both are set to the same value.
func (oc *HTTPOmConnection) AddPreferredHostname(agentApiKey string, value string, isRegexp bool) error {
	path := fmt.Sprintf("/group/v2/addPreferredHostname/%s?value=%s&isRegexp=%s&isEndsWith=%s",
		oc.GroupID(), value, strconv.FormatBool(isRegexp), strconv.FormatBool(!isRegexp))
	_, err := oc.getWithAgentAuth(path, agentApiKey)
	if err != nil {
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

	response, header, err := client.Request(method, oc.BaseURL(), path, v)
	oc.setVersionFromHeader(header)

	return response, err
}

func (oc *HTTPOmConnection) getWithAgentAuth(path string, agentApiKey string) ([]byte, error) {
	client, err := oc.getHTTPClient()
	if err != nil {
		return nil, err
	}

	response, header, err := client.RequestWithAgentAuth("GET", oc.BaseURL(), path, oc.getAgentAuthorization(agentApiKey), nil)
	oc.setVersionFromHeader(header)

	return response, err
}

// getHTTPClient gets a new or an already existing client.
func (oc *HTTPOmConnection) getHTTPClient() (*api.Client, error) {
	var err error

	oc.once.Do(func() {
		opts := api.NewHTTPOptions()

		if oc.context.CACertificate != "" {
			zap.S().Debug("Using CA Certificate")
			opts = append(opts, api.OptionCAValidate(oc.context.CACertificate))
		}

		if oc.context.AllowInvalidSSLCertificate {
			zap.S().Debug("Allowing insecure certs")
			opts = append(opts, api.OptionSkipVerify)
		}

		opts = append(opts, api.OptionDigestAuth(oc.PublicKey(), oc.PrivateKey()))

		if env.ReadBoolOrDefault("OM_DEBUG_HTTP", false) { // nolint:forbidigo
			zap.S().Debug("Enabling OM_DEBUG_HTTP mode")
			opts = append(opts, api.OptionDebug)
		}

		oc.client, err = api.NewHTTPClient(opts...)
	})

	return oc.client, err
}

// getAgentAuthorization generates the basic authorization header
func (oc *HTTPOmConnection) getAgentAuthorization(agentApiKey string) string {
	credentials := oc.GroupID() + ":" + agentApiKey
	encodedCredentials := base64.StdEncoding.EncodeToString([]byte(credentials))
	return "Basic " + encodedCredentials
}

func (oc *HTTPOmConnection) setVersionFromHeader(header http.Header) {
	if header != nil {
		oc.context.Version = versionutil.OpsManagerVersion{
			VersionString: versionutil.GetVersionFromOpsManagerApiHeader(header.Get("X-MongoDB-Service-Version")),
		}
	}
}
