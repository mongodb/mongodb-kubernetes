package om

import (
	"errors"
	"fmt"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/status"
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
	return mdbv1.BuildValidationFailure(validationResults)
}

// ValidateDelete does nothing as we assume validation on deletion is
// unnecessary
func (m *MongoDBOpsManager) ValidateDelete() error {
	return nil
}

func warningNotConfigurableForAppDB(field string) mdbv1.ValidationResult {
	return mdbv1.ValidationWarning(fmt.Sprintf("%s field is not configurable for application databases", field))
}

func deprecationWarningForOpsManager(deprecatedField, replacedWith string) mdbv1.ValidationResult {
	return mdbv1.ValidationWarning(fmt.Sprintf("%s field is not configurable for Ops Manager, use the %s field instead", deprecatedField, replacedWith))
}

func deprecationWarningForBackup(deprecatedField, replacedWith string) mdbv1.ValidationResult {
	return mdbv1.ValidationWarning(fmt.Sprintf("%s field is not configurable for Ops Manager Backup, use the %s field instead", deprecatedField, replacedWith))
}

func warningShardedClusterFieldsNotConfigurableForAppDB(field string) mdbv1.ValidationResult {
	return mdbv1.ValidationWarning(fmt.Sprintf("%s field is not configurable for application databases as it is for sharded clusters and appdbs are replica sets", field))
}

func validOmVersion(os MongoDBOpsManagerSpec) mdbv1.ValidationResult {
	_, err := os.GetVersion()
	if err != nil {
		return mdbv1.ValidationError("'%s' is an invalid value for spec.version: %s", os.Version, err)
	}
	return mdbv1.ValidationSuccess()
}

func connectivityIsNotConfigurable(os MongoDBOpsManagerSpec) mdbv1.ValidationResult {
	if os.AppDB.Connectivity != nil {
		return warningNotConfigurableForAppDB("connectivity")
	}
	return mdbv1.ValidationSuccess()
}

// ConnectionSpec fields
func credentialsIsNotConfigurable(os MongoDBOpsManagerSpec) mdbv1.ValidationResult {
	if os.AppDB.Credentials != "" {
		return warningNotConfigurableForAppDB("credentials")
	}
	return mdbv1.ValidationSuccess()
}

func opsManagerConfigIsNotConfigurable(os MongoDBOpsManagerSpec) mdbv1.ValidationResult {
	if os.AppDB.OpsManagerConfig != nil {
		return warningNotConfigurableForAppDB("opsManager")
	}
	return mdbv1.ValidationSuccess()
}

func cloudManagerConfigIsNotConfigurable(os MongoDBOpsManagerSpec) mdbv1.ValidationResult {
	if os.AppDB.CloudManagerConfig != nil {
		return warningNotConfigurableForAppDB("cloudManager")
	}
	return mdbv1.ValidationSuccess()
}

func projectNameIsNotConfigurable(os MongoDBOpsManagerSpec) mdbv1.ValidationResult {
	if os.AppDB.Project != "" {
		return warningNotConfigurableForAppDB("project")
	}
	return mdbv1.ValidationSuccess()
}

// sharded cluster fields
func configSrvPodSpecIsNotConfigurable(os MongoDBOpsManagerSpec) mdbv1.ValidationResult {
	if os.AppDB.ConfigSrvPodSpec != nil {
		return warningShardedClusterFieldsNotConfigurableForAppDB("configSrvPodSpec")
	}
	return mdbv1.ValidationSuccess()
}

func mongosPodSpecIsNotConfigurable(os MongoDBOpsManagerSpec) mdbv1.ValidationResult {
	if os.AppDB.MongosPodSpec != nil {
		return warningShardedClusterFieldsNotConfigurableForAppDB("mongosPodSpec")
	}
	return mdbv1.ValidationSuccess()
}

func shardPodSpecIsNotConfigurable(os MongoDBOpsManagerSpec) mdbv1.ValidationResult {
	if os.AppDB.ShardPodSpec != nil {
		return warningShardedClusterFieldsNotConfigurableForAppDB("shardPodSpec")
	}
	return mdbv1.ValidationSuccess()
}

func shardCountIsNotConfigurable(os MongoDBOpsManagerSpec) mdbv1.ValidationResult {
	if os.AppDB.ShardCount != 0 {
		return warningShardedClusterFieldsNotConfigurableForAppDB("shardCount")
	}
	return mdbv1.ValidationSuccess()
}

func mongodsPerShardCountIsNotConfigurable(os MongoDBOpsManagerSpec) mdbv1.ValidationResult {
	if os.AppDB.MongodsPerShardCount != 0 {
		return warningShardedClusterFieldsNotConfigurableForAppDB("mongodsPerShardCount")
	}
	return mdbv1.ValidationSuccess()
}

func mongosCountIsNotConfigurable(os MongoDBOpsManagerSpec) mdbv1.ValidationResult {
	if os.AppDB.MongosCount != 0 {
		return warningShardedClusterFieldsNotConfigurableForAppDB("mongosCount")
	}
	return mdbv1.ValidationSuccess()
}

func configServerCountIsNotConfigurable(os MongoDBOpsManagerSpec) mdbv1.ValidationResult {
	if os.AppDB.ConfigServerCount != 0 {
		return warningShardedClusterFieldsNotConfigurableForAppDB("configServerCount")
	}
	return mdbv1.ValidationSuccess()
}

// s3StoreMongodbUserSpecifiedNoMongoResource checks that 'mongodbResourceRef' is provided if 'mongodbUserRef' is configured
func s3StoreMongodbUserSpecifiedNoMongoResource(os MongoDBOpsManagerSpec) mdbv1.ValidationResult {
	if !os.Backup.Enabled || len(os.Backup.S3Configs) == 0 {
		return mdbv1.ValidationSuccess()
	}
	for _, config := range os.Backup.S3Configs {
		if config.MongoDBUserRef != nil && config.MongoDBResourceRef == nil {
			return mdbv1.ValidationWarning(
				"'mongodbResourceRef' must be specified if 'mongodbUserRef' is configured (S3 Store: %s)", config.Name)
		}
	}
	return mdbv1.ValidationSuccess()
}

func podSpecIsNotConfigurable(os MongoDBOpsManagerSpec) mdbv1.ValidationResult {
	if os.PodSpec != nil {
		return deprecationWarningForOpsManager("podSpec", "statefulSet")
	}
	return mdbv1.ValidationSuccess()
}

func podSpecIsNotConfigurableBackup(os MongoDBOpsManagerSpec) mdbv1.ValidationResult {
	if os.Backup.PodSpec != nil {
		return deprecationWarningForBackup("podSpec", "backup.statefulSet")
	}
	return mdbv1.ValidationSuccess()
}

func (om MongoDBOpsManager) RunValidations() []mdbv1.ValidationResult {
	validators := []func(m MongoDBOpsManagerSpec) mdbv1.ValidationResult{
		validOmVersion,
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
	var validationResults []mdbv1.ValidationResult

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
		if res.Level == mdbv1.ErrorLevel {
			return errors.New(res.Msg)
		}

		if res.Level == mdbv1.WarningLevel {
			m.AddWarningIfNotExists(status.Warning(res.Msg))
		}
	}

	return nil
}
