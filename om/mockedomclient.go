package om

import (
	"strconv"
	"testing"

	"reflect"
	"runtime"

	"time"

	"strings"

	"github.com/stretchr/testify/assert"
)

// Global variable for current OM connection object that was created by MongoDbController - just for tests
var CurrMockedConnection *MockedOmConnection

const (
	TestGroupId  = "abcd1234"
	TestAgentKey = "qwerty9876"
)

type MockedOmConnection struct {
	HttpOmConnection
	deployment Deployment
	// hosts are necessary for emulating "agents" are ready behavior as operator checks for hosts for agents to exist
	hosts           *Host
	numRequestsSent int
	AgentApiKey     string
	Group           *Group
	ReadGroupFunc   func(name string) (*Group, error)
	CreateGroupFunc func(group *Group) (*Group, error)
	UpdateGroupFunc func(group *Group) (*Group, error)
	// mocked client keeps track of all implemented functions called - uses reflection Func for this to enable type-safety
	// and make function names rename easier
	history []*runtime.Func
}

func NewEmptyMockedOmConnection(baseUrl, groupId, user, publicApiKey string) OmConnection {
	connection := NewMockedOmConnection(nil)
	connection.HttpOmConnection = HttpOmConnection{
		baseUrl:      strings.TrimSuffix(baseUrl, "/"),
		groupId:      groupId,
		user:         user,
		publicApiKey: publicApiKey,
	}
	// saving the object to global variable for using in the test (seems we don't need paralleling tests so far)
	if CurrMockedConnection != nil {
		// hacky: as we call func for creating connection twice - we may override previous connection - lets'
		// save group field for tests
		connection.Group = CurrMockedConnection.Group
		connection.history = CurrMockedConnection.history
	}
	CurrMockedConnection = connection

	// by default each connection just "reuses" "already created" group with agent keys existing
	connection.ReadGroupFunc = func(name string) (*Group, error) {
		return &Group{Name: name, Id: TestGroupId, Tags: []string{"EXTERNALLY_MANAGED_BY_KUBERNETES"}, AgentApiKey: TestAgentKey}, nil
	}

	return connection
}
func NewMockedOmConnection(d Deployment) *MockedOmConnection {
	connection := MockedOmConnection{deployment: d}
	connection.hosts = buildHostsFromDeployment(d)
	return &connection
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
func (oc *MockedOmConnection) ReadUpdateDeployment(wait bool, depFunc func(Deployment) error) error {
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

	return oc.AgentApiKey, nil
}

func (oc *MockedOmConnection) ReadAutomationStatus() (*AutomationStatus, error) {
	oc.addToHistory(reflect.ValueOf(oc.ReadAutomationStatus))
	// todo
	return nil, nil
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
func (oc *MockedOmConnection) RemoveHost(hostId string) error {
	oc.addToHistory(reflect.ValueOf(oc.RemoveHost))
	toKeep := make([]HostList, 0)
	for _, v := range oc.hosts.Results {
		if v.Id != hostId {
			toKeep = append(toKeep, v)
		}
	}
	oc.hosts = &Host{toKeep}
	return nil
}
func (oc *MockedOmConnection) ReadGroup(name string) (*Group, error) {
	oc.addToHistory(reflect.ValueOf(oc.ReadGroup))

	if oc.ReadGroupFunc != nil {
		return oc.ReadGroupFunc(name)
	}
	return oc.Group, nil
}

func (oc *MockedOmConnection) CreateGroup(group *Group) (*Group, error) {
	oc.addToHistory(reflect.ValueOf(oc.CreateGroup))
	if oc.CreateGroupFunc != nil {
		return oc.CreateGroupFunc(group)
	}
	oc.Group = group
	oc.Group.Id = TestGroupId
	return oc.Group, nil
}
func (oc *MockedOmConnection) UpdateGroup(group *Group) (*Group, error) {
	oc.addToHistory(reflect.ValueOf(oc.UpdateGroup))
	if oc.UpdateGroupFunc != nil {
		return oc.UpdateGroupFunc(group)
	}
	oc.Group.Tags = group.Tags
	return group, nil
}

// ************* These are native methods of Mocked client (not implementation of OmConnection)

func (oc *MockedOmConnection) CheckMonitoredHostsRemoved(t *testing.T, removedHosts []string) {
	for _, v := range oc.hosts.Results {
		for _, e := range removedHosts {
			assert.NotEqual(t, e, v.Hostname, "Host %s is expected to be removed from monitored", e)
		}
	}
}

func (oc *MockedOmConnection) CheckNumberOfUpdateRequests(t *testing.T, expected int) {
	assert.Equal(t, expected, oc.numRequestsSent)
}

func (oc *MockedOmConnection) CheckDeployment(t *testing.T, expected Deployment) {
	assert.Equal(t, expected, oc.deployment)
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

// this is internal method only for testing
func (oc *MockedOmConnection) SetHosts(hostnames []string) {
	hosts := make([]HostList, len(hostnames))
	for i, p := range hostnames {
		hosts[i] = HostList{Id: strconv.Itoa(i), Hostname: p}
	}
	oc.hosts = &Host{Results: hosts}
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
