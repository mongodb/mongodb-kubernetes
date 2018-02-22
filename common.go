package main

import (
	corev1 "k8s.io/api/core/v1"
)

func BaseContainer() corev1.PodSpec {
	return corev1.PodSpec{
		Containers: []corev1.Container{
			{
				Name:            ContainerName,
				Image:           ContainerImage,
				ImagePullPolicy: ContainerImagePullPolicy,
				EnvFrom:         BaseEnvFrom(),
			},
		},
	}
}

func BaseEnvFrom() []corev1.EnvFromSource {
	return []corev1.EnvFromSource{
		{
			ConfigMapRef: &corev1.ConfigMapEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: ContainerConfigMapName,
				},
			},
		},
	}
}

func MakeIntReference(i int32) *int32 {
	return &i
}
