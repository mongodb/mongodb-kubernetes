package operator

import (
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/envutil"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type podTemplateSpecConfigurationFunc func(podTemplateSpec *corev1.PodTemplateSpec)

func buildPodTemplateSpec(opts ...podTemplateSpecConfigurationFunc) corev1.PodTemplateSpec {
	spec := corev1.PodTemplateSpec{}
	for _, opt := range opts {
		opt(&spec)
	}
	return spec
}

func withAnnotations(annotations map[string]string) func(podTemplateSpec *corev1.PodTemplateSpec) {
	if annotations == nil {
		annotations = map[string]string{}
	}
	return func(podTemplateSpec *corev1.PodTemplateSpec) {
		podTemplateSpec.Annotations = annotations
	}
}

func withPodLabels(labels map[string]string) func(podTemplateSpec *corev1.PodTemplateSpec) {
	if labels == nil {
		labels = map[string]string{}
	}
	return func(podTemplateSpec *corev1.PodTemplateSpec) {
		podTemplateSpec.ObjectMeta.Labels = labels
	}
}

func withServiceAccount(serviceAccountName string) func(podTemplateSpec *corev1.PodTemplateSpec) {
	return func(podTemplateSpec *corev1.PodTemplateSpec) {
		podTemplateSpec.Spec.ServiceAccountName = serviceAccountName
	}
}

func withContainers(containers ...corev1.Container) func(podTemplateSpec *corev1.PodTemplateSpec) {
	return func(podTemplateSpec *corev1.PodTemplateSpec) {
		podTemplateSpec.Spec.Containers = append(podTemplateSpec.Spec.Containers, containers...)
	}
}

func editContainer(index int, funcs ...func(container *corev1.Container)) func(podTemplateSpec *corev1.PodTemplateSpec) {
	return func(podTemplateSpec *corev1.PodTemplateSpec) {
		c := &podTemplateSpec.Spec.Containers[index]
		for _, f := range funcs {
			f(c)
		}
	}
}

func withInitContainers(containers ...corev1.Container) func(podTemplateSpec *corev1.PodTemplateSpec) {
	return func(podTemplateSpec *corev1.PodTemplateSpec) {
		podTemplateSpec.Spec.InitContainers = append(podTemplateSpec.Spec.InitContainers, containers...)
	}
}

func withTerminationGracePeriodSeconds(seconds int64) func(podTemplateSpec *corev1.PodTemplateSpec) {
	s := seconds
	return func(podTemplateSpec *corev1.PodTemplateSpec) {
		podTemplateSpec.Spec.TerminationGracePeriodSeconds = &s
	}
}

func withSecurityContext(managedSecurityContext bool) func(podTemplateSpec *corev1.PodTemplateSpec) {
	return func(podTemplateSpec *corev1.PodTemplateSpec) {
		spec := &podTemplateSpec.Spec
		if !managedSecurityContext {
			spec.SecurityContext = &corev1.PodSecurityContext{
				FSGroup: util.Int64Ref(util.FsGroup),
			}
		}
	}
}
func withImagePullSecrets() func(podTemplateSpec *corev1.PodTemplateSpec) {
	return func(podTemplateSpec *corev1.PodTemplateSpec) {
		if val, found := envutil.Read(util.ImagePullSecrets); found {
			podTemplateSpec.Spec.ImagePullSecrets = append(podTemplateSpec.Spec.ImagePullSecrets, corev1.LocalObjectReference{
				Name: val,
			})
		}
	}
}

func withAffinity(stsName string) func(podTemplateSpec *corev1.PodTemplateSpec) {
	return func(podTemplateSpec *corev1.PodTemplateSpec) {
		podTemplateSpec.Spec.Affinity =
			&corev1.Affinity{
				PodAntiAffinity: &corev1.PodAntiAffinity{
					PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{{
						// Weight thoughts - seems no other affinity rule should be stronger than anti affinity one so putting
						// it to 100
						Weight: 100,
						PodAffinityTerm: corev1.PodAffinityTerm{
							LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{PodAntiAffinityLabelKey: stsName}},
							// If PodAntiAffinityTopologyKey config property is empty - then it's ok to use some default (even for standalones)
						},
					}},
				},
			}
	}
}

func withNodeAffinity(nodeAffinity *corev1.NodeAffinity) func(podTemplateSpec *corev1.PodTemplateSpec) {
	return func(podTemplateSpec *corev1.PodTemplateSpec) {
		podTemplateSpec.Spec.Affinity.NodeAffinity = nodeAffinity
	}
}

func withPodAffinity(podAffinity *corev1.PodAffinity) func(podTemplateSpec *corev1.PodTemplateSpec) {
	return func(podTemplateSpec *corev1.PodTemplateSpec) {
		podTemplateSpec.Spec.Affinity.PodAffinity = podAffinity
	}
}

func withTopologyKey(topologyKey string) func(podTemplateSpec *corev1.PodTemplateSpec) {
	return func(podTemplateSpec *corev1.PodTemplateSpec) {
		podTemplateSpec.Spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution[0].PodAffinityTerm.TopologyKey = topologyKey
	}
}

type containerConfigurationFunc func(container *corev1.Container)

func buildContainer(opts ...containerConfigurationFunc) corev1.Container {
	container := corev1.Container{}
	for _, opt := range opts {
		opt(&container)
	}
	return container
}

func withContainerResources(resourceRequirements corev1.ResourceRequirements) func(container *corev1.Container) {
	return func(container *corev1.Container) {
		container.Resources = resourceRequirements
	}
}

func withContainerLifeCycle(lifeCycle corev1.Lifecycle) func(container *corev1.Container) {
	lc := lifeCycle
	return func(container *corev1.Container) {
		container.Lifecycle = &lc
	}
}

func withContainerCommand(cmd []string) func(container *corev1.Container) {
	return func(container *corev1.Container) {
		container.Command = cmd
	}
}

func withContainerName(name string) func(container *corev1.Container) {
	return func(container *corev1.Container) {
		container.Name = name
	}
}

func withContainerImage(image string) func(container *corev1.Container) {
	return func(container *corev1.Container) {
		container.Image = image
	}
}

func withContainerEnvVars(envVars ...corev1.EnvVar) func(container *corev1.Container) {
	return func(container *corev1.Container) {
		container.Env = append(container.Env, envVars...)
	}
}

func withContainerPorts(ports []corev1.ContainerPort) func(container *corev1.Container) {
	return func(container *corev1.Container) {
		container.Ports = ports
	}
}

func withContainerPullPolicy(pullPolicy corev1.PullPolicy) func(container *corev1.Container) {
	return func(container *corev1.Container) {
		container.ImagePullPolicy = pullPolicy
	}
}

func withContainerReadinessProbe(readinessProve corev1.Probe) func(container *corev1.Container) {
	probe := readinessProve
	return func(container *corev1.Container) {
		container.ReadinessProbe = &probe
	}
}

func withContainerLivenessProbe(readinessProve corev1.Probe) func(container *corev1.Container) {
	probe := readinessProve
	return func(container *corev1.Container) {
		container.LivenessProbe = &probe
	}
}

func withContainerSecurityContext(managedSecurityContext bool) func(container *corev1.Container) {
	return func(container *corev1.Container) {
		if !managedSecurityContext {
			container.SecurityContext = &corev1.SecurityContext{
				RunAsUser:    util.Int64Ref(util.RunAsUser),
				RunAsNonRoot: util.BooleanRef(true),
			}
		}
	}
}
