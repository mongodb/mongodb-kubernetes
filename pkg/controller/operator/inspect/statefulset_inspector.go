package inspect

import (
	"fmt"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// StatefulSetState is an entity encapsulating all the information about StatefulSet state
type StatefulSetState struct {
	statefulSetKey client.ObjectKey
	updated        int32
	ready          int32
	total          int32
}

// GetResourcesNotReadyStatus returns the status of dependent resources which have any problems
func (s StatefulSetState) GetResourcesNotReadyStatus() []mdbv1.ResourceNotReady {
	if s.IsReady() {
		return []mdbv1.ResourceNotReady{}
	}
	msg := fmt.Sprintf("Not all the Pods are ready (total: %d, updated: %d, ready: %d)", s.total, s.updated, s.ready)
	return []mdbv1.ResourceNotReady{{
		Kind:    mdbv1.StatefulsetKind,
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
	return s.updated == s.ready && s.ready == s.total
}

func StatefulSet(set appsv1.StatefulSet, stsName client.ObjectKey) StatefulSetState {
	state := StatefulSetState{
		statefulSetKey: stsName,
		updated:        set.Status.UpdatedReplicas,
		ready:          set.Status.ReadyReplicas,
		total:          *set.Spec.Replicas,
	}
	return state
}
