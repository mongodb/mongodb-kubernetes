package merge

import (
	appsv1 "k8s.io/api/apps/v1"
)

// Deployments merges two Deployments together.
func Deployments(defaultDeployment, overrideDeployment appsv1.Deployment) appsv1.Deployment {
	mergedDep := defaultDeployment
	mergedDep.Labels = StringToStringMap(defaultDeployment.Labels, overrideDeployment.Labels)
	if overrideDeployment.Namespace != "" {
		mergedDep.Namespace = overrideDeployment.Namespace
	}
	if overrideDeployment.Name != "" {
		mergedDep.Name = overrideDeployment.Name
	}
	mergedDep.Spec = DeploymentSpecs(defaultDeployment.Spec, overrideDeployment.Spec)
	return mergedDep
}

// DeploymentSpecs merges two DeploymentSpecs together.
func DeploymentSpecs(defaultSpec, overrideSpec appsv1.DeploymentSpec) appsv1.DeploymentSpec {
	mergedSpec := defaultSpec
	if overrideSpec.Replicas != nil {
		mergedSpec.Replicas = overrideSpec.Replicas
	}

	mergedSpec.Selector = LabelSelectors(defaultSpec.Selector, overrideSpec.Selector)

	if overrideSpec.Strategy.Type != "" {
		mergedSpec.Strategy.Type = overrideSpec.Strategy.Type
	}

	if overrideSpec.Strategy.RollingUpdate != nil {
		mergedSpec.Strategy.RollingUpdate = overrideSpec.Strategy.RollingUpdate
	}

	if overrideSpec.MinReadySeconds != 0 {
		mergedSpec.MinReadySeconds = overrideSpec.MinReadySeconds
	}

	if overrideSpec.RevisionHistoryLimit != nil {
		mergedSpec.RevisionHistoryLimit = overrideSpec.RevisionHistoryLimit
	}

	if overrideSpec.ProgressDeadlineSeconds != nil {
		mergedSpec.ProgressDeadlineSeconds = overrideSpec.ProgressDeadlineSeconds
	}

	mergedSpec.Template = PodTemplateSpecs(defaultSpec.Template, overrideSpec.Template)
	return mergedSpec
}
