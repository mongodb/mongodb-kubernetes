package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"go.uber.org/zap"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/agent"
)

const (
	agentStatusFilePathEnv = "AGENT_STATUS_FILEPATH"

	defaultNamespace = "default"

	pollingInterval time.Duration = 1 * time.Second
	pollingDuration time.Duration = 60 * time.Second

	// Agent log file paths (typically in /var/log/mongodb-mms-automation/)
	agentLogPath        = "/var/log/mongodb-mms-automation/automation-agent.log"
	agentVerboseLogPath = "/var/log/mongodb-mms-automation/automation-agent-verbose.log"
)

// getCurrentStepInfo extracts current step and move information from health status
func getCurrentStepInfo(health agent.Health) string {
	var info []string

	for processName, status := range health.ProcessPlans {
		if len(status.Plans) == 0 {
			continue
		}

		// Get the most recent plan (last in the array)
		latestPlan := status.Plans[len(status.Plans)-1]

		// Find the current move and step
		for _, move := range latestPlan.Moves {
			for _, step := range move.Steps {
				// If step is not completed, it's the current step
				if step.Completed == nil && step.Started != nil {
					info = append(info, fmt.Sprintf("%s: %s -> %s", processName, move.Move, step.Step))
				}
			}
		}
	}

	if len(info) > 0 {
		return fmt.Sprintf(" [Current: %s]", strings.Join(info, ", "))
	}
	return ""
}



// AgentVersionInfo holds version information about the MongoDB automation agent
type AgentVersionInfo struct {
	Version     string `json:"version"`
	ImageTag    string `json:"imageTag"`
	Source      string `json:"source"`
	LastChecked string `json:"lastChecked"`
}

// getAgentVersion attempts to determine the MongoDB automation agent version
func getAgentVersion() AgentVersionInfo {
	info := AgentVersionInfo{
		LastChecked: time.Now().Format(time.RFC3339),
	}

	// Try to get version from agent logs first (most reliable)
	if version := getVersionFromAgentLogs(); version != "" {
		info.Version = version
		info.Source = "agent_logs"
		return info
	}

	// Fallback: try to get version from process list
	if version := getVersionFromProcessList(); version != "" {
		info.Version = version
		info.Source = "process_list"
		return info
	}

	info.Version = "unknown"
	info.Source = "not_found"
	return info
}

// getVersionFromAgentLogs reads the MongoDB automation agent logs to extract version information
func getVersionFromAgentLogs() string {
	// Try verbose log first, then regular log
	logPaths := []string{agentVerboseLogPath, agentLogPath}

	for _, logPath := range logPaths {
		if version := readVersionFromLogFile(logPath); version != "" {
			return version
		}
	}
	return ""
}

// readVersionFromLogFile reads a log file and extracts version information
func readVersionFromLogFile(logPath string) string {
	file, err := os.Open(logPath)
	if err != nil {
		return ""
	}
	defer file.Close()

	// Read the first few KB of the log file where version info is typically logged
	buffer := make([]byte, 8192)
	n, err := file.Read(buffer)
	if err != nil && err != io.EOF {
		return ""
	}

	content := string(buffer[:n])

	// Look for version patterns in agent logs
	// Examples: "MongoDB Automation Agent version 108.0.12.8846-1"
	//          "automation-agent version: 108.0.12.8846-1"
	//          "Starting automation agent 108.0.12.8846-1"
	versionRegex := regexp.MustCompile(`(?i)(?:automation.?agent|mongodb.?automation.?agent).*?version[:\s]+([0-9]+\.[0-9]+\.[0-9]+\.[0-9]+(?:\-[0-9]+)?)`)
	matches := versionRegex.FindStringSubmatch(content)
	if len(matches) > 1 {
		return matches[1]
	}

	// Alternative pattern: look for version in startup messages
	versionRegex2 := regexp.MustCompile(`(?i)starting.*?agent.*?([0-9]+\.[0-9]+\.[0-9]+\.[0-9]+(?:\-[0-9]+)?)`)
	matches2 := versionRegex2.FindStringSubmatch(content)
	if len(matches2) > 1 {
		return matches2[1]
	}

	return ""
}

