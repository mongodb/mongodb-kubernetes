package construct

import (
	"sort"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/probes"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/container"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/podtemplatespec"
	corev1 "k8s.io/api/core/v1"
)

// This file contains modification functions that had errors in community 0.0.7, once these are fixed and released
// we can delete them and use the fixed implementations from community.

// withVolumeMounts sets the VolumeMounts
// TODO remove in favor or community operator
func withVolumeMounts(volumeMounts []corev1.VolumeMount) container.Modification {
	volumesMountsCopy := make([]corev1.VolumeMount, len(volumeMounts))
	copy(volumesMountsCopy, volumeMounts)
	return func(container *corev1.Container) {
		merged := map[string]corev1.VolumeMount{}
		for _, ex := range container.VolumeMounts {
			merged[ex.Name+"-"+ex.MountPath+"-"+ex.SubPath] = ex
		}
		for _, des := range volumesMountsCopy {
			merged[des.Name+"-"+des.MountPath+"-"+des.SubPath] = des
		}

		var final []corev1.VolumeMount
		for _, v := range merged {
			final = append(final, v)
		}
		sort.SliceStable(final, func(i, j int) bool {
			a := final[i]
			b := final[j]
			return a.Name+"-"+a.MountPath+"-"+a.SubPath < b.Name+"-"+b.MountPath+"-"+b.SubPath
		})
		container.VolumeMounts = final
	}
}

// WithImagePullSecrets adds an ImagePullSecrets local reference with the given name
func withImagePullSecrets(name string) podtemplatespec.Modification {
	return func(podTemplateSpec *corev1.PodTemplateSpec) {
		for _, v := range podTemplateSpec.Spec.ImagePullSecrets {
			if v.Name == name {
				return
			}
		}
		podTemplateSpec.Spec.ImagePullSecrets = append(podTemplateSpec.Spec.ImagePullSecrets, corev1.LocalObjectReference{
			Name: name,
		})
	}
}

func withTimeoutSeconds(timeoutSeconds int) probes.Modification {
	return func(probe *corev1.Probe) {
		probe.TimeoutSeconds = int32(timeoutSeconds)
	}
}
