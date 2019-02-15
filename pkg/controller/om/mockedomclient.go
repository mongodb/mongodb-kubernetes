package om

import (
	"fmt"
	"math/rand"
	"strconv"
	"testing"

	"go.uber.org/zap"

	"github.com/pkg/errors"

	"reflect"
	"runtime"

	"time"

	"strings"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
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
// * There is a small trick with global variable 'CurrMockedConnection' that allows to "survive" separate calls to the
//   om creation function and allows to test more complicated scenarios (create delete). The state is cleaned as soon as
//   a new mocked api object is built (which usually occurs when the new reconciler is built)
// ********************************************************************************************************************

// Global variable for current OM connection object that was created by MongoDbController - just for tests
var CurrMockedConnection *MockedOmConnection

const (
	TestGroupID   = "abcd1234"
	TestGroupName = "my-project"
	TestOrgID     = "xyz9876"
	TestAgentKey  = "qwerty9876"
	TestURL       = "http://mycompany.com:8080"
)

type MockedOmConnection struct {
	HTTPOmConnection
	deployment Deployment
	// hosts are used for both automation agents and monitoring endpoints.
	// They are necessary for emulating "agents" are ready behavior as operator checks for hosts for agents to exist
	hosts                  *Host
	numRequestsSent        int
	AgentAPIKey            string
	AllGroups              []*Group
	AllOrganizations       []*Organization
	CreateGroupFunc        func(group *Group) (*Group, error)
	UpdateGroupFunc        func(group *Group) (*Group, error)
	BackupConfigs          map[*BackupConfig]*HostCluster
	UpdateBackupStatusFunc func(clusterId string, status BackupStatus) error
	// AgentsDelayCount is the number of loops to wait until the agents reach the goal
	AgentsDelayCount int
	// mocked client keeps track of all implemented functions called - uses reflection Func for this to enable type-safety
	// and make function names rename easier
	history []*runtime.Func
}

// NewEmptyMockedConnection is the standard function for creating mocked connections that is usually used for testing
// "full cycle" mocked controller. It has group created already
func NewEmptyMockedOmConnection(baseURL, groupID, user, publicAPIKey string) Connection {
	conn := NewEmptyMockedOmConnectionNoGroup(baseURL, groupID, user, publicAPIKey)

	// by default each connection just "reuses" "already created" group with agent keys existing
	conn.(*MockedOmConnection).AllGroups = []*Group{{
		Name:        TestGroupName,
		ID:          TestGroupID,
		Tags:        []string{"EXTERNALLY_MANAGED_BY_KUBERNETES"},
		AgentAPIKey: TestAgentKey,
		OrgID:       TestOrgID,
	}}
	conn.(*MockedOmConnection).AllOrganizations = []*Organization{{ID: TestOrgID, Name: TestGroupName}}

	return conn
}

// NewEmptyMockedOmConnectionWithDelay is the function that builds the mocked connection with some "delay" for agents
// to reach goal state
func NewEmptyMockedOmConnectionWithDelay(baseURL, groupID, user, publicAPIKey string) Connection {
	conn := NewEmptyMockedOmConnection(baseURL, groupID, user, publicAPIKey)
	conn.(*MockedOmConnection).AgentsDelayCount = 1
	return conn
}

// NewMockedConnection is the simplified connection wrapping some deployment that already exists. Should be used for
// partial functionality (not the "full cycle" controller)
func NewMockedOmConnection(d Deployment) *MockedOmConnection {
	connection := MockedOmConnection{deployment: d}
	connection.hosts = buildHostsFromDeployment(d)
	connection.BackupConfigs = make(map[*BackupConfig]*HostCluster)
	// By default we don't wait for agents to reach goal
	connection.AgentsDelayCount = 0

	return &connection
}

// NewEmptyMockedConnection is the standard function for creating mocked connections that is usually used for testing
// "full cycle" mocked controller. It has group created already
func NewEmptyMockedOmConnectionNoGroup(baseURL, groupID, user, publicAPIKey string) Connection {
	var connection *MockedOmConnection
	// That's how we can "survive" multiple calls to this function: so we can create groups or add/delete entities
	// Note, that the global connection variable is cleaned before each test (see kubeapi_test.newMockedKubeApi)
	if CurrMockedConnection != nil {
		connection = CurrMockedConnection
	} else {
		connection = NewMockedOmConnection(nil)
		connection.AllGroups = make([]*Group, 0)
		connection.AllOrganizations = make([]*Organization, 0)
	}
	connection.HTTPOmConnection = HTTPOmConnection{
		baseURL:      strings.TrimSuffix(baseURL, "/"),
		groupID:      groupID,
		user:         user,
		publicAPIKey: publicAPIKey,
	}
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
func (oc *MockedOmConnection) ReadAutomationAgents() (*AgentState, error) {
	oc.addToHistory(reflect.ValueOf(oc.ReadAutomationAgents))

	results := make([]ResultStruct, 0)
	for _, r := range oc.hosts.Results {
		results = append(results,
			ResultStruct{Hostname: r.Hostname, LastConf: time.Now().Add(time.Second * -1).Format(time.RFC3339)})
	}
	// todo extend this for real testing
	return &AgentState{Results: results}, nil
}
func (oc *MockedOmConnection) GetHosts() (*Host, error) {
	oc.addToHistory(reflect.ValueOf(oc.GetHosts))
	return oc.hosts, nil
}
func (oc *MockedOmConnection) RemoveHost(hostID string) error {
	oc.addToHistory(reflect.ValueOf(oc.RemoveHost))
	toKeep := make([]HostList, 0)
	for _, v := range oc.hosts.Results {
		if v.Id != hostID {
			toKeep = append(toKeep, v)
		}
	}
	oc.hosts = &Host{toKeep}
	return nil
}

func (oc *MockedOmConnection) ReadOrganizations() ([]*Organization, error) {
	oc.addToHistory(reflect.ValueOf(oc.ReadOrganizations))
	return oc.AllOrganizations, nil
}

func (oc *MockedOmConnection) ReadGroups() ([]*Group, error) {
	oc.addToHistory(reflect.ValueOf(oc.ReadGroups))
	return oc.AllGroups, nil
}

func (oc *MockedOmConnection) CreateGroup(group *Group) (*Group, error) {
	oc.addToHistory(reflect.ValueOf(oc.CreateGroup))
	if oc.CreateGroupFunc != nil {
		return oc.CreateGroupFunc(group)
	}
	group.ID = TestGroupID
	oc.AllGroups = append(oc.AllGroups, group)

	// We emulate the behavior of Ops Manager: we create the organization with random id and the name matching the group
	oc.AllOrganizations = append(oc.AllOrganizations, &Organization{ID: string(rand.Int()), Name: group.Name})
	return group, nil
}
func (oc *MockedOmConnection) UpdateGroup(group *Group) (*Group, error) {
	oc.addToHistory(reflect.ValueOf(oc.UpdateGroup))
	if oc.UpdateGroupFunc != nil {
		return oc.UpdateGroupFunc(group)
	}
	for k, g := range oc.AllGroups {
		if g.Name == group.Name {
			oc.AllGroups[k] = group
			return group, nil
		}
	}
	return nil, fmt.Errorf("Failed to find group")
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
	return nil, NewAPIError(errors.New("Failed to find backup config"))
}

func (oc *MockedOmConnection) ReadHostCluster(clusterId string) (*HostCluster, error) {
	oc.addToHistory(reflect.ValueOf(oc.ReadHostCluster))

	for k := range oc.BackupConfigs {
		if k.ClusterId == clusterId {
			return oc.BackupConfigs[k], nil
		}
	}
	return nil, NewAPIError(errors.New("Failed to find host cluster"))
}

func (oc *MockedOmConnection) UpdateBackupStatus(clusterId string, newStatus BackupStatus) error {
	oc.addToHistory(reflect.ValueOf(oc.UpdateBackupStatus))

	if oc.UpdateBackupStatusFunc != nil {
		return oc.UpdateBackupStatusFunc(clusterId, newStatus)
	}

	oc.doUpdateBackupStatus(clusterId, newStatus)
	return nil
}

// ************* These are native methods of Mocked client (not implementation of OmConnection)

func (oc *MockedOmConnection) CheckMonitoredHostsRemoved(t *testing.T, removedHosts []string) {
	for _, v := range oc.hosts.Results {
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

func (oc *MockedOmConnection) CheckNumberOfUpdateRequests(t *testing.T, expected int) {
	assert.Equal(t, expected, oc.numRequestsSent)
}

func (oc *MockedOmConnection) CheckDeployment(t *testing.T, expected Deployment) {
	assert.Equal(t, expected, oc.deployment)
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
	assert.Empty(t, oc.hosts.Results)

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
			assert.NotEqual(t, o, h, "Operation %v is not expected to happen", h)
		}
	}
}

// this is internal method only for testing, used by kubernetes mocked client
func (oc *MockedOmConnection) AddHosts(hostnames []string) {
	for i, p := range hostnames {
		oc.hosts.Results = append(oc.hosts.Results, HostList{Id: strconv.Itoa(i), Hostname: p})
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

func buildHostsFromDeployment(d Deployment) *Host {
	hosts := make([]HostList, 0)
	if d != nil {
		for i, p := range d.getProcesses() {
			hosts = append(hosts, HostList{Id: strconv.Itoa(i), Hostname: p.HostName()})
		}
	}
	return &Host{Results: hosts}
}

func (oc *MockedOmConnection) FindGroup(name string) *Group {
	for _, g := range oc.AllGroups {
		if g.Name == name {
			return g
		}
	}
	return nil
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
