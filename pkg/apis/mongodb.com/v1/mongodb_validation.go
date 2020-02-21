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

type validationLevel int

const (
	successLevel validationLevel = iota
	warningLevel
	errorLevel
)

type validationResult struct {
	msg   string
	level validationLevel
}

func validationSuccess() validationResult {
	return validationResult{level: successLevel}
}

func validationWarning(msg string) validationResult {
	return validationResult{msg: msg, level: warningLevel}
}

func validationError(msg string) validationResult {
	return validationResult{msg: msg, level: errorLevel}
}

var _ webhook.Validator = &MongoDB{}

// ValidateCreate and ValidateUpdate should be the same if we intend to do this
// on every reconciliation as well
func (mdb *MongoDB) ValidateCreate() error {
	return mdb.RunValidations()
}
func (mdb *MongoDB) ValidateUpdate(old runtime.Object) error {
	return mdb.RunValidations()
}

// ValidateDelete does nothing as we assume validation on deletion is
// unnecessary
func (mdb *MongoDB) ValidateDelete() error {
	return nil
}

func replicaSetHorizonsRequireTLS(ms MongoDbSpec) validationResult {
	if len(ms.Connectivity.ReplicaSetHorizons) > 0 && !ms.Security.TLSConfig.Enabled {
		msg := "TLS must be enabled in order to use replica set horizons"
		return validationError(msg)
	}
	return validationSuccess()
}

func horizonsMustEqualMembers(ms MongoDbSpec) validationResult {
	numHorizonMembers := len(ms.Connectivity.ReplicaSetHorizons)
	if numHorizonMembers > 0 && numHorizonMembers != ms.Members {
		return validationError("Number of horizons must be equal to number of members in replica set")
	}
	return validationSuccess()
}

func (m *MongoDB) RunValidations() error {
	validators := []func(ms MongoDbSpec) validationResult{
		replicaSetHorizonsRequireTLS,
		horizonsMustEqualMembers,
	}

	for _, validator := range validators {
		if res := validator(m.Spec); res.level == errorLevel {
			return errors.New(res.msg)
		}

		if res := validator(m.Spec); res.level == warningLevel {
			m.AddWarningIfNotExists(StatusWarning(res.msg))
		}
	}
	return nil
}
