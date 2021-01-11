package construct

import (
	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/mdb"
	omv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/om"
	enterprisests "github.com/10gen/ops-manager-kubernetes/pkg/kube/statefulset"
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

// BackupDaemonStatefulSet fully constructs the Backup StatefulSet.
func BackupDaemonStatefulSet(opsManager omv1.MongoDBOpsManager, additionalOpts ...func(*OpsManagerStatefulSetOptions)) (appsv1.StatefulSet, error) {
	opts := backupOptions(additionalOpts...)(opsManager)
	backupSts := statefulset.New(backupDaemonStatefulSetFunc(opts))
	var err error
	if opts.StatefulSetSpecOverride != nil {
		backupSts, err = enterprisests.MergeSpec(backupSts, opts.StatefulSetSpecOverride)
		if err != nil {
			return appsv1.StatefulSet{}, nil
		}
	}

	// the JVM env args must be determined after any potential stateful set override
	// has taken place.
	if err = setJvmArgsEnvVars(opsManager.Spec, &backupSts); err != nil {
		return appsv1.StatefulSet{}, err
	}
	return backupSts, nil
}

// backupOptions returns a function which returns the OpsManagerStatefulSetOptions to create the BackupDaemon StatefulSet.
func backupOptions(additionalOpts ...func(opts *OpsManagerStatefulSetOptions)) func(opsManager omv1.MongoDBOpsManager) OpsManagerStatefulSetOptions {
	return func(opsManager omv1.MongoDBOpsManager) OpsManagerStatefulSetOptions {
		opts := getSharedOpsManagerOptions(opsManager)

		opts.ServicePort = 8443
		opts.ServiceName = opsManager.BackupServiceName()
		opts.Name = opsManager.BackupStatefulSetName()
		opts.Replicas = 1

		if opsManager.Spec.Backup != nil {
			if opsManager.Spec.Backup.StatefulSetConfiguration != nil {
				opts.StatefulSetSpecOverride = &opsManager.Spec.Backup.StatefulSetConfiguration.Spec
			}
			if opsManager.Spec.Backup.HeadDB != nil {
				opts.HeadDbPersistenceConfig = opsManager.Spec.Backup.HeadDB
			}
		}

		for _, additionalOpt := range additionalOpts {
			additionalOpt(&opts)
		}

		return opts
	}
}

// buildBackupDaemonPodTemplateSpec constructs the Backup Daemon podTemplateSpec modification function.
func backupDaemonStatefulSetFunc(opts OpsManagerStatefulSetOptions) statefulset.Modification {
	defaultConfig := mdbv1.PersistenceConfig{Storage: util.DefaultHeadDbStorageSize}
	pvc := pvcFunc(util.PvcNameHeadDb, opts.HeadDbPersistenceConfig, defaultConfig)
	headDbMount := statefulset.CreateVolumeMount(util.PvcNameHeadDb, util.PvcMountPathHeadDb)
	return statefulset.Apply(
		backupAndOpsManagerSharedConfiguration(opts),
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
