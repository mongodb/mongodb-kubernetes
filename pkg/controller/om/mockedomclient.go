package om

import (
	"fmt"
	"math/rand"
	"reflect"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om/host"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/controlledfeature"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om/api"
	"github.com/google/uuid"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
)

// ********************************************************************************************************************
// Dev notes:
// * this is a mocked implementation of 'Connection' interface which mocks all communication with Ops Manager. It doesn't
//   do anything sophisticated - just saves the state that OM is supposed to have after these invocations - for example
//   the deployment pushed to it
// * The usual place to start from is the 'NewEmptyMockedOmConnection' method that pre-creates the group - convenient
//   in most cases. Should work fine for the "full" reconciliation testing
// * The class tracks the functions called - some methods ('CheckOrderOfOperations', 'CheckOperationsDidntHappen' - more
//   can be added) may help to check the communication happened
// * Any overriding of default behavior can be done via functions (e.g. 'CreateGroupFunc', 'UpdateGroupFunc')
// * To emulate the work of real OM it's possible to emulate the agents delay in "reaching" goal state. This can be
//   configured using 'AgentsDelayCount' property
// * As Deployment has package access to most of its data to preserve encapsulation (processes, ssl etc) this class can
//   be used as an access point to those fields for testing (see 'getProcesses' as an example)
// * Note that the variable 'CurrMockedConnection' is global (as Go tests don't allow to have setup/teardown hooks)
//  The state is cleaned as soon as a new mocked api object is built (which usually occurs when the new reconciler is
//   built)
// ********************************************************************************************************************

// Global variable for current OM connection object that was created by MongoDbController - just for tests
var CurrMockedConnection *MockedOmConnection

const (
	TestGroupID   = "abcd1234"
	TestGroupName = "my-project"
	TestOrgID     = "xyz9876"
	TestAgentKey  = "qwerty9876"
	TestURL       = "http://mycompany.com:8080"
	TestUser      = "test@mycompany.com"
	TestApiKey    = "36lj245asg06s0h70245dstgft"
)

type MockedOmConnection struct {
	HTTPOmConnection
	deployment            Deployment
	automationConfig      *AutomationConfig
	backupAgentConfig     *BackupAgentConfig
	monitoringAgentConfig *MonitoringAgentConfig
	controlledFeature     *controlledfeature.ControlledFeature
	// hosts are used for both automation agents and monitoring endpoints.
	// They are necessary for emulating "agents" are ready behavior as operator checks for hosts for agents to exist
	hostResults *host.Result

	numRequestsSent         int
	AgentAPIKey             string
	OrganizationsWithGroups map[*Organization][]*Project
	CreateGroupFunc         func(group *Project) (*Project, error)
	UpdateGroupFunc         func(group *Project) (*Project, error)
	BackupConfigs           map[*BackupConfig]*HostCluster
	UpdateBackupStatusFunc  func(clusterId string, status BackupStatus) error
	AgentAuthMechanism      string

	// UpdateMonitoringAgentConfigFunc is delegated to if not nil when UpdateMonitoringAgentConfig is called
	UpdateMonitoringAgentConfigFunc func(mac *MonitoringAgentConfig, log *zap.SugaredLogger) ([]byte, error)
	// AgentsDelayCount is the number of loops to wait until the agents reach the goal
	AgentsDelayCount int
	// mocked client keeps track of all implemented functions called - uses reflection Func for this to enable type-safety
	// and make function names rename easier
	history []*runtime.Func
}

var _ Connection = &MockedOmConnection{}

// NewEmptyMockedConnection is the standard function for creating mocked connections that is usually used for testing
// "full cycle" mocked controller. It has group created already, but doesn't have the deployment. Also it "survives"
// recreations (as this is what we do in 'ReconcileCommonController.prepareConnection')
func NewEmptyMockedOmConnection(ctx *OMContext) Connection {
	conn := NewEmptyMockedOmConnectionNoGroup(ctx)
	// by default each connection just "reuses" "already created" group with agent keys existing
	conn.(*MockedOmConnection).OrganizationsWithGroups = map[*Organization][]*Project{
		{ID: TestOrgID, Name: TestGroupName}: {{
			Name:        TestGroupName,
			ID:          TestGroupID,
			Tags:        []string{util.OmGroupExternallyManagedTag},
			AgentAPIKey: TestAgentKey,
			OrgID:       TestOrgID,
		}},
	}

	return conn
}

