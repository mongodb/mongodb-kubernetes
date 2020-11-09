package main

import (
	"fmt"
	"testing"

	"github.com/10gen/ops-manager-kubernetes/probe/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestPerformCheckHeadlessMode(t *testing.T) {
	c := testConfigForHeadless()
	c.ClientSet = fake.NewSimpleClientset(testPod(c.Namespace, c.Hostname), testSecret(c.Namespace, c.AutomationConfigSecretName, 11))
	status := healthStatus{
		ProcessPlans: map[string]mmsDirectorStatus{c.Hostname: {
			LastGoalStateClusterConfigVersion: 10,
		}},
	}

	achieved, err := PerformCheckHeadlessMode(status, c)

	require.NoError(t, err)
	assert.False(t, achieved)

	thePod, _ := c.ClientSet.CoreV1().Pods(c.Namespace).Get(c.Hostname, metav1.GetOptions{})
	assert.Equal(t, map[string]string{"agent.mongodb.com/version": "10"}, thePod.Annotations)
}

func testConfigForHeadless() config.Config {
	return config.Config{
		Namespace:                  "test-ns",
		AutomationConfigSecretName: "test-mongodb-automation-config",
		Hostname:                   "test-mongodb-0",
	}
}
func testSecret(namespace, name string, version int) *corev1.Secret {
	// We don't need to create a full automation config - just the json with version field is enough
	deployment := fmt.Sprintf("{\"version\": %d}", version)
	secret := &corev1.Secret{Data: map[string][]byte{"cluster-config.json": []byte(deployment)}}
	secret.ObjectMeta = metav1.ObjectMeta{Namespace: namespace, Name: name}
	return secret
}
func testPod(namespace, name string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
}
