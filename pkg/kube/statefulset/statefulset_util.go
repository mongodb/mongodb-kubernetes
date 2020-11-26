package statefulset

import (
	"reflect"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/kube"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/statefulset"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
)

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
	existingCertHash := existingStatefulSet.Spec.Template.Annotations["certHash"]
	newCertHash := statefulSetToCreate.Spec.Template.Annotations["certHash"]
	if existingCertHash != "" && newCertHash == "" {
		statefulSetToCreate.Spec.Template.Annotations["certHash"] = existingCertHash
	}

	// If upgrading operator, it might happen that the spec.selector field
	// and spec.ServiceName have changed. This is not allowed and for now
	// We remove immutable fields from the update.
	// A decision on how to more gracefully handle this will be
	// taken in CLOUDP-76513
	areSelectorsEqual := reflect.DeepEqual(statefulSetToCreate.Spec.Selector, existingStatefulSet.Spec.Selector)
	areServiceNamesEqual := reflect.DeepEqual(statefulSetToCreate.Spec.ServiceName, existingStatefulSet.Spec.ServiceName)
	if !areSelectorsEqual || !areServiceNamesEqual {
		log.Warn("At least one immutable field in the StatefulSet has changed. Keeping the old one.")
		statefulSetToCreate.Spec.Selector = existingStatefulSet.Spec.Selector
		statefulSetToCreate.Spec.ServiceName = existingStatefulSet.Spec.ServiceName

		// This one is not immutable, but needs to match the matchlabels in the spec.selector
		statefulSetToCreate.Spec.Template.Labels = existingStatefulSet.Spec.Template.Labels
	}

	updatedSts, err := getUpdateCreator.UpdateStatefulSet(*statefulSetToCreate)
	if err != nil {
		return nil, err
	}
	return &updatedSts, nil
}
