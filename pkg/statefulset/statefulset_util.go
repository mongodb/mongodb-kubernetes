package statefulset

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	gocmp "github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiEquality "k8s.io/apimachinery/pkg/api/equality"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/mongodb/mongodb-kubernetes/controllers/operator/inspect"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
)

const PVCSizeAnnotation = "mongodb.com/storageSize"

// isVolumeClaimEqualOnForbiddenFields takes two sts PVCs
// and returns whether we are allowed to update the first one to the second one.
func isVolumeClaimEqualOnForbiddenFields(existing, desired corev1.PersistentVolumeClaim) bool {
	oldSpec := existing.Spec
	newSpec := desired.Spec

	if !gocmp.Equal(oldSpec.AccessModes, newSpec.AccessModes, cmpopts.EquateEmpty()) {
		return false
	}

	if newSpec.Selector != nil && !gocmp.Equal(oldSpec.Selector, newSpec.Selector, cmpopts.EquateEmpty()) {
		return false
	}

	// using api-machinery here for semantic equality
	if !apiEquality.Semantic.DeepEqual(oldSpec.Resources, newSpec.Resources) {
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

// isStatefulSetEqualOnForbiddenFields takes two statefulsets
// and returns whether we are allowed to update the first one to the second one.
// This is decided on equality on forbidden fields.
func isStatefulSetEqualOnForbiddenFields(existing, desired appsv1.StatefulSet) bool {
	// We are using cmp equal on purpose to enforce equality between nil and []
	selectorsEqual := desired.Spec.Selector == nil || gocmp.Equal(existing.Spec.Selector, desired.Spec.Selector, cmpopts.EquateEmpty())
	serviceNamesEqual := existing.Spec.ServiceName == desired.Spec.ServiceName
	podMgmtEqual := desired.Spec.PodManagementPolicy == "" || desired.Spec.PodManagementPolicy == existing.Spec.PodManagementPolicy
	revHistoryLimitEqual := desired.Spec.RevisionHistoryLimit == nil || reflect.DeepEqual(desired.Spec.RevisionHistoryLimit, existing.Spec.RevisionHistoryLimit)

	if len(existing.Spec.VolumeClaimTemplates) != len(desired.Spec.VolumeClaimTemplates) {
		return false
	}

	// VolumeClaimTemplates must be checked one-by-one, to deal with empty string, nil pointers
	for index, existingClaim := range existing.Spec.VolumeClaimTemplates {
		if !isVolumeClaimEqualOnForbiddenFields(existingClaim, desired.Spec.VolumeClaimTemplates[index]) {
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
func CreateOrUpdateStatefulset(ctx context.Context, getUpdateCreator kubernetesClient.Client, ns string, log *zap.SugaredLogger, statefulSetToCreate *appsv1.StatefulSet) (*appsv1.StatefulSet, error) {
	log = log.With("statefulset", kube.ObjectKey(ns, statefulSetToCreate.Name))
	existingStatefulSet, err := getUpdateCreator.GetStatefulSet(ctx, kube.ObjectKey(ns, statefulSetToCreate.Name))
	if err != nil {
		if apiErrors.IsNotFound(err) {
			if err = getUpdateCreator.CreateStatefulSet(ctx, *statefulSetToCreate); err != nil {
				return nil, err
			}
		} else {
			return nil, err
		}
		log.Debug("Created StatefulSet")
		return statefulSetToCreate, nil
	}

	// there already exists a pvc size annotation, that means we did resize at least once
	// we need to make sure to keep the annotation.
	if _, okExisting := existingStatefulSet.Spec.Template.Annotations[PVCSizeAnnotation]; okExisting {
		if err := AddPVCAnnotation(statefulSetToCreate); err != nil {
			return nil, err
		}
	}

	log.Debug("Checking if we can update the current statefulset")
	if !isStatefulSetEqualOnForbiddenFields(existingStatefulSet, *statefulSetToCreate) {
		// Running into this code means we have updated sts fields which are not allowed to be changed.
		log.Debug("Can't update the stateful set")
		return nil, StatefulSetCantBeUpdatedError{
			msg: "can't execute update on forbidden fields",
		}
	}

	updatedSts, err := getUpdateCreator.UpdateStatefulSet(ctx, *statefulSetToCreate)
	if err != nil {
		return nil, err
	}

	return &updatedSts, nil
}

// AddPVCAnnotation adds pvc annotation to the template, this can either trigger a rolling restart
// if the template has changed is a noop for an already existing one.
func AddPVCAnnotation(statefulSetToCreate *appsv1.StatefulSet) error {
	type pvcSizes struct {
		Name string
		Size string
	}
	if statefulSetToCreate.Spec.Template.Annotations == nil {
		statefulSetToCreate.Spec.Template.Annotations = map[string]string{}
	}
	var p []pvcSizes
	for _, template := range statefulSetToCreate.Spec.VolumeClaimTemplates {
		p = append(p, pvcSizes{
			Name: template.Name,
			Size: template.Spec.Resources.Requests.Storage().String(),
		})
	}

	// ensure a strict order to not have unnecessary restarts
	slices.SortFunc(p, func(a, b pvcSizes) int {
		return cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})

	jsonString, err := json.Marshal(p)
	if err != nil {
		return err
	}
	statefulSetToCreate.Spec.Template.Annotations[PVCSizeAnnotation] = string(jsonString)
	return nil
}

// GetFilePathFromAnnotationOrDefault returns a concatenation of a default path and an annotation, or a default value
// if the annotation is not present.
func GetFilePathFromAnnotationOrDefault(sts appsv1.StatefulSet, key string, path string, defaultValue string) string {
	val, ok := sts.Annotations[key]

	if ok {
		return fmt.Sprintf("%s/%s", path, val)
	}

	return defaultValue
}

// GetStatefulSetStatus returns the workflow.Status based on the status of the
// If the StatefulSet is not ready the request will be retried in 3 seconds (instead of the default 10 seconds)
// allowing to reach "ready" status sooner
func GetStatefulSetStatus(ctx context.Context, namespace, name string, client kubernetesClient.Client) workflow.Status {
	set, err := client.GetStatefulSet(ctx, kube.ObjectKey(namespace, name))
	i := 0

	// Sometimes it is possible that the StatefulSet which has just been created
	// returns a not found error when getting it too soon afterwards.
	for apiErrors.IsNotFound(err) && i < 10 {
		i++
		zap.S().Debugf("StatefulSet was not found: %s, attempt %d", err, i)
		time.Sleep(time.Second * 1)
		set, err = client.GetStatefulSet(ctx, kube.ObjectKey(namespace, name))
	}

	if err != nil {
		return workflow.Failed(err)
	}

	if statefulSetState := inspect.StatefulSet(set); !statefulSetState.IsReady() {
		return workflow.Pending("%s", statefulSetState.GetMessage()).
			WithResourcesNotReady(statefulSetState.GetResourcesNotReadyStatus()).
			WithRetry(3)
	} else {
		zap.S().Debugf("StatefulSet %s/%s is ready on check attempt #%d, state: %+v: ", namespace, name, i, statefulSetState)
	}

	for i := int32(0); i < *set.Spec.Replicas; i++ {
		podName := fmt.Sprintf("%s-%d", set.Name, i)
		pod, err := client.GetPod(ctx, kube.ObjectKey(namespace, podName))
		for apiErrors.IsNotFound(err) && i < 10 {
			i++
			zap.S().Debugf("Pod was not found: %s, attempt %d", err, i)
			time.Sleep(time.Second * 1)
			pod, err = client.GetPod(ctx, kube.ObjectKey(namespace, podName))
		}

		if pod.DeletionTimestamp != nil {
			return workflow.Pending("Pod %s is being deleted", podName).WithRetry(10)
		}
	}

	return workflow.OK()
}

func GetOutdatedPods(ctx context.Context, namespace, name string, client kubernetesClient.Client) ([]string, error) {
	set, err := client.GetStatefulSet(ctx, kube.ObjectKey(namespace, name))
	if err != nil {
		return nil, err
	}

	var outdatedPods []string
	for i := int32(0); i < *set.Spec.Replicas; i++ {
		podName := fmt.Sprintf("%s-%d", set.Name, i)
		pod, err := client.GetPod(ctx, kube.ObjectKey(namespace, podName))
		if err != nil {
			return nil, err
		}

		if pod.Labels == nil || pod.Labels["controller-revision-hash"] != set.Status.UpdateRevision {
			outdatedPods = append(outdatedPods, podName)
		}
	}

	return outdatedPods, nil
}
