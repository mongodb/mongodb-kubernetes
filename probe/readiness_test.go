package main

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestDeadlockDetection verifies that if the agent is stuck in "WaitAllRsMembersUp" phase (started > 15 seconds ago)
// then the function returns "ready"
func TestDeadlockDetection(t *testing.T) {
	assert.True(t, isPodReady("health-status-deadlocked.json", nil))
}

// TestDeadlockDetection verifies that if the agent is in "WaitAllRsMembersUp" phase but started < 15 seconds ago
// then the function returns "not ready". To achieve this "started" is put into some long future.
// Note, that the status file is artificial: it has two plans (the first one is complete and has no moves) to make sure
// the readiness logic takes only the last plan for consideration
func TestNotReadyWaitingForRsReady(t *testing.T) {
	assert.False(t, isPodReady("health-status-pending.json", nil))
}

// TestNotReadyHealthFileHasNoPlans verifies that the readiness script doesn't panic if the health file has unexpected
// data (there are no plans at all)
func TestNotReadyHealthFileHasNoPlans(t *testing.T) {
	assert.False(t, isPodReady("health-status-no-plans.json", nil))
}

// TestNotReadyHealthFileHasNoProcesses verifies that the readiness script doesn't panic if the health file has unexpected
// data (there are no processes at all)
func TestNotReadyHealthFileHasNoProcesses(t *testing.T) {
	assert.False(t, isPodReady("health-status-no-processes.json", nil))
}

// TestReady verifies that the probe reports "ready" despite "WaitRsInit" stage reporting as not reached
// (this is some bug in Automation Agent which we can work with)
func TestReady(t *testing.T) {
	assert.True(t, isPodReady("health-status-ok.json", nil))
}

// TestNoDeadlockForDownloadProcess verifies that the steps not listed as "riskySteps" (like "download") are not
// considered as stuck
func TestNoDeadlockForDownloadProcess(t *testing.T) {
	before := time.Now().Add(time.Duration(-30) * time.Second)
	downloadStatus := &StepStatus{
		Step:      "Download",
		Started:   &before,
		Completed: nil,
		Result:    "",
	}

	assert.False(t, isDeadlocked(downloadStatus))
}

// TestNoDeadlockForImmediateWaitRs verifies the "WaitRsInit" step is not marked as deadlocked if
// it was started < 15 seconds ago
func TestNoDeadlockForImmediateWaitRs(t *testing.T) {
	before := time.Now().Add(time.Duration(-10) * time.Second)
	downloadStatus := &StepStatus{
		Step:      "WaitRsInit",
		Started:   &before,
		Completed: nil,
		Result:    "Wait",
	}

	assert.False(t, isDeadlocked(downloadStatus))
}

// TestHeadlessAgentHasntReachedGoal verifies that the probe reports "false" if the config version is higher than the
// last achieved version of the Agent
// Note that the edge case is checked here: the health-status-ok.json has the "WaitRsInit" phase stuck in the last plan
// (as Agent doesn't marks all the step statuses finished when it reaches the goal) but this doesn't affect the result
// as the whole plan is complete already
func TestHeadlessAgentHasntReachedGoal(t *testing.T) {
	_ = os.Setenv(HeadlessAgent, "true")
	_ = os.Setenv(PodNamespaceEnv, "test")
	_ = os.Setenv(AutomationConfigMapEnv, "om-db-config")
	mockedReader := NewMockedConfigMapReader("test", "om-db-config", 6)
	assert.False(t, isPodReady("health-status-ok.json", mockedReader))
}

// TestHeadlessAgentReachedGoal verifies that the probe reports "true" if the config version is equal to the
// last achieved version of the Agent
func TestHeadlessAgentReachedGoal(t *testing.T) {
	_ = os.Setenv(HeadlessAgent, "true")
	_ = os.Setenv(PodNamespaceEnv, "test")
	_ = os.Setenv(AutomationConfigMapEnv, "om-db-config")
	mockedReader := NewMockedConfigMapReader("test", "om-db-config", 5)
	assert.True(t, isPodReady("health-status-ok.json", mockedReader))
}

// TestHeadlessAgentPanicsIfEnvVarsNotSet verifies that the probe panics if the environment variables are not set
// Must happen only for headless mode
func TestHeadlessAgentPanicsIfEnvVarsNotSet(t *testing.T) {
	os.Clearenv()
	_ = os.Setenv(HeadlessAgent, "true")

	mockedReader := NewMockedConfigMapReader("test", "om-db-config", 5)
	assert.Panics(t, func() { isPodReady("health-status-ok.json", mockedReader) })

	_ = os.Setenv(PodNamespaceEnv, "test")
	// Still panics
	assert.Panics(t, func() { isPodReady("health-status-ok.json", mockedReader) })
}

// MockedConfigMapReader is a mocked implementation of ConfigMapReader
type MockedConfigMapReader struct {
	configMap *v1.ConfigMap
}

func NewMockedConfigMapReader(namespace, name string, version int) *MockedConfigMapReader {
	// We don't need to create a full automation config - just the json with version field is enough
	deployment := fmt.Sprintf("{\"version\": %d}", version)
	configMap := &v1.ConfigMap{Data: map[string]string{"cluster-config.json": deployment}}
	configMap.ObjectMeta = metav1.ObjectMeta{Namespace: namespace, Name: name}
	return &MockedConfigMapReader{configMap: configMap}
}

func (r *MockedConfigMapReader) readConfigMap(namespace, configMapName string) (*v1.ConfigMap, error) {
	if r != nil && r.configMap.Namespace == namespace && r.configMap.Name == configMapName {
		return r.configMap, nil
	}
	return nil, &errors.StatusError{ErrStatus: metav1.Status{Reason: metav1.StatusReasonNotFound}}
}
