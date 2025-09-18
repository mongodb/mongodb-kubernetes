package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/google/go-cmp/cmp"
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
)

func main() {
	ctx := context.Background()
	logger := setupLogger()

	logger.Info("Running agent sidecar for rolling restart coordination")

	if statusPath := os.Getenv(agentStatusFilePathEnv); statusPath == "" {
		logger.Fatalf(`Required environment variable "%s" not set`, agentStatusFilePathEnv)
		return
	}

	// Continuously monitor agent health status for WaitDeleteMyPodKube step
	var lastHealth agent.Health
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

		// Check if health status has changed
		if isFirstRead {
			logger.Infof("Agent health status initialized: %s", getHealthSummary(health))
			lastHealth = health
			isFirstRead = false
		} else if diff := cmp.Diff(lastHealth, health); diff != "" {
			logger.Infof("Agent health status changed:\n%s", diff)
			lastHealth = health
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



// getHealthSummary returns a concise summary of the current health status
func getHealthSummary(health agent.Health) string {
	hostname := getHostname()

	// Check process health
	status, hasStatus := health.Healthiness[hostname]
	processStatus, hasProcessStatus := health.ProcessPlans[hostname]

	if !hasStatus {
		return "no_health_data"
	}

	goalState := "in_goal"
	if !status.IsInGoalState {
		goalState = "not_in_goal"
	}

	if !hasProcessStatus || len(processStatus.Plans) == 0 {
		return fmt.Sprintf("%s_no_plans", goalState)
	}

	// Get the latest plan
	latestPlan := processStatus.Plans[len(processStatus.Plans)-1]
	if latestPlan.Completed != nil {
		return fmt.Sprintf("%s_plan_completed", goalState)
	}

	// Find current move
	for _, move := range latestPlan.Moves {
		if move.Move == "WaitDeleteMyPodKube" {
			return "waiting_for_pod_deletion"
		}
		// Check if this move has incomplete steps
		for _, step := range move.Steps {
			if step.Completed == nil {
				return fmt.Sprintf("%s_executing_%s", goalState, move.Move)
			}
		}
	}

	return fmt.Sprintf("%s_plan_running", goalState)
}

// shouldDeletePod returns a boolean value indicating if this pod should be deleted
// this would be the case if the agent is currently trying to execute WaitDeleteMyPodKube step
func shouldDeletePod(health agent.Health) (bool, error) {
	status, ok := health.ProcessPlans[getHostname()]
	if !ok {
		return false, fmt.Errorf("hostname %s was not in the process plans", getHostname())
	}
	return isWaitingToBeDeleted(status), nil
}

// isWaitingToBeDeleted determines if the agent is currently waiting
// for the pod to be deleted by external sidecar. We check if the most recent step
// is "WaitDeleteMyPodKube"
func isWaitingToBeDeleted(healthStatus agent.MmsDirectorStatus) bool {
	if len(healthStatus.Plans) == 0 {
		return false
	}
	lastPlan := healthStatus.Plans[len(healthStatus.Plans)-1]
	for _, m := range lastPlan.Moves {
		// Check if the current step is WaitDeleteMyPodKube
		if m.Move == "WaitDeleteMyPodKube" {
			return true
		}
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
