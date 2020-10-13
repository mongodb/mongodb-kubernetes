package om

import (
	"errors"
	"fmt"

	v1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/status"

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
	return m.ProcessValidationsOnReconcile()
}

func (m *MongoDBOpsManager) ValidateUpdate(old runtime.Object) error {
	return m.ProcessValidationsOnReconcile()
}

// ValidateDelete does nothing as we assume validation on deletion is
// unnecessary
func (m *MongoDBOpsManager) ValidateDelete() error {
	return nil
}

func errorNotConfigurableForAppDB(field string) v1.ValidationResult {
	return v1.ValidationError(fmt.Sprintf("%s field is not configurable for application databases", field))
}

func deprecationErrorForOpsManager(deprecatedField, replacedWith string) v1.ValidationResult {
	return v1.ValidationError(fmt.Sprintf("%s field is not configurable for Ops Manager, use the %s field instead", deprecatedField, replacedWith))
}

func deprecationErrorForBackup(deprecatedField, replacedWith string) v1.ValidationResult {
	return v1.ValidationError(fmt.Sprintf("%s field is not configurable for Ops Manager Backup, use the %s field instead", deprecatedField, replacedWith))
}

func errorShardedClusterFieldsNotConfigurableForAppDB(field string) v1.ValidationResult {
	return v1.ValidationError(fmt.Sprintf("%s field is not configurable for application databases as it is for sharded clusters and appdbs are replica sets", field))
}

func validOmVersion(os MongoDBOpsManagerSpec) v1.ValidationResult {
	_, err := versionutil.StringToSemverVersion(os.Version)
	if err != nil {
		return v1.ValidationError("'%s' is an invalid value for spec.version: %s", os.Version, err)
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

// sharded cluster fields
func configSrvPodSpecIsNotConfigurable(os MongoDBOpsManagerSpec) v1.ValidationResult {
	if os.AppDB.ConfigSrvPodSpec != nil {
		return errorShardedClusterFieldsNotConfigurableForAppDB("configSrvPodSpec")
	}
	return v1.ValidationSuccess()
}

func mongosPodSpecIsNotConfigurable(os MongoDBOpsManagerSpec) v1.ValidationResult {
	if os.AppDB.MongosPodSpec != nil {
		return errorShardedClusterFieldsNotConfigurableForAppDB("mongosPodSpec")
	}
	return v1.ValidationSuccess()
}

func shardPodSpecIsNotConfigurable(os MongoDBOpsManagerSpec) v1.ValidationResult {
	if os.AppDB.ShardPodSpec != nil {
		return errorShardedClusterFieldsNotConfigurableForAppDB("shardPodSpec")
	}
	return v1.ValidationSuccess()
}

func shardCountIsNotConfigurable(os MongoDBOpsManagerSpec) v1.ValidationResult {
	if os.AppDB.ShardCount != 0 {
		return errorShardedClusterFieldsNotConfigurableForAppDB("shardCount")
	}
	return v1.ValidationSuccess()
}

func mongodsPerShardCountIsNotConfigurable(os MongoDBOpsManagerSpec) v1.ValidationResult {
	if os.AppDB.MongodsPerShardCount != 0 {
		return errorShardedClusterFieldsNotConfigurableForAppDB("mongodsPerShardCount")
	}
	return v1.ValidationSuccess()
}

func mongosCountIsNotConfigurable(os MongoDBOpsManagerSpec) v1.ValidationResult {
	if os.AppDB.MongosCount != 0 {
		return errorShardedClusterFieldsNotConfigurableForAppDB("mongosCount")
	}
	return v1.ValidationSuccess()
}

func configServerCountIsNotConfigurable(os MongoDBOpsManagerSpec) v1.ValidationResult {
	if os.AppDB.ConfigServerCount != 0 {
		return errorShardedClusterFieldsNotConfigurableForAppDB("configServerCount")
	}
	return v1.ValidationSuccess()
}

// s3StoreMongodbUserSpecifiedNoMongoResource checks that 'mongodbResourceRef' is provided if 'mongodbUserRef' is configured
func s3StoreMongodbUserSpecifiedNoMongoResource(os MongoDBOpsManagerSpec) v1.ValidationResult {
	if !os.Backup.Enabled || len(os.Backup.S3Configs) == 0 {
		return v1.ValidationSuccess()
	}
	for _, config := range os.Backup.S3Configs {
		if config.MongoDBUserRef != nil && config.MongoDBResourceRef == nil {
			return v1.ValidationError(
				"'mongodbResourceRef' must be specified if 'mongodbUserRef' is configured (S3 Store: %s)", config.Name)
		}
	}
	return v1.ValidationSuccess()
}

func podSpecIsNotConfigurable(os MongoDBOpsManagerSpec) v1.ValidationResult {
	if os.PodSpec != nil {
		return deprecationErrorForOpsManager("podSpec", "statefulSet")
	}
	return v1.ValidationSuccess()
}

func podSpecIsNotConfigurableBackup(os MongoDBOpsManagerSpec) v1.ValidationResult {
	if os.Backup.PodSpec != nil {
		return deprecationErrorForBackup("podSpec", "backup.statefulSet")
	}
	return v1.ValidationSuccess()
}

func usesShortcutResource(os MongoDBOpsManagerSpec) v1.ValidationResult {
	if mdbv1.UsesDeprecatedResourceFields(*os.AppDB.PodSpec) {
		return v1.ValidationWarning(mdbv1.UseOfDeprecatedShortcutFieldsWarning)
	}

	return v1.ValidationSuccess()
}

func (om MongoDBOpsManager) RunValidations() []v1.ValidationResult {
	validators := []func(m MongoDBOpsManagerSpec) v1.ValidationResult{
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
	var validationResults []v1.ValidationResult

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
		if res.Level == v1.ErrorLevel {
			return errors.New(res.Msg)
		}

		if res.Level == v1.WarningLevel {
			m.AddWarningIfNotExists(status.Warning(res.Msg))
		}
	}

	return nil
}