// NewEmptyMockedOmConnectionWithDelay is the function that builds the mocked connection with some "delay" for agents
// to reach goal state, apart of this it's the same as 'NewEmptyMockedOmConnection'
func NewEmptyMockedOmConnectionWithDelay(ctx *OMContext) Connection {
	conn := NewEmptyMockedOmConnection(ctx)
	conn.(*MockedOmConnection).AgentsDelayCount = 1
	return conn
}

// NewMockedConnection is the simplified connection wrapping some deployment that already exists. Should be used for
// partial functionality (not the "full cycle" controller), for example read-update operation for the deployment
func NewMockedOmConnection(d Deployment) *MockedOmConnection {
	connection := MockedOmConnection{deployment: d}
	connection.hostResults = buildHostsFromDeployment(d)
	connection.BackupConfigs = make(map[*BackupConfig]*HostCluster)
	// By default we don't wait for agents to reach goal
	connection.AgentsDelayCount = 0
	// We use a simplified version of context as this is the only thing needed to get lock for the update
	connection.context = &OMContext{GroupName: TestGroupName, OrgID: TestOrgID}
	return &connection
}

// NewEmptyMockedOmConnectionWithAutomationConfigChanges returns a Connect instance that has had
// changes applied to the underlying AutomationConfig. This is to update the state of the AutomationConfig
// before the connection is used.
func NewEmptyMockedOmConnectionWithAutomationConfigChanges(ctx *OMContext, acFunc func(ac *AutomationConfig)) Connection {
	connection := NewEmptyMockedOmConnection(ctx)
	_ = connection.ReadUpdateAutomationConfig(func(ac *AutomationConfig) error {
		acFunc(ac)
		return nil
	}, nil)
	return connection
}

// NewEmptyMockedConnection is the standard function for creating mocked connections that is usually used for testing
// "full cycle" mocked controller. It doesn't have the group created.
func NewEmptyMockedOmConnectionNoGroup(ctx *OMContext) Connection {
	var connection *MockedOmConnection
	// That's how we can "survive" multiple calls to this function: so we can create groups or add/delete entities
	// Note, that the global connection variable is cleaned before each test (see kubeapi_test.newMockedKubeApi)
	if CurrMockedConnection != nil {
		connection = CurrMockedConnection
	} else {
		connection = NewMockedOmConnection(nil)
		connection.OrganizationsWithGroups = make(map[*Organization][]*Project)
	}

	connection.HTTPOmConnection = HTTPOmConnection{ctx}
	CurrMockedConnection = connection

	return connection
}

func (oc *MockedOmConnection) UpdateDeployment(d Deployment) ([]byte, error) {
	oc.addToHistory(reflect.ValueOf(oc.UpdateDeployment))
	oc.numRequestsSent++
	oc.deployment = d
	return nil, nil
}

func (oc *MockedOmConnection) ReadDeployment() (Deployment, error) {
	oc.addToHistory(reflect.ValueOf(oc.ReadDeployment))
	if oc.deployment == nil {
		return NewDeployment(), nil
	}
	return oc.deployment, nil
}
func (oc *MockedOmConnection) ReadUpdateDeployment(depFunc func(Deployment) error, log *zap.SugaredLogger) error {
	oc.addToHistory(reflect.ValueOf(oc.ReadUpdateDeployment))
	if oc.deployment == nil {
		oc.deployment = NewDeployment()
	}
	depFunc(oc.deployment)
	oc.numRequestsSent++
	return nil
}

func (oc *MockedOmConnection) ReadUpdateMonitoringAgentConfig(matFunc func(*MonitoringAgentConfig) error, log *zap.SugaredLogger) error {
	oc.addToHistory(reflect.ValueOf(oc.ReadUpdateMonitoringAgentConfig))
	if oc.monitoringAgentConfig == nil {
		oc.monitoringAgentConfig = &MonitoringAgentConfig{MonitoringAgentTemplate: &MonitoringAgentTemplate{}}
	}

	err := matFunc(oc.monitoringAgentConfig)
	if err != nil {
		return err
	}
	_, err = oc.UpdateMonitoringAgentConfig(oc.monitoringAgentConfig, log)
	return err
}

