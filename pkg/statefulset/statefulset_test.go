package statefulset

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/kube/statefulset"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/util/merge"
)

const (
	TestNamespace = "test-ns"
	TestName      = "test-name"
)

func int64Ref(i int64) *int64 {
	return &i
}

func TestGetContainerIndexByName(t *testing.T) {
	containers := []corev1.Container{
		{
			Name: "container-0",
		},
		{
			Name: "container-1",
		},
		{
			Name: "container-2",
		},
	}

	stsBuilder := defaultStatefulSetBuilder().SetPodTemplateSpec(podTemplateWithContainers(containers))
	idx, err := stsBuilder.GetContainerIndexByName("container-0")

	assert.NoError(t, err)
	assert.NotEqual(t, -1, idx)
	assert.Equal(t, 0, idx)

	idx, err = stsBuilder.GetContainerIndexByName("container-1")

	assert.NoError(t, err)
	assert.NotEqual(t, -1, idx)
	assert.Equal(t, 1, idx)

	idx, err = stsBuilder.GetContainerIndexByName("container-2")

	assert.NoError(t, err)
	assert.NotEqual(t, -1, idx)
	assert.Equal(t, 2, idx)

	idx, err = stsBuilder.GetContainerIndexByName("doesnt-exist")

	assert.Error(t, err)
	assert.Equal(t, -1, idx)
}

func TestAddVolumeAndMount(t *testing.T) {
	var stsBuilder *statefulset.Builder
	var sts appsv1.StatefulSet
	var err error
	vmd := statefulset.VolumeMountData{
		MountPath: "mount-path",
		Name:      "mount-name",
		ReadOnly:  true,
		Volume:    statefulset.CreateVolumeFromConfigMap("mount-name", "config-map"),
	}

	stsBuilder = defaultStatefulSetBuilder().SetPodTemplateSpec(podTemplateWithContainers([]corev1.Container{{Name: "container-name"}})).AddVolumeAndMount(vmd, "container-name")
	sts, err = stsBuilder.Build()

	// assert container was correctly updated with the volumes
	assert.NoError(t, err, "volume should successfully mount when the container exists")
	assert.Len(t, sts.Spec.Template.Spec.Containers[0].VolumeMounts, 1, "volume mount should have been added to the container in the stateful set")
	assert.Equal(t, sts.Spec.Template.Spec.Containers[0].VolumeMounts[0].Name, "mount-name")
	assert.Equal(t, sts.Spec.Template.Spec.Containers[0].VolumeMounts[0].MountPath, "mount-path")

	// assert the volumes were added to the podspec template
	assert.Len(t, sts.Spec.Template.Spec.Volumes, 1)
	assert.Equal(t, sts.Spec.Template.Spec.Volumes[0].Name, "mount-name")
	assert.NotNil(t, sts.Spec.Template.Spec.Volumes[0].ConfigMap, "volume should have been configured from a config map source")
	assert.Nil(t, sts.Spec.Template.Spec.Volumes[0].Secret, "volume should not have been configured from a secret source")

	stsBuilder = defaultStatefulSetBuilder().SetPodTemplateSpec(podTemplateWithContainers([]corev1.Container{{Name: "container-0"}, {Name: "container-1"}})).AddVolumeAndMount(vmd, "container-0")
	sts, err = stsBuilder.Build()

	assert.NoError(t, err, "volume should successfully mount when the container exists")

	secretVmd := statefulset.VolumeMountData{
		MountPath: "mount-path-secret",
		Name:      "mount-name-secret",
		ReadOnly:  true,
		Volume:    statefulset.CreateVolumeFromSecret("mount-name-secret", "secret"),
	}

	// add a 2nd container to previously defined stsBuilder
	sts, err = stsBuilder.AddVolumeAndMount(secretVmd, "container-1").Build()

	assert.NoError(t, err, "volume should successfully mount when the container exists")
	assert.Len(t, sts.Spec.Template.Spec.Containers[1].VolumeMounts, 1, "volume mount should have been added to the container in the stateful set")
	assert.Equal(t, sts.Spec.Template.Spec.Containers[1].VolumeMounts[0].Name, "mount-name-secret")
	assert.Equal(t, sts.Spec.Template.Spec.Containers[1].VolumeMounts[0].MountPath, "mount-path-secret")

	assert.Len(t, sts.Spec.Template.Spec.Volumes, 2)
	assert.Equal(t, sts.Spec.Template.Spec.Volumes[1].Name, "mount-name-secret")
	assert.Nil(t, sts.Spec.Template.Spec.Volumes[1].ConfigMap, "volume should not have been configured from a config map source")
	assert.NotNil(t, sts.Spec.Template.Spec.Volumes[1].Secret, "volume should have been configured from a secret source")
}

