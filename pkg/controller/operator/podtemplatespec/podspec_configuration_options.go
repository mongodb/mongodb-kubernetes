package podtemplatespec

import (
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/envutil"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type ConfigurationFunc func(podTemplateSpec *corev1.PodTemplateSpec)

func Build(opts ...ConfigurationFunc) corev1.PodTemplateSpec {
	spec := corev1.PodTemplateSpec{}
	for _, opt := range opts {
		opt(&spec)
	}
	return spec
}

func WithAnnotations(annotations map[string]string) ConfigurationFunc {
	if annotations == nil {
		annotations = map[string]string{}
	}
	return func(podTemplateSpec *corev1.PodTemplateSpec) {
		podTemplateSpec.Annotations = annotations
	}
}

func WithTolerations(tolerations []corev1.Toleration) ConfigurationFunc {
	return func(podTemplateSpec *corev1.PodTemplateSpec) {
		podTemplateSpec.Spec.Tolerations = tolerations
	}
}

func WithPodLabels(labels map[string]string) ConfigurationFunc {
	if labels == nil {
		labels = map[string]string{}
	}
	return func(podTemplateSpec *corev1.PodTemplateSpec) {
		podTemplateSpec.ObjectMeta.Labels = labels
	}
}

func WithServiceAccount(serviceAccountName string) ConfigurationFunc {
	return func(podTemplateSpec *corev1.PodTemplateSpec) {
		podTemplateSpec.Spec.ServiceAccountName = serviceAccountName
	}
}

func WithContainers(containers ...corev1.Container) ConfigurationFunc {
	return func(podTemplateSpec *corev1.PodTemplateSpec) {
		podTemplateSpec.Spec.Containers = append(podTemplateSpec.Spec.Containers, containers...)
	}
}

func EditContainer(index int, funcs ...func(container *corev1.Container)) func(podTemplateSpec *corev1.PodTemplateSpec) {
	return func(podTemplateSpec *corev1.PodTemplateSpec) {
		c := &podTemplateSpec.Spec.Containers[index]
		for _, f := range funcs {
			f(c)
		}
	}
}

func WithInitContainers(containers ...corev1.Container) ConfigurationFunc {
	return func(podTemplateSpec *corev1.PodTemplateSpec) {
		podTemplateSpec.Spec.InitContainers = append(podTemplateSpec.Spec.InitContainers, containers...)
	}
}

func WithTerminationGracePeriodSeconds(seconds int64) ConfigurationFunc {
	s := seconds
	return func(podTemplateSpec *corev1.PodTemplateSpec) {
		podTemplateSpec.Spec.TerminationGracePeriodSeconds = &s
	}
}

func WithSecurityContext(managedSecurityContext bool) ConfigurationFunc {
	return func(podTemplateSpec *corev1.PodTemplateSpec) {
		spec := &podTemplateSpec.Spec
		if !managedSecurityContext {
			spec.SecurityContext = &corev1.PodSecurityContext{
				FSGroup: util.Int64Ref(util.FsGroup),
			}
		}
	}
}
func WithImagePullSecrets() ConfigurationFunc {
	return func(podTemplateSpec *corev1.PodTemplateSpec) {
		if val, found := envutil.Read(util.ImagePullSecrets); found {
			podTemplateSpec.Spec.ImagePullSecrets = append(podTemplateSpec.Spec.ImagePullSecrets, corev1.LocalObjectReference{
				Name: val,
			})
		}
	}
}

func WithAffinity(stsName, antiAffinityLabelKey string) ConfigurationFunc {
	return func(podTemplateSpec *corev1.PodTemplateSpec) {
		podTemplateSpec.Spec.Affinity =
			&corev1.Affinity{
				PodAntiAffinity: &corev1.PodAntiAffinity{
					PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{{
						// Weight thoughts - seems no other affinity rule should be stronger than anti affinity one so putting
						// it to 100
						Weight: 100,
						PodAffinityTerm: corev1.PodAffinityTerm{
							LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{antiAffinityLabelKey: stsName}},
							// If PodAntiAffinityTopologyKey config property is empty - then it's ok to use some default (even for standalones)
						},
					}},
				},
			}
	}
}

func WithNodeAffinity(nodeAffinity *corev1.NodeAffinity) ConfigurationFunc {
	return func(podTemplateSpec *corev1.PodTemplateSpec) {
		podTemplateSpec.Spec.Affinity.NodeAffinity = nodeAffinity
	}
}

func WithPodAffinity(podAffinity *corev1.PodAffinity) ConfigurationFunc {
	return func(podTemplateSpec *corev1.PodTemplateSpec) {
		podTemplateSpec.Spec.Affinity.PodAffinity = podAffinity
	}
}

func WithTopologyKey(topologyKey string) ConfigurationFunc {
	return func(podTemplateSpec *corev1.PodTemplateSpec) {
		podTemplateSpec.Spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution[0].PodAffinityTerm.TopologyKey = topologyKey
	}
}

type ContainerConfigurationFunc func(container *corev1.Container)

func BuildContainer(opts ...ContainerConfigurationFunc) corev1.Container {
	container := corev1.Container{}
	for _, opt := range opts {
		opt(&container)
	}
	return container
}

func WithContainerResources(resourceRequirements corev1.ResourceRequirements) ContainerConfigurationFunc {
	return func(container *corev1.Container) {
		container.Resources = resourceRequirements
	}
}

func WithContainerLifeCycle(lifeCycle corev1.Lifecycle) ContainerConfigurationFunc {
	lc := lifeCycle
	return func(container *corev1.Container) {
		container.Lifecycle = &lc
	}
}

func WithContainerCommand(cmd []string) ContainerConfigurationFunc {
	return func(container *corev1.Container) {
		container.Command = cmd
	}
}

func WithContainerName(name string) ContainerConfigurationFunc {
	return func(container *corev1.Container) {
		container.Name = name
	}
}

func WithContainerImage(image string) ContainerConfigurationFunc {
	return func(container *corev1.Container) {
		container.Image = image
	}
}

func WithContainerEnvVars(envVars ...corev1.EnvVar) ContainerConfigurationFunc {
	return func(container *corev1.Container) {
		container.Env = append(container.Env, envVars...)
	}
}

func WithContainerPorts(ports []corev1.ContainerPort) ContainerConfigurationFunc {
	return func(container *corev1.Container) {
		container.Ports = ports
	}
}

func WithContainerPullPolicy(pullPolicy corev1.PullPolicy) ContainerConfigurationFunc {
	return func(container *corev1.Container) {
		container.ImagePullPolicy = pullPolicy
	}
}

func WithContainerReadinessProbe(readinessProve corev1.Probe) ContainerConfigurationFunc {
	probe := readinessProve
	return func(container *corev1.Container) {
		container.ReadinessProbe = &probe
	}
}

func WithContainerLivenessProbe(readinessProve corev1.Probe) ContainerConfigurationFunc {
	probe := readinessProve
	return func(container *corev1.Container) {
		container.LivenessProbe = &probe
	}
}

func WithContainerSecurityContext(managedSecurityContext bool) ContainerConfigurationFunc {
	return func(container *corev1.Container) {
		if !managedSecurityContext {
			container.SecurityContext = &corev1.SecurityContext{
				RunAsUser:    util.Int64Ref(util.RunAsUser),
				RunAsNonRoot: util.BooleanRef(true),
			}
		}
	}
}
