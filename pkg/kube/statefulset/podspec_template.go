package statefulset

import (
	"github.com/imdario/mergo"
	corev1 "k8s.io/api/core/v1"
)

// MergePodSpecs takes all of the values that exist in defaultPodTemplateSpec, and merges them into
// customPodTemplateSpec. Values that exist in both will not be touched.
func MergePodSpecs(customPodTemplateSpec corev1.PodTemplateSpec, defaultPodTemplateSpec corev1.PodTemplateSpec) (corev1.PodTemplateSpec, error) {
	var err error
	mergedContainers, err := mergeContainers(customPodTemplateSpec.Spec.Containers, defaultPodTemplateSpec.Spec.Containers)
	mergedInitContainers, err := mergeContainers(customPodTemplateSpec.Spec.InitContainers, defaultPodTemplateSpec.Spec.InitContainers)
	if err != nil {
		return corev1.PodTemplateSpec{}, err
	}
	mergedPodTemplateSpec := corev1.PodTemplateSpec{}
	if err = mergo.Merge(&mergedPodTemplateSpec, defaultPodTemplateSpec); err != nil {
		return corev1.PodTemplateSpec{}, err
	}
	if err = mergo.Merge(&mergedPodTemplateSpec, customPodTemplateSpec, mergo.WithOverride, mergo.WithAppendSlice); err != nil {
		return corev1.PodTemplateSpec{}, err
	}
	mergedPodTemplateSpec.Spec.Containers = mergedContainers
	mergedPodTemplateSpec.Spec.InitContainers = mergedInitContainers
	return mergedPodTemplateSpec, nil
}

// mergeContainers merges containers identified by their name
func mergeContainers(customContainers, defaultContainers []corev1.Container) ([]corev1.Container, error) {
	defaultContainerMap := createContainerMap(defaultContainers)
	customContainerMap := createContainerMap(customContainers)
	mergedContainers := []corev1.Container{}
	for _, defaultContainer := range defaultContainers {
		if customContainer, ok := customContainerMap[defaultContainer.Name]; ok {
			// need to merge
			// merge volume mounts first
			var mergedVolumeMounts []corev1.VolumeMount
			var err error
			if mergedVolumeMounts, err = mergeVolumeMounts(customContainer.VolumeMounts, defaultContainer.VolumeMounts); err != nil {
				return nil, err
			}
			if err = mergo.Merge(&defaultContainer, customContainer, mergo.WithOverride); err != nil {
				return nil, err
			}
			// completely override any resources that were provided
			// this prevents issues with custom requests giving errors due
			// to the defaulted limits
			defaultContainer.Resources = customContainer.Resources
			defaultContainer.VolumeMounts = mergedVolumeMounts
		}
		mergedContainers = append(mergedContainers, defaultContainer)
	}
	for _, customContainer := range customContainers {
		if _, ok := defaultContainerMap[customContainer.Name]; ok {
			// custom container has been merged already
			continue
		}
		mergedContainers = append(mergedContainers, customContainer)
	}
	return mergedContainers, nil
}

func createContainerMap(containers []corev1.Container) map[string]corev1.Container {
	containerMap := make(map[string]corev1.Container)
	for _, c := range containers {
		containerMap[c.Name] = c
	}
	return containerMap
}

func createMountsMap(volumeMounts []corev1.VolumeMount) map[string]corev1.VolumeMount {
	mountMap := make(map[string]corev1.VolumeMount)
	for _, m := range volumeMounts {
		mountMap[m.Name] = m
	}
	return mountMap
}

func mergeVolumeMounts(customMounts, defaultMounts []corev1.VolumeMount) ([]corev1.VolumeMount, error) {
	defaultMountsMap := createMountsMap(defaultMounts)
	customMountsMap := createMountsMap(customMounts)
	mergedVolumeMounts := []corev1.VolumeMount{}
	for _, defaultMount := range defaultMounts {
		if customMount, ok := customMountsMap[defaultMount.Name]; ok {
			// needs merge
			if err := mergo.Merge(&defaultMount, customMount, mergo.WithAppendSlice); err != nil {
				return nil, err
			}
		}
		mergedVolumeMounts = append(mergedVolumeMounts, defaultMount)
	}
	for _, customMount := range customMounts {
		if _, ok := defaultMountsMap[customMount.Name]; ok {
			// already merged
			continue
		}
		mergedVolumeMounts = append(mergedVolumeMounts, customMount)
	}
	return mergedVolumeMounts, nil
}

func getMergedDefaultPodSpecTemplate(defaultPodSpecTemplate corev1.PodTemplateSpec, podTemplateOverride *corev1.PodTemplateSpec) (corev1.PodTemplateSpec, error) {
	if podTemplateOverride == nil {
		return defaultPodSpecTemplate, nil
	}
	// there is a user defined pod spec template, we need to merge in all of the default values
	return MergePodSpecs(*podTemplateOverride, defaultPodSpecTemplate)
}
