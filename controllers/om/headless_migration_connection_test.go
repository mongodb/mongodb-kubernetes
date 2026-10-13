package om

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubernetesClient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

const testMigrationBaselineVersion = int64(7)

func migrationTestPod(name string, headless bool, achievedVersion string) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testHeadlessNamespace,
			OwnerReferences: []metav1.OwnerReference{
				{APIVersion: "apps/v1", Kind: "StatefulSet", Name: "my-replica-set"},
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: util.AgentContainerName}},
		},
	}
	if headless {
		pod.Spec.Containers[0].Env = []corev1.EnvVar{{Name: util.HeadlessAgentEnv, Value: "true"}}
	}
	if achievedVersion != "" {
		pod.Annotations = map[string]string{HeadlessAgentVersionAnnotation: achievedVersion}
	}
	return pod
}

// newTestMigrationTarget returns a mocked target Ops Manager connection that only knows about
// the first process, i.e. the one whose pod has already been rolled to the online agent.
func newTestMigrationTarget(onlineProcessName string) *MockedOmConnection {
	target := NewMockedOmConnection(NewDeployment())
	target.ReadAutomationStatusFunc = func() (*AutomationStatus, error) {
		return &AutomationStatus{
			GoalVersion: 1,
			Processes: []ProcessStatus{
				{Name: onlineProcessName, Hostname: onlineProcessName + ".old-host.example.com", LastGoalVersionAchieved: 1},
			},
		}, nil
	}
	target.ReadAutomationAgentsFunc = func(_ int) (Paginated, error) {
		return AutomationAgentStatusResponse{
			AutomationAgents: []AgentStatus{{
				Hostname:  onlineProcessName + ".old-host.example.com",
				LastConf:  time.Now().UTC().Format(time.RFC3339),
				TypeName:  "AUTOMATION",
				StateName: "ACTIVE",
			}},
			OMPaginated: OMPaginated{TotalCount: 1},
		}, nil
	}
	return target
}

// newTestMigrationConnection sets up a headless connection over the <name>-config Secret (4
// processes, baseline version 7) and the given pods, wrapped by the migration decorator. Pods
// my-replica-set-1 and my-replica-set-2 are always added as headless pods.
func newTestMigrationConnection(t *testing.T, additionalPods ...kubernetesClient.Object) *HeadlessMigrationConnection {
	t.Helper()

	objs := append([]kubernetesClient.Object{
		migrationTestPod("my-replica-set-1", true, "7"),
		migrationTestPod("my-replica-set-2", true, "5"),
	}, additionalPods...)

	headless, _ := newTestHeadlessConnection(t, objs...)

	deployment := NewDeployment()
	deployment["version"] = testMigrationBaselineVersion
	deployment["processes"] = []Process{
		headlessTestProcess("my-replica-set-0"),
		headlessTestProcess("my-replica-set-1"),
		headlessTestProcess("my-replica-set-2"),
		headlessTestProcess("my-replica-set-3"),
	}
	_, err := headless.UpdateDeployment(deployment)
	require.NoError(t, err)

	return NewHeadlessMigrationConnection(newTestMigrationTarget("my-replica-set-0"), headless, testMigrationBaselineVersion)
}

func TestHeadlessMigrationConnectionReadAutomationStatus(t *testing.T) {
	migration := newTestMigrationConnection(t, migrationTestPod("my-replica-set-0", false, ""))

	status, err := migration.ReadAutomationStatus()
	require.NoError(t, err)
	assert.Equal(t, 1, status.GoalVersion)

	achieved := map[string]int{}
	for _, process := range status.Processes {
		achieved[process.Name] = process.LastGoalVersionAchieved
	}
	assert.Equal(t, 1, achieved["my-replica-set-0"], "online pod keeps the Ops Manager entry")
	assert.Equal(t, 1, achieved["my-replica-set-1"], "headless pod at baseline is normalized to the target goal")
	assert.Equal(t, 5, achieved["my-replica-set-2"], "headless pod behind baseline is reported as-is")
	assert.NotContains(t, achieved, "my-replica-set-3", "a process without a pod is absent")

	// processes come back sorted by name for deterministic downstream comparisons
	require.Len(t, status.Processes, 3)
	assert.Equal(t, []string{"my-replica-set-0", "my-replica-set-1", "my-replica-set-2"}, []string{
		status.Processes[0].Name, status.Processes[1].Name, status.Processes[2].Name,
	})
}

func TestHeadlessMigrationConnectionReadAutomationAgents(t *testing.T) {
	migration := newTestMigrationConnection(t, migrationTestPod("my-replica-set-0", false, ""))

	response, err := migration.ReadAutomationAgents(1)
	require.NoError(t, err)
	agents, ok := response.(AutomationAgentStatusResponse)
	require.True(t, ok)

	hostnames := make([]string, 0, len(agents.AutomationAgents))
	for _, agent := range agents.AutomationAgents {
		hostnames = append(hostnames, agent.Hostname)
	}

	assert.Contains(t, hostnames, "my-replica-set-0.old-host.example.com", "target agents are preserved")
	assert.Contains(t, hostnames, headlessTestProcess("my-replica-set-1").HostName(), "headless agents are synthesized")
	assert.Contains(t, hostnames, headlessTestProcess("my-replica-set-2").HostName(), "headless agents are synthesized")
	assert.NotContains(t, hostnames, headlessTestProcess("my-replica-set-0").HostName(),
		"an online pod must not be synthesized as a registered agent")
	assert.NotContains(t, hostnames, headlessTestProcess("my-replica-set-3").HostName(),
		"a process without a pod is not registered")
	assert.Len(t, hostnames, 3)
	assert.Equal(t, testMigrationBaselineVersion, migration.BaselineVersion())
}
