package mdbmulti

import (
	"errors"
	"fmt"

	v1 "github.com/10gen/ops-manager-kubernetes/api/v1"
	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	runtime "k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

var _ webhook.Validator = &MongoDBMulti{}

func (m *MongoDBMulti) ValidateCreate() error {
	return m.ProcessValidationsOnReconcile(nil)
}

func (m *MongoDBMulti) ValidateUpdate(old runtime.Object) error {
	return m.ProcessValidationsOnReconcile(old.(*MongoDBMulti))
}
func (m *MongoDBMulti) ValidateDelete() error {
	return nil
}

func (m *MongoDBMulti) ProcessValidationsOnReconcile(old *MongoDBMulti) error {
	for _, res := range m.RunValidations(old) {
		if res.Level == v1.ErrorLevel {
			return errors.New(res.Msg)
		}
	}
	return nil
}

func (m *MongoDBMulti) RunValidations(old *MongoDBMulti) []v1.ValidationResult {
	multiClusterValidators := []func(ms MongoDBMultiSpec) v1.ValidationResult{
		validateUniqueClusterNames,
	}

	var validationResults []v1.ValidationResult

	for _, validator := range multiClusterValidators {
		res := validator(m.Spec)
		if res.Level > 0 {
			validationResults = append(validationResults, res)
		}
	}

	for _, validator := range mdbv1.CommonValidators() {
		res := validator(m.Spec.DbCommonSpec)
		if res.Level > 0 {
			validationResults = append(validationResults, res)
		}
	}

	return validationResults
}
func validateUniqueClusterNames(ms MongoDBMultiSpec) v1.ValidationResult {
	present := make(map[string]struct{})

	for _, e := range ms.ClusterSpecList {
		if _, ok := present[e.ClusterName]; ok {
			msg := fmt.Sprintf("Multiple clusters with the same name(%s) are not allowed", e.ClusterName)
			return v1.ValidationError(msg)
		}
		present[e.ClusterName] = struct{}{}
	}
	return v1.ValidationSuccess()
}
