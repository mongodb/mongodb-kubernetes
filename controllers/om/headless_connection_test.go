package om

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubernetesClient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/mongodb/mongodb-kubernetes/pkg/automationconfig"
)

const testHeadlessNamespace = "test-namespace"

func newTestHeadlessConnection(t *testing.T, objs ...kubernetesClient.Object) (*HeadlessConnection, kubernetesClient.Client) {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	conn, ok := NewHeadlessConnection(&OMContext{
		GroupID:   "my-replica-set",
		GroupName: "my-replica-set",
		OrgID:     testHeadlessNamespace,
		Headless: &HeadlessContext{
			Client:     kubeClient,
			Namespace:  testHeadlessNamespace,
			Name:       "my-replica-set",
			SecretName: "my-replica-set" + HeadlessAutomationConfigSecretSuffix,
		},
	}).(*HeadlessConnection)
	require.True(t, ok)

	return conn, kubeClient
}

func readHeadlessACSecret(t *testing.T, kubeClient kubernetesClient.Client) map[string][]byte {
	t.Helper()

	secret := &corev1.Secret{}
	err := kubeClient.Get(t.Context(), kubernetesClient.ObjectKey{
		Namespace: testHeadlessNamespace,
		Name:      "my-replica-set" + HeadlessAutomationConfigSecretSuffix,
	}, secret)
	require.NoError(t, err)
	return secret.Data
}

func headlessTestProcess(name string) Process {
	return NewProcessFromInterface(map[string]interface{}{
		"name":     name,
		"hostname": name + ".my-replica-set-svc." + testHeadlessNamespace + ".svc.cluster.local",
		"version":  "8.0.6",
	})
}

func TestHeadlessConnectionReadsEmptyDeploymentWhenSecretMissing(t *testing.T) {
	conn, _ := newTestHeadlessConnection(t)

	deployment, err := conn.ReadDeployment()
	require.NoError(t, err)
	assert.Equal(t, 0, deployment.NumberOfProcesses())
	assert.Equal(t, int64(-1), deployment.Version())
}

func TestHeadlessConnectionUpdateDeploymentVersionsChanges(t *testing.T) {
	conn, kubeClient := newTestHeadlessConnection(t)

	deployment := NewDeployment()
	_, err := conn.UpdateDeployment(deployment)
	require.NoError(t, err)

	raw := readHeadlessACSecret(t, kubeClient)[automationconfig.ConfigKey]
	require.NotEmpty(t, raw)
	stored, err := BuildDeploymentFromBytes(raw)
	require.NoError(t, err)
	assert.Equal(t, int64(1), stored.Version())

	// No semantic change: the version must not move.
	deployment = stored.ToCanonicalForm()
	_, err = conn.UpdateDeployment(deployment)
	require.NoError(t, err)
	stored, err = BuildDeploymentFromBytes(readHeadlessACSecret(t, kubeClient)[automationconfig.ConfigKey])
	require.NoError(t, err)
	assert.Equal(t, int64(1), stored.Version())

	// A real change bumps the version and is persisted.
	deployment = stored.ToCanonicalForm()
	deployment.setProcesses([]Process{headlessTestProcess("my-replica-set-0")})
	_, err = conn.UpdateDeployment(deployment)
	require.NoError(t, err)

	stored, err = BuildDeploymentFromBytes(readHeadlessACSecret(t, kubeClient)[automationconfig.ConfigKey])
	require.NoError(t, err)
	assert.Equal(t, int64(2), stored.Version())
	assert.Equal(t, 1, stored.NumberOfProcesses())
}

func TestHeadlessConnectionReadUpdateDeploymentPersistsChanges(t *testing.T) {
	conn, kubeClient := newTestHeadlessConnection(t)

	err := conn.ReadUpdateDeployment(func(deployment Deployment) error {
		deployment.setProcesses([]Process{headlessTestProcess("my-replica-set-0")})
		return nil
	}, zap.S())
	require.NoError(t, err)

	stored, err := BuildDeploymentFromBytes(readHeadlessACSecret(t, kubeClient)[automationconfig.ConfigKey])
	require.NoError(t, err)
	assert.Equal(t, int64(1), stored.Version())
	require.Equal(t, 1, stored.NumberOfProcesses())
	assert.Equal(t, "my-replica-set-0", stored.getProcesses()[0].Name())
}

func TestHeadlessConnectionAutomationConfigRoundTrip(t *testing.T) {
	conn, _ := newTestHeadlessConnection(t)

	err := conn.ReadUpdateAutomationConfig(func(ac *AutomationConfig) error {
		ac.Auth.AutoAuthMechanism = "SCRAM-SHA-256"
		ac.Auth.Enable()
		ac.Deployment.setProcesses([]Process{headlessTestProcess("my-replica-set-0")})
		return nil
	}, zap.S())
	require.NoError(t, err)

	ac, err := conn.ReadAutomationConfig()
	require.NoError(t, err)
	require.NotNil(t, ac.Auth)
	require.NotNil(t, ac.AgentSSL)
	assert.Equal(t, "SCRAM-SHA-256", ac.Auth.AutoAuthMechanism)
	assert.True(t, ac.Auth.IsEnabled())
	require.Equal(t, 1, ac.Deployment.NumberOfProcesses())

	mode, err := conn.GetAgentAuthMode()
	require.NoError(t, err)
	assert.Equal(t, "SCRAM-SHA-256", mode)
}