func TestAddVolumeClaimTemplates(t *testing.T) {
	claim := corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: "claim-0",
		},
	}
	mount := corev1.VolumeMount{
		Name: "mount-0",
	}
	sts, err := defaultStatefulSetBuilder().SetPodTemplateSpec(podTemplateWithContainers([]corev1.Container{{Name: "container-name"}})).AddVolumeClaimTemplates([]corev1.PersistentVolumeClaim{claim}).AddVolumeMounts("container-name", []corev1.VolumeMount{mount}).Build()

	assert.NoError(t, err)
	assert.Len(t, sts.Spec.VolumeClaimTemplates, 1)
	assert.Equal(t, sts.Spec.VolumeClaimTemplates[0].Name, "claim-0")
	assert.Len(t, sts.Spec.Template.Spec.Containers[0].VolumeMounts, 1)
	assert.Equal(t, sts.Spec.Template.Spec.Containers[0].VolumeMounts[0].Name, "mount-0")
}

func TestBuildStructImmutable(t *testing.T) {
	labels := map[string]string{"label_1": "a", "label_2": "b"}
	stsBuilder := defaultStatefulSetBuilder().SetLabels(labels).SetReplicas(2)
	var sts appsv1.StatefulSet
	var err error
	sts, err = stsBuilder.Build()
	assert.NoError(t, err)
	assert.Len(t, sts.Labels, 2)
	assert.Equal(t, *sts.Spec.Replicas, int32(2))

	delete(labels, "label_2")
	// checks that modifying the underlying object did not change the built statefulset
	assert.Len(t, sts.Labels, 2)
	assert.Equal(t, *sts.Spec.Replicas, int32(2))
	sts, err = stsBuilder.Build()
	assert.NoError(t, err)
	assert.Len(t, sts.Labels, 1)
	assert.Equal(t, *sts.Spec.Replicas, int32(2))
}

func defaultStatefulSetBuilder() *statefulset.Builder {
	return statefulset.NewBuilder().
		SetName(TestName).
		SetNamespace(TestNamespace).
		SetServiceName(fmt.Sprintf("%s-svc", TestName)).
		SetLabels(map[string]string{})
}

func podTemplateWithContainers(containers []corev1.Container) corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: containers,
		},
	}
}

func TestBuildStatefulSet_SortedEnvVariables(t *testing.T) {
	podTemplateSpec := podTemplateWithContainers([]corev1.Container{{Name: "container-name"}})
	podTemplateSpec.Spec.Containers[0].Env = []corev1.EnvVar{
		{Name: "one", Value: "X"},
		{Name: "two", Value: "Y"},
		{Name: "three", Value: "Z"},
	}
	sts, err := defaultStatefulSetBuilder().SetPodTemplateSpec(podTemplateSpec).Build()
	assert.NoError(t, err)
	expectedVars := []corev1.EnvVar{
		{Name: "one", Value: "X"},
		{Name: "three", Value: "Z"},
		{Name: "two", Value: "Y"},
	}
	assert.Equal(t, expectedVars, sts.Spec.Template.Spec.Containers[0].Env)
}

func getDefaultContainer() corev1.Container {
	return corev1.Container{
		Name:  "container-0",
		Image: "image-0",
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{
				Path: "/foo",
			}},
			PeriodSeconds: 10,
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name: "container-0.volume-mount-0",
			},
		},
	}
}

func getCustomContainer() corev1.Container {
	return corev1.Container{
		Name:  "container-1",
		Image: "image-1",
	}
}

