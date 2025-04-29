package om

import (
	"errors"
	"fmt"
	"net"

	"github.com/blang/semver"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	v1 "github.com/mongodb/mongodb-kubernetes/api/v1"
	"github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/api/v1/status"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/versionutil"
)

// IMPORTANT: this package is intended to contain only "simple" validation—in
// other words, validation that is based only on the properties in the Ops Manager
// resource. More complex validation, such as validation that needs to observe
// the state of the cluster, belongs somewhere else.

var _ webhook.Validator = &MongoDBOpsManager{}

// ValidateCreate and ValidateUpdate should be the same if we intend to do this
// on every reconciliation as well
func (om *MongoDBOpsManager) ValidateCreate() (admission.Warnings, error) {
	return nil, om.ProcessValidationsWebhook()
}

func (om *MongoDBOpsManager) ValidateUpdate(_ runtime.Object) (admission.Warnings, error) {
	return nil, om.ProcessValidationsWebhook()
}

// ValidateDelete does nothing as we assume validation on deletion is
// unnecessary
func (om *MongoDBOpsManager) ValidateDelete() (admission.Warnings, error) {
	return nil, nil
}

func errorNotConfigurableForAppDB(field string) v1.ValidationResult {
	return v1.OpsManagerResourceValidationError(fmt.Sprintf("%s field is not configurable for application databases", field), status.AppDb)
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
	if os.Backup == nil || !os.Backup.Enabled || os.Backup.Encryption == nil || os.Backup.Encryption.Kmip == nil {
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

func validateEmptyClusterSpecListSingleCluster(os MongoDBOpsManagerSpec) v1.ValidationResult {
	if !os.AppDB.IsMultiCluster() {
		if len(os.AppDB.ClusterSpecList) > 0 {
			return v1.OpsManagerResourceValidationError("Single cluster AppDB deployment should have empty clusterSpecList", status.OpsManager)
		}
	}
	return v1.ValidationSuccess()
}

func validateTopologyIsSpecified(os MongoDBOpsManagerSpec) v1.ValidationResult {
	if len(os.ClusterSpecList) > 0 {
		if !os.IsMultiCluster() {
			return v1.OpsManagerResourceValidationError("Topology 'MultiCluster' must be specified while setting a not empty spec.clusterSpecList", status.OpsManager)
		}
	}
	return v1.ValidationSuccess()
}

func featureCompatibilityVersionValidation(os MongoDBOpsManagerSpec) v1.ValidationResult {
	fcv := os.AppDB.FeatureCompatibilityVersion
	return mdb.ValidateFCV(fcv)
}

func validateClusterSpecList(os MongoDBOpsManagerSpec) v1.ValidationResult {
	if os.IsMultiCluster() {
		if len(os.ClusterSpecList) == 0 {
			return v1.OpsManagerResourceValidationError("At least one ClusterSpecList entry must be specified for MultiCluster mode OM", status.OpsManager)
		}
		if os.Backup != nil && os.Backup.Enabled {
			backupMembersConfigured := false
			for _, clusterSpec := range os.ClusterSpecList {
				if clusterSpec.Backup != nil && clusterSpec.Backup.Members > 0 {
					backupMembersConfigured = true
					break
				}
			}
			if !backupMembersConfigured {
				return v1.OpsManagerResourceValidationError("At least one ClusterSpecList item must have backup members configured", status.OpsManager)
			}
		}
	}
	if !os.IsMultiCluster() {
		if len(os.ClusterSpecList) > 0 {
			return v1.OpsManagerResourceValidationError("ClusterSpecList cannot be specified for SingleCluster mode OM", status.OpsManager)
		}
	}
	return v1.ValidationSuccess()
}

// validateAppDBUniqueExternalDomains validates uniqueness of the domains if they are provided.
// External domain might be specified at the top level in spec.externalAccess.externalDomain or in every member cluster.
// We make sure that if external domains are used, every member cluster has unique external domain defined.
func validateAppDBUniqueExternalDomains(os MongoDBOpsManagerSpec) v1.ValidationResult {
	appDBSpec := os.AppDB

	externalDomains := make(map[string]string)
	for _, e := range appDBSpec.ClusterSpecList {
		if externalDomain := appDBSpec.GetExternalDomainForMemberCluster(e.ClusterName); externalDomain != nil {
			externalDomains[e.ClusterName] = *externalDomain
		}
	}

	// We don't need to validate external domains if there aren't any specified.
	// We don't have any flag that enables usage of external domains. We use them if they are provided.
	if len(externalDomains) == 0 {
		return v1.ValidationSuccess()
	}

	present := map[string]struct{}{}
	for _, e := range appDBSpec.ClusterSpecList {
		externalDomain, ok := externalDomains[e.ClusterName]
		if !ok {
			return v1.ValidationError("The externalDomain is not set for member cluster: %s", e.ClusterName)
		}

		if _, ok := present[externalDomain]; ok {
			return v1.ValidationError("Multiple member clusters with the same externalDomain (%s) are not allowed. "+
				"Check if all spec.applicationDatabase.clusterSpecList[*].externalAccess.externalDomain fields are defined and are unique.", externalDomain)
		}
		present[externalDomain] = struct{}{}
	}
	return v1.ValidationSuccess()
}

func validateBackupS3Stores(os MongoDBOpsManagerSpec) v1.ValidationResult {
	backup := os.Backup
	if backup == nil || !backup.Enabled {
		return v1.ValidationSuccess()
	}

	if len(backup.S3Configs) > 0 {
		for _, config := range backup.S3Configs {
			if config.IRSAEnabled {
				if config.S3SecretRef != nil {
					return v1.OpsManagerResourceValidationWarning("'s3SecretRef' must not be specified if using IRSA (S3 Store: %s)", status.OpsManager, config.Name)
				}
			} else if config.S3SecretRef == nil || config.S3SecretRef.Name == "" {
				return v1.OpsManagerResourceValidationError("'s3SecretRef' must be specified if not using IRSA (S3 Store: %s)", status.OpsManager, config.Name)
			}
		}
	}

	if len(backup.S3OplogStoreConfigs) > 0 {
		for _, oplogStoreConfig := range backup.S3OplogStoreConfigs {
			if oplogStoreConfig.IRSAEnabled {
				if oplogStoreConfig.S3SecretRef != nil {
					return v1.OpsManagerResourceValidationWarning("'s3SecretRef' must not be specified if using IRSA (S3 OpLog Store: %s)", status.OpsManager, oplogStoreConfig.Name)
				}
			} else if oplogStoreConfig.S3SecretRef == nil || oplogStoreConfig.S3SecretRef.Name == "" {
				return v1.OpsManagerResourceValidationError("'s3SecretRef' must be specified if not using IRSA (S3 OpLog Store: %s)", status.OpsManager, oplogStoreConfig.Name)
			}
		}
	}

	return v1.ValidationSuccess()
}

func (om *MongoDBOpsManager) RunValidations() []v1.ValidationResult {
	validators := []func(m MongoDBOpsManagerSpec) v1.ValidationResult{
		validOmVersion,
		validAppDBVersion,
		connectivityIsNotConfigurable,
		cloudManagerConfigIsNotConfigurable,
		opsManagerConfigIsNotConfigurable,
		credentialsIsNotConfigurable,
		s3StoreMongodbUserSpecifiedNoMongoResource,
		kmipValidation,
		validateEmptyClusterSpecListSingleCluster,
		validateTopologyIsSpecified,
		validateClusterSpecList,
		validateBackupS3Stores,
		featureCompatibilityVersionValidation,
		validateAppDBUniqueExternalDomains,
	}

	multiClusterAppDBSharedClusterValidators := []func(ms mdb.ClusterSpecList) v1.ValidationResult{
		mdb.ValidateUniqueClusterNames,
		mdb.ValidateNonEmptyClusterSpecList,
		mdb.ValidateMemberClusterIsSubsetOfKubeConfig,
	}

	var validationResults []v1.ValidationResult

	for _, validator := range validators {
		res := validator(om.Spec)
		if res.Level > 0 {
			validationResults = append(validationResults, res)
		}
	}

	// Explicit tests for AppDB multi-cluster
	if om.Spec.AppDB.IsMultiCluster() {
		for _, validator := range multiClusterAppDBSharedClusterValidators {
			res := validator(om.Spec.AppDB.ClusterSpecList)
			if res.Level > 0 {
				validationResults = append(validationResults, res)
			}
		}
	}

	return validationResults
}

func (om *MongoDBOpsManager) ProcessValidationsWebhook() error {
	for _, res := range om.RunValidations() {
		if res.Level == v1.ErrorLevel {
			return errors.New(res.Msg)
		}
	}
	return nil
}

func (om *MongoDBOpsManager) ProcessValidationsOnReconcile() (status.Part, error) {
	for _, res := range om.RunValidations() {
		if res.Level == v1.ErrorLevel {
			return res.OmStatusPart, errors.New(res.Msg)
		}

		if res.Level == v1.WarningLevel {
			switch res.OmStatusPart {
			case status.OpsManager:
				om.AddOpsManagerWarningIfNotExists(status.Warning(res.Msg))
			case status.AppDb:
				om.AddAppDBWarningIfNotExists(status.Warning(res.Msg))
			case status.Backup:
				om.AddBackupWarningIfNotExists(status.Warning(res.Msg))
			}
		}
	}

	return status.None, nil
}
