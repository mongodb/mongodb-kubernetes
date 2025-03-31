package construct

import (
	"context"
	"fmt"

	"go.uber.org/zap"
	"golang.org/x/xerrors"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	omv1 "github.com/10gen/ops-manager-kubernetes/api/v1/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/secrets"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/api/v1/common"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/kube/container"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/kube/lifecycle"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/kube/podtemplatespec"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/kube/probes"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/kube/secret"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/kube/statefulset"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/util/merge"
	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	"github.com/10gen/ops-manager-kubernetes/pkg/multicluster"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/vault"
)

const (
	BackupDaemonServicePort           = 8443
	backupDaemonEnv                   = "BACKUP_DAEMON"
	healthEndpointPortEnv             = "HEALTH_ENDPOINT_PORT"
	backupDaemonReadinessProbeCommand = "/opt/scripts/backup-daemon-readiness-probe"
	backupDaemonLivenessProbeCommand  = "/opt/scripts/backup-daemon-liveness-probe.sh"
)

// BackupDaemonStatefulSet fully constructs the Backup StatefulSet.
func BackupDaemonStatefulSet(ctx context.Context, centralClusterSecretClient secrets.SecretClient, opsManager *omv1.MongoDBOpsManager, memberCluster multicluster.MemberCluster, log *zap.SugaredLogger, additionalOpts ...func(*OpsManagerStatefulSetOptions)) (appsv1.StatefulSet, error) {
	opts := backupOptions(memberCluster, additionalOpts...)(opsManager)
	if err := opts.updateHTTPSCertSecret(ctx, centralClusterSecretClient, memberCluster, opsManager.OwnerReferences, log); err != nil {
		return appsv1.StatefulSet{}, err
	}

	secretName := opsManager.Spec.Backup.QueryableBackupSecretRef.Name
	opts.QueryableBackupPemSecretName = secretName
	if secretName != "" {
		// if the secret is specified, we must have a queryable.pem entry.
		_, err := secret.ReadKey(ctx, memberCluster.SecretClient, "queryable.pem", kube.ObjectKey(opsManager.Namespace, secretName))
		if err != nil {
			return appsv1.StatefulSet{}, xerrors.Errorf("error reading queryable.pem key from secret %s/%s: %w", opsManager.Namespace, secretName, err)
		}
	}

	backupSts := statefulset.New(backupDaemonStatefulSetFunc(opts))
	var err error
	if opts.StatefulSetSpecOverride != nil {
		backupSts.Spec = merge.StatefulSetSpecs(backupSts.Spec, *opts.StatefulSetSpecOverride)
	}

	// the JVM env args must be determined after any potential stateful set override
	// has taken place.
	if err = setJvmArgsEnvVars(opsManager.Spec, util.BackupDaemonContainerName, &backupSts); err != nil {
		return appsv1.StatefulSet{}, err
	}
	return backupSts, nil
}