func getDefaultPodSpec() corev1.PodTemplateSpec {
	initContainer := getDefaultContainer()
	initContainer.Name = "init-container-default"
	return corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-default-name",
			Namespace: "my-default-namespace",
			Labels:    map[string]string{"app": "operator"},
		},
		Spec: corev1.PodSpec{
			NodeSelector: map[string]string{
				"node-0": "node-0",
			},
			ServiceAccountName:            "my-default-service-account",
			TerminationGracePeriodSeconds: int64Ref(12),
			ActiveDeadlineSeconds:         int64Ref(10),
			Containers:                    []corev1.Container{getDefaultContainer()},
			InitContainers:                []corev1.Container{initContainer},
			Affinity:                      affinity("hostname", "default"),
		},
	}
}

func affinity(antiAffinityKey, nodeAffinityKey string) *corev1.Affinity {
	return &corev1.Affinity{
		PodAntiAffinity: &corev1.PodAntiAffinity{
			PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{{
				PodAffinityTerm: corev1.PodAffinityTerm{
					TopologyKey: antiAffinityKey,
				},
			}},
		},
		NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{
				MatchFields: []corev1.NodeSelectorRequirement{{
					Key: nodeAffinityKey,
				}},
			}}},
		},
	}
}

func getCustomPodSpec() corev1.PodTemplateSpec {
	initContainer := getCustomContainer()
	initContainer.Name = "init-container-custom"
	return corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{"custom": "some"},
		},
		Spec: corev1.PodSpec{
			NodeSelector: map[string]string{
				"node-1": "node-1",
			},
			ServiceAccountName:            "my-service-account-override",
			TerminationGracePeriodSeconds: int64Ref(11),
			NodeName:                      "my-node-name",
			RestartPolicy:                 corev1.RestartPolicyAlways,
			Containers:                    []corev1.Container{getCustomContainer()},
			InitContainers:                []corev1.Container{initContainer},
			Affinity:                      affinity("zone", "custom"),
		},
	}
}

func TestMergePodSpecsEmptyCustom(t *testing.T) {
	defaultPodSpec := getDefaultPodSpec()
	customPodSpecTemplate := corev1.PodTemplateSpec{}

	mergedPodTemplateSpec := merge.PodTemplateSpecs(defaultPodSpec, customPodSpecTemplate)
	assert.Equal(t, "my-default-service-account", mergedPodTemplateSpec.Spec.ServiceAccountName)
	assert.Equal(t, int64Ref(12), mergedPodTemplateSpec.Spec.TerminationGracePeriodSeconds)

	assert.Equal(t, "my-default-name", mergedPodTemplateSpec.Name)
	assert.Equal(t, "my-default-namespace", mergedPodTemplateSpec.Namespace)
	assert.Equal(t, int64Ref(10), mergedPodTemplateSpec.Spec.ActiveDeadlineSeconds)

	// ensure collections have been merged
	assert.Contains(t, mergedPodTemplateSpec.Spec.NodeSelector, "node-0")
	assert.Len(t, mergedPodTemplateSpec.Spec.Containers, 1)
	assert.Equal(t, "container-0", mergedPodTemplateSpec.Spec.Containers[0].Name)
	assert.Equal(t, "image-0", mergedPodTemplateSpec.Spec.Containers[0].Image)
	assert.Equal(t, "container-0.volume-mount-0", mergedPodTemplateSpec.Spec.Containers[0].VolumeMounts[0].Name)
	assert.Len(t, mergedPodTemplateSpec.Spec.InitContainers, 1)
	assert.Equal(t, "init-container-default", mergedPodTemplateSpec.Spec.InitContainers[0].Name)
}

func TestMergePodSpecsEmptyDefault(t *testing.T) {
	defaultPodSpec := corev1.PodTemplateSpec{}
	customPodSpecTemplate := getCustomPodSpec()

	mergedPodTemplateSpec := merge.PodTemplateSpecs(customPodSpecTemplate, defaultPodSpec)

	assert.Equal(t, "my-service-account-override", mergedPodTemplateSpec.Spec.ServiceAccountName)
	assert.Equal(t, int64Ref(11), mergedPodTemplateSpec.Spec.TerminationGracePeriodSeconds)
	assert.Equal(t, "my-node-name", mergedPodTemplateSpec.Spec.NodeName)
	assert.Equal(t, corev1.RestartPolicy("Always"), mergedPodTemplateSpec.Spec.RestartPolicy)

	assert.Len(t, mergedPodTemplateSpec.Spec.Containers, 1)
	assert.Equal(t, "container-1", mergedPodTemplateSpec.Spec.Containers[0].Name)
	assert.Equal(t, "image-1", mergedPodTemplateSpec.Spec.Containers[0].Image)
	assert.Len(t, mergedPodTemplateSpec.Spec.InitContainers, 1)
	assert.Equal(t, "init-container-custom", mergedPodTemplateSpec.Spec.InitContainers[0].Name)
}

