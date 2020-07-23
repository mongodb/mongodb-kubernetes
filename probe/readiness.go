package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"strings"
	"time"

	"github.com/spf13/cast"
	"go.uber.org/zap"
)

const (
	defaultAgentHealthStatusFilePath = "/var/log/mongodb-mms-automation/agent-health-status.json"
	defaultLogPath                   = "/var/log/mongodb-mms-automation/readiness.log"
	appDBAutomationConfigKey         = "cluster-config.json"
	podNamespaceEnv                  = "POD_NAMESPACE"
	automationConfigMapEnv           = "AUTOMATION_CONFIG_MAP"
	headlessAgent                    = "HEADLESS_AGENT"
	agentHealthStatusFilePathEnv     = "AGENT_STATUS_FILEPATH"
	logPathEnv                       = "LOG_FILE_PATH"
)

var riskySteps []string
var logger *zap.SugaredLogger

func init() {
	riskySteps = []string{"WaitAllRsMembersUp", "WaitRsInit"}

	// By default we log to the output (convenient for tests)
	cfg := zap.NewDevelopmentConfig()
	log, err := cfg.Build()
	if err != nil {
		panic(err)
	}
	logger = log.Sugar()
}

// isPodReady main function which makes decision if the pod is ready or not. The decision is based on the information
// from the AA health status file.
// The logic depends on if the pod is a standard MongoDB or an AppDB one.
// - If MongoDB: then just the 'statuses[0].IsInGoalState` field is used to learn if the Agent has reached the goal
// - if AppDB: the 'mmsStatus[0].lastGoalVersionAchieved' field is compared with the one from mounted automation config
// Additionally if the previous check hasn't returned 'true' the "deadlock" case is checked to make sure the Agent is
// not waiting for the other members.
func isPodReady(healStatusPath string, secretReader SecretReader) bool {
	fd, err := os.Open(healStatusPath)
	if err != nil {
		logger.Warn("No health status file exists, assuming the Automation agent is old")
		return true
	}
	defer fd.Close()

	health, err := readAgentHealthStatus(fd)
	if err != nil {
		logger.Errorf("Failed to read agent health status file: %s", err)
		// panicking allows to see the problem in the events for the pod (kubectl describe pod ..)
		panic("Failed to read agent health status file: %s")
	}

	if len(health.Healthiness) == 0 {
		logger.Info("'statuses' is empty. We assume there is no automation config for the agent yet.")
		return true
	}

	// If the agent has reached the goal state - returning true
	ok, err := isInGoalState(health, secretReader)

	if err != nil {
		logger.Errorf("There was problem checking the health status: %s", err)
		panic(err)
	}
	if ok {
		return true
	}

	// Failback logic: the agent is not in goal state and got stuck in some steps
	if hasDeadlockedSteps(health) {
		return true
	}

	return false
}

func readAgentHealthStatus(file *os.File) (healthStatus, error) {
	var health healthStatus

	data, err := ioutil.ReadAll(file)
	if err != nil {
		return health, err
	}

	err = json.Unmarshal(data, &health)
	return health, err
}

// hasDeadlockedSteps returns true if the agent is stuck on waiting for the other agents
func hasDeadlockedSteps(health healthStatus) bool {
	currentStep := findCurrentStep(health.ProcessPlans)
	if currentStep != nil {
		return isDeadlocked(currentStep)
	}
	return false
}

// findCurrentStep returns the step which seems to be run by the Agent now. The step is always in the last plan
// (see https://github.com/10gen/ops-manager-kubernetes/pull/401#discussion_r333071555) so we iterate over all the steps
// there and find the last step which has "Started" non nil
// (indeed this is not the perfect logic as sometimes the agent doesn't update the 'Started' as well - see
// 'health-status-ok.json', but seems it works for finding deadlocks still
//noinspection GoNilness
func findCurrentStep(processStatuses map[string]mmsDirectorStatus) *stepStatus {
	var currentPlan *planStatus
	if len(processStatuses) == 0 {
		// Seems shouldn't happen but let's check anyway - may be needs to be changed to Info if this happens
		logger.Warnf("There is no information about Agent process plans")
		return nil
	}
	if len(processStatuses) > 1 {
		logger.Errorf("Only one process status is expected but got %d!", len(processStatuses))
		return nil
	}
	// There is always only one process managed by the Agent - so there will be only one loop
	for k, v := range processStatuses {
		if len(v.Plans) == 0 {
			logger.Errorf("The process %s doesn't contain any plans!", k)
			return nil
		}
		currentPlan = v.Plans[len(v.Plans)-1]
	}

	if currentPlan.Completed != nil {
		logger.Debugf("The Agent hasn't reported working on the new config yet, the last plan finished at %s",
			currentPlan.Completed.Format(time.RFC3339))
		return nil
	}

	var lastStartedStep *stepStatus
	for _, m := range currentPlan.Moves {
		for _, s := range m.Steps {
			if s.Started != nil {
				lastStartedStep = s
			}
		}
	}

	return lastStartedStep
}

