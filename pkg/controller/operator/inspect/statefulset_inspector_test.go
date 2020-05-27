package inspect

import (
	"testing"

	"github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/status"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestStatefulSetInspector(t *testing.T) {

	statefulSet := appsv1.StatefulSet{
		Spec: appsv1.StatefulSetSpec{
			Replicas: util.Int32Ref(3),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sts",
			Namespace: "ns",
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
	assert.Equal(t, state.GetResourcesNotReadyStatus()[0].Kind, status.StatefulsetKind)
	assert.Equal(t, state.GetResourcesNotReadyStatus()[0].Name, "sts")

	// StatefulSet "got" to ready state
	statefulSet.Status.UpdatedReplicas = 3
	statefulSet.Status.ReadyReplicas = 3

	state = StatefulSet(statefulSet)
	assert.True(t, state.IsReady())
	assert.Len(t, state.GetResourcesNotReadyStatus(), 0)

}