func TestMergePodSpecsBoth(t *testing.T) {
	defaultPodSpec := getDefaultPodSpec()
	customPodSpecTemplate := getCustomPodSpec()

	var mergedPodTemplateSpec corev1.PodTemplateSpec

	// multiple merges must give the same result
	for i := 0; i < 3; i++ {
		mergedPodTemplateSpec = merge.PodTemplateSpecs(defaultPodSpec, customPodSpecTemplate)
		// ensure values that were specified in the custom pod spec template remain unchanged
		assert.Equal(t, "my-service-account-override", mergedPodTemplateSpec.Spec.ServiceAccountName)
		assert.Equal(t, int64Ref(11), mergedPodTemplateSpec.Spec.TerminationGracePeriodSeconds)
		assert.Equal(t, "my-node-name", mergedPodTemplateSpec.Spec.NodeName)
		assert.Equal(t, corev1.RestartPolicy("Always"), mergedPodTemplateSpec.Spec.RestartPolicy)

		// ensure values from the default pod spec template have been merged in
		assert.Equal(t, "my-default-name", mergedPodTemplateSpec.Name)
		assert.Equal(t, "my-default-namespace", mergedPodTemplateSpec.Namespace)
		assert.Equal(t, int64Ref(10), mergedPodTemplateSpec.Spec.ActiveDeadlineSeconds)

		// ensure collections have been merged
		assert.Contains(t, mergedPodTemplateSpec.Spec.NodeSelector, "node-0")
		assert.Contains(t, mergedPodTemplateSpec.Spec.NodeSelector, "node-1")
		assert.Len(t, mergedPodTemplateSpec.Spec.Containers, 2)
		assert.Equal(t, "container-0", mergedPodTemplateSpec.Spec.Containers[0].Name)
		assert.Equal(t, "image-0", mergedPodTemplateSpec.Spec.Containers[0].Image)
		assert.Equal(t, "container-0.volume-mount-0", mergedPodTemplateSpec.Spec.Containers[0].VolumeMounts[0].Name)
		assert.Equal(t, "container-1", mergedPodTemplateSpec.Spec.Containers[1].Name)
		assert.Equal(t, "image-1", mergedPodTemplateSpec.Spec.Containers[1].Image)
		assert.Len(t, mergedPodTemplateSpec.Spec.InitContainers, 2)
		assert.Equal(t, "init-container-custom", mergedPodTemplateSpec.Spec.InitContainers[0].Name)
		assert.Equal(t, "init-container-default", mergedPodTemplateSpec.Spec.InitContainers[1].Name)

		// ensure labels were appended
		assert.Len(t, mergedPodTemplateSpec.Labels, 2)
		assert.Contains(t, mergedPodTemplateSpec.Labels, "app")
		assert.Contains(t, mergedPodTemplateSpec.Labels, "custom")

		// ensure the pointers are not the same
		assert.NotEqual(t, mergedPodTemplateSpec.Spec.Affinity, defaultPodSpec.Spec.Affinity)

		// ensure the affinity rules slices were overridden
		assert.Equal(t, affinity("zone", "custom"), mergedPodTemplateSpec.Spec.Affinity)
	}
}