// getVersionFromProcessList attempts to get agent version from running processes
func getVersionFromProcessList() string {
	// Try to find the automation agent process and extract version from command line
	// This is a fallback method when logs are not available

	// Read /proc/*/cmdline to find automation agent processes
	procDirs, err := os.ReadDir("/proc")
	if err != nil {
		return ""
	}

	for _, procDir := range procDirs {
		if !procDir.IsDir() {
			continue
		}

		// Check if directory name is numeric (PID)
		if matched, _ := regexp.MatchString(`^\d+$`, procDir.Name()); !matched {
			continue
		}

		cmdlinePath := fmt.Sprintf("/proc/%s/cmdline", procDir.Name())
		cmdlineBytes, err := os.ReadFile(cmdlinePath)
		if err != nil {
			continue
		}

		cmdline := string(cmdlineBytes)

		// Look for automation agent in command line
		if strings.Contains(cmdline, "automation-agent") || strings.Contains(cmdline, "mongodb-mms-automation-agent") {
			// Try to extract version from command line arguments
			versionRegex := regexp.MustCompile(`([0-9]+\.[0-9]+\.[0-9]+\.[0-9]+(?:\-[0-9]+)?)`)
			matches := versionRegex.FindStringSubmatch(cmdline)
			if len(matches) > 1 {
				return matches[1]
			}
		}
	}

	return ""
}

// getLastGoalStateClusterConfigVersion extracts the LastGoalStateClusterConfigVersion from health status
func getLastGoalStateClusterConfigVersion(health agent.Health) int64 {
	hostname := getHostname()
	if processStatus, ok := health.ProcessPlans[hostname]; ok {
		return processStatus.LastGoalStateClusterConfigVersion
	}
	return 0
}

// getCurrentMoveAndStep returns a concise description of the current move and step
func getCurrentMoveAndStep(health agent.Health) string {
	hostname := getHostname()

	// Check process health
	status, hasStatus := health.Healthiness[hostname]
	processStatus, hasProcessStatus := health.ProcessPlans[hostname]

	if !hasStatus {
		return "no health data"
	}

	goalState := "in_goal"
	if !status.IsInGoalState {
		goalState = "not_in_goal"
	}

	if !hasProcessStatus || len(processStatus.Plans) == 0 {
		return fmt.Sprintf("%s (no plans)", goalState)
	}

	// Get the latest plan
	latestPlan := processStatus.Plans[len(processStatus.Plans)-1]
	if latestPlan.Completed != nil {
		return fmt.Sprintf("%s (plan completed)", goalState)
	}

	// Find current move and step
	for _, move := range latestPlan.Moves {
		// Check if this move has incomplete steps
		for _, step := range move.Steps {
			if step.Completed == nil {
				return fmt.Sprintf("current move: %s, current step: %s", move.Move, step.Step)
			}
		}
	}

	return fmt.Sprintf("%s (plan running)", goalState)
}

// startVersionAPI starts a simple HTTP server to expose agent version information
func startVersionAPI(port string) {
	http.HandleFunc("/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		versionInfo := getAgentVersion()
		json.NewEncoder(w).Encode(versionInfo)
	})

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{
			"status": "healthy",
			"time":   time.Now().Format(time.RFC3339),
		})
	})

	go func() {
		zap.S().Infof("Starting version API server on port %s", port)
		if err := http.ListenAndServe(":"+port, nil); err != nil {
			zap.S().Errorf("Version API server failed: %v", err)
		}
	}()
}

