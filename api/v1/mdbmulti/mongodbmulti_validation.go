package mdbmulti

import (
	"errors"
	"fmt"

	v1 "github.com/10gen/ops-manager-kubernetes/api/v1"
	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	runtime "k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

var _ webhook.Validator = &MongoDBMultiCluster{}

func (m *MongoDBMultiCluster) ValidateCreate() error {
	return m.ProcessValidationsOnReconcile(nil)
}

func (m *MongoDBMultiCluster) ValidateUpdate(old runtime.Object) error {
	return m.ProcessValidationsOnReconcile(old.(*MongoDBMultiCluster))
}
func (m *MongoDBMultiCluster) ValidateDelete() error {
	return nil
}

func (m *MongoDBMultiCluster) ProcessValidationsOnReconcile(old *MongoDBMultiCluster) error {
	for _, res := range m.RunValidations(old) {
		if res.Level == v1.ErrorLevel {
			return errors.New(res.Msg)
		}
	}
	return nil
}

func (m *MongoDBMultiCluster) RunValidations(old *MongoDBMultiCluster) []v1.ValidationResult {
	multiClusterValidators := []func(ms MongoDBMultiSpec) v1.ValidationResult{
		validateUniqueExternalDomains,
	}

	// shared validators between MongoDBMulti and AppDB
	multiClusterAppDBSharedClusterValidators := []func(ms []mdbv1.ClusterSpecItem) v1.ValidationResult{
		mdbv1.ValidateUniqueClusterNames,
		mdbv1.ValidateNonEmptyClusterSpecList,
		mdbv1.ValidateMemberClusterIsSubsetOfKubeConfig,
	}

	var validationResults []v1.ValidationResult

	for _, validator := range mdbv1.CommonValidators() {
		res := validator(m.Spec.DbCommonSpec)
		if res.Level > 0 {
			validationResults = append(validationResults, res)
		}
	}

	for _, validator := range multiClusterValidators {
		res := validator(m.Spec)
		if res.Level > 0 {
			validationResults = append(validationResults, res)
		}
	}

	for _, validator := range multiClusterAppDBSharedClusterValidators {
		res := validator(m.Spec.ClusterSpecList)
		if res.Level > 0 {
			validationResults = append(validationResults, res)
		}
	}

	return validationResults
}

func validateUniqueExternalDomains(ms MongoDBMultiSpec) v1.ValidationResult {
	if ms.ExternalAccessConfiguration != nil {
		present := make(map[string]struct{})

		for _, e := range ms.ClusterSpecList {
			val := e.ExternalAccessConfiguration.ExternalDomain
			if val == nil {
				return v1.ValidationError("The externalDomain is not set for cluster name %s", e.ClusterName)
			}
			valAsString := *e.ExternalAccessConfiguration.ExternalDomain
			if _, ok := present[valAsString]; ok {
				msg := fmt.Sprintf("Multiple externalDomains with the same name (%s) are not allowed", valAsString)
				return v1.ValidationError(msg)
			}
			present[valAsString] = struct{}{}
		}
	}
	return v1.ValidationSuccess()
}
