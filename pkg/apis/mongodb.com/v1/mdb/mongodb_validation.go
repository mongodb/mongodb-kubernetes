package mdb

// IMPORTANT: this package is intended to contain only "simple" validation—in
// other words, validation that is based only on the properties in the MongoDB
// resource. More complex validation, such as validation that needs to observe
// the state of the cluster, belongs somewhere else.

import (
	"errors"
	"strings"

	v1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/status"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

var _ webhook.Validator = &MongoDB{}

// ValidateCreate and ValidateUpdate should be the same if we intend to do this
// on every reconciliation as well
func (mdb *MongoDB) ValidateCreate() error {
	return mdb.validate()
}

func (mdb *MongoDB) ValidateUpdate(old runtime.Object) error {
	return mdb.validate()
}

func (mdb MongoDB) validate() error {
	validationResults := mdb.RunValidations()
	if len(validationResults) == 0 {
		return nil
	}
	return v1.BuildValidationFailure(validationResults)
}

// ValidateDelete does nothing as we assume validation on deletion is
// unnecessary
func (mdb *MongoDB) ValidateDelete() error {
	return nil
}

func replicaSetHorizonsRequireTLS(ms MongoDbSpec) v1.ValidationResult {
	if len(ms.Connectivity.ReplicaSetHorizons) > 0 && !ms.Security.TLSConfig.Enabled {
		msg := "TLS must be enabled in order to use replica set horizons"
		return v1.ValidationError(msg)
	}
	return v1.ValidationSuccess()
}

func horizonsMustEqualMembers(ms MongoDbSpec) v1.ValidationResult {
	numHorizonMembers := len(ms.Connectivity.ReplicaSetHorizons)
	if numHorizonMembers > 0 && numHorizonMembers != ms.Members {
		return v1.ValidationError("Number of horizons must be equal to number of members in replica set")
	}
	return v1.ValidationSuccess()
}

func deploymentsMustHaveTLSInX509Env(ms MongoDbSpec) v1.ValidationResult {
	authSpec := ms.Security.Authentication
	if authSpec == nil {
		return v1.ValidationSuccess()
	}
	if authSpec.Enabled && authSpec.IsX509Enabled() && !ms.GetTLSConfig().Enabled {
		return v1.ValidationError("Cannot have a non-tls deployment when x509 authentication is enabled")
	}
	return v1.ValidationSuccess()
}

func deploymentsMustHaveAgentModesIfAuthIsEnabled(ms MongoDbSpec) v1.ValidationResult {
	authSpec := ms.Security.Authentication
	if authSpec == nil {
		return v1.ValidationSuccess()
	}
	if authSpec.Enabled && len(authSpec.Modes) == 0 {
		return v1.ValidationError("Cannot enable authentication without modes specified")
	}
	return v1.ValidationSuccess()
}

func deploymentsMustHaveAgentModeInAuthModes(ms MongoDbSpec) v1.ValidationResult {
	authSpec := ms.Security.Authentication
	if authSpec == nil {
		return v1.ValidationSuccess()
	}
	if !authSpec.Enabled {
		return v1.ValidationSuccess()
	}

	if authSpec.Agents.Mode != "" && !stringutil.Contains(authSpec.Modes, authSpec.Agents.Mode) {
		return v1.ValidationError("Cannot configure an Agent authentication mechanism that is not specified in authentication modes")
	}
	return v1.ValidationSuccess()
}

func ldapAuthRequiresEnterprise(ms MongoDbSpec) v1.ValidationResult {
	authSpec := ms.Security.Authentication
	if authSpec != nil && authSpec.isLDAPEnabled() && !strings.HasSuffix(ms.Version, "-ent") {
		return v1.ValidationError("Cannot enable LDAP authentication with MongoDB Community Builds")
	}
	return v1.ValidationSuccess()
}

func additionalMongodConfig(ms MongoDbSpec) v1.ValidationResult {
	if ms.ResourceType == ShardedCluster {
		if ms.AdditionalMongodConfig != nil && len(ms.AdditionalMongodConfig) > 0 {
			return v1.ValidationError("'spec.additionalMongodConfig' cannot be specified if type of MongoDB is %s", ShardedCluster)
		}
		return v1.ValidationSuccess()
	}
	// Standalone or ReplicaSet
	if ms.ShardSpec != nil || ms.ConfigSrvSpec != nil || ms.MongosSpec != nil {
		return v1.ValidationError("'spec.mongos', 'spec.configSrv', 'spec.shard' cannot be specified if type of MongoDB is %s", ms.ResourceType)
	}
	return v1.ValidationSuccess()
}

func (m MongoDB) RunValidations() []v1.ValidationResult {
	validators := []func(ms MongoDbSpec) v1.ValidationResult{
		replicaSetHorizonsRequireTLS,
		horizonsMustEqualMembers,
		deploymentsMustHaveTLSInX509Env,
		deploymentsMustHaveAgentModesIfAuthIsEnabled,
		deploymentsMustHaveAgentModeInAuthModes,
		additionalMongodConfig,
		ldapAuthRequiresEnterprise,
		rolesAttributeisCorrectlyConfigured,
	}

	var validationResults []v1.ValidationResult

	for _, validator := range validators {
		res := validator(m.Spec)
		if res.Level > 0 {
			validationResults = append(validationResults, res)
		}
	}
	return validationResults
}

func (m *MongoDB) ProcessValidationsOnReconcile() error {
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