func main() {
	ctx := context.Background()
	logger := setupLogger()

	// Get and log agent version information
	versionInfo := getAgentVersion()
	logger.Infof("Running agent sidecar for rolling restart coordination")
	logger.Infof("MongoDB Agent Version: %s (source: %s)", versionInfo.Version, versionInfo.Source)
	if versionInfo.ImageTag != "" {
		logger.Infof("Agent Image: %s", versionInfo.ImageTag)
	}

	// Start version API server on port 8080
	startVersionAPI("8080")

	if statusPath := os.Getenv(agentStatusFilePathEnv); statusPath == "" {
		logger.Fatalf(`Required environment variable "%s" not set`, agentStatusFilePathEnv)
		return
	}

	// Continuously monitor agent health status for WaitDeleteMyPodKube step
	var lastConfigVersion int64 = -1
	var isFirstRead = true
	for {
		health, err := getAgentHealthStatus()
		if err != nil {
			// If the pod has just restarted then the status file will not exist.
			// In that case we continue monitoring
			if os.IsNotExist(err) {
				logger.Info("Agent health status file not found, monitoring...")
			} else {
				logger.Errorf("Error getting the agent health file: %s", err)
			}

			// Wait before trying again
			time.Sleep(5 * time.Second)
			continue
		}

		// Check if LastGoalStateClusterConfigVersion has changed
		currentConfigVersion := getLastGoalStateClusterConfigVersion(health)
		if isFirstRead {
			hostname := getHostname()
			logger.Infof("Agent health status initialized for hostname '%s': %s", hostname, getCurrentMoveAndStep(health))

			// Debug: show available hostnames in health data
			var availableHostnames []string
			for h := range health.Healthiness {
				availableHostnames = append(availableHostnames, h)
			}
			if len(availableHostnames) > 0 {
				logger.Infof("Available hostnames in health data: %v", availableHostnames)
			} else {
				logger.Warnf("No hostnames found in health data")
			}

			lastConfigVersion = currentConfigVersion
			isFirstRead = false
		} else if currentConfigVersion != lastConfigVersion {
			logger.Infof("Cluster config version changed from %d to %d: %s",
				lastConfigVersion, currentConfigVersion, getCurrentMoveAndStep(health))
			lastConfigVersion = currentConfigVersion
		}

		shouldDelete, err := shouldDeletePod(health)
		if err != nil {
			logger.Errorf("Error checking if pod should be deleted: %s", err)
			time.Sleep(5 * time.Second)
			continue
		}

		if shouldDelete {
			logger.Infof("🚨 WaitDeleteMyPodKube step detected - deleting pod")
			if err := deletePod(ctx); err != nil {
				// We should not raise an error if the Pod could not be deleted. It can have even
				// worse consequences: Pod being restarted with the same version, and the agent
				// killing it immediately after.
				logger.Errorf("Could not manually trigger restart of this Pod because of: %s", err)
				logger.Errorf("Make sure the Pod is restarted in order for the upgrade process to continue")
			} else {
				logger.Info("✅ Successfully deleted pod - waiting for restart...")
			}

			// If the Pod needs to be killed, we'll wait until the Pod
			// is killed by Kubernetes, bringing the new container image
			// into play.
			quit := make(chan struct{})
			logger.Info("Pod killed itself, waiting...")
			<-quit
		} else {
			// Continue monitoring
			time.Sleep(2 * time.Second)
		}
	}
}

func setupLogger() *zap.SugaredLogger {
	log, err := zap.NewDevelopment()
	if err != nil {
		zap.S().Errorf("Error building logger config: %s", err)
		os.Exit(1)
	}

	return log.Sugar()
}

// waitForAgentHealthStatus will poll the health status file and wait for it to be updated.
// The agent doesn't write the plan to the file right away and hence we need to wait for the
// latest plan to be written.
func waitForAgentHealthStatus() (agent.Health, error) {
	ticker := time.NewTicker(pollingInterval)
	defer ticker.Stop()

	totalTime := time.Duration(0)
	for range ticker.C {
		if totalTime > pollingDuration {
			break
		}
		totalTime += pollingInterval

		health, err := getAgentHealthStatus()
		if err != nil {
			return agent.Health{}, err
		}

		status, ok := health.Healthiness[getHostname()]
		if !ok {
			return agent.Health{}, fmt.Errorf("couldn't find status for hostname %s", getHostname())
		}

		// We determine if the file has been updated by checking if the process is not in goal state.
		// As the agent is currently executing a plan, the process should not be in goal state.
		if !status.IsInGoalState {
			return health, nil
		}
	}
	return agent.Health{}, fmt.Errorf("agent health status not ready after waiting %s", pollingDuration.String())
}

