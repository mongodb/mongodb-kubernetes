package backup

import (
	"context"
	"reflect"

	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/types"

	apiErrors "k8s.io/apimachinery/pkg/api/errors"

	v1 "github.com/10gen/ops-manager-kubernetes/api/v1"
	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/api/v1/status"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/secrets"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/workflow"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
)

type ConfigReaderUpdater interface {
	GetBackupSpec() *mdbv1.Backup
	GetResourceType() mdbv1.ResourceType
	GetResourceName() string
	v1.CustomResourceReadWriter
}

// EnsureBackupConfigurationInOpsManager updates the backup configuration based on the MongoDB resource
// specification.
func EnsureBackupConfigurationInOpsManager(ctx context.Context, mdb ConfigReaderUpdater, secretsReader secrets.SecretClient, projectId string, configReadUpdater ConfigHostReadUpdater, groupConfigReader GroupConfigReader, groupConfigUpdater GroupConfigUpdater, log *zap.SugaredLogger) (workflow.Status, []status.Option) {
	if mdb.GetBackupSpec() == nil {
		return workflow.OK(), nil
	}

	desiredConfig := getMongoDBBackupConfig(mdb.GetBackupSpec(), projectId)

	configs, err := configReadUpdater.ReadBackupConfigs()
	if err != nil {
		return workflow.Failed(err), nil
	}

	projectConfigs := configs.Configs

	if len(projectConfigs) == 0 {
		return workflow.Pending("Waiting for backup configuration to be created in Ops Manager").WithRetry(10), nil
	}

	err = ensureGroupConfig(ctx, mdb, secretsReader, groupConfigReader, groupConfigUpdater)
	if err != nil {
		return workflow.Failed(err), nil
	}

	return ensureBackupConfigStatuses(mdb, projectConfigs, desiredConfig, log, configReadUpdater)
}

func ensureGroupConfig(ctx context.Context, mdb ConfigReaderUpdater, secretsReader secrets.SecretClient, reader GroupConfigReader, updater GroupConfigUpdater) error {
	if mdb.GetBackupSpec() == nil || (mdb.GetBackupSpec().AssignmentLabels == nil && mdb.GetBackupSpec().Encryption == nil) {
		return nil
	}

	assignmentLabels := mdb.GetBackupSpec().AssignmentLabels
	kmip := mdb.GetBackupSpec().GetKmip()

	config, err := reader.ReadGroupBackupConfig()
	if err != nil {
		return err
	}

	requiresUpdate := false

	if kmip != nil {
		desiredPath := util.KMIPClientSecretsHome + "/" + kmip.Client.ClientCertificateSecretName(mdb.GetName()) + kmip.Client.ClientCertificateSecretKeyName()
		if config.KmipClientCertPath == nil || desiredPath != *config.KmipClientCertPath {
			config.KmipClientCertPath = &desiredPath
			requiresUpdate = true
		}

		// The password is optional, so we propagate the error only if something abnormal happens
		kmipPasswordSecret, err := secretsReader.GetSecret(ctx, types.NamespacedName{
			Name:      kmip.Client.ClientCertificatePasswordSecretName(mdb.GetName()),
			Namespace: mdb.GetNamespace(),
		})
		if err == nil {
			desiredPassword := string(kmipPasswordSecret.Data[kmip.Client.ClientCertificatePasswordKeyName()])
			if config.KmipClientCertPassword == nil || desiredPassword != *config.KmipClientCertPassword {
				config.KmipClientCertPassword = &desiredPassword
				requiresUpdate = true
			}
		} else if !apiErrors.IsNotFound(err) {
			return err
		}
	}

	if assignmentLabels != nil {
		if config.LabelFilter == nil || !reflect.DeepEqual(config.LabelFilter, assignmentLabels) {
			config.LabelFilter = mdb.GetBackupSpec().AssignmentLabels
			requiresUpdate = true
		}
	}

	if requiresUpdate {
		_, err = updater.UpdateGroupBackupConfig(config)
	}
	return err
}

