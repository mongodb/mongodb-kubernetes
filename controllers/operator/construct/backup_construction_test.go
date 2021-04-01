package construct

import (
	"testing"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/probes"

	omv1 "github.com/10gen/ops-manager-kubernetes/api/v1/om"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/stretchr/testify/assert"
)

func Test_buildBackupDaemonStatefulSet(t *testing.T) {
	sts, err := BackupDaemonStatefulSet(omv1.NewOpsManagerBuilderDefault().SetName("test-om").Build())
	assert.NoError(t, err)
	assert.Equal(t, "test-om-backup-daemon", sts.ObjectMeta.Name)
	assert.Equal(t, util.BackupDaemonContainerName, sts.Spec.Template.Spec.Containers[0].Name)
	assert.NotNil(t, sts.Spec.Template.Spec.Containers[0].ReadinessProbe)
}

func TestBackupPodTemplate_TerminationTimeout(t *testing.T) {
	set, err := BackupDaemonStatefulSet(omv1.NewOpsManagerBuilderDefault().SetName("test-om").Build())
	assert.NoError(t, err)
	podSpecTemplate := set.Spec.Template
	assert.Equal(t, int64(4200), *podSpecTemplate.Spec.TerminationGracePeriodSeconds)
}

func TestBuildBackupDaemonContainer(t *testing.T) {
	sts, err := BackupDaemonStatefulSet(omv1.NewOpsManagerBuilderDefault().SetVersion("4.2.0").Build())
	assert.NoError(t, err)
	template := sts.Spec.Template
	container := template.Spec.Containers[0]
	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-ops-manager:4.2.0", container.Image)

	assert.Equal(t, util.BackupDaemonContainerName, container.Name)

	expectedProbe := probes.New(buildBackupDaemonReadinessProbe())
	assert.Equal(t, &expectedProbe, container.ReadinessProbe)

	assert.Equal(t, []string{"/bin/sh", "-c", "/mongodb-ops-manager/bin/mongodb-mms stop_backup_daemon"},
		container.Lifecycle.PreStop.Exec.Command)
}
