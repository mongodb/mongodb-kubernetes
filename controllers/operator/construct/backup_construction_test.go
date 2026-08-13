package construct

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/probes"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/vault"
)

func TestBuildBackupDaemonStatefulSet(t *testing.T) {
	ctx := context.Background()
	client, _ := mock.NewDefaultFakeClient()
	secretsClient := secrets.SecretClient{
		VaultClient: &vault.VaultClient{},
		KubeClient:  client,
	}
	sts, err := BackupDaemonStatefulSet(ctx, secretsClient, omv1.NewOpsManagerBuilderDefault().SetName("test-om").Build(), multicluster.GetLegacyCentralMemberCluster(1, 0, client, secretsClient), zap.S())
	assert.NoError(t, err)
	assert.Equal(t, "test-om-backup-daemon", sts.Name)
	assert.Equal(t, util.BackupDaemonContainerName, sts.Spec.Template.Spec.Containers[0].Name)
	assert.NotNil(t, sts.Spec.Template.Spec.Containers[0].ReadinessProbe)
}

// TestBackupDaemonProbesRemainExec guards the backup daemon against the shared Ops Manager
// options now carrying a probe Scheme: the daemon's probes are exec-based and must stay that way.
func TestBackupDaemonProbesRemainExec(t *testing.T) {
	ctx := context.Background()
	client, _ := mock.NewDefaultFakeClient(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "om-tls-cert", Namespace: mock.TestNamespace},
		Type:       corev1.SecretTypeOpaque,
	})
	secretsClient := secrets.SecretClient{
		VaultClient: &vault.VaultClient{},
		KubeClient:  client,
	}

	om := omv1.NewOpsManagerBuilderDefault().
		SetName("test-om").
		SetNamespace(mock.TestNamespace).
		SetTLSConfig(omv1.MongoDBOpsManagerTLS{
			SecretRef: omv1.TLSSecretRef{Name: "om-tls-cert"},
		}).
		Build()

	sts, err := BackupDaemonStatefulSet(ctx, secretsClient, om, multicluster.GetLegacyCentralMemberCluster(1, 0, client, secretsClient), zap.S())
	require.NoError(t, err)
	require.Len(t, sts.Spec.Template.Spec.Containers, 1)
	containerObj := sts.Spec.Template.Spec.Containers[0]

	for probeName, probe := range map[string]*corev1.Probe{
		"readiness": containerObj.ReadinessProbe,
		"liveness":  containerObj.LivenessProbe,
		"startup":   containerObj.StartupProbe,
	} {
		require.NotNil(t, probe, "%s probe must be set", probeName)
		assert.Nil(t, probe.HTTPGet, "%s probe must not use an HTTPGet handler", probeName)
		require.NotNil(t, probe.Exec, "%s probe must use an Exec handler", probeName)
	}
}

func TestBackupPodTemplate_TerminationTimeout(t *testing.T) {
	ctx := context.Background()
	client, _ := mock.NewDefaultFakeClient()
	secretsClient := secrets.SecretClient{
		VaultClient: &vault.VaultClient{},
		KubeClient:  client,
	}
	set, err := BackupDaemonStatefulSet(ctx, secretsClient, omv1.NewOpsManagerBuilderDefault().SetName("test-om").Build(), multicluster.GetLegacyCentralMemberCluster(1, 0, client, secretsClient), zap.S())
	assert.NoError(t, err)
	podSpecTemplate := set.Spec.Template
	assert.Equal(t, int64(4200), *podSpecTemplate.Spec.TerminationGracePeriodSeconds)
}

func TestBuildBackupDaemonContainer(t *testing.T) {
	ctx := context.Background()
	client, _ := mock.NewDefaultFakeClient()
	secretsClient := secrets.SecretClient{
		VaultClient: &vault.VaultClient{},
		KubeClient:  client,
	}
	sts, err := BackupDaemonStatefulSet(ctx, secretsClient, omv1.NewOpsManagerBuilderDefault().SetVersion("4.2.0").Build(), multicluster.GetLegacyCentralMemberCluster(1, 0, client, secretsClient), zap.S(),
		WithOpsManagerImage("quay.io/mongodb/mongodb-enterprise-ops-manager:4.2.0"),
	)
	assert.NoError(t, err)
	template := sts.Spec.Template
	container := template.Spec.Containers[0]
	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-ops-manager:4.2.0", container.Image)

	assert.Equal(t, util.BackupDaemonContainerName, container.Name)

	expectedProbe := probes.New(buildBackupDaemonReadinessProbe())
	assert.Equal(t, &expectedProbe, container.ReadinessProbe)

	expectedProbe = probes.New(buildBackupDaemonLivenessProbe())
	assert.Equal(t, &expectedProbe, container.LivenessProbe)

	expectedProbe = probes.New(buildBackupDaemonStartupProbe())
	assert.Equal(t, &expectedProbe, container.StartupProbe)

	assert.Equal(t, []string{"/bin/sh", "-c", "/mongodb-ops-manager/bin/mongodb-mms stop_backup_daemon"},
		container.Lifecycle.PreStop.Exec.Command)
}

func TestMultipleBackupDaemons(t *testing.T) {
	ctx := context.Background()
	client, _ := mock.NewDefaultFakeClient()
	secretsClient := secrets.SecretClient{
		VaultClient: &vault.VaultClient{},
		KubeClient:  client,
	}
	sts, err := BackupDaemonStatefulSet(ctx, secretsClient, omv1.NewOpsManagerBuilderDefault().SetVersion("4.2.0").SetBackupMembers(3).Build(), multicluster.GetLegacyCentralMemberCluster(1, 0, client, secretsClient), zap.S())
	assert.NoError(t, err)
	assert.Equal(t, 3, int(*sts.Spec.Replicas))
}