// ensureBackupConfigStatuses makes sure that every config in the project has reached the desired state.
func ensureBackupConfigStatuses(mdb ConfigReaderUpdater, projectConfigs []*Config, desiredConfig *Config, log *zap.SugaredLogger, configReadUpdater ConfigHostReadUpdater) (workflow.Status, []status.Option) {
	result := workflow.OK()

	for _, config := range projectConfigs {
		desiredConfig.ClusterId = config.ClusterId

		cluster, err := configReadUpdater.ReadHostCluster(config.ClusterId)
		if err != nil {
			return workflow.Failed(err), nil
		}

		// There is one HostConfig per component of the deployment being backed up.
		// E.g. a sharded cluster with 2 shards is composed of 4 backup configurations.
		// 1x CONFIG_SERVER_REPLICA_SET (config server)
		// 2x REPLICA_SET (each shard)
		// 1x SHARDED_REPLICA_SET (the source of truth for sharded cluster configuration)
		// Only the SHARDED_REPLICA_SET can be configured, we need to ensure that based on the cluster wide
		// we care about we are only updating the config if the type and name are correct.
		resourceType := MongoDbResourceType(mdb.GetResourceType())
		nameIsEqual := cluster.ClusterName == mdb.GetResourceName()
		isReplicaSet := resourceType == ReplicaSetType && cluster.TypeName == "REPLICA_SET"
		isShardedCluster := resourceType == ShardedClusterType && cluster.TypeName == "SHARDED_REPLICA_SET"
		shouldUpdateBackupConfiguration := nameIsEqual && (isReplicaSet || isShardedCluster)
		// If we are configuring a sharded cluster, we must only update the config of the whole cluster, not each individual shard.
		// Status: 409 (Conflict), ErrorCode: CANNOT_MODIFY_SHARD_BACKUP_CONFIG, Detail: Cannot modify backup configuration for individual shard; use cluster ID 611a63f668d22f4e2e62c2e3 for entire cluster.
		if !shouldUpdateBackupConfiguration {
			continue
		}

		// config.Status        = current backup status in OM
		// desiredConfig.Status = spec.backup.mode from CR, mapped as:
		//  status in CR | status in OM
		//  ----------------------------
		//  enabled        Started,
		//  disabled       Stopped,
		//  terminated     Terminating,
		// desiredStatus here is desiredConfig.Status modified to potentially handle intermediate steps according to what the user specified in spec.backup.mode
		desiredStatus := getDesiredStatus(desiredConfig, config)

		intermediateStepRequired := desiredStatus != desiredConfig.Status
		if intermediateStepRequired {
			result.Requeue()
		}

		desiredConfig.Status = desiredStatus

		// If backup was never enabled and the deployment has `spec.backup.mode=disabled` specified
		// we don't send this state to OM, or we will get
		// CANNOT_STOP_BACKUP_INVALID_STATE, Detail: Cannot stop backup unless the cluster is in the STARTED state.
		if desiredConfig.Status == Stopped && config.Status == Inactive {
			continue
		}

		// When mdb is newly created or backup is being enabled from terminated state, it is not possible to send snapshot schedule to OM,
		// so the first run will be skipped. Update will be executed again at the end of this method when the backup reaches valid status.
		// When the backup is already configured (not in INACTIVE state) and it is not changing its status, then this execution will update snapshot schedule.
		// When both status and snapshot schedule is changing (e.g. backup starting from stopped), then this method will update snapshot schedule twice, which is harmless.
		if err := updateSnapshotSchedule(mdb.GetBackupSpec().SnapshotSchedule, configReadUpdater, config, log); err != nil {
			return workflow.Failed(err), nil
		}

		if desiredConfig.Status == config.Status {
			log.Debug("Config is already in the desired state, not updating configuration")

			// we are already in the desired state, nothing to change
			// if we attempt to send the desired state again we get
			// CANNOT_START_BACKUP_INVALID_STATE: Cannot start backup unless the cluster is in the INACTIVE or STOPPED state.
			continue
		}

		updatedConfig, err := configReadUpdater.UpdateBackupConfig(desiredConfig)
		if err != nil {
			return workflow.Failed(err), nil
		}

		log.Debugw("Project Backup Configuration", "desiredConfig", desiredConfig, "updatedConfig", updatedConfig)

		if ok, msg := waitUntilBackupReachesStatus(configReadUpdater, updatedConfig, desiredConfig.Status, log); !ok {
			log.Debugf("wait error message: %s", msg)
			statusOpts, err := getCurrentBackupStatusOption(configReadUpdater, config.ClusterId)
			if err != nil {
				return workflow.Failed(err), nil
			}
			return workflow.Pending("Backup configuration %s has not yet reached the desired status", updatedConfig.ClusterId).WithRetry(1), statusOpts
		}

		log.Debugf("Backup has reached the desired state of %s", desiredConfig.Status)

		// second run for cases when backup was inactive (see comment above)
		if err := updateSnapshotSchedule(mdb.GetBackupSpec().SnapshotSchedule, configReadUpdater, desiredConfig, log); err != nil {
			return workflow.Failed(err), nil
		}

		backupOpts, err := getCurrentBackupStatusOption(configReadUpdater, desiredConfig.ClusterId)
		if err != nil {
			return workflow.Failed(err), nil
		}
		return result, backupOpts
	}

	return result, nil
}