// getAgentHealthStatus returns an instance of agent.Health read
// from the health file on disk
func getAgentHealthStatus() (agent.Health, error) {
	f, err := os.Open(os.Getenv(agentStatusFilePathEnv))
	if err != nil {
		return agent.Health{}, err
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			zap.S().Warnf("Failed to close agent health file: %v", closeErr)
		}
	}()

	h, err := readAgentHealthStatus(f)
	if err != nil {
		return agent.Health{}, fmt.Errorf("could not read health status file: %s", err)
	}
	return h, err
}



// readAgentHealthStatus reads an instance of health.Health from the provided
// io.Reader
func readAgentHealthStatus(reader io.Reader) (agent.Health, error) {
	var h agent.Health
	data, err := io.ReadAll(reader)
	if err != nil {
		return h, err
	}
	err = json.Unmarshal(data, &h)
	return h, err
}

func getHostname() string {
	return os.Getenv("HOSTNAME")
}





// shouldDeletePod returns a boolean value indicating if this pod should be deleted
// this would be the case if the agent is currently trying to execute WaitDeleteMyPodKube step
func shouldDeletePod(health agent.Health) (bool, error) {
	hostname := getHostname()
	status, ok := health.ProcessPlans[hostname]
	if !ok {
		// Don't error on missing hostname - this is normal during startup
		// Just return false (don't delete) and let the monitoring continue
		return false, nil
	}
	return isWaitingToBeDeleted(status), nil
}

// isWaitingToBeDeleted determines if the agent is currently waiting
// for the pod to be deleted by external sidecar. We check if the current/last step
// is "WaitDeleteMyPodKube"
func isWaitingToBeDeleted(healthStatus agent.MmsDirectorStatus) bool {
	if len(healthStatus.Plans) == 0 {
		return false
	}
	lastPlan := healthStatus.Plans[len(healthStatus.Plans)-1]

	lastMove := lastPlan.Moves[len(lastPlan.Moves)-1]
	lastStep := lastMove.Steps[len(lastMove.Steps)-1]
	if lastStep.Step == "WaitDeleteMyPodKube" && lastStep.Completed == nil && lastStep.Started != nil {
		return true
	}
	return false
}

// deletePod attempts to delete the pod this mongod is running in
func deletePod(ctx context.Context) error {
	thisPod, err := getThisPod()
	if err != nil {
		return fmt.Errorf("could not get pod: %s", err)
	}
	k8sClient, err := inClusterClient()
	if err != nil {
		return fmt.Errorf("could not get client: %s", err)
	}

	if err := k8sClient.Delete(ctx, &thisPod); err != nil {
		return fmt.Errorf("could not delete pod: %s", err)
	}
	return nil
}

// getThisPod returns an instance of corev1.Pod that points to the current pod
func getThisPod() (corev1.Pod, error) {
	podName := getHostname()
	if podName == "" {
		return corev1.Pod{}, fmt.Errorf("environment variable HOSTNAME was not present")
	}

	ns, err := getNamespace()
	if err != nil {
		return corev1.Pod{}, fmt.Errorf("could not read namespace: %s", err)
	}

	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: ns,
		},
	}, nil
}

func inClusterClient() (client.Client, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("could not get cluster config: %s", err)
	}

	k8sClient, err := client.New(config, client.Options{})
	if err != nil {
		return nil, fmt.Errorf("could not create client: %s", err)
	}
	return k8sClient, nil
}

func getNamespace() (string, error) {
	data, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		return "", err
	}
	if ns := strings.TrimSpace(string(data)); len(ns) > 0 {
		return ns, nil
	}
	return defaultNamespace, nil
}
