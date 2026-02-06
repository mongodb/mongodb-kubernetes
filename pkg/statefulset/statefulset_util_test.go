package statefulset

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
)

func TestIsStatefulSetUpdatableTo(t *testing.T) {
	tests := []struct {
		name     string
		existing v1.StatefulSet
		desired  v1.StatefulSet
		want     bool
	}{
		{
			name:     "empty",
			existing: v1.StatefulSet{},
			desired:  v1.StatefulSet{},
			want:     true,
		},
		{
			name: "TypeMeta: unequal",
			existing: v1.StatefulSet{
				TypeMeta: metav1.TypeMeta{
					Kind: "123",
				},
			},
			desired: v1.StatefulSet{
				TypeMeta: metav1.TypeMeta{
					Kind: "something else",
				},
			},
			want: true,
		},
		{
			name: "Selector: unequal",
			existing: v1.StatefulSet{
				Spec: v1.StatefulSetSpec{
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"a": "b"},
					},
				},
			},
			desired: v1.StatefulSet{
				Spec: v1.StatefulSetSpec{
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"c": "d"},
					},
				},
			},
			want: false,
		},
		{
			name: "Selector: equal nil and empty",
			existing: v1.StatefulSet{
				Spec: v1.StatefulSetSpec{
					Selector: &metav1.LabelSelector{
						MatchExpressions: nil,
						MatchLabels:      nil,
					},
				},
			},
			desired: v1.StatefulSet{
				Spec: v1.StatefulSetSpec{
					Selector: &metav1.LabelSelector{
						MatchExpressions: []metav1.LabelSelectorRequirement{},
						MatchLabels:      map[string]string{},
					},
				},
			},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equalf(t, tt.want, isStatefulSetEqualOnForbiddenFields(tt.existing, tt.desired), "isStatefulSetEqualOnForbiddenFields(%v, %v)", tt.existing, tt.desired)
		})
	}
}

func TestIsVolumeClaimUpdatableTo(t *testing.T) {
	tests := []struct {
		name     string
		existing corev1.PersistentVolumeClaim
		desired  corev1.PersistentVolumeClaim
		want     bool
	}{
		{
			name:     "empty",
			existing: corev1.PersistentVolumeClaim{},
			desired:  corev1.PersistentVolumeClaim{},
			want:     true,
		},
		{
			name: "TypeMeta: unequal",
			existing: corev1.PersistentVolumeClaim{
				TypeMeta: metav1.TypeMeta{
					Kind: "123",
				},
			},
			desired: corev1.PersistentVolumeClaim{
				TypeMeta: metav1.TypeMeta{
					Kind: "abc",
				},
			},
			want: true,
		},
		{
			name: "AccessModes: equal nil and empty",
			existing: corev1.PersistentVolumeClaim{
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes: nil,
				},
			},
			desired: corev1.PersistentVolumeClaim{
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{},
				},
			},
			want: true,
		},
		{
			name: "AccessModes: unequal",
			existing: corev1.PersistentVolumeClaim{
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{"a"},
				},
			},
			desired: corev1.PersistentVolumeClaim{
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{"b"},
				},
			},
			want: false,
		},
		{
			name: "Storage: unequal",
			existing: corev1.PersistentVolumeClaim{
				Spec: corev1.PersistentVolumeClaimSpec{
					Resources: corev1.VolumeResourceRequirements{
						Requests: map[corev1.ResourceName]resource.Quantity{corev1.ResourceStorage: resource.MustParse("1Gi")},
					},
				},
			},
			desired: corev1.PersistentVolumeClaim{
				Spec: corev1.PersistentVolumeClaimSpec{
					Resources: corev1.VolumeResourceRequirements{
						Requests: map[corev1.ResourceName]resource.Quantity{corev1.ResourceStorage: resource.MustParse("2Gi")},
					},
				},
			},
			want: false,
		},
		{
			name: "Selector: equal nil and empty",
			existing: corev1.PersistentVolumeClaim{
				Spec: corev1.PersistentVolumeClaimSpec{
					Selector: &metav1.LabelSelector{
						MatchExpressions: nil,
						MatchLabels:      nil,
					},
				},
			},
			desired: corev1.PersistentVolumeClaim{
				Spec: corev1.PersistentVolumeClaimSpec{
					Selector: &metav1.LabelSelector{
						MatchExpressions: []metav1.LabelSelectorRequirement{},
						MatchLabels:      map[string]string{},
					},
				},
			},
			want: true,
		},
		{
			// CLOUDP-275888
			name: "Storage: fractional value that needs canonicalizing",
			existing: corev1.PersistentVolumeClaim{
				Spec: corev1.PersistentVolumeClaimSpec{
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("966367641600m")},
					},
				},
			},
			desired: corev1.PersistentVolumeClaim{
				Spec: corev1.PersistentVolumeClaimSpec{
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("0.9Gi")},
					},
				},
			},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equalf(t, tt.want, isVolumeClaimEqualOnForbiddenFields(tt.existing, tt.desired), "isVolumeClaimEqualOnForbiddenFields(%v, %v)", tt.existing, tt.desired)
		})
	}
}

