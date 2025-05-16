package pod

import (
	"context"
	"strconv"
	"strings"

	"go.uber.org/zap"
	"k8s.io/client-go/kubernetes"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const mongodbAgentVersionAnnotation = "agent.mongodb.com/version"

func PatchPodAnnotation(ctx context.Context, podNamespace string, lastVersionAchieved int64, memberName string, clientSet kubernetes.Interface) error {
	pod, err := clientSet.CoreV1().Pods(podNamespace).Get(ctx, memberName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	var payload []patchValue

	if len(pod.Annotations) == 0 {
		payload = append(payload, patchValue{
			Op:    "add",
			Path:  "/metadata/annotations",
			Value: make(map[string]string),
		})
	}
	mdbAgentVersion := strconv.FormatInt(lastVersionAchieved, 10)
	payload = append(payload, patchValue{
		Op:    "add",
		Path:  "/metadata/annotations/" + strings.ReplaceAll(mongodbAgentVersionAnnotation, "/", "~1"),
		Value: mdbAgentVersion,
	})

	patcher := NewKubernetesPodPatcher(clientSet)
	updatedPod, err := patcher.patchPod(ctx, podNamespace, memberName, payload)
	if updatedPod != nil {
		zap.S().Debugf("Updated Pod annotation: %v (%s)", pod.Annotations, memberName)
	}
	return err
}
