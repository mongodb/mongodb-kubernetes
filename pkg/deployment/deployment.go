package deployment

import (
	"maps"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type Modification func(*appsv1.Deployment)

func New(mods ...Modification) appsv1.Deployment {
	dep := appsv1.Deployment{}
	for _, mod := range mods {
		mod(&dep)
	}
	return dep
}

func Apply(funcs ...Modification) Modification {
	return func(dep *appsv1.Deployment) {
		for _, f := range funcs {
			f(dep)
		}
	}
}

// NOOP is a valid Modification which applies no changes
func NOOP() Modification {
	return func(dep *appsv1.Deployment) {}
}

func WithName(name string) Modification {
	return func(dep *appsv1.Deployment) {
		dep.Name = name
	}
}

func WithNamespace(namespace string) Modification {
	return func(dep *appsv1.Deployment) {
		dep.Namespace = namespace
	}
}

func WithLabels(labels map[string]string) Modification {
	return func(dep *appsv1.Deployment) {
		if dep.Labels == nil {
			dep.Labels = map[string]string{}
		}
		maps.Copy(dep.Labels, labels)
	}
}

func WithAnnotations(annotations map[string]string) Modification {
	return func(dep *appsv1.Deployment) {
		if dep.Annotations == nil {
			dep.Annotations = map[string]string{}
		}
		maps.Copy(dep.Annotations, annotations)
	}
}

func WithOwnerReference(ownerRefs []metav1.OwnerReference) Modification {
	ownerReference := make([]metav1.OwnerReference, len(ownerRefs))
	copy(ownerReference, ownerRefs)
	return func(dep *appsv1.Deployment) {
		dep.OwnerReferences = ownerReference
	}
}

func WithMatchLabels(matchLabels map[string]string) Modification {
	return func(dep *appsv1.Deployment) {
		if dep.Spec.Selector == nil {
			dep.Spec.Selector = &metav1.LabelSelector{}
		}
		if dep.Spec.Selector.MatchLabels == nil {
			dep.Spec.Selector.MatchLabels = map[string]string{}
		}
		maps.Copy(dep.Spec.Selector.MatchLabels, matchLabels)
	}
}

func WithReplicas(replicas int32) Modification {
	return func(dep *appsv1.Deployment) {
		dep.Spec.Replicas = &replicas
	}
}

func WithStrategyType(strategyType appsv1.DeploymentStrategyType) Modification {
	return func(dep *appsv1.Deployment) {
		dep.Spec.Strategy = appsv1.DeploymentStrategy{
			Type: strategyType,
		}
	}
}

func WithPodSpecTemplate(templateFunc func(*corev1.PodTemplateSpec)) Modification {
	return func(dep *appsv1.Deployment) {
		template := &dep.Spec.Template
		templateFunc(template)
	}
}
