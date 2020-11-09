package main

import (
	"github.com/10gen/ops-manager-kubernetes/probe/pod"
	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"os"
	"testing"
)

func TestPerformCheckHeadlessMode(t *testing.T) {
	namespace := "test-ns"
	podName := "test-mongodb-0"
	_ = os.Setenv(automationConfigMapEnv, "test-mongodb-automation-config")
	_ = os.Setenv("HOSTNAME", podName)
	status := healthStatus{
		ProcessPlans: map[string]mmsDirectorStatus{podName: {
			LastGoalStateClusterConfigVersion: 10,
		}},
	}
	clientSet := fake.NewSimpleClientset(&v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: namespace,
		},
	})
	patcher := pod.NewKubernetesPodPatcher(clientSet)
	achieved, err := performCheckHeadlessMode(namespace, status, NewMockedSecretReader(namespace, "test-mongodb-automation-config", 11), patcher)

	assert.NoError(t, err)
	assert.False(t, achieved)

	thePod, _ := clientSet.CoreV1().Pods(namespace).Get(podName, metav1.GetOptions{})
	assert.Equal(t, map[string]string{"agent.mongodb.com/version": "10"}, thePod.Annotations)
}