func TestMergeSpec(t *testing.T) {
	t.Run("Add Container to PodSpecTemplate", func(t *testing.T) {
		sts, err := defaultStatefulSetBuilder().Build()
		assert.NoError(t, err)
		customSts, err := defaultStatefulSetBuilder().SetPodTemplateSpec(podTemplateWithContainers([]corev1.Container{{Name: "container-0"}})).Build()
		assert.NoError(t, err)

		mergedSpec := merge.StatefulSetSpecs(sts.Spec, customSts.Spec)
		assert.Contains(t, mergedSpec.Template.Spec.Containers, corev1.Container{Name: "container-0"})
	})
	t.Run("Change terminationGracePeriodSeconds", func(t *testing.T) {
		sts, err := defaultStatefulSetBuilder().Build()
		assert.NoError(t, err)
		sts.Spec.Template.Spec.TerminationGracePeriodSeconds = int64Ref(30)
		customSts, err := defaultStatefulSetBuilder().SetPodTemplateSpec(podTemplateWithContainers([]corev1.Container{{Name: "container-0"}})).Build()
		sts.Spec.Template.Spec.TerminationGracePeriodSeconds = int64Ref(600)
		assert.NoError(t, err)

		mergedSpec := merge.StatefulSetSpecs(sts.Spec, customSts.Spec)
		assert.Contains(t, mergedSpec.Template.Spec.Containers, corev1.Container{Name: "container-0"})
		assert.Equal(t, mergedSpec.Template.Spec.TerminationGracePeriodSeconds, int64Ref(600))
	})
	t.Run("Containers are added to existing list", func(t *testing.T) {
		sts, err := defaultStatefulSetBuilder().SetPodTemplateSpec(podTemplateWithContainers([]corev1.Container{{Name: "container-0"}})).Build()
		assert.NoError(t, err)
		customSts, err := defaultStatefulSetBuilder().SetPodTemplateSpec(podTemplateWithContainers([]corev1.Container{{Name: "container-1"}})).Build()
		assert.NoError(t, err)

		mergedSpec := merge.StatefulSetSpecs(sts.Spec, customSts.Spec)
		assert.Len(t, mergedSpec.Template.Spec.Containers, 2)
		assert.Contains(t, mergedSpec.Template.Spec.Containers, corev1.Container{Name: "container-0"})
		assert.Contains(t, mergedSpec.Template.Spec.Containers, corev1.Container{Name: "container-1"})
	})
	t.Run("Cannot change fields in the StatefulSet outside of the spec", func(t *testing.T) {
		sts, err := defaultStatefulSetBuilder().Build()
		assert.NoError(t, err)
		customSts, err := defaultStatefulSetBuilder().Build()
		assert.NoError(t, err)
		customSts.Annotations = map[string]string{
			"some-annotation": "some-value",
		}
		mergedSts := merge.StatefulSets(sts, customSts)
		assert.NotContains(t, mergedSts.Annotations, "some-annotation")
	})
	t.Run("change fields in the StatefulSet the Operator doesn't touch", func(t *testing.T) {
		sts, err := defaultStatefulSetBuilder().Build()
		assert.NoError(t, err)
		customSts, err := defaultStatefulSetBuilder().AddVolumeClaimTemplates([]corev1.PersistentVolumeClaim{{
			ObjectMeta: metav1.ObjectMeta{
				Name: "my-volume-claim",
			},
		}}).Build()
		assert.NoError(t, err)

		mergedSpec := merge.StatefulSetSpecs(sts.Spec, customSts.Spec)
		assert.Len(t, mergedSpec.VolumeClaimTemplates, 1)
		assert.Equal(t, "my-volume-claim", mergedSpec.VolumeClaimTemplates[0].Name)
	})
	t.Run("Volume Claim Templates are added to existing StatefulSet", func(t *testing.T) {
		sts, err := defaultStatefulSetBuilder().AddVolumeClaimTemplates([]corev1.PersistentVolumeClaim{{
			ObjectMeta: metav1.ObjectMeta{
				Name: "my-volume-claim-0",
			},
		}}).Build()

		assert.NoError(t, err)
		customSts, err := defaultStatefulSetBuilder().AddVolumeClaimTemplates([]corev1.PersistentVolumeClaim{{
			ObjectMeta: metav1.ObjectMeta{
				Name: "my-volume-claim-1",
			},
		}}).Build()
		assert.NoError(t, err)

		mergedSpec := merge.StatefulSetSpecs(sts.Spec, customSts.Spec)
		assert.Len(t, mergedSpec.VolumeClaimTemplates, 2)
		assert.Equal(t, "my-volume-claim-0", mergedSpec.VolumeClaimTemplates[0].Name)
		assert.Equal(t, "my-volume-claim-1", mergedSpec.VolumeClaimTemplates[1].Name)
	})

	t.Run("Volume Claim Templates are changed by name", func(t *testing.T) {
		sts, err := defaultStatefulSetBuilder().AddVolumeClaimTemplates([]corev1.PersistentVolumeClaim{{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "my-volume-claim-0",
				Namespace: "first-ns",
			},
		}}).Build()

		assert.NoError(t, err)
		customSts, err := defaultStatefulSetBuilder().AddVolumeClaimTemplates([]corev1.PersistentVolumeClaim{{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "my-volume-claim-0",
				Namespace: "new-ns",
			},
		}}).Build()
		assert.NoError(t, err)

		mergedSpec := merge.StatefulSetSpecs(sts.Spec, customSts.Spec)
		assert.Len(t, mergedSpec.VolumeClaimTemplates, 1)
		assert.Equal(t, "my-volume-claim-0", mergedSpec.VolumeClaimTemplates[0].Name)
		assert.Equal(t, "new-ns", mergedSpec.VolumeClaimTemplates[0].Namespace)
	})

	t.Run("Volume Claims are added", func(t *testing.T) {
		sts, err := defaultStatefulSetBuilder().
			SetPodTemplateSpec(getDefaultPodSpec()).
			AddVolumeAndMount(statefulset.VolumeMountData{
				MountPath: "path",
				Name:      "vol-0",
				ReadOnly:  false,
				Volume: corev1.Volume{
					Name: "vol-0",
				},
			}, "container-0").Build()
		assert.NoError(t, err)
		customSts, err := defaultStatefulSetBuilder().
			SetPodTemplateSpec(getDefaultPodSpec()).
			AddVolumeAndMount(statefulset.VolumeMountData{
				MountPath: "path-1",
				Name:      "vol-1",
				ReadOnly:  false,
				Volume: corev1.Volume{
					Name: "vol-1",
				},
			}, "container-0").
			AddVolumeAndMount(statefulset.VolumeMountData{
				MountPath: "path-2",
				Name:      "vol-2",
				ReadOnly:  false,
				Volume: corev1.Volume{
					Name: "vol-2",
				},
			}, "container-0").Build()
		assert.NoError(t, err)

		mergedSpec := merge.StatefulSetSpecs(sts.Spec, customSts.Spec)

		assert.Len(t, mergedSpec.Template.Spec.Volumes, 3)
		for i, vol := range mergedSpec.Template.Spec.Volumes {
			assert.Equal(t, fmt.Sprintf("vol-%d", i), vol.Name)
		}
	})

	t.Run("Custom StatefulSet zero values don't override operator configured ones", func(t *testing.T) {
		sts, err := defaultStatefulSetBuilder().SetServiceName("service-name").Build()
		assert.NoError(t, err)
		customSts, err := defaultStatefulSetBuilder().SetServiceName("").Build()
		assert.NoError(t, err)
		mergedSpec := merge.StatefulSetSpecs(sts.Spec, customSts.Spec)
		assert.Equal(t, mergedSpec.ServiceName, "service-name")
	})
}