func (oc *MockedOmConnection) UpdateAutomationConfig(ac *AutomationConfig, log *zap.SugaredLogger) error {
	oc.addToHistory(reflect.ValueOf(oc.UpdateAutomationConfig))
	oc.deployment = ac.Deployment
	oc.automationConfig = ac
	return nil
}

func (oc *MockedOmConnection) ReadAutomationConfig() (*AutomationConfig, error) {
	oc.addToHistory(reflect.ValueOf(oc.ReadAutomationConfig))
	if oc.automationConfig == nil {
		if oc.deployment == nil {
			oc.deployment = NewDeployment()
		}
		oc.automationConfig = NewAutomationConfig(oc.deployment)
	}
	return oc.automationConfig, nil
}

func (oc *MockedOmConnection) ReadUpdateAutomationConfig(acFunc func(ac *AutomationConfig) error, log *zap.SugaredLogger) error {
	oc.addToHistory(reflect.ValueOf(oc.ReadUpdateAutomationConfig))
	if oc.automationConfig == nil {
		if oc.deployment == nil {
			oc.deployment = NewDeployment()
		}
		oc.automationConfig = NewAutomationConfig(oc.deployment)
	}

	// when we update the mocked automation config, update the corresponding deployment
	err := acFunc(oc.automationConfig)

	// mock the change of auto auth mechanism that is done based on the provided autoAuthMechanisms
	updateAutoAuthMechanism(oc.automationConfig)

	_ = oc.automationConfig.Apply()
	oc.deployment = oc.automationConfig.Deployment
	return err
}

func (oc *MockedOmConnection) AddHost(host host.Host) error {
	oc.hostResults.Results = append(oc.hostResults.Results, host)
	return nil
}

func (oc *MockedOmConnection) MarkProjectAsBackingDatabase(_ BackingDatabaseType) error {
	return nil
}

func (oc *MockedOmConnection) UpgradeAgentsToLatest() (string, error) {
	oc.addToHistory(reflect.ValueOf(oc.UpgradeAgentsToLatest))
	return "new-version", nil
}

func (oc *MockedOmConnection) ReadBackupAgentConfig() (*BackupAgentConfig, error) {
	oc.addToHistory(reflect.ValueOf(oc.ReadBackupAgentConfig))
	if oc.backupAgentConfig == nil {
		oc.backupAgentConfig = &BackupAgentConfig{BackupAgentTemplate: &BackupAgentTemplate{}}
	}
	return oc.backupAgentConfig, nil
}

func (oc *MockedOmConnection) UpdateBackupAgentConfig(bac *BackupAgentConfig, log *zap.SugaredLogger) ([]byte, error) {
	oc.addToHistory(reflect.ValueOf(oc.UpdateBackupAgentConfig))
	oc.backupAgentConfig = bac
	return nil, nil
}

func (oc *MockedOmConnection) ReadUpdateBackupAgentConfig(bacFunc func(*BackupAgentConfig) error, log *zap.SugaredLogger) error {
	oc.addToHistory(reflect.ValueOf(oc.ReadUpdateBackupAgentConfig))
	if oc.backupAgentConfig == nil {
		oc.backupAgentConfig = &BackupAgentConfig{BackupAgentTemplate: &BackupAgentTemplate{}}
	}
	return bacFunc(oc.backupAgentConfig)
}

func (oc *MockedOmConnection) ReadMonitoringAgentConfig() (*MonitoringAgentConfig, error) {
	oc.addToHistory(reflect.ValueOf(oc.ReadMonitoringAgentConfig))
	if oc.monitoringAgentConfig == nil {
		oc.monitoringAgentConfig = &MonitoringAgentConfig{MonitoringAgentTemplate: &MonitoringAgentTemplate{}}
	}
	return oc.monitoringAgentConfig, nil
}

