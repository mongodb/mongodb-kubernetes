package statefulset

import (
	"fmt"
	"reflect"

	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/statefulset"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
)

// isVolumeClaimUpdatableTo takes two sts' PVC and returns wether we are allowed to update the first one to the second one.
func isVolumeClaimUpdatableTo(existing, desired corev1.PersistentVolumeClaim) bool {

	oldSpec := existing.Spec
	newSpec := desired.Spec

	if !reflect.DeepEqual(oldSpec.AccessModes, newSpec.AccessModes) {
		return false
	}

	if newSpec.Selector != nil && !reflect.DeepEqual(oldSpec.Selector, newSpec.Selector) {
		return false
	}

	if !reflect.DeepEqual(oldSpec.Resources, newSpec.Resources) {
		return false
	}

	if newSpec.VolumeName != "" && newSpec.VolumeName != oldSpec.VolumeName {
		return false
	}

	if newSpec.StorageClassName != nil && !reflect.DeepEqual(oldSpec.StorageClassName, newSpec.StorageClassName) {
		return false

	}

	if newSpec.VolumeMode != nil && !reflect.DeepEqual(newSpec.VolumeMode, oldSpec.VolumeMode) {
		return false
	}

	if newSpec.DataSource != nil && !reflect.DeepEqual(newSpec.DataSource, oldSpec.DataSource) {
		return false
	}

	return true
}

// isStatefulSetUpdatableTo takes two statefulsts and returns wether we are allowed to update the first one to the second one.
func isStatefulSetUpdatableTo(existing, desired appsv1.StatefulSet) bool {
	selectorsEqual := desired.Spec.Selector == nil || reflect.DeepEqual(existing.Spec.Selector, desired.Spec.Selector)
	serviceNamesEqual := existing.Spec.ServiceName == desired.Spec.ServiceName
	podMgmtEqual := desired.Spec.PodManagementPolicy == "" || desired.Spec.PodManagementPolicy == existing.Spec.PodManagementPolicy
	revHistoryLimitEqual := desired.Spec.RevisionHistoryLimit == nil || reflect.DeepEqual(desired.Spec.RevisionHistoryLimit, existing.Spec.RevisionHistoryLimit)

	if len(existing.Spec.VolumeClaimTemplates) != len(desired.Spec.VolumeClaimTemplates) {
		return false
	}

	// VolumeClaimTemplates must be checked one-by-one, to deal with empty string, nil pointers
	for index, existingClaim := range existing.Spec.VolumeClaimTemplates {
		if !isVolumeClaimUpdatableTo(existingClaim, desired.Spec.VolumeClaimTemplates[index]) {
			return false
		}
	}

	return selectorsEqual && serviceNamesEqual && podMgmtEqual && revHistoryLimitEqual
}

// StatefulSetCantBeUpdatedError is returned when we are trying to update immutable fields on a sts.
type StatefulSetCantBeUpdatedError struct {
	msg string
}

func (s StatefulSetCantBeUpdatedError) Error() string {
	return s.msg
}

// CreateOrUpdateStatefulset will create or update a StatefulSet in Kubernetes.
//
// The method has to be flexible (create/update) as there are cases when custom resource is created but statefulset - not
// Service named "serviceName" is created optionally (it may already exist - created by either user or by operator before)
// Note the logic for "exposeExternally" parameter: if it is true then the second service is created of type "NodePort"
// (the random port will be allocated by Kubernetes) otherwise only one service of type "ClusterIP" is created and it
// won't be connectible from external (unless pods in statefulset expose themselves to outside using "hostNetwork: true")
// Function returns the service port number assigned
func CreateOrUpdateStatefulset(getUpdateCreator statefulset.GetUpdateCreator, ns string, log *zap.SugaredLogger, statefulSetToCreate *appsv1.StatefulSet) (*appsv1.StatefulSet, error) {
	log = log.With("statefulset", kube.ObjectKey(ns, statefulSetToCreate.Name))
	existingStatefulSet, err := getUpdateCreator.GetStatefulSet(kube.ObjectKey(ns, statefulSetToCreate.Name))
	if err != nil {
		if apiErrors.IsNotFound(err) {
			if err = getUpdateCreator.CreateStatefulSet(*statefulSetToCreate); err != nil {
				return nil, err
			}
		} else {
			return nil, err
		}
		log.Debug("Created StatefulSet")
		return statefulSetToCreate, nil
	}

	// preserve existing certificate hash if new one is not statefulSetToCreate
	existingCertHash, okExisting := existingStatefulSet.Spec.Template.Annotations["certHash"]
	newCertHash, okNew := statefulSetToCreate.Spec.Template.Annotations["certHash"]
	if existingCertHash != "" && newCertHash == "" && okExisting && okNew {
		statefulSetToCreate.Spec.Template.Annotations["certHash"] = existingCertHash
	}

	log.Debug("Checking if we can update the current statefulset")
	if !isStatefulSetUpdatableTo(existingStatefulSet, *statefulSetToCreate) {
		log.Debug("Can't update the stateful set")
		return nil, StatefulSetCantBeUpdatedError{
			msg: "can't execute update on forbidden fields",
		}
	}

	updatedSts, err := getUpdateCreator.UpdateStatefulSet(*statefulSetToCreate)
	if err != nil {
		return nil, err
	}
	return &updatedSts, nil
}

// func GetFilePathFromAnnotationOrDefault returns a concatennation of a default path and an annotation, or a default value
// if the annotation is not present.
func GetFilePathFromAnnotationOrDefault(sts appsv1.StatefulSet, key string, path string, defaultValue string) string {
	val, ok := sts.Annotations[key]

	if ok {
		return fmt.Sprintf("%s/%s", path, val)
	}

	return defaultValue
}
