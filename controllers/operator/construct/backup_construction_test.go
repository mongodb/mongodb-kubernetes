package construct

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/probes"

	omv1 "github.com/10gen/ops-manager-kubernetes/api/v1/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/mock"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/secrets"
	"github.com/10gen/ops-manager-kubernetes/pkg/multicluster"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/vault"
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
	assert.Equal(t, "test-om-backup-daemon", sts.ObjectMeta.Name)
	assert.Equal(t, util.BackupDaemonContainerName, sts.Spec.Template.Spec.Containers[0].Name)
	assert.NotNil(t, sts.Spec.Template.Spec.Containers[0].ReadinessProbe)
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
	sts, err := BackupDaemonStatefulSet(ctx, secretsClient, omv1.NewOpsManagerBuilderDefault().SetVersion("4.2.0").Build(), multicluster.GetLegacyCentralMemberCluster(1, 0, client, secretsClient), zap.S())
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

func Test_BackupDaemonStatefulSetWithRelatedImages(t *testing.T) {
	ctx := context.Background()
	initOpsManagerRelatedImageEnv := fmt.Sprintf("RELATED_IMAGE_%s_1_2_3", util.InitOpsManagerImageUrl)
	opsManagerRelatedImageEnv := fmt.Sprintf("RELATED_IMAGE_%s_5_0_0", util.OpsManagerImageUrl)

	t.Setenv(util.InitOpsManagerImageUrl, "quay.io/mongodb/mongodb-enterprise-init-appdb")
	t.Setenv(util.InitOpsManagerVersion, "1.2.3")
	t.Setenv(util.OpsManagerImageUrl, "quay.io/mongodb/mongodb-enterprise-ops-manager")
	t.Setenv(initOpsManagerRelatedImageEnv, "quay.io/mongodb/mongodb-enterprise-init-ops-manager:@sha256:MONGODB_INIT_APPDB")
	t.Setenv(opsManagerRelatedImageEnv, "quay.io/mongodb/mongodb-enterprise-ops-manager:@sha256:MONGODB_OPS_MANAGER")

	client, _ := mock.NewDefaultFakeClient()
	secretsClient := secrets.SecretClient{
		VaultClient: &vault.VaultClient{},
		KubeClient:  client,
	}

	sts, err := BackupDaemonStatefulSet(ctx, secretsClient, omv1.NewOpsManagerBuilderDefault().SetVersion("5.0.0").Build(), multicluster.GetLegacyCentralMemberCluster(1, 0, client, secretsClient), zap.S())
	assert.NoError(t, err)
	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-init-ops-manager:@sha256:MONGODB_INIT_APPDB", sts.Spec.Template.Spec.InitContainers[0].Image)
	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-ops-manager:@sha256:MONGODB_OPS_MANAGER", sts.Spec.Template.Spec.Containers[0].Image)
}
