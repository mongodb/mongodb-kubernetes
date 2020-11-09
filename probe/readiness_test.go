package main

import (
	"os"
	"testing"
	"time"

	"github.com/10gen/ops-manager-kubernetes/probe/config"

	"github.com/stretchr/testify/assert"
)

// TestDeadlockDetection verifies that if the agent is stuck in "WaitAllRsMembersUp" phase (started > 15 seconds ago)
// then the function returns "ready"
func TestDeadlockDetection(t *testing.T) {
	assert.True(t, isPodReady(testConfig("health-status-deadlocked.json")))
}

// TestNoDeadlock verifies that if the agent has started (but not finished) "WaitRsInit" and then there is another
// started phase ("WaitFeatureCompatibilityVersionCorrect") then no deadlock is found as the latter is considered to
// be the "current" step
func TestNoDeadlock(t *testing.T) {
	health := readHealthinessFile("health-status-no-deadlock.json")
	stepStatus := findCurrentStep(health.ProcessPlans)

	assert.Equal(t, "WaitFeatureCompatibilityVersionCorrect", stepStatus.Step)

	assert.False(t, isPodReady(testConfig("health-status-no-deadlock.json")))
}

// TestDeadlockDetection verifies that if the agent is in "WaitAllRsMembersUp" phase but started < 15 seconds ago
// then the function returns "not ready". To achieve this "started" is put into some long future.
// Note, that the status file is artificial: it has two plans (the first one is complete and has no moves) to make sure
// the readiness logic takes only the last plan for consideration
func TestNotReadyWaitingForRsReady(t *testing.T) {
	assert.False(t, isPodReady(testConfig("health-status-pending.json")))
}

// TestNotReadyHealthFileHasNoPlans verifies that the readiness script doesn't panic if the health file has unexpected
// data (there are no plans at all)
func TestNotReadyHealthFileHasNoPlans(t *testing.T) {
	assert.False(t, isPodReady(testConfig("health-status-no-plans.json")))
}

// TestNotReadyHealthFileHasNoProcesses verifies that the readiness script doesn't panic if the health file has unexpected
// data (there are no processes at all)
func TestNotReadyHealthFileHasNoProcesses(t *testing.T) {
	assert.False(t, isPodReady(testConfig("health-status-no-processes.json")))
}

// TestReady verifies that the probe reports "ready" despite "WaitRsInit" stage reporting as not reached
// (this is some bug in Automation Agent which we can work with)
func TestReady(t *testing.T) {
	assert.True(t, isPodReady(testConfig("health-status-ok.json")))
}

// TestNoDeadlockForDownloadProcess verifies that the steps not listed as "riskySteps" (like "download") are not
// considered as stuck
func TestNoDeadlockForDownloadProcess(t *testing.T) {
	before := time.Now().Add(time.Duration(-30) * time.Second)
	downloadStatus := &stepStatus{
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
	downloadStatus := &stepStatus{
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
	t.SkipNow()
	//_ = os.Setenv(headlessAgent, "true")
	//_ = os.Setenv(podNamespaceEnv, "test")
	//_ = os.Setenv(automationConfigMapEnv, "om-db-config")
	//mockedReader := NewMockedSecretReader("test", "om-db-config", 6)
	//assert.False(t, isPodReady("health-status-ok.json", mockedReader, pod.patcher{}))
}

// TestHeadlessAgentReachedGoal verifies that the probe reports "true" if the config version is equal to the
// last achieved version of the Agent
func TestHeadlessAgentReachedGoal(t *testing.T) {
	t.SkipNow()
	//_ = os.Setenv(headlessAgent, "true")
	//_ = os.Setenv(podNamespaceEnv, "test")
	//_ = os.Setenv(automationConfigMapEnv, "om-db-config")
	//mockedReader := NewMockedSecretReader("test", "om-db-config", 5)
	//assert.True(t, isPodReady("health-status-ok.json", mockedReader, pod.patcher{}))
}

// TestHeadlessAgentPanicsIfEnvVarsNotSet verifies that the probe panics if the environment variables are not set
// Must happen only for headless mode
func TestHeadlessAgentPanicsIfEnvVarsNotSet(t *testing.T) {
	os.Clearenv()
	_ = os.Setenv(headlessAgent, "true")

	//mockedReader := NewMockedSecretReader("test", "om-db-config", 5)
	//assert.Panics(t, func() { isPodReady("health-status-ok.json", mockedReader, pod.patcher{}) })
	//
	//_ = os.Setenv(podNamespaceEnv, "test")
	//// Still panics
	//assert.Panics(t, func() { isPodReady("health-status-ok.json", mockedReader, pod.patcher{}) })
}

func readHealthinessFile(path string) healthStatus {
	fd, _ := os.Open(path)
	health, _ := readAgentHealthStatus(fd)
	return health
}

func testConfig(healthFilePath string) config.Config {
	return config.Config{
		HealthStatusFilePath: healthFilePath,
	}
}