func TestHeadlessConnectionAutomationStatusFromPodAnnotations(t *testing.T) {
	newPod := func(name string, version string, ownedByStatefulSet string) *corev1.Pod {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: testHeadlessNamespace,
				OwnerReferences: []metav1.OwnerReference{
					{APIVersion: "apps/v1", Kind: "StatefulSet", Name: ownedByStatefulSet},
				},
			},
		}
		if version != "" {
			pod.Annotations = map[string]string{HeadlessAgentVersionAnnotation: version}
		}
		return pod
	}

	t.Run("processes without pods are not reported", func(t *testing.T) {
		conn, _ := newTestHeadlessConnection(t)
		err := conn.ReadUpdateDeployment(func(deployment Deployment) error {
			deployment.setProcesses([]Process{
				headlessTestProcess("my-replica-set-0"),
				headlessTestProcess("my-replica-set-1"),
			})
			return nil
		}, zap.S())
		require.NoError(t, err)

		status, err := conn.ReadAutomationStatus()
		require.NoError(t, err)
		assert.Equal(t, 1, status.GoalVersion)
		assert.Empty(t, status.Processes)
	})

	t.Run("reported versions are compared against the goal version", func(t *testing.T) {
		conn, kubeClient := newTestHeadlessConnection(t)
		err := conn.ReadUpdateDeployment(func(deployment Deployment) error {
			deployment.setProcesses([]Process{
				headlessTestProcess("my-replica-set-0"),
				headlessTestProcess("my-replica-set-1"),
				headlessTestProcess("my-replica-set-2"),
			})
			return nil
		}, zap.S())
		require.NoError(t, err)

		require.NoError(t, kubeClient.Create(t.Context(), newPod("my-replica-set-0", "1", "my-replica-set")))
		require.NoError(t, kubeClient.Create(t.Context(), newPod("my-replica-set-1", "0", "my-replica-set")))
		require.NoError(t, kubeClient.Create(t.Context(), newPod("my-replica-set-2", "", "my-replica-set")))
		// A pod owned by another resource must be ignored.
		require.NoError(t, kubeClient.Create(t.Context(), newPod("other-0", "1", "other")))

		status, err := conn.ReadAutomationStatus()
		require.NoError(t, err)
		assert.Equal(t, 1, status.GoalVersion)
		require.Len(t, status.Processes, 3)

		achieved := map[string]int{}
		for _, process := range status.Processes {
			achieved[process.Name] = process.LastGoalVersionAchieved
		}
		assert.Equal(t, 1, achieved["my-replica-set-0"])
		assert.Equal(t, 0, achieved["my-replica-set-1"])
		assert.Equal(t, 0, achieved["my-replica-set-2"])
	})
}

func TestHeadlessConnectionUnsupportedBackupOperations(t *testing.T) {
	conn, _ := newTestHeadlessConnection(t)

	_, err := conn.ReadBackupConfigs()
	require.Error(t, err)
	_, err = conn.ReadGroupBackupConfig()
	require.Error(t, err)
	_, err = conn.UpgradeAgentsToLatest()
	require.Error(t, err)
}

func TestHeadlessConnectionIdentityAndVersion(t *testing.T) {
	conn, _ := newTestHeadlessConnection(t)

	assert.Empty(t, conn.BaseURL())
	assert.Equal(t, "my-replica-set", conn.GroupID())
	assert.Equal(t, "my-replica-set", conn.GroupName())
	assert.False(t, conn.OpsManagerVersion().IsUnknown())

	version, err := conn.OpsManagerVersion().Semver()
	require.NoError(t, err)
	assert.Equal(t, headlessFakeOpsManagerVersion, version.String())
}

func TestHeadlessConnectionNormalizesDeploymentForAgentSchema(t *testing.T) {
	conn, kubeClient := newTestHeadlessConnection(t)

	deployment := NewDeployment()
	deployment.setProcesses([]Process{headlessTestProcess("my-replica-set-0")})
	// TLS disabled leaves a tls object without a CA path, which the agent schema rejects.
	deployment["tls"] = map[string]any{"clientCertificateMode": "OPTIONAL"}

	_, err := conn.UpdateDeployment(deployment)
	require.NoError(t, err)

	stored, err := BuildDeploymentFromBytes(readHeadlessACSecret(t, kubeClient)[automationconfig.ConfigKey])
	require.NoError(t, err)

	options, ok := stored["options"].(map[string]interface{})
	require.True(t, ok)
	assert.NotEmpty(t, options["downloadBase"])

	versions, ok := stored["mongoDbVersions"].([]interface{})
	require.True(t, ok)
	require.Len(t, versions, 1)
	assert.Equal(t, "8.0.6", versions[0].(map[string]interface{})["name"])

	_, hasTLS := stored["tls"]
	assert.False(t, hasTLS, "tls without CAFilePath must be dropped")

	// A tls object carrying a CA path is preserved.
	deployment = stored.ToCanonicalForm()
	deployment["tls"] = map[string]any{"clientCertificateMode": "OPTIONAL", "CAFilePath": "/mongodb-automation/tls/ca/ca-pem"}
	_, err = conn.UpdateDeployment(deployment)
	require.NoError(t, err)

	stored, err = BuildDeploymentFromBytes(readHeadlessACSecret(t, kubeClient)[automationconfig.ConfigKey])
	require.NoError(t, err)
	require.Contains(t, stored, "tls")
}