func updateSnapshotSchedule(specSnapshotSchedule *mdbv1.SnapshotSchedule, configReadUpdater ConfigHostReadUpdater, config *Config, log *zap.SugaredLogger) error {
	if specSnapshotSchedule == nil {
		return nil
	}

	// in Inactive state we cannot update snapshot schedule in OM
	if config.Status == Inactive {
		log.Debugf("Skipping updating backup snapshot schedule due to Inactive status")
		return nil
	}

	omSnapshotSchedule, err := configReadUpdater.ReadSnapshotSchedule(config.ClusterId)
	if err != nil {
		return xerrors.Errorf("failed to read snapshot schedule: %w", err)
	}

	snapshotSchedule := mergeExistingScheduleWithSpec(*omSnapshotSchedule, *specSnapshotSchedule)

	// we need to use DeepEqual in order to compare pointers' underlying values
	if !reflect.DeepEqual(snapshotSchedule, *omSnapshotSchedule) {
		if err := configReadUpdater.UpdateSnapshotSchedule(snapshotSchedule.ClusterID, &snapshotSchedule); err != nil {
			return xerrors.Errorf("failed to update backup snapshot schedule for cluster %s: %w", config.ClusterId, err)
		}
		log.Debugf("Updated backup snapshot schedule with: %s", snapshotSchedule)
	} else {
		log.Infof("Backup snapshot schedule is up to date")
	}

	return nil
}

// getCurrentBackupStatusOption fetches the latest information from the backup config
// with the given cluster id and returns the relevant status Options.
func getCurrentBackupStatusOption(configReader ConfigReader, clusterId string) ([]status.Option, error) {
	config, err := configReader.ReadBackupConfig(clusterId)
	if err != nil {
		return nil, err
	}
	return []status.Option{
		status.NewBackupStatusOption(
			string(config.Status),
		),
	}, nil
}

// getMongoDBBackupConfig builds the backup configuration from the given MongoDB resource
func getMongoDBBackupConfig(backupSpec *mdbv1.Backup, projectId string) *Config {
	mappings := getStatusMappings()
	return &Config{
		// the encryptionEnabled field is also only used in old backup, 4.2 backup will copy all files whether they are encrypted
		// the encryption happens at the mongod level and should be managed by the customer
		EncryptionEnabled: false,

		// 4.2 backup does not yet support filtering namespaces, both excluded and included namespaces will be ignored
		ExcludedNamespaces: []string{},
		// validation requires exactly one of these being set
		// INVALID_FILTERLIST, Detail: Backup configuration cannot specify both included namespaces and excluded namespaces.
		IncludedNamespaces: nil,

		// we map our more declarative API to the values required by backup
		Status: mappings[string(backupSpec.Mode)],

		// with 4.2 backup we only need to support wired tiger
		StorageEngineName: wiredTigerStorageEngine,
		// syncSource is only required on pre-4.2 backup, the value is still validated however, so we can just send primary
		SyncSource: "PRIMARY",
		ProjectId:  projectId,
	}
}

// getStatusMappings returns a map which maps the fields exposed on the CRD
// to the fields expected by the Backup API
func getStatusMappings() map[string]Status {
	return map[string]Status{
		"enabled":    Started,
		"disabled":   Stopped,
		"terminated": Terminating,
	}
}

// getDesiredStatus takes the desired config and the current config and returns the Status
// that the operator should try to configure for this reconciliation
func getDesiredStatus(desiredConfig, currentConfig *Config) Status {
	if currentConfig == nil {
		return desiredConfig.Status
	}
	// valid transitions can be found here https://github.com/10gen/mms/blob/7487cf31e775a38703ca6ef247b31b4d10c78c41/server/src/main/com/xgen/svc/mms/api/res/ApiBackupConfigsResource.java#L186
	// transitioning from Started to Terminating is not a valid transition
	// we need to first go to Stopped.
	if desiredConfig.Status == Terminating && currentConfig.Status == Started {
		return Stopped
	}

	// transitioning from Stopped to Terminating is not possible, it is only possible through
	// Stopped -> Started -> Terminating
	if desiredConfig.Status == Stopped && currentConfig.Status == Terminating {
		return Started
	}

	return desiredConfig.Status
}
