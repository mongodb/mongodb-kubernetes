package util

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func getDefaultContainer() corev1.Container {
	return corev1.Container{
		Name:  "container-0",
		Image: "image-0",
		VolumeMounts: []corev1.VolumeMount{
			{
				Name: "container-0.volume-mount-0",
			},
		},
	}
}

func getCustomContainer() corev1.Container {
	return corev1.Container{
		Name:  "container-1",
		Image: "image-1",
	}
}

func TestCreateContainerMap(t *testing.T) {
	defaultContainer := getDefaultContainer()
	customContainer := getCustomContainer()
	result := createContainerMap([]corev1.Container{defaultContainer, customContainer})
	assert.Len(t, result, 2)
	assert.Equal(t, defaultContainer, result["container-0"])
	assert.Equal(t, customContainer, result["container-1"])
}

func TestMergeVolumeMounts(t *testing.T) {
	vol0 := corev1.VolumeMount{Name: "container-0.volume-mount-0"}
	vol1 := corev1.VolumeMount{Name: "another-mount"}
	volumeMounts := []corev1.VolumeMount{vol0, vol1}
	var mergedVolumeMounts []corev1.VolumeMount
	var err error

	mergedVolumeMounts, err = mergeVolumeMounts(nil, volumeMounts)
	assert.NoError(t, err)
	assert.Equal(t, []corev1.VolumeMount{vol0, vol1}, mergedVolumeMounts)

	vol2 := vol1
	vol2.MountPath = "/somewhere"
	mergedVolumeMounts, err = mergeVolumeMounts([]corev1.VolumeMount{vol2}, []corev1.VolumeMount{vol0, vol1})
	assert.NoError(t, err)
	assert.Equal(t, []corev1.VolumeMount{vol0, vol2}, mergedVolumeMounts)
}

func TestMergeContainer(t *testing.T) {
	vol0 := corev1.VolumeMount{Name: "container-0.volume-mount-0"}
	sideCarVol := corev1.VolumeMount{Name: "container-1.volume-mount-0"}

	anotherVol := corev1.VolumeMount{Name: "another-mount"}

	overrideDefaultContainer := getDefaultContainer()
	overrideDefaultContainer.Image = "overridden"

	otherDefaultContainer := getDefaultContainer()
	otherDefaultContainer.Name = "default-side-car"
	otherDefaultContainer.VolumeMounts = []corev1.VolumeMount{sideCarVol}

	overrideOtherDefaultContainer := otherDefaultContainer
	overrideOtherDefaultContainer.Env = []corev1.EnvVar{{Name: "env_var", Value: "xxx"}}
	overrideOtherDefaultContainer.VolumeMounts = []corev1.VolumeMount{anotherVol}

	mergedContainers, err := mergeContainers(
		[]corev1.Container{getCustomContainer(), overrideDefaultContainer, overrideOtherDefaultContainer},
		[]corev1.Container{getDefaultContainer(), otherDefaultContainer},
	)
	assert.NoError(t, err)
	assert.Len(t, mergedContainers, 3)

	assert.Equal(t, getCustomContainer(), mergedContainers[2])

	mergedDefaultContainer := mergedContainers[0]
	assert.Equal(t, "container-0", mergedDefaultContainer.Name)
	assert.Equal(t, []corev1.VolumeMount{vol0}, mergedDefaultContainer.VolumeMounts)
	assert.Equal(t, "overridden", mergedDefaultContainer.Image)

	mergedOtherContainer := mergedContainers[1]
	assert.Equal(t, "default-side-car", mergedOtherContainer.Name)
	assert.Equal(t, []corev1.VolumeMount{sideCarVol, anotherVol}, mergedOtherContainer.VolumeMounts)
	assert.Len(t, mergedOtherContainer.Env, 1)
	assert.Equal(t, "env_var", mergedOtherContainer.Env[0].Name)
	assert.Equal(t, "xxx", mergedOtherContainer.Env[0].Value)
}

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
			Containers:                    []corev1.Container{getDefaultContainer()},
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
			Containers:                    []corev1.Container{getCustomContainer()},
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
