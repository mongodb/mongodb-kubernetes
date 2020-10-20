package inspect

import (
	"fmt"

	"go.uber.org/zap"

	"github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/status"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// StatefulSetState is an entity encapsulating all the information about StatefulSet state
type StatefulSetState struct {
	statefulSetKey     client.ObjectKey
	updated            int32
	ready              int32
	total              int32
	generation         int64
	observedGeneration int64
}

// GetResourcesNotReadyStatus returns the status of dependent resources which have any problems
func (s StatefulSetState) GetResourcesNotReadyStatus() []status.ResourceNotReady {
	if s.IsReady() {
		return []status.ResourceNotReady{}
	}
	msg := fmt.Sprintf("Not all the Pods are ready (total: %d, updated: %d, ready: %d)", s.total, s.updated, s.ready)
	return []status.ResourceNotReady{{
		Kind:    status.StatefulsetKind,
		Name:    s.statefulSetKey.Name,
		Message: msg,
	}}
}

// GetMessage returns the general message to be shown in status or/and printed in logs
func (s StatefulSetState) GetMessage() string {
	if s.IsReady() {
		return fmt.Sprintf("StatefulSet %s is ready", s.statefulSetKey)
	}
	return fmt.Sprintf("StatefulSet not ready")
}

func (s StatefulSetState) IsReady() bool {
	zap.S().Debugf("StatefulSet %s (total: %d, ready: %d, updated: %d, generation: %d, observedGeneration: %d)", s.statefulSetKey.Name, s.total, s.ready, s.updated, s.generation, s.observedGeneration)
	return s.updated == s.ready && s.ready == s.total && s.observedGeneration == s.generation
}

func StatefulSet(set appsv1.StatefulSet) StatefulSetState {
	state := StatefulSetState{
		statefulSetKey:     types.NamespacedName{Namespace: set.Namespace, Name: set.Name},
		updated:            set.Status.UpdatedReplicas,
		ready:              set.Status.ReadyReplicas,
		total:              *set.Spec.Replicas,
		observedGeneration: set.Status.ObservedGeneration,
		generation:         set.Generation,
	}
	return state
}
