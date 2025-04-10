package inspect

import (
	"testing"

	"github.com/stretchr/testify/assert"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/10gen/ops-manager-kubernetes/api/v1/status"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
)

func TestStatefulSetInspector(t *testing.T) {
	statefulSet := appsv1.StatefulSet{
		Spec: appsv1.StatefulSetSpec{
			Replicas: util.Int32Ref(3),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:       "sts",
			Namespace:  "ns",
			Generation: 1,
		},
		Status: appsv1.StatefulSetStatus{
			Replicas:        3,
			ReadyReplicas:   1,
			UpdatedReplicas: 2,
		},
	}

	state := StatefulSet(statefulSet)
	assert.False(t, state.IsReady())
	assert.Len(t, state.GetResourcesNotReadyStatus(), 1)
	assert.Contains(t, state.GetResourcesNotReadyStatus()[0].Message, "Not all the Pods are ready")
	assert.Contains(t, state.GetMessage(), "not ready")
	assert.Equal(t, state.GetResourcesNotReadyStatus()[0].Kind, status.StatefulsetKind)
	assert.Equal(t, state.GetResourcesNotReadyStatus()[0].Name, "sts")

	// StatefulSet "got" to ready state
	statefulSet.Status.UpdatedReplicas = 3
	statefulSet.Status.ReadyReplicas = 3
	statefulSet.Status.ObservedGeneration = 1

	state = StatefulSet(statefulSet)
	assert.True(t, state.IsReady())
	assert.Contains(t, state.GetMessage(), "is ready")
	assert.Len(t, state.GetResourcesNotReadyStatus(), 0)

	// We "scale" the StatefulSet
	// Even though every other properties are the same, we need Spec.Replicas to be equal to Status.Replicas to be ready
	statefulSet.Spec.Replicas = util.Int32Ref(5)

	state = StatefulSet(statefulSet)
	assert.False(t, state.IsReady())
	assert.Contains(t, state.GetResourcesNotReadyStatus()[0].Message, "Not all the Pods are ready")
}
