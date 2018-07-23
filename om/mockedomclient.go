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

type MockedOmConnection struct {
	HttpOmConnection
	deployment Deployment
	// hosts are necessary for emulating "agents" are ready behavior as operator checks for hosts for agents to exist
	hosts           *Host
	numRequestsSent int
	// mocked client keeps track of all implemented functions called - uses reflection Func for this to enable type-safety
	// and make function names rename easier
	history []*runtime.Func
}

func NewEmptyMockedOmConnection(baseUrl, groupId, user, publicApiKey string) OmConnection {
	connection := NewMockedOmConnection(nil)
	// unfortunately we cannot just assert that params are not empty here (as we don't have access to t.Testing)
	// so we need to save parameters as fields to validate them later
	connection.HttpOmConnection = HttpOmConnection{
		baseUrl:      strings.TrimSuffix(baseUrl, "/"),
		groupId:      groupId,
		user:         user,
		publicApiKey: publicApiKey,
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
	// todo
	return "", nil
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
	for _, h := range oc.history {
		if h.Name() == runtime.FuncForPC(value[j].Pointer()).Name() {
			j++
		}
		if j == len(value) {
			break
		}
	}
	assert.Equal(t, len(value), j, "Only %d of %d expected operations happened in expected order, history: %v, expected: %v", j, len(value), oc.history, value)
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
