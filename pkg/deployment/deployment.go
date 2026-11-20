package deployment

import (
	"maps"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type Modification func(*appsv1.Deployment)

func Apply(funcs ...Modification) Modification {
	return func(dep *appsv1.Deployment) {
		for _, f := range funcs {
			f(dep)
		}
	}
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
