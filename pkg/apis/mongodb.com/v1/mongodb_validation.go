package v1

// IMPORTANT: this package is intended to contain only "simple" validation—in
// other words, validation that is based only on the properties in the MongoDB
// resource. More complex validation, such as validation that needs to observe
// the state of the cluster, belongs somewhere else.

import (
	"errors"

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
	return buildValidationFailure(validationResults)
}

// ValidateDelete does nothing as we assume validation on deletion is
// unnecessary
func (mdb *MongoDB) ValidateDelete() error {
	return nil
}

func replicaSetHorizonsRequireTLS(ms MongoDbSpec) ValidationResult {
	if len(ms.Connectivity.ReplicaSetHorizons) > 0 && !ms.Security.TLSConfig.Enabled {
		msg := "TLS must be enabled in order to use replica set horizons"
		return validationError(msg)
	}
	return validationSuccess()
}

func horizonsMustEqualMembers(ms MongoDbSpec) ValidationResult {
	numHorizonMembers := len(ms.Connectivity.ReplicaSetHorizons)
	if numHorizonMembers > 0 && numHorizonMembers != ms.Members {
		return validationError("Number of horizons must be equal to number of members in replica set")
	}
	return validationSuccess()
}

func deploymentsMustHaveTLSInX509Env(ms MongoDbSpec) ValidationResult {
	authSpec := ms.Security.Authentication
	if authSpec == nil {
		return validationSuccess()
	}
	if authSpec.Enabled && authSpec.IsX509Enabled() && !ms.GetTLSConfig().Enabled {
		return validationError("Cannot have a non-tls deployment when x509 authentication is enabled")
	}
	return validationSuccess()
}

func (m MongoDB) RunValidations() []ValidationResult {
	validators := []func(ms MongoDbSpec) ValidationResult{
		replicaSetHorizonsRequireTLS,
		horizonsMustEqualMembers,
		deploymentsMustHaveTLSInX509Env,
	}

	var validationResults []ValidationResult

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
		if res.Level == ErrorLevel {
			return errors.New(res.Msg)
		}

		if res.Level == WarningLevel {
			m.AddWarningIfNotExists(StatusWarning(res.Msg))
		}
	}

	return nil
}