func (oc *MockedOmConnection) UpdateMonitoringAgentConfig(mac *MonitoringAgentConfig, log *zap.SugaredLogger) ([]byte, error) {
	oc.addToHistory(reflect.ValueOf(oc.UpdateMonitoringAgentConfig))
	if oc.UpdateMonitoringAgentConfigFunc != nil {
		return oc.UpdateMonitoringAgentConfigFunc(mac, log)
	}
	oc.monitoringAgentConfig = mac
	return nil, nil
}

func (oc *MockedOmConnection) GenerateAgentKey() (string, error) {
	oc.addToHistory(reflect.ValueOf(oc.GenerateAgentKey))

	return oc.AgentAPIKey, nil
}

func (oc *MockedOmConnection) ReadAutomationStatus() (*AutomationStatus, error) {
	oc.addToHistory(reflect.ValueOf(oc.ReadAutomationStatus))

	if oc.AgentsDelayCount <= 0 {
		// Emulating "agents reached goal state": returning the proper status for all the
		// processes in the deployment
		return oc.buildAutomationStatusFromDeployment(oc.deployment, true), nil
	}
	oc.AgentsDelayCount--

	return oc.buildAutomationStatusFromDeployment(oc.deployment, false), nil
}
func (oc *MockedOmConnection) ReadAutomationAgents(pageNum int) (Paginated, error) {
	oc.addToHistory(reflect.ValueOf(oc.ReadAutomationAgents))

	results := make([]AgentStatus, 0)
	for _, r := range oc.hostResults.Results {
		results = append(results,
			AgentStatus{Hostname: r.Hostname, LastConf: time.Now().Add(time.Second * -1).Format(time.RFC3339)})
	}
	// todo extend this for real testing
	return automationAgentStatusResponse{AutomationAgents: results}, nil
}
func (oc *MockedOmConnection) GetHosts() (*host.Result, error) {
	oc.addToHistory(reflect.ValueOf(oc.GetHosts))
	return oc.hostResults, nil
}
func (oc *MockedOmConnection) RemoveHost(hostID string) error {
	oc.addToHistory(reflect.ValueOf(oc.RemoveHost))
	toKeep := make([]host.Host, 0)
	for _, v := range oc.hostResults.Results {
		if v.Id != hostID {
			toKeep = append(toKeep, v)
		}
	}
	oc.hostResults = &host.Result{Results: toKeep}
	return nil
}

func (oc *MockedOmConnection) ReadOrganizationsByName(name string) ([]*Organization, error) {
	oc.addToHistory(reflect.ValueOf(oc.ReadOrganizationsByName))
	allOrgs := make([]*Organization, 0)
	for k := range oc.OrganizationsWithGroups {
		if k.Name == name {
			allOrgs = append(allOrgs, k)
		}
	}
	if len(allOrgs) == 0 {
		return nil, api.NewErrorWithCode(api.OrganizationNotFound)
	}
	return allOrgs, nil
}

func (oc *MockedOmConnection) ReadOrganizations(page int) (Paginated, error) {
	oc.addToHistory(reflect.ValueOf(oc.ReadOrganizations))
	// We don't set Next field - so there should be no pagination
	allOrgs := make([]*Organization, 0)
	for k := range oc.OrganizationsWithGroups {
		allOrgs = append(allOrgs, k)
	}
	response := OrganizationsResponse{Organizations: allOrgs, OMPaginated: OMPaginated{TotalCount: len(oc.OrganizationsWithGroups)}}
	return &response, nil
}

func (oc *MockedOmConnection) ReadOrganization(orgID string) (*Organization, error) {
	oc.addToHistory(reflect.ValueOf(oc.ReadOrganization))
	return oc.findOrganization(orgID)
}

func (oc *MockedOmConnection) ReadProjectsInOrganizationByName(orgID string, name string) ([]*Project, error) {
	oc.addToHistory(reflect.ValueOf(oc.ReadProjectsInOrganizationByName))
	org, err := oc.findOrganization(orgID)
	if err != nil {
		return nil, err
	}
	projects := make([]*Project, 0)
	for _, p := range oc.OrganizationsWithGroups[org] {
		if p.Name == name {
			projects = append(projects, p)
		}
	}
	return projects, nil
}