func TestMergingVolumeMounts(t *testing.T) {
	container0 := corev1.Container{
		Name: "container-0",
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "database-scripts",
				MountPath: "/opt/scripts",
				SubPath:   "",
			},
			{
				Name:      "data",
				MountPath: "/data",
				SubPath:   "data",
			},
			{
				Name:      "data",
				MountPath: "/journal",
				SubPath:   "journal",
			},
			{
				Name:      "data",
				MountPath: "/var/log/mongodb-mms-automation",
				SubPath:   "logs",
			},
		},
	}

	container1 := corev1.Container{
		Name: "container-0",
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "test-volume",
				MountPath: "/somewhere",
				SubPath:   "",
			},
		},
	}

	podSpec0 := corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				container0,
			},
		},
	}

	podSpec1 := corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				container1,
			},
		},
	}

	merged := merge.PodTemplateSpecs(podSpec0, podSpec1)

	assert.Len(t, merged.Spec.Containers, 1)
	mounts := merged.Spec.Containers[0].VolumeMounts

	assert.Equal(t, container0.VolumeMounts[1], mounts[0])
	assert.Equal(t, container0.VolumeMounts[2], mounts[1])
	assert.Equal(t, container0.VolumeMounts[3], mounts[2])
	assert.Equal(t, container0.VolumeMounts[0], mounts[3])
	assert.Equal(t, container1.VolumeMounts[0], mounts[4])
}
