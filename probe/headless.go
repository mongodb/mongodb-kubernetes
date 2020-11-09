package main

import (
	"encoding/json"

	"github.com/10gen/ops-manager-kubernetes/probe/config"
	"github.com/10gen/ops-manager-kubernetes/probe/pod"
	"github.com/spf13/cast"
	"k8s.io/client-go/kubernetes"
)

// performCheckHeadlessMode validates if the Agent has reached the correct goal state
// The state is fetched from K8s automation config Secret directly to avoid flakiness of mounting process
// Dev note: there is an alternative way to get current namespace: to read from
// /var/run/secrets/kubernetes.io/serviceaccount/namespace file (see
// https://kubernetes.io/docs/tasks/access-application-cluster/access-cluster/#accessing-the-api-from-a-pod)
// though passing the namespace as an environment variable makes the code simpler for testing and saves an IO operation
func PerformCheckHeadlessMode(health healthStatus, conf config.Config) (bool, error) {
	targetVersion, err := readAutomationConfigVersionFromSecret(conf.Namespace, conf.ClientSet, conf.AutomationConfigSecretName)
	if err != nil {
		return false, err
	}

	currentAgentVersion := readCurrentAgentInfo(health, targetVersion)

	if err = pod.PatchPodAnnotation(conf.Namespace, currentAgentVersion, conf.Hostname, conf.ClientSet); err != nil {
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

func readAutomationConfigVersionFromSecret(namespace string, clientSet kubernetes.Interface, automationConfigMap string) (int64, error) {
	secretReader := newKubernetesSecretReader(clientSet)
	theSecret, err := secretReader.readSecret(namespace, automationConfigMap)
	if err != nil {
		return -1, err
	}
	var existingDeployment map[string]interface{}
	if err := json.Unmarshal(theSecret.Data[appDBAutomationConfigKey], &existingDeployment); err != nil {
		return -1, err
	}

	version, ok := existingDeployment["version"]
	if !ok {
		return -1, err
	}
	return cast.ToInt64(version), nil
}