func (oc *MockedOmConnection) ReadProjectsInOrganization(orgID string, page int) (Paginated, error) {
	oc.addToHistory(reflect.ValueOf(oc.ReadProjectsInOrganization))
	org, err := oc.findOrganization(orgID)
	if err != nil {
		return nil, err
	}
	response := &ProjectsResponse{Groups: oc.OrganizationsWithGroups[org], OMPaginated: OMPaginated{TotalCount: len(oc.OrganizationsWithGroups[org])}}
	return response, nil
}

func (oc *MockedOmConnection) CreateProject(project *Project) (*Project, error) {
	oc.addToHistory(reflect.ValueOf(oc.CreateProject))
	if oc.CreateGroupFunc != nil {
		return oc.CreateGroupFunc(project)
	}
	project.ID = TestGroupID

	// We emulate the behavior of Ops Manager: we create the organization with random id and the name matching the project
	organization := &Organization{ID: strconv.Itoa(rand.Int()), Name: project.Name}
	if _, exists := oc.OrganizationsWithGroups[organization]; !exists {
		oc.OrganizationsWithGroups[organization] = make([]*Project, 0)
	}
	project.OrgID = organization.ID
	oc.OrganizationsWithGroups[organization] = append(oc.OrganizationsWithGroups[organization], project)
	return project, nil
}
func (oc *MockedOmConnection) UpdateProject(project *Project) (*Project, error) {
	oc.addToHistory(reflect.ValueOf(oc.UpdateProject))
	if oc.UpdateGroupFunc != nil {
		return oc.UpdateGroupFunc(project)
	}
	org, err := oc.findOrganization(project.OrgID)
	if err != nil {
		return nil, err
	}
	for _, g := range oc.OrganizationsWithGroups[org] {
		if g.Name == project.Name {
			*g = *project
			return project, nil
		}
	}
	return nil, fmt.Errorf("Failed to find project")
}

func (oc *MockedOmConnection) ReadBackupConfigs() (*BackupConfigsResponse, error) {
	oc.addToHistory(reflect.ValueOf(oc.ReadBackupConfigs))

	keys := make([]*BackupConfig, 0, len(oc.BackupConfigs))
	for k := range oc.BackupConfigs {
		keys = append(keys, k)
	}
	return &BackupConfigsResponse{Configs: keys}, nil
}
func (oc *MockedOmConnection) ReadBackupConfig(clusterId string) (*BackupConfig, error) {
	oc.addToHistory(reflect.ValueOf(oc.ReadBackupConfig))

	for k := range oc.BackupConfigs {
		if k.ClusterId == clusterId {
			return k, nil
		}
	}
	return nil, api.NewError(errors.New("Failed to find backup config"))
}

func (oc *MockedOmConnection) ReadHostCluster(clusterId string) (*HostCluster, error) {
	oc.addToHistory(reflect.ValueOf(oc.ReadHostCluster))

	for k := range oc.BackupConfigs {
		if k.ClusterId == clusterId {
			return oc.BackupConfigs[k], nil
		}
	}
	return nil, api.NewError(errors.New("Failed to find host cluster"))
}

func (oc *MockedOmConnection) UpdateBackupStatus(clusterId string, newStatus BackupStatus) error {
	oc.addToHistory(reflect.ValueOf(oc.UpdateBackupStatus))

	if oc.UpdateBackupStatusFunc != nil {
		return oc.UpdateBackupStatusFunc(clusterId, newStatus)
	}

	oc.doUpdateBackupStatus(clusterId, newStatus)
	return nil
}

func (oc *MockedOmConnection) UpdateControlledFeature(cf *controlledfeature.ControlledFeature) error {
	oc.controlledFeature = cf
	return nil
}

func (oc *MockedOmConnection) GetControlledFeature() (*controlledfeature.ControlledFeature, error) {
	if oc.controlledFeature == nil {
		oc.controlledFeature = &controlledfeature.ControlledFeature{}
	}
	return oc.controlledFeature, nil
}

func (oc *MockedOmConnection) GetAgentAuthMode() (string, error) {
	return oc.AgentAuthMechanism, nil
}

// ************* These are native methods of Mocked client (not implementation of OmConnection)

func (oc *MockedOmConnection) CheckMonitoredHostsRemoved(t *testing.T, removedHosts []string) {
	for _, v := range oc.hostResults.Results {
		for _, e := range removedHosts {
			assert.NotEqual(t, e, v.Hostname, "Host %s is expected to be removed from monitored", e)
		}
	}
}

