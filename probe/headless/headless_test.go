package headless

import (
	"testing"

	"github.com/10gen/ops-manager-kubernetes/probe/health"
	"github.com/10gen/ops-manager-kubernetes/probe/testdata"

	"github.com/10gen/ops-manager-kubernetes/probe/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestPerformCheckHeadlessMode(t *testing.T) {
	c := testConfig()
	c.ClientSet = fake.NewSimpleClientset(testdata.TestPod(c.Namespace, c.Hostname), testdata.TestSecret(c.Namespace, c.AutomationConfigSecretName, 11))
	status := health.Status{
		ProcessPlans: map[string]health.MmsDirectorStatus{c.Hostname: {
			LastGoalStateClusterConfigVersion: 10,
		}},
	}

	achieved, err := PerformCheckHeadlessMode(status, c)

	require.NoError(t, err)
	assert.False(t, achieved)

	thePod, _ := c.ClientSet.CoreV1().Pods(c.Namespace).Get(c.Hostname, metav1.GetOptions{})
	assert.Equal(t, map[string]string{"agent.mongodb.com/version": "10"}, thePod.Annotations)
}

func testConfig() config.Config {
	return config.Config{
		Namespace:                  "test-ns",
		AutomationConfigSecretName: "test-mongodb-automation-config",
		Hostname:                   "test-mongodb-0",
	}
}
