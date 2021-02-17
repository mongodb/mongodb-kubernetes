package backup

import (
	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/api/v1/status"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/workflow"
	"go.uber.org/zap"
)

// EnsureBackupConfigurationInOpsManager updates the backup configuration based on the MongoDB resource
// specification.
func EnsureBackupConfigurationInOpsManager(backupSpec *mdbv1.Backup, projectId string, configReadUpdater ConfigReadUpdater, log *zap.SugaredLogger) (workflow.Status, []status.Option) {
	if backupSpec == nil {
		return workflow.OK(), nil
	}

	desiredConfig := getMongoBDBackupConfig(backupSpec, projectId)

	configs, err := configReadUpdater.ReadBackupConfigs()
	if err != nil {
		return workflow.Failed(err.Error()), nil
	}

	projectConfigs := configs.Configs
	if len(projectConfigs) > 1 {
		return workflow.Failed("There should be a maximum of one backup config per project!"), nil
	}

	if len(projectConfigs) == 0 {
		return workflow.Pending("Waiting for backup configuration to be created in Ops Manager").WithRetry(10), nil
	}

	currentConfig := projectConfigs[0]
	desiredConfig.ClusterId = currentConfig.ClusterId

	okResult := workflow.OK()

	desiredStatus := getDesiredStatus(desiredConfig, currentConfig)

	needToRequeue := desiredStatus != desiredConfig.Status
	if needToRequeue {
		okResult.Requeue()
	}

	// If backup was never enabled and the deployment has `spec.backup.mode=disabled` specified
	// we don't send this state to OM or we will get
	// CANNOT_STOP_BACKUP_INVALID_STATE, Detail: Cannot stop backup unless the cluster is in the STARTED state.'
	if desiredConfig.Status == Stopped && currentConfig.Status == Inactive {
		return workflow.OK(), nil
	}

	if desiredConfig.Status == currentConfig.Status {
		log.Debug("Config is already in the desired state, not updating configuration")
		// we are already in the desired state, nothing to change
		// if we attempt to send the desired state again we get
		// CANNOT_START_BACKUP_INVALID_STATE: Cannot start backup unless the cluster is in the INACTIVE or STOPPED state.
		statusOpts, err := getCurrentBackupStatusOption(configReadUpdater, currentConfig.ClusterId)
		if err != nil {
			return workflow.Failed(err.Error()), nil
		}
		return okResult, statusOpts
	}

	updatedConfig, err := configReadUpdater.UpdateBackupConfig(desiredConfig)
	if err != nil {
		return workflow.Failed(err.Error()), nil
	}

	log.Debugw("Project Backup Configuration", "desiredConfig", desiredConfig, "updatedConfig", updatedConfig)

	if !waitUntilBackupReachesStatus(configReadUpdater, updatedConfig, desiredConfig.Status, log) {
		statusOpts, err := getCurrentBackupStatusOption(configReadUpdater, currentConfig.ClusterId)
		if err != nil {
			return workflow.Failed(err.Error()), nil
		}
		return workflow.Pending("Backup configuration has not yet reached the desired status").WithRetry(1), statusOpts
	}

	statusOpts, err := getCurrentBackupStatusOption(configReadUpdater, currentConfig.ClusterId)
	if err != nil {
		return workflow.Failed(err.Error()), nil
	}
	return okResult, statusOpts
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
		)}, nil
}

// getMongoBDBackupConfig builds the backup configuration from the given MongoDB resource
func getMongoBDBackupConfig(backupSpec *mdbv1.Backup, projectId string) *Config {
	mappings := getStatusMappings()
	return &Config{
		// the encryptionEnabled field is also only used in old backup, 4.2 backup will copy all files whether or not they are encrypted
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
		// syncSource is only required on pre-4.2 backup, the value is still validated however so we can just send primary
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
