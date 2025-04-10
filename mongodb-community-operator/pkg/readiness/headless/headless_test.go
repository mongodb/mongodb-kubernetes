package headless

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes/fake"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/cmd/readiness/testdata"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/readiness/config"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/readiness/health"
)

func TestPerformCheckHeadlessMode(t *testing.T) {
	ctx := context.Background()
	c := testConfig()

	c.ClientSet = fake.NewSimpleClientset(testdata.TestPod(c.Namespace, c.Hostname), testdata.TestSecret(c.Namespace, c.AutomationConfigSecretName, 11))
	status := health.Status{
		MmsStatus: map[string]health.MmsDirectorStatus{c.Hostname: {
			LastGoalStateClusterConfigVersion: 10,
		}},
	}

	achieved, err := PerformCheckHeadlessMode(ctx, status, c)

	require.NoError(t, err)
	assert.False(t, achieved)

	thePod, _ := c.ClientSet.CoreV1().Pods(c.Namespace).Get(ctx, c.Hostname, metav1.GetOptions{})
	assert.Equal(t, map[string]string{"agent.mongodb.com/version": "10"}, thePod.Annotations)
}

func testConfig() config.Config {
	return config.Config{
		Namespace:                  "test-ns",
		AutomationConfigSecretName: "test-mongodb-automation-config",
		Hostname:                   "test-mongodb-0",
	}
}
