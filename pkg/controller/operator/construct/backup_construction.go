package construct

import (
	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/container"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/lifecycle"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/podtemplatespec"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/statefulset"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

const (
	backupDaemonEnv = "BACKUP_DAEMON"
)

type BackupBuilder interface {
	OpsManagerBuilder
	GetHeadDbPersistenceConfig() *mdbv1.PersistenceConfig
}

// BackupStatefulSet fully constructs the Backup StatefulSet
func BackupStatefulSet(backupBuilder BackupBuilder) appsv1.StatefulSet {
	return statefulset.New(backupDaemonStatefulSetFunc(backupBuilder))
}

// buildBackupDaemonPodTemplateSpec constructs the Backup Daemon podTemplateSpec modification function
func backupDaemonStatefulSetFunc(backupBuilder BackupBuilder) statefulset.Modification {
	defaultConfig := mdbv1.PersistenceConfig{Storage: util.DefaultHeadDbStorageSize}
	pvc := pvcFunc(util.PvcNameHeadDb, backupBuilder.GetHeadDbPersistenceConfig(), defaultConfig)
	headDbMount := statefulset.CreateVolumeMount(util.PvcNameHeadDb, util.PvcMountPathHeadDb)
	return statefulset.Apply(
		backupAndOpsManagerSharedConfiguration(backupBuilder),
		statefulset.WithVolumeClaim(util.PvcNameHeadDb, pvc),
		statefulset.WithPodSpecTemplate(
			podtemplatespec.Apply(
				// 70 minutes for Backup Damon (internal timeout is 65 minutes, see CLOUDP-61849)
				podtemplatespec.WithTerminationGracePeriodSeconds(4200),
				podtemplatespec.WithContainerByIndex(0,
					container.Apply(
						container.WithName(util.BackupDaemonContainerName),
						container.WithEnvs(backupDaemonEnvVars()...),
						container.WithLifecycle(buildBackupDaemonLifecycle()),
						withVolumeMounts([]corev1.VolumeMount{headDbMount}),
					),
				)),
		),
	)
}

func backupDaemonEnvVars() []corev1.EnvVar {
	return []corev1.EnvVar{{
		// For the OM Docker image to run as Backup Daemon, the BACKUP_DAEMON env variable
		// needs to be passed with any value.configureJvmParams
		Name:  backupDaemonEnv,
		Value: "backup",
	}}
}

func buildBackupDaemonLifecycle() lifecycle.Modification {
	return lifecycle.WithPrestopCommand([]string{"/bin/sh", "-c", "/mongodb-ops-manager/bin/mongodb-mms stop_backup_daemon"})
}