func TestGetStatefulSetStatus(t *testing.T) {
	ctx := context.Background()
	namespace := "test-namespace"
	stsName := "test-statefulset"

	t.Run("StatefulSet exists and is ready with matching expectedGeneration", func(t *testing.T) {
		// Create a ready StatefulSet with generation 1
		sts := createStatefulSet(stsName, namespace, 1, 1, 3, 3, 3)
		fakeClient := createFakeClientWithoutInterceptor(sts)

		// Check status with expectedGeneration = 1
		status := GetStatefulSetStatus(ctx, namespace, stsName, 1, fakeClient)

		assert.True(t, status.IsOK(), "Status should be OK when StatefulSet is ready and generation matches")
	})

	t.Run("StatefulSet exists but not all replicas are ready", func(t *testing.T) {
		// Create a StatefulSet where not all replicas are ready
		sts := createStatefulSet(stsName, namespace, 1, 1, 3, 2, 2)
		fakeClient := createFakeClientWithoutInterceptor(sts)

		status := GetStatefulSetStatus(ctx, namespace, stsName, 1, fakeClient)

		assert.False(t, status.IsOK(), "Status should not be OK when not all replicas are ready")
	})

	t.Run("StatefulSet with mismatched replicas - should not be ready", func(t *testing.T) {
		// Create a StatefulSet where updated replicas don't match ready replicas
		sts := createStatefulSet(stsName, namespace, 1, 1, 3, 2, 3)
		fakeClient := createFakeClientWithoutInterceptor(sts)

		status := GetStatefulSetStatus(ctx, namespace, stsName, 1, fakeClient)

		assert.False(t, status.IsOK(), "Status should not be OK when replicas don't all match")
	})

	t.Run("StatefulSet was updated, but it does not happen immediately", func(t *testing.T) {
		// Create initial StatefulSet with generation 1
		sts := createStatefulSet(stsName, namespace, 1, 1, 3, 3, 3)
		fakeClient := createFakeClientWithoutInterceptor(sts)

		// Modify spec to trigger generation increment (in real K8s, the API server would do this during an update)
		currentSts, err := fakeClient.GetStatefulSet(ctx, client.ObjectKey{Namespace: namespace, Name: stsName})
		require.NoError(t, err)
		currentSts.Generation = 2
		_, err = fakeClient.UpdateStatefulSet(ctx, currentSts)
		require.NoError(t, err)

		// Get the updated StatefulSet to have the correct resource version for status update
		currentSts, err = fakeClient.GetStatefulSet(ctx, client.ObjectKey{Namespace: namespace, Name: stsName})
		require.NoError(t, err)

		// Check status with expectedGeneration = 2 - should fail since observedGeneration is still 1
		status := GetStatefulSetStatus(ctx, namespace, stsName, 2, fakeClient)
		assert.False(t, status.IsOK(), "Status is not OK when expectedGeneration is not yet incremented")

		// Now update the status to reflect that the update has been observed
		currentSts.Status.ObservedGeneration = 2
		err = fakeClient.Status().Update(ctx, &currentSts)
		require.NoError(t, err)

		// Check status with expectedGeneration = 2 - should succeed
		status = GetStatefulSetStatus(ctx, namespace, stsName, 2, fakeClient)
		assert.True(t, status.IsOK(), "Status should be OK when StatefulSet is updated to generation 2 and expectedGeneration is 2")
	})
}

// createStatefulSet creates a StatefulSet with specific generation and status for testing purposes
func createStatefulSet(name, namespace string, generation int64, observedGeneration int64, replicas int32, readyReplicas int32, updatedReplicas int32) *v1.StatefulSet {
	return &v1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  namespace,
			Generation: generation,
		},
		Spec: v1.StatefulSetSpec{
			Replicas: ptr.To(replicas),
		},
		Status: v1.StatefulSetStatus{
			ObservedGeneration: observedGeneration,
			Replicas:           replicas,
			ReadyReplicas:      readyReplicas,
			UpdatedReplicas:    updatedReplicas,
		},
	}
}

// createFakeClientWithoutInterceptor creates a fake client without the auto-ready interceptor for testing
func createFakeClientWithoutInterceptor(objects ...client.Object) kubernetesClient.Client {
	builder := mock.NewEmptyFakeClientBuilder()
	if len(objects) > 0 {
		builder.WithObjects(objects...)
	}
	return kubernetesClient.NewClient(builder.Build())
}
