package pod

import (
	"go.uber.org/zap"
	"strconv"
)

type mongodbAgentVersion struct {
	Version string `json:"agent.mongodb.com/version"`
}

func PatchPodAnnotation(podNamespace string, lastVersionAchieved int64, memberName string, patcher Patcher) error {
	mdbAgentVersion := mongodbAgentVersion{Version: strconv.FormatInt(lastVersionAchieved, 10)}
	return patchPod(patcher, podNamespace, mdbAgentVersion, memberName)
}

func patchPod(patcher Patcher, podNamespace string, mdbAgentVersion mongodbAgentVersion, memberName string) error {
	payload := []patchValue{{
		Op:    "add",
		Path:  "/metadata/annotations",
		Value: mdbAgentVersion,
	}}

	pod, err := patcher.patchPod(podNamespace, memberName, payload)
	if pod != nil {
		zap.S().Debugf("Updated Pod annotation: %v (%s)", pod.Annotations, memberName)
	}
	return err
}
