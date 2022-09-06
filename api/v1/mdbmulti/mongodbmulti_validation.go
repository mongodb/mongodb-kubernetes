package mdbmulti

import (
	"errors"
	"fmt"

	v1 "github.com/10gen/ops-manager-kubernetes/api/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/multicluster"
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
	validators := []func(ms MongoDBMultiSpec) v1.ValidationResult{
		validateUniqueClusterNames,
		validateSpecifiedClusterNames,
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
func validateUniqueClusterNames(ms MongoDBMultiSpec) v1.ValidationResult {
	present := make(map[string]struct{})

	for _, e := range ms.ClusterSpecList.ClusterSpecs {
		if _, ok := present[e.ClusterName]; ok {
			msg := fmt.Sprintf("Multiple clusters with the same name(%s) are not allowed", e.ClusterName)
			return v1.ValidationError(msg)
		}
		present[e.ClusterName] = struct{}{}
	}
	return v1.ValidationSuccess()
}

func validateSpecifiedClusterNames(ms MongoDBMultiSpec) v1.ValidationResult {
	kubeConfigFile, err := multicluster.NewKubeConfigFile()
	if err != nil {
		return v1.ValidationError(fmt.Sprintf("failed to open kubeconfig file: %s, err: %s", multicluster.KubeConfigPath, err))
	}

	kubeConfig, err := kubeConfigFile.LoadKubeConfigFile()
	if err != nil {
		msg := fmt.Sprintf("Couldn't load kubeconfig file from the path: %s", multicluster.KubeConfigPath)
		return v1.ValidationError(msg)
	}

	clusters := make(map[string]struct{})
	for _, context := range kubeConfig.Contexts {
		clusters[context.Context.Cluster] = struct{}{}
	}

	for _, cluster := range ms.ClusterSpecList.ClusterSpecs {
		if _, ok := clusters[cluster.ClusterName]; !ok {
			return v1.ValidationError("Cluster %s credentials is not specified in Kubeconfig", cluster.ClusterName)
		}
	}

	return v1.ValidationSuccess()
}