// backupOptions returns a function which returns the OpsManagerStatefulSetOptions to create the BackupDaemon StatefulSet.
func backupOptions(memberCluster multicluster.MemberCluster, additionalOpts ...func(opts *OpsManagerStatefulSetOptions)) func(opsManager *omv1.MongoDBOpsManager) OpsManagerStatefulSetOptions {
	return func(opsManager *omv1.MongoDBOpsManager) OpsManagerStatefulSetOptions {
		opts := getSharedOpsManagerOptions(opsManager)

		opts.Annotations = opsManager.Annotations
		opts.ServicePort = BackupDaemonServicePort
		if memberCluster.Legacy {
			opts.ServiceName = opsManager.BackupDaemonServiceName()
			opts.Name = opsManager.BackupDaemonStatefulSetName()
		} else {
			opts.ServiceName = opsManager.BackupDaemonHeadlessServiceNameForClusterIndex(memberCluster.Index)
			opts.Name = opsManager.BackupDaemonStatefulSetNameForClusterIndex(memberCluster.Index)
		}
		opts.Replicas = opsManager.Spec.Backup.Members
		opts.AppDBConnectionSecretName = opsManager.AppDBMongoConnectionStringSecretName()

		opts.LoggingConfiguration = opsManager.Spec.Backup.Logging

		if opsManager.Spec.Backup != nil {
			if opsManager.Spec.Backup.StatefulSetConfiguration != nil {
				opts.StatefulSetSpecOverride = &opsManager.Spec.Backup.StatefulSetConfiguration.SpecWrapper.Spec
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

// backupDaemonStatefulSetFunc constructs the Backup Daemon podTemplateSpec modification function.
func backupDaemonStatefulSetFunc(opts OpsManagerStatefulSetOptions) statefulset.Modification {
	// PodSecurityContext is added in the backupAndOpsManagerSharedConfiguration
	_, configureContainerSecurityContext := podtemplatespec.WithDefaultSecurityContextsModifications()

	defaultConfig := common.PersistenceConfig{Storage: util.DefaultHeadDbStorageSize}
	pvc := pvcFunc(util.PvcNameHeadDb, opts.HeadDbPersistenceConfig, defaultConfig, opts.Labels)
	headDbMount := statefulset.CreateVolumeMount(util.PvcNameHeadDb, util.PvcMountPathHeadDb)

	caVolumeFunc := podtemplatespec.NOOP()
	caVolumeMountFunc := container.NOOP()

	volumeMounts := []corev1.VolumeMount{headDbMount}
	mmsMongoUriVolume := corev1.Volume{}
	var mmsMongoUriMount corev1.VolumeMount

	if !vault.IsVaultSecretBackend() {
		// configure the AppDB Connection String volume from a secret
		mmsMongoUriVolume, mmsMongoUriMount = buildMmsMongoUriVolume(opts)
		volumeMounts = append(volumeMounts, mmsMongoUriMount)
	}

	return statefulset.Apply(
		backupAndOpsManagerSharedConfiguration(opts),
		statefulset.WithVolumeClaim(util.PvcNameHeadDb, pvc),
		statefulset.WithPodSpecTemplate(
			podtemplatespec.Apply(
				// 70 minutes for Backup Damon (internal timeout is 65 minutes, see CLOUDP-61849)
				podtemplatespec.WithTerminationGracePeriodSeconds(4200),
				addUriVolume(mmsMongoUriVolume),
				caVolumeFunc,
				podtemplatespec.WithContainerByIndex(0,
					container.Apply(
						container.WithName(util.BackupDaemonContainerName),
						container.WithEnvs(backupDaemonEnvVars()...),
						container.WithLifecycle(buildBackupDaemonLifecycle()),
						container.WithVolumeMounts(volumeMounts),
						container.WithLivenessProbe(buildBackupDaemonLivenessProbe()),
						container.WithReadinessProbe(buildBackupDaemonReadinessProbe()),
						container.WithStartupProbe(buildBackupDaemonStartupProbe()),
						caVolumeMountFunc,
						configureContainerSecurityContext,
					),
				)),
		),
	)
}

func addUriVolume(volume corev1.Volume) podtemplatespec.Modification {
	if !vault.IsVaultSecretBackend() {
		return podtemplatespec.WithVolume(volume)
	}
	return podtemplatespec.NOOP()
}

func backupDaemonEnvVars() []corev1.EnvVar {
	return []corev1.EnvVar{
		{
			// For the OM Docker image to run as Backup Daemon, the BACKUP_DAEMON env variable
			// needs to be passed with any value.configureJvmParams
			Name:  backupDaemonEnv,
			Value: "backup",
		},
		{
			// Specify the port of the backup daemon health endpoint for the liveness probe.
			Name:  healthEndpointPortEnv,
			Value: fmt.Sprintf("%d", backupDaemonHealthPort),
		},
	}
}

func buildBackupDaemonLifecycle() lifecycle.Modification {
	return lifecycle.WithPrestopCommand([]string{"/bin/sh", "-c", "/mongodb-ops-manager/bin/mongodb-mms stop_backup_daemon"})
}

// buildBackupDaemonReadinessProbe returns a probe modification which will add
// the readiness probe.
func buildBackupDaemonReadinessProbe() probes.Modification {
	return probes.Apply(
		probes.WithExecCommand([]string{backupDaemonReadinessProbeCommand}),
		probes.WithFailureThreshold(3),
		probes.WithInitialDelaySeconds(1),
		probes.WithSuccessThreshold(1),
		probes.WithPeriodSeconds(3),
		probes.WithTimeoutSeconds(5),
	)
}

// buildBackupDaemonLivenessProbe returns a probe modification which will add
// the liveness probe.
func buildBackupDaemonLivenessProbe() probes.Modification {
	return probes.Apply(
		probes.WithExecCommand([]string{backupDaemonLivenessProbeCommand}),
		probes.WithFailureThreshold(10),
		probes.WithInitialDelaySeconds(10),
		probes.WithSuccessThreshold(1),
		probes.WithPeriodSeconds(30),
		probes.WithTimeoutSeconds(5),
	)
}

// buildBackupDaemonStartupProbe returns a probe modification which will add
// the startup probe.
func buildBackupDaemonStartupProbe() probes.Modification {
	return probes.Apply(
		probes.WithExecCommand([]string{backupDaemonLivenessProbeCommand}),
		probes.WithFailureThreshold(20),
		probes.WithInitialDelaySeconds(1),
		probes.WithSuccessThreshold(1),
		probes.WithPeriodSeconds(30),
		probes.WithTimeoutSeconds(5),
	)
}