func (oc *MockedOmConnection) doUpdateBackupStatus(clusterID string, newStatus BackupStatus) {
	for k := range oc.BackupConfigs {
		if k.ClusterId == clusterID {
			if newStatus == "TERMINATING" {
				k.Status = "INACTIVE"
			} else {
				k.Status = newStatus
			}
		}
	}
}

func (oc *MockedOmConnection) GetProcesses() []Process {
	return oc.deployment.getProcesses()
}

func (oc *MockedOmConnection) GetSSL() map[string]interface{} {
	return oc.deployment.getSSL()
}

func (oc *MockedOmConnection) CheckNumberOfUpdateRequests(t *testing.T, expected int) {
	assert.Equal(t, expected, oc.numRequestsSent)
}

func (oc *MockedOmConnection) CheckDeployment(t *testing.T, expected Deployment, ignoreFields ...string) {
	for key := range expected {
		if !stringutil.Contains(ignoreFields, key) {
			assert.Equal(t, expected[key], oc.deployment[key])
		}
	}

}

func (oc *MockedOmConnection) CheckResourcesDeleted(t *testing.T) {
	oc.CheckResourcesAndBackupDeleted(t, "")
}

// CheckResourcesDeleted verifies the results of "delete" operations in OM: the deployment and monitoring must be empty,
// backup - inactive (note, that in real life backup config will disappear together with monitoring hosts, but we
// ignore this for the sake of testing)
func (oc *MockedOmConnection) CheckResourcesAndBackupDeleted(t *testing.T, resourceName string) {
	// This can be improved for some more complicated scenarios when we have different resources in parallel - so far
	// just checking if deployment
	assert.Empty(t, oc.deployment.getProcesses())
	assert.Empty(t, oc.deployment.getReplicaSets())
	assert.Empty(t, oc.deployment.getShardedClusters())
	assert.Empty(t, oc.deployment.getMonitoringVersions())
	assert.Empty(t, oc.deployment.getBackupVersions())
	assert.Empty(t, oc.hostResults.Results)

	if resourceName != "" {
		assert.NotEmpty(t, oc.BackupConfigs)

		found := false
		for k, v := range oc.BackupConfigs {
			if v.ClusterName == resourceName {
				assert.Equal(t, BackupStatus("INACTIVE"), k.Status)
				found = true
			}
		}
		assert.True(t, found)

		oc.CheckOrderOfOperations(t, reflect.ValueOf(oc.ReadBackupConfigs), reflect.ValueOf(oc.ReadHostCluster),
			reflect.ValueOf(oc.UpdateBackupStatus))
	}
}

func (oc *MockedOmConnection) CleanHistory() {
	oc.history = make([]*runtime.Func, 0)
}

// CheckOrderOfOperations verifies the mocked client operations were called in specified order
func (oc *MockedOmConnection) CheckOrderOfOperations(t *testing.T, value ...reflect.Value) {
	j := 0
	matched := ""
	for _, h := range oc.history {
		zap.S().Info(h.Name())
		if h.Name() == runtime.FuncForPC(value[j].Pointer()).Name() {
			matched += h.Name() + " "
			j++
		}
		if j == len(value) {
			break
		}
	}
	assert.Equal(t, len(value), j, "Only %d of %d expected operations happened in expected order (%s)", j, len(value), matched)
}

func (oc *MockedOmConnection) CheckOperationsDidntHappen(t *testing.T, value ...reflect.Value) {
	for _, h := range oc.history {
		for _, o := range value {
			assert.NotEqual(t, h.Name(), runtime.FuncForPC(o.Pointer()).Name(), "Operation %v is not expected to happen", h.Name())
		}
	}
}

// this is internal method only for testing, used by kubernetes mocked client
func (oc *MockedOmConnection) AddHosts(hostnames []string) {
	for i, p := range hostnames {
		oc.hostResults.Results = append(oc.hostResults.Results, host.Host{Id: strconv.Itoa(i), Hostname: p})
	}
}

