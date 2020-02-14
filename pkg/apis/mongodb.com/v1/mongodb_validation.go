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

func replicaSetHorizonsRequireTLS(ms MongoDbSpec) error {
	if len(ms.Connectivity.ReplicaSetHorizons) > 0 && !ms.Security.TLSConfig.Enabled {
		return errors.New("TLS must be enabled in order to use replica set horizons")
	}
	return nil
}

func (m MongoDB) RunValidations() error {
	validators := []func(ms MongoDbSpec) error{
		replicaSetHorizonsRequireTLS,
	}

	for _, validator := range validators {
		if err := validator(m.Spec); err != nil {
			return err
		}
	}
	return nil
}
