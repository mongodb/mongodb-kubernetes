package merge

import (
	"testing"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func TestDeploymentSpecs_NoOverride(t *testing.T) {
	replicas := int32(1)
	defaultSpec := appsv1.DeploymentSpec{
		Replicas: &replicas,
		Template: corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: "envoy", Image: "envoy:latest"},
				},
			},
		},
	}

	merged := DeploymentSpecs(defaultSpec, appsv1.DeploymentSpec{})

	assert.Equal(t, &replicas, merged.Replicas)
	assert.Len(t, merged.Template.Spec.Containers, 1)
	assert.Equal(t, "envoy:latest", merged.Template.Spec.Containers[0].Image)
}

func TestDeploymentSpecs_OverrideReplicas(t *testing.T) {
	defaultReplicas := int32(1)
	overrideReplicas := int32(3)

	defaultSpec := appsv1.DeploymentSpec{Replicas: &defaultReplicas}
	overrideSpec := appsv1.DeploymentSpec{Replicas: &overrideReplicas}

	merged := DeploymentSpecs(defaultSpec, overrideSpec)
	assert.Equal(t, int32(3), *merged.Replicas)
}

func TestDeploymentSpecs_OverrideStrategy(t *testing.T) {
	defaultSpec := appsv1.DeploymentSpec{
		Strategy: appsv1.DeploymentStrategy{
			Type: appsv1.RollingUpdateDeploymentStrategyType,
		},
	}

	maxSurge := intstr.FromInt32(2)
	overrideSpec := appsv1.DeploymentSpec{
		Strategy: appsv1.DeploymentStrategy{
			Type: appsv1.RecreateDeploymentStrategyType,
			RollingUpdate: &appsv1.RollingUpdateDeployment{
				MaxSurge: &maxSurge,
			},
		},
	}

	merged := DeploymentSpecs(defaultSpec, overrideSpec)
	assert.Equal(t, appsv1.RecreateDeploymentStrategyType, merged.Strategy.Type)
	assert.Equal(t, &maxSurge, merged.Strategy.RollingUpdate.MaxSurge)
}

func TestDeploymentSpecs_OverrideRevisionHistoryAndProgressDeadline(t *testing.T) {
	revHistory := int32(5)
	progressDeadline := int32(120)

	defaultSpec := appsv1.DeploymentSpec{}
	overrideSpec := appsv1.DeploymentSpec{
		RevisionHistoryLimit:    &revHistory,
		ProgressDeadlineSeconds: &progressDeadline,
		MinReadySeconds:         30,
	}

	merged := DeploymentSpecs(defaultSpec, overrideSpec)
	assert.Equal(t, int32(5), *merged.RevisionHistoryLimit)
	assert.Equal(t, int32(120), *merged.ProgressDeadlineSeconds)
	assert.Equal(t, int32(30), merged.MinReadySeconds)
}

func TestDeploymentSpecs_MergePodTemplate_Tolerations(t *testing.T) {
	defaultSpec := appsv1.DeploymentSpec{
		Template: corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: "envoy", Image: "envoy:latest"},
				},
			},
		},
	}

	overrideSpec := appsv1.DeploymentSpec{
		Template: corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Tolerations: []corev1.Toleration{
					{Key: "dedicated", Value: "search", Effect: corev1.TaintEffectNoSchedule},
				},
				NodeSelector: map[string]string{"node-type": "search"},
			},
		},
	}

	merged := DeploymentSpecs(defaultSpec, overrideSpec)
	assert.Len(t, merged.Template.Spec.Tolerations, 1)
	assert.Equal(t, "dedicated", merged.Template.Spec.Tolerations[0].Key)
	assert.Equal(t, map[string]string{"node-type": "search"}, merged.Template.Spec.NodeSelector)
	// Original container preserved
	assert.Len(t, merged.Template.Spec.Containers, 1)
	assert.Equal(t, "envoy:latest", merged.Template.Spec.Containers[0].Image)
}

func TestDeploymentSpecs_MergePodTemplate_ContainerByName(t *testing.T) {
	defaultSpec := appsv1.DeploymentSpec{
		Template: corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "envoy",
						Image: "envoy:latest",
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("100m"),
							},
						},
					},
				},
			},
		},
	}

	overrideSpec := appsv1.DeploymentSpec{
		Template: corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name: "envoy",
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("500m"),
							},
						},
					},
				},
			},
		},
	}

	merged := DeploymentSpecs(defaultSpec, overrideSpec)
	assert.Len(t, merged.Template.Spec.Containers, 1)
	assert.Equal(t, "envoy", merged.Template.Spec.Containers[0].Name)
	// Image preserved from default
	assert.Equal(t, "envoy:latest", merged.Template.Spec.Containers[0].Image)
	// Resources overridden
	assert.Equal(t, resource.MustParse("500m"), merged.Template.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU])
}

func TestDeployments_MergeLabels(t *testing.T) {
	defaultDep := appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "envoy-lb",
			Namespace: "ns",
			Labels:    map[string]string{"app": "envoy"},
		},
	}

	overrideDep := appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{"team": "search", "app": "envoy-custom"},
		},
	}

	merged := Deployments(defaultDep, overrideDep)
	assert.Equal(t, "envoy-lb", merged.Name)
	assert.Equal(t, "ns", merged.Namespace)
	assert.Equal(t, "envoy-custom", merged.Labels["app"])
	assert.Equal(t, "search", merged.Labels["team"])
}

func TestDeploymentSpecs_MergeSelector(t *testing.T) {
	defaultSpec := appsv1.DeploymentSpec{
		Selector: &metav1.LabelSelector{
			MatchLabels: map[string]string{"app": "envoy"},
		},
	}

	overrideSpec := appsv1.DeploymentSpec{
		Selector: &metav1.LabelSelector{
			MatchLabels: map[string]string{"version": "v2"},
		},
	}

	merged := DeploymentSpecs(defaultSpec, overrideSpec)
	assert.Equal(t, "envoy", merged.Selector.MatchLabels["app"])
	assert.Equal(t, "v2", merged.Selector.MatchLabels["version"])
}
