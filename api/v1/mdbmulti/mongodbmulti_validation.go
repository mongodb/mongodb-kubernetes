package mdbmulti

import (
	"errors"
	"fmt"

	"github.com/10gen/ops-manager-kubernetes/api/v1/status"

	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	v1 "github.com/10gen/ops-manager-kubernetes/api/v1"
	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	runtime "k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

var _ webhook.Validator = &MongoDBMultiCluster{}

func (m *MongoDBMultiCluster) ValidateCreate() (admission.Warnings, error) {
	return nil, m.ProcessValidationsOnReconcile(nil)
}

func (m *MongoDBMultiCluster) ValidateUpdate(old runtime.Object) (admission.Warnings, error) {
	return nil, m.ProcessValidationsOnReconcile(old.(*MongoDBMultiCluster))
}

func (m *MongoDBMultiCluster) ValidateDelete() (admission.Warnings, error) {
	return nil, nil
}

func (m *MongoDBMultiCluster) ProcessValidationsOnReconcile(old *MongoDBMultiCluster) error {
	for _, res := range m.RunValidations(old) {
		if res.Level == v1.ErrorLevel {
			return errors.New(res.Msg)
		}
		if res.Level == v1.WarningLevel {
			m.AddWarningIfNotExists(status.Warning(res.Msg))
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

// validateUniqueExternalDomains validates uniqueness of the domains if they are provided.
// External domain might be specified at the top level in spec.externalAccess.externalDomain or in every member cluster.
// We make sure that if external domains are used, every member cluster has unique external domain defined.
func validateUniqueExternalDomains(ms MongoDBMultiSpec) v1.ValidationResult {
	externalDomains := make(map[string]string)

	for _, e := range ms.ClusterSpecList {
		if externalDomain := ms.GetExternalDomainForMemberCluster(e.ClusterName); externalDomain != nil {
			externalDomains[e.ClusterName] = *externalDomain
		}
	}

	// We don't need to validate external domains if there aren't any specified.
	// We don't have any flag that enables usage of external domains. We use them if they are provided.
	if len(externalDomains) == 0 {
		return v1.ValidationSuccess()
	}

	present := map[string]struct{}{}
	for _, e := range ms.ClusterSpecList {
		externalDomain, ok := externalDomains[e.ClusterName]
		if !ok {
			return v1.ValidationError("The externalDomain is not set for member cluster: %s", e.ClusterName)
		}

		if _, ok := present[externalDomain]; ok {
			msg := fmt.Sprintf("Multiple member clusters with the same externalDomain (%s) are not allowed. "+
				"Check if all spec.clusterSpecList[*].externalAccess.externalDomain fields are defined and are unique.", externalDomain)
			return v1.ValidationError(msg)
		}
		present[externalDomain] = struct{}{}
	}
	return v1.ValidationSuccess()
}

func (m *MongoDBMultiCluster) AddWarningIfNotExists(warning status.Warning) {
	m.Status.Warnings = status.Warnings(m.Status.Warnings).AddIfNotExists(warning)
}