func isDeadlocked(status *stepStatus) bool {
	// Some logic behind 15 seconds: the health status file is dumped each 10 seconds so we are sure that if the agent
	// has been in the the step for 10 seconds - this means it is waiting for the other hosts and they are not available
	fifteenSecondsAgo := time.Now().Add(time.Duration(-15) * time.Second)
	if containsString(riskySteps, status.Step) && status.Completed == nil && status.Started.Before(fifteenSecondsAgo) {
		logger.Infof("Indicated a possible deadlock, status: %s, started at %s but hasn't finished "+
			"yet. Marking the probe as ready", status.Step, status.Started.Format(time.RFC3339))
		return true
	}
	return false
}

func isInGoalState(health healthStatus, secretReader SecretReader) (bool, error) {
	if isHeadlessMode() {
		return performCheckHeadlessMode(health, secretReader)
	}
	return performCheckOMMode(health)

}

// performCheckOMMode does a general check if the Agent has reached the goal state - must be called when Agent is in
// "OM mode"
func performCheckOMMode(health healthStatus) (bool, error) {
	for _, v := range health.Healthiness {
		logger.Debug(v)
		if v.IsInGoalState {
			return true, nil
		}
	}
	return false, nil
}

// performCheckHeadlessMode validates if the Agent has reached the correct goal state
// The state is fetched from K8s automation config map directly to avoid flakiness of mounting process
// Dev note: there is an alternative way to get current namespace: to read from
// /var/run/secrets/kubernetes.io/serviceaccount/namespace file (see
// https://kubernetes.io/docs/tasks/access-application-cluster/access-cluster/#accessing-the-api-from-a-pod)
// though passing the namespace as an environment variable makes the code simpler for testing and saves an IO operation
func performCheckHeadlessMode(health healthStatus, secretReader SecretReader) (bool, error) {
	namespace := os.Getenv(podNamespaceEnv)
	if namespace == "" {
		return false, fmt.Errorf("the '%s' environment variable must be set", podNamespaceEnv)
	}
	automationConfigMap := os.Getenv(automationConfigMapEnv)
	if automationConfigMap == "" {
		return false, fmt.Errorf("the '%s' environment variable must be set", automationConfigMapEnv)
	}

	configMap, err := secretReader.readSecret(namespace, automationConfigMap)
	if err != nil {
		return false, err
	}
	var existingDeployment map[string]interface{}
	if err := json.Unmarshal(configMap.Data[appDBAutomationConfigKey], &existingDeployment); err != nil {
		return false, err
	}

	version, ok := existingDeployment["version"]
	if !ok {
		return false, err
	}
	expectedVersion := cast.ToInt64(version)

	for _, v := range health.ProcessPlans {
		logger.Debugf("Automation Config version: %d, Agent last version: %d", expectedVersion, v.LastGoalStateClusterConfigVersion)
		return v.LastGoalStateClusterConfigVersion == expectedVersion, nil
	}

	return false, errors.New("health file doesn't have information about process status")
}

func isHeadlessMode() bool {
	return os.Getenv(headlessAgent) == "true"
}

func containsString(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

func getEnvOrDefault(envVar, defaultValue string) string {
	value := strings.TrimSpace(os.Getenv(envVar))
	if value == "" {
		return defaultValue
	}
	return value
}

func getHealthStatusFilePath() string {
	return getEnvOrDefault(agentHealthStatusFilePathEnv, defaultAgentHealthStatusFilePath)
}

func getLogFilePath() string {
	return getEnvOrDefault(logPathEnv, defaultLogPath)
}

// Main entry point to the readiness script. All configurations are specific to production environment so
// the method should not be directly called from unit tests
func main() {
	cfg := zap.NewDevelopmentConfig()
	// In "production" we log to the file
	cfg.OutputPaths = []string{
		getLogFilePath(),
	}
	log, err := cfg.Build()
	if err != nil {
		panic(err)
	}
	logger = log.Sugar()
	if !isPodReady(getHealthStatusFilePath(), newKubernetesSecretReader()) {
		os.Exit(1)
	}
}
