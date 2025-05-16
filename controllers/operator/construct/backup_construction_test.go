package construct

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"

	omv1 "github.com/mongodb/mongodb-kubernetes/api/v1/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/probes"
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
