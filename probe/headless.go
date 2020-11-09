package main

import (
	"encoding/json"
	"fmt"
	"github.com/10gen/ops-manager-kubernetes/probe/pod"
	"os"

	"github.com/spf13/cast"
)

func hostname() string {
	return os.Getenv("HOSTNAME")
}

// performCheckHeadlessMode validates if the Agent has reached the correct goal state
// The state is fetched from K8s automation config Secret directly to avoid flakiness of mounting process
// Dev note: there is an alternative way to get current namespace: to read from
// /var/run/secrets/kubernetes.io/serviceaccount/namespace file (see
// https://kubernetes.io/docs/tasks/access-application-cluster/access-cluster/#accessing-the-api-from-a-pod)
// though passing the namespace as an environment variable makes the code simpler for testing and saves an IO operation
func performCheckHeadlessMode(podNamespace string, health healthStatus, secretReader SecretReader, patcher pod.Patcher) (bool, error) {
	targetVersion, err := readAutomationConfigVersionFromSecret(podNamespace, secretReader)
	if err != nil {
		return false, err
	}

	currentAgentVersion := readCurrentAgentInfo(health, targetVersion)

	if err = pod.PatchPodAnnotation(podNamespace, currentAgentVersion, hostname(), patcher); err != nil {
		return false, err
	}

	return targetVersion == currentAgentVersion, nil
}

// readCurrentAgentInfo returns the version the Agent has reached and the rs member name
func readCurrentAgentInfo(health healthStatus, targetVersion int64) int64 {
	for _, v := range health.ProcessPlans {
		logger.Debugf("Automation Config version: %d, Agent last version: %d", targetVersion, v.LastGoalStateClusterConfigVersion)
		return v.LastGoalStateClusterConfigVersion
	}
	// The edge case: if the scale down operation is happening and the member + process are removed
	// from the Automation Config - the Agent just doesn't write the 'mmsStatus' at all so there is no indication of
	// the version it has achieved (though health file contains 'IsInGoalState=true')
	// Let's return the desired version in case if the Agent is in goal state and no plans exist in the health file
	for _, v := range health.Healthiness {
		if v.IsInGoalState {
			return targetVersion
		}
		return -1
	}

	// There's a small theoretical probability that the Pod got restarted right when the Agent shutdown the Mongodb
	// on scale down - in this case the 'health' file is empty - so we return the target version to avoid locking
	// the Operator waiting for the annotation
	return targetVersion
}

func readAutomationConfigVersionFromSecret(namespace string, secretReader SecretReader) (int64, error) {
	automationConfigMap := os.Getenv(automationConfigMapEnv)
	if automationConfigMap == "" {
		return -1, fmt.Errorf("the '%s' environment variable must be set", automationConfigMapEnv)
	}

	secret, err := secretReader.readSecret(namespace, automationConfigMap)
	if err != nil {
		return -1, err
	}
	var existingDeployment map[string]interface{}
	if err := json.Unmarshal(secret.Data[appDBAutomationConfigKey], &existingDeployment); err != nil {
		return -1, err
	}

	version, ok := existingDeployment["version"]
	if !ok {
		return -1, err
	}
	return cast.ToInt64(version), nil
}
