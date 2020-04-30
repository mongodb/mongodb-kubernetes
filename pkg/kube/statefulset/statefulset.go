package statefulset

import (
	"fmt"

	"github.com/imdario/mergo"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

// VolumeMountData contains values required for the MountVolume function
type VolumeMountData struct {
	MountPath string
	Name      string
	ReadOnly  bool
	Volume    corev1.Volume
}

func CreateVolumeFromConfigMap(name, sourceName string) corev1.Volume {
	return corev1.Volume{
		Name: name,
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: sourceName,
				},
			},
		},
	}
}

func CreateVolumeFromSecret(name, sourceName string) corev1.Volume {
	return corev1.Volume{
		Name: name,
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName: sourceName,
			},
		},
	}
}

// CreateVolumeMount convenience function to build a VolumeMount.
func CreateVolumeMount(name, path, subpath string) corev1.VolumeMount {
	volumeMount := corev1.VolumeMount{
		Name:      name,
		MountPath: path,
	}
	if subpath != "" {
		volumeMount.SubPath = subpath
	}
	return volumeMount
}

func mergeStatefulSetSpec(defaultStatefulSetSpec appsv1.StatefulSetSpec, overrideStatefulSetSpec *appsv1.StatefulSetSpec) (appsv1.StatefulSetSpec, error) {
	if overrideStatefulSetSpec == nil {
		return defaultStatefulSetSpec, nil
	}
	mergedPodSpecTemplate, err := getMergedDefaultPodSpecTemplate(defaultStatefulSetSpec.Template, &overrideStatefulSetSpec.Template)
	if err != nil {
		return appsv1.StatefulSetSpec{}, fmt.Errorf("error merging podSpecTemplate: %v", err)
	}

	// the operator configures VolumeClaimTemplates so we need to merge by name
	volumeClaimTemplates, err := mergeVolumeClaimTemplates(defaultStatefulSetSpec.VolumeClaimTemplates, overrideStatefulSetSpec.VolumeClaimTemplates)
	if err != nil {
		return appsv1.StatefulSetSpec{}, fmt.Errorf("error merging volume claim templates: %v", err)
	}

	if err = mergo.Merge(&defaultStatefulSetSpec, overrideStatefulSetSpec, mergo.WithOverride); err != nil {
		return appsv1.StatefulSetSpec{}, fmt.Errorf("error merging statefulsets: %v", err)
	}
	defaultStatefulSetSpec.Template = mergedPodSpecTemplate

	defaultStatefulSetSpec.VolumeClaimTemplates = volumeClaimTemplates
	return defaultStatefulSetSpec, nil
}

func createVolumeClaimMap(volumeMounts []corev1.PersistentVolumeClaim) map[string]corev1.PersistentVolumeClaim {
	mountMap := make(map[string]corev1.PersistentVolumeClaim)
	for _, m := range volumeMounts {
		mountMap[m.Name] = m
	}
	return mountMap
}

func mergeVolumeClaimTemplates(defaultTemplates []corev1.PersistentVolumeClaim, overrideTemplates []corev1.PersistentVolumeClaim) ([]corev1.PersistentVolumeClaim, error) {
	defaultMountsMap := createVolumeClaimMap(defaultTemplates)
	customMountsMap := createVolumeClaimMap(overrideTemplates)
	var mergedVolumes []corev1.PersistentVolumeClaim
	for _, defaultMount := range defaultMountsMap {
		if customMount, ok := customMountsMap[defaultMount.Name]; ok {
			// needs merge
			if err := mergo.Merge(&defaultMount, customMount, mergo.WithAppendSlice, mergo.WithOverride); err != nil {
				return nil, err
			}
		}
		mergedVolumes = append(mergedVolumes, defaultMount)
	}
	for _, customMount := range customMountsMap {
		if _, ok := defaultMountsMap[customMount.Name]; ok {
			// already merged
			continue
		}
		mergedVolumes = append(mergedVolumes, customMount)
	}
	return mergedVolumes, nil
}

// MergeSpec takes a default StatefulSet provided, and applies the changes from the given overrideSpec
// on top of it, a new, merged result is returned and an error if anything went wrong.
func MergeSpec(defaultStsSpec appsv1.StatefulSet, overrideSpec *appsv1.StatefulSetSpec) (appsv1.StatefulSet, error) {
	if overrideSpec == nil {
		return defaultStsSpec, nil
	}
	mergedSpec, err := mergeStatefulSetSpec(defaultStsSpec.Spec, overrideSpec)
	if err != nil {
		return appsv1.StatefulSet{}, fmt.Errorf("error merging specs: %v", err)
	}
	defaultStsSpec.Spec = mergedSpec
	return defaultStsSpec, nil
}
