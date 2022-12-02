package om

import (
	"errors"
	"fmt"
	"net"

	"github.com/blang/semver"

	v1 "github.com/10gen/ops-manager-kubernetes/api/v1"
	"github.com/10gen/ops-manager-kubernetes/api/v1/status"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/versionutil"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

// IMPORTANT: this package is intended to contain only "simple" validation—in
// other words, validation that is based only on the properties in the Ops Manager
// resource. More complex validation, such as validation that needs to observe
// the state of the cluster, belongs somewhere else.

var _ webhook.Validator = &MongoDBOpsManager{}

// ValidateCreate and ValidateUpdate should be the same if we intend to do this
// on every reconciliation as well
func (m *MongoDBOpsManager) ValidateCreate() error {
	return m.ProcessValidationsWebhook()
}

func (m *MongoDBOpsManager) ValidateUpdate(old runtime.Object) error {
	return m.ProcessValidationsWebhook()
}

// ValidateDelete does nothing as we assume validation on deletion is
// unnecessary
func (m *MongoDBOpsManager) ValidateDelete() error {
	return nil
}

func errorNotConfigurableForAppDB(field string) v1.ValidationResult {
	return v1.OpsManagerResourceValidationError(fmt.Sprintf("%s field is not configurable for application databases", field), status.AppDb)
}

func deprecationErrorForOpsManager(deprecatedField, replacedWith string) v1.ValidationResult {
	return v1.OpsManagerResourceValidationError(fmt.Sprintf("%s field is not configurable for Ops Manager, use the %s field instead", deprecatedField, replacedWith), status.OpsManager)
}

func deprecationErrorForBackup(deprecatedField, replacedWith string) v1.ValidationResult {
	return v1.OpsManagerResourceValidationError(fmt.Sprintf("%s field is not configurable for Ops Manager Backup, use the %s field instead", deprecatedField, replacedWith), status.Backup)
}

func errorShardedClusterFieldsNotConfigurableForAppDB(field string) v1.ValidationResult {
	return v1.OpsManagerResourceValidationError(fmt.Sprintf("%s field is not configurable for application databases as it is for sharded clusters and appdbs are replica sets", field), status.AppDb)
}

func validOmVersion(os MongoDBOpsManagerSpec) v1.ValidationResult {
	_, err := versionutil.StringToSemverVersion(os.Version)
	if err != nil {
		return v1.OpsManagerResourceValidationError(fmt.Sprintf("'%s' is an invalid value for spec.version: %s", os.Version, err), status.OpsManager)
	}
	return v1.ValidationSuccess()
}

func validAppDBVersion(os MongoDBOpsManagerSpec) v1.ValidationResult {
	version := os.AppDB.GetMongoDBVersion()
	v, err := semver.Make(version)
	if err != nil {
		return v1.OpsManagerResourceValidationError(fmt.Sprintf("'%s' is an invalid value for spec.applicationDatabase.version: %s", version, err), status.AppDb)
	}
	fourZero, _ := semver.Make("4.0.0")
	if v.LT(fourZero) {
		return v1.OpsManagerResourceValidationError("the version of Application Database must be >= 4.0", status.AppDb)
	}

	return v1.ValidationSuccess()
}

func connectivityIsNotConfigurable(os MongoDBOpsManagerSpec) v1.ValidationResult {
	if os.AppDB.Connectivity != nil {
		return errorNotConfigurableForAppDB("connectivity")
	}
	return v1.ValidationSuccess()
}

// ConnectionSpec fields
func credentialsIsNotConfigurable(os MongoDBOpsManagerSpec) v1.ValidationResult {
	if os.AppDB.Credentials != "" {
		return errorNotConfigurableForAppDB("credentials")
	}
	return v1.ValidationSuccess()
}

func opsManagerConfigIsNotConfigurable(os MongoDBOpsManagerSpec) v1.ValidationResult {
	if os.AppDB.OpsManagerConfig != nil {
		return errorNotConfigurableForAppDB("opsManager")
	}
	return v1.ValidationSuccess()
}

func cloudManagerConfigIsNotConfigurable(os MongoDBOpsManagerSpec) v1.ValidationResult {
	if os.AppDB.CloudManagerConfig != nil {
		return errorNotConfigurableForAppDB("cloudManager")
	}
	return v1.ValidationSuccess()
}

func projectNameIsNotConfigurable(os MongoDBOpsManagerSpec) v1.ValidationResult {
	if os.AppDB.Project != "" {
		return errorNotConfigurableForAppDB("project")
	}
	return v1.ValidationSuccess()
}

// onlyFileSystemStoreIsEnabled checks if only FileSystemSnapshotStore is configured and not S3Store/Blockstore
func onlyFileSystemStoreIsEnabled(bp MongoDBOpsManagerBackup) bool {
	if len(bp.BlockStoreConfigs) == 0 && len(bp.S3Configs) == 0 && len(bp.FileSystemStoreConfigs) > 0 {
		return true
	}
	return false
}

// s3StoreMongodbUserSpecifiedNoMongoResource checks that 'mongodbResourceRef' is provided if 'mongodbUserRef' is configured
func s3StoreMongodbUserSpecifiedNoMongoResource(os MongoDBOpsManagerSpec) v1.ValidationResult {
	if !os.Backup.Enabled || len(os.Backup.S3Configs) == 0 {
		return v1.ValidationSuccess()
	}

	if onlyFileSystemStoreIsEnabled(*os.Backup) {
		return v1.ValidationSuccess()
	}

	for _, config := range os.Backup.S3Configs {
		if config.MongoDBUserRef != nil && config.MongoDBResourceRef == nil {
			return v1.OpsManagerResourceValidationError(
				fmt.Sprintf("'mongodbResourceRef' must be specified if 'mongodbUserRef' is configured (S3 Store: %s)", config.Name), status.OpsManager,
			)
		}
	}
	return v1.ValidationSuccess()
}

func kmipValidation(os MongoDBOpsManagerSpec) v1.ValidationResult {
	if os.Backup == nil || os.Backup.Enabled == false || os.Backup.Encryption == nil || os.Backup.Encryption.Kmip == nil {
		return v1.ValidationSuccess()
	}

	if _, _, err := net.SplitHostPort(os.Backup.Encryption.Kmip.Server.URL); err != nil {
		return v1.OpsManagerResourceValidationError(fmt.Sprintf("kmip url can not be splitted into host and port, see %v", err), status.OpsManager)
	}

	if len(os.Backup.Encryption.Kmip.Server.CA) == 0 {
		return v1.OpsManagerResourceValidationError("kmip CA ConfigMap name can not be empty", status.OpsManager)
	}

	return v1.ValidationSuccess()
}

func (om MongoDBOpsManager) RunValidations() []v1.ValidationResult {
	validators := []func(m MongoDBOpsManagerSpec) v1.ValidationResult{
		validOmVersion,
		validAppDBVersion,
		connectivityIsNotConfigurable,
		projectNameIsNotConfigurable,
		cloudManagerConfigIsNotConfigurable,
		opsManagerConfigIsNotConfigurable,
		credentialsIsNotConfigurable,
		s3StoreMongodbUserSpecifiedNoMongoResource,
		kmipValidation,
	}
	var validationResults []v1.ValidationResult

	for _, validator := range validators {
		res := validator(om.Spec)
		if res.Level > 0 {
			validationResults = append(validationResults, res)
		}
	}

	return validationResults
}

func (m *MongoDBOpsManager) ProcessValidationsWebhook() error {
	for _, res := range m.RunValidations() {
		if res.Level == v1.ErrorLevel {
			return errors.New(res.Msg)
		}
	}
	return nil
}
func (m *MongoDBOpsManager) ProcessValidationsOnReconcile() (error, status.Part) {
	for _, res := range m.RunValidations() {
		if res.Level == v1.ErrorLevel {
			return errors.New(res.Msg), res.OmStatusPart
		}

		if res.Level == v1.WarningLevel {
			switch res.OmStatusPart {
			case status.OpsManager:
				m.AddOpsManagerWarningIfNotExists(status.Warning(res.Msg))
			case status.AppDb:
				m.AddAppDBWarningIfNotExists(status.Warning(res.Msg))
			case status.Backup:
				m.AddBackupWarningIfNotExists(status.Warning(res.Msg))
			}
		}
	}

	return nil, status.None
}
