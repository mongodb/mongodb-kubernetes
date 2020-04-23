package v1

import (
	"errors"
	"fmt"

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
	return m.validate()
}

func (m *MongoDBOpsManager) ValidateUpdate(old runtime.Object) error {
	return m.validate()
}

func (m MongoDBOpsManager) validate() error {
	validationResults := m.RunValidations()
	if len(validationResults) == 0 {
		return nil
	}
	return buildValidationFailure(validationResults)
}

// ValidateDelete does nothing as we assume validation on deletion is
// unnecessary
func (m *MongoDBOpsManager) ValidateDelete() error {
	return nil
}

func warningNotConfigurableForAppDB(field string) ValidationResult {
	return validationWarning(fmt.Sprintf("%s field is not configurable for application databases", field))
}

func warningNotConfigurableForOpsManager(field string) ValidationResult {
	return validationWarning(fmt.Sprintf("%s field is not configurable for Ops Manager", field))
}

func warningNotConfigurableForBackup(field string) ValidationResult {
	return validationWarning(fmt.Sprintf("%s field is not configurable for Ops Manager Backup", field))
}

func warningShardedClusterFieldsNotConfigurableForAppDB(field string) ValidationResult {
	return validationWarning(fmt.Sprintf("%s field is not configurable for application databases as it is for sharded clusters and appdbs are replica sets", field))
}

func connectivityIsNotConfigurable(os MongoDBOpsManagerSpec) ValidationResult {
	if os.AppDB.Connectivity != nil {
		return warningNotConfigurableForAppDB("connectivity")
	}
	return validationSuccess()
}

// ConnectionSpec fields
func credentialsIsNotConfigurable(os MongoDBOpsManagerSpec) ValidationResult {
	if os.AppDB.Credentials != "" {
		return warningNotConfigurableForAppDB("credentials")
	}
	return validationSuccess()
}

func opsManagerConfigIsNotConfigurable(os MongoDBOpsManagerSpec) ValidationResult {
	if os.AppDB.OpsManagerConfig != nil {
		return warningNotConfigurableForAppDB("opsManager")
	}
	return validationSuccess()
}

func cloudManagerConfigIsNotConfigurable(os MongoDBOpsManagerSpec) ValidationResult {
	if os.AppDB.CloudManagerConfig != nil {
		return warningNotConfigurableForAppDB("cloudManager")
	}
	return validationSuccess()
}

func projectNameIsNotConfigurable(os MongoDBOpsManagerSpec) ValidationResult {
	if os.AppDB.ProjectName != "" {
		return warningNotConfigurableForAppDB("projectName")
	}
	return validationSuccess()
}

// sharded cluster fields
func configSrvPodSpecIsNotConfigurable(os MongoDBOpsManagerSpec) ValidationResult {
	if os.AppDB.ConfigSrvPodSpec != nil {
		return warningShardedClusterFieldsNotConfigurableForAppDB("configSrvPodSpec")
	}
	return validationSuccess()
}

func mongosPodSpecIsNotConfigurable(os MongoDBOpsManagerSpec) ValidationResult {
	if os.AppDB.MongosPodSpec != nil {
		return warningShardedClusterFieldsNotConfigurableForAppDB("mongosPodSpec")
	}
	return validationSuccess()
}

func shardPodSpecIsNotConfigurable(os MongoDBOpsManagerSpec) ValidationResult {
	if os.AppDB.ShardPodSpec != nil {
		return warningShardedClusterFieldsNotConfigurableForAppDB("shardPodSpec")
	}
	return validationSuccess()
}

func shardCountIsNotConfigurable(os MongoDBOpsManagerSpec) ValidationResult {
	if os.AppDB.ShardCount != 0 {
		return warningShardedClusterFieldsNotConfigurableForAppDB("shardCount")
	}
	return validationSuccess()
}

func mongodsPerShardCountIsNotConfigurable(os MongoDBOpsManagerSpec) ValidationResult {
	if os.AppDB.MongodsPerShardCount != 0 {
		return warningShardedClusterFieldsNotConfigurableForAppDB("mongodsPerShardCount")
	}
	return validationSuccess()
}

func mongosCountIsNotConfigurable(os MongoDBOpsManagerSpec) ValidationResult {
	if os.AppDB.MongosCount != 0 {
		return warningShardedClusterFieldsNotConfigurableForAppDB("mongosCount")
	}
	return validationSuccess()
}

func configServerCountIsNotConfigurable(os MongoDBOpsManagerSpec) ValidationResult {
	if os.AppDB.ConfigServerCount != 0 {
		return warningShardedClusterFieldsNotConfigurableForAppDB("configServerCount")
	}
	return validationSuccess()
}

// s3StoreMongodbUserSpecifiedNoMongoResource checks that 'mongodbResourceRef' is provided if 'mongodbUserRef' is configured
func s3StoreMongodbUserSpecifiedNoMongoResource(os MongoDBOpsManagerSpec) ValidationResult {
	if !os.Backup.Enabled || len(os.Backup.S3Configs) == 0 {
		return validationSuccess()
	}
	for _, config := range os.Backup.S3Configs {
		if config.MongoDBUserRef != nil && config.MongoDBResourceRef == nil {
			return validationWarning(
				"'mongodbResourceRef' must be specified if 'mongodbUserRef' is configured (S3 Store: %s)", config.Name)
		}
	}
	return validationSuccess()
}

func podSpecIsNotConfigurable(os MongoDBOpsManagerSpec) ValidationResult {
	if os.PodSpec != nil {
		return warningNotConfigurableForOpsManager("podSpec")
	}
	return validationSuccess()
}

func podSpecIsNotConfigurableBackup(os MongoDBOpsManagerSpec) ValidationResult {
	if os.Backup.PodSpec != nil {
		return warningNotConfigurableForBackup("podSpec")
	}
	return validationSuccess()
}

func (om MongoDBOpsManager) RunValidations() []ValidationResult {
	validators := []func(m MongoDBOpsManagerSpec) ValidationResult{
		connectivityIsNotConfigurable,
		projectNameIsNotConfigurable,
		cloudManagerConfigIsNotConfigurable,
		opsManagerConfigIsNotConfigurable,
		credentialsIsNotConfigurable,
		configSrvPodSpecIsNotConfigurable,
		mongosPodSpecIsNotConfigurable,
		shardPodSpecIsNotConfigurable,
		shardCountIsNotConfigurable,
		mongodsPerShardCountIsNotConfigurable,
		mongosCountIsNotConfigurable,
		configServerCountIsNotConfigurable,
		s3StoreMongodbUserSpecifiedNoMongoResource,
		podSpecIsNotConfigurable,
		podSpecIsNotConfigurableBackup,
	}
	var validationResults []ValidationResult

	for _, validator := range validators {
		res := validator(om.Spec)
		if res.Level > 0 {
			validationResults = append(validationResults, res)
		}
	}

	return validationResults
}

func (m *MongoDBOpsManager) ProcessValidationsOnReconcile() error {
	for _, res := range m.RunValidations() {
		if res.Level == ErrorLevel {
			return errors.New(res.Msg)
		}

		if res.Level == WarningLevel {
			m.AddWarningIfNotExists(StatusWarning(res.Msg))
		}
	}

	return nil
}
