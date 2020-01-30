package util

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func getDefaultPodSpec() corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-default-name",
			Namespace: "my-default-namespace",
		},
		Spec: corev1.PodSpec{
			NodeSelector: map[string]string{
				"node-0": "node-0",
			},
			ServiceAccountName:            "my-default-service-account",
			TerminationGracePeriodSeconds: Int64Ref(12),
			ActiveDeadlineSeconds:         Int64Ref(10),
			Containers: []corev1.Container{{
				Name:  "container-0",
				Image: "image-0",
				VolumeMounts: []corev1.VolumeMount{
					{
						Name: "container-0.volume-mount-0",
					},
				},
			}},
		},
	}
}

func getCustomPodSpec() corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			NodeSelector: map[string]string{
				"node-1": "node-1",
			},
			ServiceAccountName:            "my-service-account-override",
			TerminationGracePeriodSeconds: Int64Ref(11),
			NodeName:                      "my-node-name",
			RestartPolicy:                 corev1.RestartPolicy("Always"),
			Containers: []corev1.Container{{
				Name:  "container-1",
				Image: "image-1",
			}},
		},
	}
}

func TestMergePodSpecsEmptyCustom(t *testing.T) {

	defaultPodSpec := getDefaultPodSpec()
	customPodSpecTemplate := corev1.PodTemplateSpec{}

	mergedPodTemplateSpec, err := MergePodSpecs(customPodSpecTemplate, defaultPodSpec)

	assert.NoError(t, err)
	assert.Equal(t, "my-default-service-account", mergedPodTemplateSpec.Spec.ServiceAccountName)
	assert.Equal(t, Int64Ref(12), mergedPodTemplateSpec.Spec.TerminationGracePeriodSeconds)

	assert.Equal(t, "my-default-name", mergedPodTemplateSpec.ObjectMeta.Name)
	assert.Equal(t, "my-default-namespace", mergedPodTemplateSpec.ObjectMeta.Namespace)
	assert.Equal(t, Int64Ref(10), mergedPodTemplateSpec.Spec.ActiveDeadlineSeconds)

	// ensure collections have been merged
	assert.Contains(t, mergedPodTemplateSpec.Spec.NodeSelector, "node-0")
	assert.Len(t, mergedPodTemplateSpec.Spec.Containers, 1)
	assert.Equal(t, "container-0", mergedPodTemplateSpec.Spec.Containers[0].Name)
	assert.Equal(t, "image-0", mergedPodTemplateSpec.Spec.Containers[0].Image)
	assert.Equal(t, "container-0.volume-mount-0", mergedPodTemplateSpec.Spec.Containers[0].VolumeMounts[0].Name)
}

func TestMergePodSpecsEmptyDefault(t *testing.T) {

	defaultPodSpec := corev1.PodTemplateSpec{}
	customPodSpecTemplate := getCustomPodSpec()

	mergedPodTemplateSpec, err := MergePodSpecs(customPodSpecTemplate, defaultPodSpec)

	assert.NoError(t, err)
	assert.Equal(t, "my-service-account-override", mergedPodTemplateSpec.Spec.ServiceAccountName)
	assert.Equal(t, Int64Ref(11), mergedPodTemplateSpec.Spec.TerminationGracePeriodSeconds)
	assert.Equal(t, "my-node-name", mergedPodTemplateSpec.Spec.NodeName)
	assert.Equal(t, corev1.RestartPolicy("Always"), mergedPodTemplateSpec.Spec.RestartPolicy)

	assert.Len(t, mergedPodTemplateSpec.Spec.Containers, 1)
	assert.Equal(t, "container-1", mergedPodTemplateSpec.Spec.Containers[0].Name)
	assert.Equal(t, "image-1", mergedPodTemplateSpec.Spec.Containers[0].Image)

}

func TestMergePodSpecsBoth(t *testing.T) {

	defaultPodSpec := getDefaultPodSpec()
	customPodSpecTemplate := getCustomPodSpec()

	var mergedPodTemplateSpec corev1.PodTemplateSpec
	var err error

	// multiple merges must give the same result
	for i := 0; i < 3; i++ {
		mergedPodTemplateSpec, err = MergePodSpecs(customPodSpecTemplate, defaultPodSpec)

		assert.NoError(t, err)
		// ensure values that were specified in the custom pod spec template remain unchanged
		assert.Equal(t, "my-service-account-override", mergedPodTemplateSpec.Spec.ServiceAccountName)
		assert.Equal(t, Int64Ref(11), mergedPodTemplateSpec.Spec.TerminationGracePeriodSeconds)
		assert.Equal(t, "my-node-name", mergedPodTemplateSpec.Spec.NodeName)
		assert.Equal(t, corev1.RestartPolicy("Always"), mergedPodTemplateSpec.Spec.RestartPolicy)

		// ensure values from the default pod spec template have been merged in
		assert.Equal(t, "my-default-name", mergedPodTemplateSpec.ObjectMeta.Name)
		assert.Equal(t, "my-default-namespace", mergedPodTemplateSpec.ObjectMeta.Namespace)
		assert.Equal(t, Int64Ref(10), mergedPodTemplateSpec.Spec.ActiveDeadlineSeconds)

		// ensure collections have been merged
		assert.Contains(t, mergedPodTemplateSpec.Spec.NodeSelector, "node-0")
		assert.Contains(t, mergedPodTemplateSpec.Spec.NodeSelector, "node-1")
		assert.Len(t, mergedPodTemplateSpec.Spec.Containers, 2)
		assert.Equal(t, "container-0", mergedPodTemplateSpec.Spec.Containers[0].Name)
		assert.Equal(t, "image-0", mergedPodTemplateSpec.Spec.Containers[0].Image)
		assert.Equal(t, "container-0.volume-mount-0", mergedPodTemplateSpec.Spec.Containers[0].VolumeMounts[0].Name)
		assert.Equal(t, "container-1", mergedPodTemplateSpec.Spec.Containers[1].Name)
		assert.Equal(t, "image-1", mergedPodTemplateSpec.Spec.Containers[1].Image)
	}
}
