package inspect

import (
	"fmt"

	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1 "k8s.io/api/apps/v1"

	"github.com/10gen/ops-manager-kubernetes/api/v1/status"
)

// StatefulSetState is an entity encapsulating all the information about StatefulSet state
type StatefulSetState struct {
	statefulSetKey     client.ObjectKey
	updated            int32
	ready              int32
	current            int32
	wanted             int32
	generation         int64
	observedGeneration int64
	updateStrategyType appsv1.StatefulSetUpdateStrategyType
}

// GetResourcesNotReadyStatus returns the status of dependent resources which have any problems
func (s StatefulSetState) GetResourcesNotReadyStatus() []status.ResourceNotReady {
	if s.IsReady() {
		return []status.ResourceNotReady{}
	}
	zap.S().Debugf("StatefulSet %s (wanted: %d, ready: %d, updated: %d, generation: %d, observedGeneration: %d)", s.statefulSetKey.Name, s.wanted, s.ready, s.updated, s.generation, s.observedGeneration)
	msg := fmt.Sprintf("Not all the Pods are ready (wanted: %d, updated: %d, ready: %d, current: %d)", s.wanted, s.updated, s.ready, s.current)
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
	return "StatefulSet not ready"
}

func (s StatefulSetState) IsReady() bool {
	isReady := s.updated == s.ready &&
		s.ready == s.wanted &&
		s.observedGeneration == s.generation &&
		s.current == s.wanted
	return isReady || s.updateStrategyType == appsv1.OnDeleteStatefulSetStrategyType
}

func StatefulSet(set appsv1.StatefulSet) StatefulSetState {
	state := StatefulSetState{
		statefulSetKey:     types.NamespacedName{Namespace: set.Namespace, Name: set.Name},
		updated:            set.Status.UpdatedReplicas,
		ready:              set.Status.ReadyReplicas,
		current:            set.Status.Replicas,
		wanted:             *set.Spec.Replicas,
		observedGeneration: set.Status.ObservedGeneration,
		generation:         set.Generation,
		updateStrategyType: set.Spec.UpdateStrategy.Type,
	}
	return state
}
