package util

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestMergePodSpecs(t *testing.T) {
	defaultPodSpec := corev1.PodTemplateSpec{
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

	customPodSpecTemplate := corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			NodeSelector: map[string]string{
				"node-1": "node-1",
			},
			ServiceAccountName:            "my-service-account-override",
			TerminationGracePeriodSeconds: Int64Ref(11),
			NodeName:                      "my-node-name",
			RestartPolicy:                 corev1.RestartPolicy("Always"),
		},
	}

	err := MergePodSpecs(&customPodSpecTemplate, defaultPodSpec)

	assert.NoError(t, err)

	// ensure values that were specified in the custom pod spec template remain unchanged
	assert.Equal(t, "my-service-account-override", customPodSpecTemplate.Spec.ServiceAccountName)
	assert.Equal(t, Int64Ref(11), customPodSpecTemplate.Spec.TerminationGracePeriodSeconds)
	assert.Equal(t, "my-node-name", customPodSpecTemplate.Spec.NodeName)
	assert.Equal(t, corev1.RestartPolicy("Always"), customPodSpecTemplate.Spec.RestartPolicy)

	// ensure values from the default pod spec template have been merged in
	assert.Equal(t, "my-default-name", customPodSpecTemplate.ObjectMeta.Name)
	assert.Equal(t, "my-default-namespace", customPodSpecTemplate.ObjectMeta.Namespace)
	assert.Equal(t, Int64Ref(10), customPodSpecTemplate.Spec.ActiveDeadlineSeconds)

	// ensure collections have been merged
	assert.Contains(t, customPodSpecTemplate.Spec.NodeSelector, "node-0")
	assert.Contains(t, customPodSpecTemplate.Spec.NodeSelector, "node-1")
	assert.Equal(t, "container-0", customPodSpecTemplate.Spec.Containers[0].Name)
	assert.Equal(t, "image-0", customPodSpecTemplate.Spec.Containers[0].Image)
	assert.Equal(t, "container-0.volume-mount-0", customPodSpecTemplate.Spec.Containers[0].VolumeMounts[0].Name)
}
