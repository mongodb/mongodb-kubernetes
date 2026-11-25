package deployment

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
)

func TestGetDeploymentStatusRequiresCompleteRollout(t *testing.T) {
	tests := []struct {
		name   string
		status appsv1.DeploymentStatus
		phase  status.Phase
	}{
		{
			name: "all desired replicas are available",
			status: appsv1.DeploymentStatus{
				Replicas:           1,
				UpdatedReplicas:    1,
				ReadyReplicas:      1,
				AvailableReplicas:  1,
				ObservedGeneration: 2,
			},
			phase: status.PhaseRunning,
		},
		{
			name: "old ready replica remains while replacement is unavailable",
			status: appsv1.DeploymentStatus{
				Replicas:           2,
				UpdatedReplicas:    1,
				ReadyReplicas:      1,
				AvailableReplicas:  1,
				ObservedGeneration: 2,
			},
			phase: status.PhasePending,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, appsv1.AddToScheme(scheme))
			replicas := int32(1)
			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "router",
					Namespace:  "test",
					Generation: 2,
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: &replicas,
				},
				Status: tt.status,
			}
			kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(deployment).Build()

			result := GetDeploymentStatus(context.Background(), "test", "router", 2, kubeClient)

			require.Equal(t, tt.phase, result.Phase())
		})
	}
}
