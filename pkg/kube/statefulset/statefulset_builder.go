package statefulset

import (
	"fmt"
	"sort"

	multierror "github.com/hashicorp/go-multierror"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type Builder struct {
	name        string
	namespace   string
	replicas    *int32
	serviceName string

	// these fields need to be initialised
	labels                   map[string]string
	matchLabels              map[string]string
	ownerReference           []metav1.OwnerReference
	podTemplateSpec          corev1.PodTemplateSpec
	volumeClaimsTemplates    []corev1.PersistentVolumeClaim
	volumeMountsPerContainer map[string][]corev1.VolumeMount
}

func (s *Builder) SetLabels(labels map[string]string) *Builder {
	s.labels = labels
	return s
}

func (s *Builder) SetName(name string) *Builder {
	s.name = name
	return s
}

func (s *Builder) SetNamespace(namespace string) *Builder {
	s.namespace = namespace
	return s
}

func (s *Builder) SetOwnerReference(ownerReference []metav1.OwnerReference) *Builder {
	s.ownerReference = ownerReference
	return s
}

func (s *Builder) SetServiceName(serviceName string) *Builder {
	s.serviceName = serviceName
	return s
}

func (s *Builder) SetReplicas(replicas *int32) *Builder {
	s.replicas = replicas
	return s
}

func (s *Builder) SetMatchLabels(matchLabels map[string]string) *Builder {
	s.matchLabels = matchLabels
	return s
}

func (s *Builder) SetPodTemplateSpec(podTemplateSpec corev1.PodTemplateSpec) *Builder {
	s.podTemplateSpec = *podTemplateSpec.DeepCopy()
	return s
}

func (s *Builder) AddVolumeClaimTemplates(claims []corev1.PersistentVolumeClaim) *Builder {
	s.volumeClaimsTemplates = append(s.volumeClaimsTemplates, claims...)
	return s
}

func (s *Builder) AddVolumeMount(containerName string, mount corev1.VolumeMount) *Builder {
	s.volumeMountsPerContainer[containerName] = append(s.volumeMountsPerContainer[containerName], mount)
	return s
}

func (s *Builder) AddVolumeMounts(containerName string, mounts []corev1.VolumeMount) *Builder {
	for _, m := range mounts {
		s.AddVolumeMount(containerName, m)
	}
	return s
}

func (s *Builder) AddVolume(volume corev1.Volume) *Builder {
	s.podTemplateSpec.Spec.Volumes = append(s.podTemplateSpec.Spec.Volumes, volume)
	return s
}

func (s *Builder) AddVolumes(volumes []corev1.Volume) *Builder {
	for _, v := range volumes {
		s.AddVolume(v)
	}
	return s
}

// getContainerIndexByName returns the index of the container with containerName
func (s Builder) getContainerIndexByName(containerName string) (int, error) {
	return getContainerByName(containerName, s.podTemplateSpec.Spec.Containers)
}

// getInitContainerIndexByName returns the index of the container with containerName for initContainers
func (s Builder) getInitContainerIndexByName(containerName string) (int, error) {
	return getContainerByName(containerName, s.podTemplateSpec.Spec.InitContainers)
}

func getContainerByName(containerName string, containers []corev1.Container) (int, error) {
	for i, c := range containers {
		if c.Name == containerName {
			return i, nil
		}
	}
	return -1, fmt.Errorf("no container with name [%s] found", containerName)
}

func (s *Builder) AddVolumeAndMount(volumeMountData VolumeMountData, containerNames ...string) *Builder {
	s.AddVolume(volumeMountData.Volume)
	for _, containerName := range containerNames {
		s.AddVolumeMount(
			containerName,
			corev1.VolumeMount{
				Name:      volumeMountData.Name,
				ReadOnly:  volumeMountData.ReadOnly,
				MountPath: volumeMountData.MountPath,
			},
		)
	}
	return s
}

// addVolumeMountToContainers tries to mount the volumes to the container
// (identified by name) in the podTemplateSpec. Note that it will work both for
// "normal" containers and init containers
func (s Builder) addVolumeMountToContainers(containerName string, volumeMounts []corev1.VolumeMount, podTemplateSpec *corev1.PodTemplateSpec) (bool, error) {
	idx, err := s.getContainerIndexByName(containerName)
	if err != nil {
		return false, err
	}
	var errs error
	ok := false
	existingVolumeMounts := map[string]bool{}
	for _, volumeMount := range volumeMounts {
		if prevMount, seen := existingVolumeMounts[volumeMount.MountPath]; seen {
			// Volume with the same path already mounted
			errs = multierror.Append(errs, fmt.Errorf("Volume %v already mounted as %v", volumeMount, prevMount))
			continue
		}
		podTemplateSpec.Spec.Containers[idx].VolumeMounts = append(podTemplateSpec.Spec.Containers[idx].VolumeMounts, volumeMount)
		existingVolumeMounts[volumeMount.MountPath] = true
		ok = true
	}
	return ok, errs
}

func (s Builder) addVolumeMountToInitContainers(containerName string, volumeMounts []corev1.VolumeMount, podTemplateSpec *corev1.PodTemplateSpec) (bool, error) {
	idx, err := s.getInitContainerIndexByName(containerName)
	if err != nil {
		return false, err
	}
	for _, volumeMount := range volumeMounts {
		// init containers just override volume mounts
		podTemplateSpec.Spec.InitContainers[idx].VolumeMounts = append(podTemplateSpec.Spec.InitContainers[idx].VolumeMounts, volumeMount)
	}
	return true, nil
}

func (s Builder) buildPodTemplateSpec() (corev1.PodTemplateSpec, error) {
	podTemplateSpec := s.podTemplateSpec.DeepCopy()
	var errs error
	for containerName, volumeMounts := range s.volumeMountsPerContainer {
		var found bool
		var errContainer, errInitContainer error
		if found, errContainer = s.addVolumeMountToContainers(containerName, volumeMounts, podTemplateSpec); found {
			continue
		}
		if found, errInitContainer = s.addVolumeMountToInitContainers(containerName, volumeMounts, podTemplateSpec); found {
			continue
		}
		// reaches here only in case of error in both cases
		errs = multierror.Append(errs, errContainer)
		errs = multierror.Append(errs, errInitContainer)
	}

	// sorts environment variables for all containers
	for _, container := range podTemplateSpec.Spec.Containers {
		envVars := container.Env
		sort.SliceStable(envVars, func(i, j int) bool {
			return envVars[i].Name < envVars[j].Name
		})
	}
	return *podTemplateSpec, errs
}

func copyMap(originalMap map[string]string) map[string]string {
	newMap := map[string]string{}
	for k, v := range originalMap {
		newMap[k] = v
	}
	return newMap
}

func (s Builder) Build() (appsv1.StatefulSet, error) {
	podTemplateSpec, err := s.buildPodTemplateSpec()
	if err != nil {
		return appsv1.StatefulSet{}, err
	}

	var stsReplicas *int32
	if s.replicas != nil {
		replicas := *s.replicas
		stsReplicas = &replicas
	}

	ownerReference := make([]metav1.OwnerReference, len(s.ownerReference))
	copy(ownerReference, s.ownerReference)

	volumeClaimsTemplates := make([]corev1.PersistentVolumeClaim, len(s.volumeClaimsTemplates))
	copy(volumeClaimsTemplates, s.volumeClaimsTemplates)

	sts := appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:            s.name,
			Namespace:       s.namespace,
			Labels:          copyMap(s.labels),
			OwnerReferences: ownerReference,
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName: s.serviceName,
			Replicas:    stsReplicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: copyMap(s.matchLabels),
			},
			Template:             podTemplateSpec,
			VolumeClaimTemplates: volumeClaimsTemplates,
		},
	}
	return sts, err
}

func NewBuilder() *Builder {
	return &Builder{
		labels:                   map[string]string{},
		matchLabels:              map[string]string{},
		ownerReference:           []metav1.OwnerReference{},
		podTemplateSpec:          corev1.PodTemplateSpec{},
		volumeClaimsTemplates:    []corev1.PersistentVolumeClaim{},
		volumeMountsPerContainer: map[string][]corev1.VolumeMount{},
	}
}