func (oc *MockedOmConnection) EnableBackup(resourceName string, resourceType MongoDbResourceType) {
	if resourceType == ReplicaSetType {
		config := BackupConfig{ClusterId: uuid.New().String(), Status: Started}
		cluster := HostCluster{TypeName: "REPLICA_SET", ClusterName: resourceName, ReplicaSetName: resourceName}
		oc.BackupConfigs[&config] = &cluster
	} else {
		config := BackupConfig{ClusterId: uuid.New().String(), Status: Started}
		cluster := HostCluster{TypeName: "SHARDED_REPLICA_SET", ClusterName: resourceName, ShardName: resourceName}
		oc.BackupConfigs[&config] = &cluster

		// adding some host clusters for one shard and one config server - we don't care about relevance as they are
		// expected top be ignored by Operator

		config1 := BackupConfig{ClusterId: uuid.New().String(), Status: Inactive}
		cluster1 := HostCluster{TypeName: "REPLICA_SET", ClusterName: resourceName, ShardName: resourceName + "-0"}
		oc.BackupConfigs[&config1] = &cluster1

		config2 := BackupConfig{ClusterId: uuid.New().String(), Status: Inactive}
		cluster2 := HostCluster{TypeName: "REPLICA_SET", ClusterName: resourceName, ShardName: resourceName + "-config-rs-0"}
		oc.BackupConfigs[&config2] = &cluster2
	}
}

func (oc *MockedOmConnection) addToHistory(value reflect.Value) {
	oc.history = append(oc.history, runtime.FuncForPC(value.Pointer()))
}

func buildHostsFromDeployment(d Deployment) *host.Result {
	hosts := make([]host.Host, 0)
	if d != nil {
		for i, p := range d.getProcesses() {
			hosts = append(hosts, host.Host{Id: strconv.Itoa(i), Hostname: p.HostName()})
		}
	}
	return &host.Result{Results: hosts}
}

func (oc *MockedOmConnection) buildAutomationStatusFromDeployment(d Deployment, reached bool) *AutomationStatus {
	// edge case: if there are no processes - we think that
	processStatuses := make([]ProcessStatus, 0)
	if d != nil {
		for _, p := range d.getProcesses() {
			if reached {
				processStatuses = append(processStatuses, ProcessStatus{Name: p.Name(), LastGoalVersionAchieved: 1})
			} else {
				processStatuses = append(processStatuses, ProcessStatus{Name: p.Name(), LastGoalVersionAchieved: 0})
			}
		}
	}
	return &AutomationStatus{GoalVersion: 1, Processes: processStatuses}
}

func (oc *MockedOmConnection) CheckGroupInOrganization(t *testing.T, orgName, groupName string) {
	for k, v := range oc.OrganizationsWithGroups {
		if k.Name == orgName {
			for _, g := range v {
				if g.Name == groupName {
					return
				}
			}
		}
	}
	assert.Fail(t, fmt.Sprintf("Project %s not found in organization %s", groupName, orgName))
}

func (oc *MockedOmConnection) FindGroup(groupName string) *Project {
	for _, v := range oc.OrganizationsWithGroups {
		for _, g := range v {
			if g.Name == groupName {
				return g
			}
		}
	}
	return nil
}

func (oc *MockedOmConnection) findOrganization(orgId string) (*Organization, error) {
	for k := range oc.OrganizationsWithGroups {
		if k.ID == orgId {
			return k, nil
		}
	}
	return nil, api.NewError(fmt.Errorf("Organization with id %s not found", orgId))
}

// updateAutoAuthMechanism simulates the changes made by Ops Manager and the agents in deciding which
// mechanism will be specified as the "autoAuthMechanisms"
func updateAutoAuthMechanism(ac *AutomationConfig) {
	mechanisms := ac.Auth.AutoAuthMechanisms
	if stringutil.Contains(mechanisms, "SCRAM-SHA-256") {
		ac.Auth.AutoAuthMechanism = "SCRAM-SHA-256"
	} else if stringutil.Contains(mechanisms, "MONGODB-CR") {
		ac.Auth.AutoAuthMechanism = "MONGODB-CR"
	} else if stringutil.Contains(mechanisms, "MONGODB-X509") {
		ac.Auth.AutoAuthMechanism = "MONGODB-X509"
	}
}
