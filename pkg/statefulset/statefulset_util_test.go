package statefulset

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/api/resource"

	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
