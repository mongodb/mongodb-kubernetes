package operator

import (
	"context"
	"os"
	"path"
	"testing"
	"time"

	"github.com/10gen/ops-manager-kubernetes/pkg/kube/statefulset"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/mock"
	"github.com/10gen/ops-manager-kubernetes/pkg/kube/service"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"

	"k8s.io/apimachinery/pkg/api/resource"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestApplyPodSpec(t *testing.T) {
	sts := defaultSetHelper()
	container := newMongoDBContainer(defaultPodVars())
	template := getDatabasePodTemplate(*sts, nil, "service-account", container)
	assert.Equal(t, "default-sts-name", template.Spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution[0].PodAffinityTerm.LabelSelector.MatchLabels[PodAntiAffinityLabelKey])
	emptyPodSpecWrapper := mdbv1.NewEmptyPodSpecWrapperBuilder().Build()

	var result corev1.PodTemplateSpec
	// merge with empty is giving back the original template
	result = configureDefaultAffinityAndResources(template, emptyPodSpecWrapper, "sts-name")
	// configureDefaultAffinityAndResources overrides the pod-anti-affinity with the last string parameter
	// overriding it to allow for equality test
	template.Spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution[0].PodAffinityTerm.LabelSelector.MatchLabels[PodAntiAffinityLabelKey] = "sts-name"
	assert.Equal(t, result, template)
}

func TestBuildStatefulSet_PersistentFlag(t *testing.T) {
	set, _ := defaultSetHelper().SetPersistence(nil).BuildStatefulSet()
	assert.Len(t, set.Spec.VolumeClaimTemplates, 1)
	assert.Len(t, set.Spec.Template.Spec.Containers[0].VolumeMounts, 3)

	set, _ = defaultSetHelper().SetPersistence(util.BooleanRef(true)).BuildStatefulSet()
	assert.Len(t, set.Spec.VolumeClaimTemplates, 1)
	assert.Len(t, set.Spec.Template.Spec.Containers[0].VolumeMounts, 3)

	set, _ = defaultSetHelper().SetPersistence(util.BooleanRef(false)).BuildStatefulSet()
	assert.Len(t, set.Spec.VolumeClaimTemplates, 0)
	assert.Len(t, set.Spec.Template.Spec.Containers[0].VolumeMounts, 0)
}

// TestBuildStatefulSet_PersistentVolumeClaimSingle checks that one persistent volume claim is created that is mounted by
// 3 points
func TestBuildStatefulSet_PersistentVolumeClaimSingle(t *testing.T) {
	labels := map[string]string{"app": "foo"}
	persistence := mdbv1.NewPersistenceBuilder("40G").SetStorageClass("fast").SetLabelSelector(labels)
	podSpec := mdbv1.NewPodSpecWrapperBuilder().SetSinglePersistence(persistence).Build()
	set, _ := defaultSetHelper().SetPodSpec(podSpec).BuildStatefulSet()

	checkPvClaims(t, set, []corev1.PersistentVolumeClaim{pvClaim(util.PvcNameData, "40G", stringutil.Ref("fast"), labels)})

	checkMounts(t, set, []corev1.VolumeMount{
		volMount(util.PvcNameData, util.PvcMountPathData, util.PvcNameData),
		volMount(util.PvcNameData, util.PvcMountPathJournal, util.PvcNameJournal),
		volMount(util.PvcNameData, util.PvcMountPathLogs, util.PvcNameLogs),
	})
}

//TestBuildStatefulSet_PersistentVolumeClaimMultiple checks multiple volumes for multiple mounts. Note, that subpaths
//for mount points are not created (unlike in single mode)
func TestBuildStatefulSet_PersistentVolumeClaimMultiple(t *testing.T) {
	labels1 := map[string]string{"app": "bar"}
	labels2 := map[string]string{"app": "foo"}
	podSpec := mdbv1.NewPodSpecWrapperBuilder().SetMultiplePersistence(
		mdbv1.NewPersistenceBuilder("40G").SetStorageClass("fast"),
		mdbv1.NewPersistenceBuilder("3G").SetStorageClass("slow").SetLabelSelector(labels1),
		mdbv1.NewPersistenceBuilder("500M").SetStorageClass("fast").SetLabelSelector(labels2),
	).Build()
	set, _ := defaultSetHelper().SetPodSpec(podSpec).BuildStatefulSet()

	checkPvClaims(t, set, []corev1.PersistentVolumeClaim{
		pvClaim(util.PvcNameData, "40G", stringutil.Ref("fast"), nil),
		pvClaim(util.PvcNameJournal, "3G", stringutil.Ref("slow"), labels1),
		pvClaim(util.PvcNameLogs, "500M", stringutil.Ref("fast"), labels2),
	})

	checkMounts(t, set, []corev1.VolumeMount{
		volMount(util.PvcNameData, util.PvcMountPathData, ""),
		volMount(util.PvcNameJournal, util.PvcMountPathJournal, ""),
		volMount(util.PvcNameLogs, util.PvcMountPathLogs, ""),
	})
}

// TestBuildStatefulSet_PersistentVolumeClaimMultipleDefaults checks the scenario when storage is provided only for one
// mount point. Default values are expected to be used for two others
func TestBuildStatefulSet_PersistentVolumeClaimMultipleDefaults(t *testing.T) {
	podSpec := mdbv1.NewPodSpecWrapperBuilder().SetMultiplePersistence(
		mdbv1.NewPersistenceBuilder("40G").SetStorageClass("fast"),
		nil,
		nil).
		Build()
	set, _ := defaultSetHelper().SetPodSpec(podSpec).BuildStatefulSet()

	checkPvClaims(t, set, []corev1.PersistentVolumeClaim{
		pvClaim(util.PvcNameData, "40G", stringutil.Ref("fast"), nil),
		pvClaim(util.PvcNameJournal, util.DefaultJournalStorageSize, nil, nil),
		pvClaim(util.PvcNameLogs, util.DefaultLogsStorageSize, nil, nil),
	})

	checkMounts(t, set, []corev1.VolumeMount{
		volMount(util.PvcNameData, util.PvcMountPathData, ""),
		volMount(util.PvcNameJournal, util.PvcMountPathJournal, ""),
		volMount(util.PvcNameLogs, util.PvcMountPathLogs, ""),
	})
}

func TestBuildAppDbStatefulSetDefault(t *testing.T) {
	appDbSts, err := defaultAppDbSetHelper().BuildAppDbStatefulSet()
	assert.NoError(t, err)
	podSpecTemplate := appDbSts.Spec.Template.Spec
	assert.Len(t, podSpecTemplate.Containers, 1, "Should have only the db")
	assert.Equal(t, "mongodb-enterprise-appdb", podSpecTemplate.Containers[0].Name, "Database container should always be first")
}

func TestReadPemHashFromSecret(t *testing.T) {
	stsHelper := baseSetHelper()

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: stsHelper.Name + "-cert", Namespace: mock.TestNamespace},
		Data:       map[string][]byte{"hello": []byte("world")},
	}

	assert.Empty(t, stsHelper.readPemHashFromSecret(), "secret does not exist so pem hash should be empty")
	stsHelper.Helper.client.Update(context.TODO(), secret)
	assert.NotEmpty(t, stsHelper.readPemHashFromSecret(), "pem hash should be read from the secret")
}

func TestBasePodSpec_Affinity(t *testing.T) {
	nodeAffinity := defaultNodeAffinity()
	podAffinity := defaultPodAffinity()

	podSpec := mdbv1.NewPodSpecWrapperBuilder().
		SetNodeAffinity(nodeAffinity).
		SetPodAffinity(podAffinity).
		SetPodAntiAffinityTopologyKey("nodeId").
		Build()
	setHelper := defaultSetHelper().SetName("s").SetPodSpec(podSpec)

	template := getDatabasePodTemplate(*setHelper, nil, "testAccount", corev1.Container{})
	spec := template.Spec
	assert.Equal(t, nodeAffinity, *spec.Affinity.NodeAffinity)
	assert.Equal(t, podAffinity, *spec.Affinity.PodAffinity)
	assert.Len(t, spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution, 1)
	assert.Len(t, spec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution, 0)
	term := spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution[0]
	assert.Equal(t, int32(100), term.Weight)
	assert.Equal(t, map[string]string{PodAntiAffinityLabelKey: "s"}, term.PodAffinityTerm.LabelSelector.MatchLabels)
	assert.Equal(t, "nodeId", term.PodAffinityTerm.TopologyKey)
	//assert.Equal(t, "testAccount", spec.ServiceAccountName)
}

// TestBasePodSpec_AntiAffinityDefaultTopology checks that the default topology key is created if the topology key is
// not specified
func TestBasePodSpec_AntiAffinityDefaultTopology(t *testing.T) {
	helper := defaultSetHelper().SetPodSpec(mdbv1.NewPodSpecWrapperBuilder().Build())

	template := getDatabasePodTemplate(*helper, map[string]string{}, "", corev1.Container{})
	spec := template.Spec
	term := spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution[0]
	assert.Equal(t, int32(100), term.Weight)
	assert.Equal(t, map[string]string{PodAntiAffinityLabelKey: "default-sts-name"}, term.PodAffinityTerm.LabelSelector.MatchLabels)
	assert.Equal(t, util.DefaultAntiAffinityTopologyKey, term.PodAffinityTerm.TopologyKey)
}

// TestBasePodSpec_ImagePullSecrets verifies that 'spec.ImagePullSecrets' is created only if env variable
// IMAGE_PULL_SECRETS is initialized
func TestBasePodSpec_ImagePullSecrets(t *testing.T) {
	// Cleaning the state (there is no tear down in go test :( )
	defer InitDefaultEnvVariables()

	template := getDatabasePodTemplate(*defaultSetHelper(), map[string]string{}, "", corev1.Container{})
	assert.Nil(t, template.Spec.ImagePullSecrets)

	_ = os.Setenv(util.ImagePullSecrets, "foo")

	template = getDatabasePodTemplate(*defaultSetHelper(), map[string]string{}, "", corev1.Container{})
	assert.Equal(t, []corev1.LocalObjectReference{{Name: "foo"}}, template.Spec.ImagePullSecrets)

}

// TestBasePodSpec_TerminationGracePeriodSeconds verifies that the TerminationGracePeriodSeconds is set to 600 seconds
func TestBasePodSpec_TerminationGracePeriodSeconds(t *testing.T) {
	template := getDatabasePodTemplate(*defaultSetHelper(), map[string]string{}, "", corev1.Container{})
	assert.Equal(t, util.Int64Ref(600), template.Spec.TerminationGracePeriodSeconds)
}

func checkPvClaims(t *testing.T, set appsv1.StatefulSet, expectedClaims []corev1.PersistentVolumeClaim) {
	assert.Len(t, set.Spec.VolumeClaimTemplates, len(expectedClaims))

	for i, c := range expectedClaims {
		assert.Equal(t, c, set.Spec.VolumeClaimTemplates[i])
	}
}
func checkMounts(t *testing.T, set appsv1.StatefulSet, expectedMounts []corev1.VolumeMount) {
	assert.Len(t, set.Spec.Template.Spec.Containers[0].VolumeMounts, len(expectedMounts))

	for i, c := range expectedMounts {
		assert.Equal(t, c, set.Spec.Template.Spec.Containers[0].VolumeMounts[i])
	}
}

func pvClaim(pvName, size string, storageClass *string, labels map[string]string) corev1.PersistentVolumeClaim {
	quantity, _ := resource.ParseQuantity(size)
	expectedClaim := corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: pvName,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: quantity},
			},
			StorageClassName: storageClass,
		}}
	if len(labels) > 0 {
		expectedClaim.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
	}
	return expectedClaim
}

func volMount(pvName, mountPath, subPath string) corev1.VolumeMount {
	return corev1.VolumeMount{Name: pvName, MountPath: mountPath, SubPath: subPath}
}

func TestDefaultPodSpec_FsGroup(t *testing.T) {
	defer InitDefaultEnvVariables()

	podSpecTemplate := getDatabasePodTemplate(*defaultSetHelper(), map[string]string{}, "", corev1.Container{})

	spec := podSpecTemplate.Spec
	assert.Len(t, spec.InitContainers, 0)
	require.NotNil(t, spec.SecurityContext)
	assert.Equal(t, util.Int64Ref(util.FsGroup), spec.SecurityContext.FSGroup)

	_ = os.Setenv(util.ManagedSecurityContextEnv, "true")

	podSpecTemplate = getDatabasePodTemplate(*defaultSetHelper(), map[string]string{}, "", corev1.Container{})
	assert.Nil(t, podSpecTemplate.Spec.SecurityContext)
	// TODO: assert the container security context

}

func TestPodSpec_Requirements(t *testing.T) {
	podSpec := mdbv1.NewPodSpecWrapperBuilder().
		SetCpuRequests("0.1").
		SetMemoryRequest("512M").
		SetCpu("0.3").
		SetMemory("1012M").
		Build()

	setHelper := defaultSetHelper().SetPodSpec(podSpec)

	podSpecTemplate := getDatabasePodTemplate(*setHelper, map[string]string{}, "", newMongoDBContainer(defaultPodVars()))

	container := podSpecTemplate.Spec.Containers[0]
	expectedLimits := corev1.ResourceList{corev1.ResourceCPU: parseQuantityOrZero("0.3"), corev1.ResourceMemory: parseQuantityOrZero("1012M")}
	expectedRequests := corev1.ResourceList{corev1.ResourceCPU: parseQuantityOrZero("0.1"), corev1.ResourceMemory: parseQuantityOrZero("512M")}
	assert.Equal(t, expectedLimits, container.Resources.Limits)
	assert.Equal(t, expectedRequests, container.Resources.Requests)
}

func TestService_merge0(t *testing.T) {
	dst := corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "my-service", Namespace: "my-namespace"}}
	src := corev1.Service{}

	dst = service.Merge(dst, src)
	assert.Equal(t, "my-service", dst.ObjectMeta.Name)
	assert.Equal(t, "my-namespace", dst.ObjectMeta.Namespace)

	// Name and Namespace will not be copied over.
	src = corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "new-service", Namespace: "new-namespace"}}
	dst = service.Merge(dst, src)
	assert.Equal(t, "my-service", dst.ObjectMeta.Name)
	assert.Equal(t, "my-namespace", dst.ObjectMeta.Namespace)
}

func TestService_NodePortIsNotOverwritten(t *testing.T) {
	dst := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "my-service", Namespace: "my-namespace"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{NodePort: 30030}}},
	}
	src := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "my-service", Namespace: "my-namespace"},
		Spec:       corev1.ServiceSpec{},
	}

	dst = service.Merge(dst, src)
	assert.Equal(t, int32(30030), dst.Spec.Ports[0].NodePort)
}

func TestService_NodePortIsNotOverwrittenIfNoNodePortIsSpecified(t *testing.T) {
	dst := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "my-service", Namespace: "my-namespace"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{NodePort: 30030}}},
	}
	src := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "my-service", Namespace: "my-namespace"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{}}},
	}

	dst = service.Merge(dst, src)
	assert.Equal(t, int32(30030), dst.Spec.Ports[0].NodePort)
}

func TestService_NodePortIsKeptWhenChangingServiceType(t *testing.T) {
	dst := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "my-service", Namespace: "my-namespace"},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{NodePort: 30030}},
			Type:  corev1.ServiceTypeLoadBalancer,
		},
	}
	src := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "my-service", Namespace: "my-namespace"},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{NodePort: 30099}},
			Type:  corev1.ServiceTypeNodePort,
		},
	}
	dst = service.Merge(dst, src)
	assert.Equal(t, int32(30099), dst.Spec.Ports[0].NodePort)

	src = corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "my-service", Namespace: "my-namespace"},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{NodePort: 30011}},
			Type:  corev1.ServiceTypeLoadBalancer,
		},
	}

	dst = service.Merge(dst, src)
	assert.Equal(t, int32(30011), dst.Spec.Ports[0].NodePort)
}

func TestService_mergeAnnotations(t *testing.T) {
	// Annotations will be added
	annotationsDest := make(map[string]string)
	annotationsDest["annotation0"] = "value0"
	annotationsDest["annotation1"] = "value1"
	annotationsSrc := make(map[string]string)
	annotationsSrc["annotation0"] = "valueXXXX"
	annotationsSrc["annotation2"] = "value2"

	src := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: annotationsSrc,
		},
	}
	dst := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "my-service", Namespace: "my-namespace",
			Annotations: annotationsDest,
		},
	}
	dst = service.Merge(dst, src)
	assert.Len(t, dst.ObjectMeta.Annotations, 3)
	assert.Equal(t, dst.ObjectMeta.Annotations["annotation0"], "valueXXXX")
}

func TestBuildBackupDaemonContainer(t *testing.T) {
	helper := testDefaultBackupSetHelper()
	container := buildBackupDaemonContainer(helper)
	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-ops-manager:4.2.0", container.Image)

	assert.Equal(t, util.BackupDaemonContainerName, container.Name)
	assert.Nil(t, container.ReadinessProbe)

	assert.Equal(t, []string{"/bin/sh", "-c", "/mongodb-ops-manager/bin/mongodb-mms stop_backup_daemon"},
		container.Lifecycle.PreStop.Exec.Command)
}

// TestOpsManagerPodTemplate_Container verifies the default OM container built by 'opsManagerPodTemplate' method
func TestOpsManagerPodTemplate_Container(t *testing.T) {
	om := DefaultOpsManagerBuilder().Build()
	helper := omSetHelperFromResource(om)
	template := opsManagerPodTemplate(map[string]string{"one": "two"}, helper, buildOpsManagerContainer(helper))

	assert.Equal(t, map[string]string{"one": "two"}, template.Labels)
	assert.Len(t, template.Spec.Containers, 1)
	container := template.Spec.Containers[0]
	assert.Equal(t, util.OpsManagerContainerName, container.Name)
	// TODO change when we stop using versioning
	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-ops-manager:4.2.0", container.Image)
	assert.Equal(t, corev1.PullNever, container.ImagePullPolicy)

	assert.Equal(t, int32(util.OpsManagerDefaultPortHTTP), container.Ports[0].ContainerPort)
	assert.Equal(t, "/monitor/health", container.ReadinessProbe.Handler.HTTPGet.Path)
	assert.Equal(t, int32(8080), container.ReadinessProbe.Handler.HTTPGet.Port.IntVal)

	assert.Equal(t, []string{"/bin/sh", "-c", "/mongodb-ops-manager/bin/mongodb-mms stop_mms"},
		container.Lifecycle.PreStop.Exec.Command)
}

func TestOpsManagerPodTemplate_ImagePullPolicy(t *testing.T) {
	defer InitDefaultEnvVariables()
	testHelper := testDefaultOMSetHelper()
	podSpecTemplate := opsManagerPodTemplate(map[string]string{}, testHelper, buildOpsManagerContainer(testHelper))
	spec := podSpecTemplate.Spec

	assert.Nil(t, spec.ImagePullSecrets)

	os.Setenv(util.ImagePullSecrets, "my-cool-secret")
	podSpecTemplate = opsManagerPodTemplate(map[string]string{}, testHelper, buildOpsManagerContainer(testHelper))
	spec = podSpecTemplate.Spec

	assert.NotNil(t, spec.ImagePullSecrets)
	assert.Equal(t, spec.ImagePullSecrets[0].Name, "my-cool-secret")
}

func TestOpsManagerPodTemplate_TerminationTimeout(t *testing.T) {
	testHelper := testDefaultOMSetHelper()
	podSpecTemplate := opsManagerPodTemplate(map[string]string{}, testHelper, buildOpsManagerContainer(testHelper))

	assert.Equal(t, int64(1800), *podSpecTemplate.Spec.TerminationGracePeriodSeconds)
}

// TestOpsManagerPodTemplate_SecurityContext verifies that security context is created correctly
// in OpsManager/BackupDaemon podTemplate. It's not built if 'MANAGED_SECURITY_CONTEXT' env var
// is set to 'true'
func TestOpsManagerPodTemplate_SecurityContext(t *testing.T) {
	defer InitDefaultEnvVariables()

	testHelper := testDefaultOMSetHelper()
	podSpecTemplate := opsManagerPodTemplate(map[string]string{}, testHelper, buildOpsManagerContainer(testHelper))

	spec := podSpecTemplate.Spec
	assert.Len(t, spec.InitContainers, 1)
	assert.Equal(t, spec.InitContainers[0].Name, "mongodb-enterprise-init-ops-manager")
	require.NotNil(t, spec.SecurityContext)
	assert.Equal(t, util.Int64Ref(util.FsGroup), spec.SecurityContext.FSGroup)

	_ = os.Setenv(util.ManagedSecurityContextEnv, "true")

	podSpecTemplate = opsManagerPodTemplate(map[string]string{}, testHelper, buildOpsManagerContainer(testHelper))
	assert.Nil(t, podSpecTemplate.Spec.SecurityContext)
}

// TestOpsManagerPodTemplate_PodSpec verifies that PodSpec is applied correctly to OpsManager/Backup pod template.
func TestOpsManagerPodTemplate_PodSpec(t *testing.T) {
	podSpec := mdbv1.NewPodSpecWrapperBuilder().
		SetPodAffinity(defaultPodAffinity()).SetNodeAffinity(defaultNodeAffinity()).SetPodAntiAffinityTopologyKey("rack").Build()
	helper := omSetHelperFromResource(DefaultOpsManagerBuilder().SetPodSpec(*podSpec).Build())
	template := opsManagerPodTemplate(map[string]string{}, helper, buildOpsManagerContainer(helper))

	spec := template.Spec
	assert.Equal(t, defaultNodeAffinity(), *spec.Affinity.NodeAffinity)
	assert.Equal(t, defaultPodAffinity(), *spec.Affinity.PodAffinity)
	term := spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution[0]
	assert.Equal(t, "rack", term.PodAffinityTerm.TopologyKey)

	req := spec.Containers[0].Resources.Limits
	assert.Len(t, req, 2)
	cpu := req[corev1.ResourceCPU]
	memory := req[corev1.ResourceMemory]
	assert.Equal(t, "1", (&cpu).String())
	assert.Equal(t, int64(500000000), (&memory).Value())

	req = spec.Containers[0].Resources.Requests
	assert.Len(t, req, 2)
	cpu = req[corev1.ResourceCPU]
	memory = req[corev1.ResourceMemory]
	assert.Equal(t, "500m", (&cpu).String())
	assert.Equal(t, int64(400000000), (&memory).Value())
}

// TestOpsManagerPodTemplate_MergePodTemplate checks the custom pod template provided by the user.
// It's supposed to override the values produced by the Operator and leave everything else as is
func TestOpsManagerPodTemplate_MergePodTemplate(t *testing.T) {
	expectedAnnotations := map[string]string{"customKey": "customVal"}
	expectedTolerations := []corev1.Toleration{{Key: "dedicated", Value: "database"}}
	newContainer := corev1.Container{
		Name:  "my-custom-container",
		Image: "my-custom-image",
	}
	podSpec := mdbv1.NewPodSpecWrapperBuilder().SetPodTemplate(&corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Annotations: expectedAnnotations},
		Spec: corev1.PodSpec{
			ServiceAccountName: "test-account",
			Tolerations:        expectedTolerations,
			Containers:         []corev1.Container{newContainer},
		},
	}).Build()
	om := DefaultOpsManagerBuilder().SetPodSpec(*podSpec).Build()
	helper := omSetHelperFromResource(om)
	template := opsManagerPodTemplate(map[string]string{"key": "value"}, helper, buildOpsManagerContainer(helper))

	operatorSts := appsv1.StatefulSet{
		Spec: appsv1.StatefulSetSpec{
			Template: template,
		},
	}

	mergedSts, err := statefulset.MergeSpec(operatorSts, &appsv1.StatefulSetSpec{
		Template: *podSpec.PodTemplate,
	})
	assert.NoError(t, err)
	template = mergedSts.Spec.Template
	// Service account gets overriden by custom pod template
	assert.Equal(t, "test-account", template.Spec.ServiceAccountName)
	assert.Equal(t, expectedAnnotations, template.Annotations)
	assert.Equal(t, expectedTolerations, template.Spec.Tolerations)
	assert.Len(t, template.Spec.Containers, 2)
	assert.Equal(t, newContainer, template.Spec.Containers[1])

	// Some validation that the Operator-made config hasn't suffered
	assert.Equal(t, map[string]string{"key": "value"}, template.Labels)
	require.NotNil(t, template.Spec.SecurityContext)
	assert.Equal(t, util.Int64Ref(util.FsGroup), template.Spec.SecurityContext.FSGroup)
	assert.Equal(t, util.OpsManagerContainerName, template.Spec.Containers[0].Name)
}

func Test_buildOpsManagerStatefulSet(t *testing.T) {
	sts, err := buildOpsManagerStatefulSet(testDefaultOMSetHelper())
	assert.NoError(t, err)
	assert.Equal(t, "testOM", sts.ObjectMeta.Name)
	assert.Equal(t, util.OpsManagerContainerName, sts.Spec.Template.Spec.Containers[0].Name)
}

func Test_buildBackupDaemonStatefulSet(t *testing.T) {
	sts, err := buildBackupDaemonStatefulSet(testDefaultBackupSetHelper())
	assert.NoError(t, err)
	assert.Equal(t, "testOM-backup-daemon", sts.ObjectMeta.Name)
	assert.Equal(t, util.BackupDaemonContainerName, sts.Spec.Template.Spec.Containers[0].Name)
	assert.Nil(t, sts.Spec.Template.Spec.Containers[0].ReadinessProbe)
}

func TestBuildOpsManagerStatefulSet_EnvVarsSorted(t *testing.T) {
	om := DefaultOpsManagerBuilder().
		AddConfiguration(util.MmsCentralUrlPropKey, "http://om-svc").
		AddConfiguration("mms.adminEmailAddr", "cloud-manager-support@mongodb.com").
		Build()
	sts, err := buildOpsManagerStatefulSet(omSetHelperFromResource(om))
	assert.NoError(t, err)

	// env vars are in sorted order
	expectedVars := []corev1.EnvVar{
		{Name: "CUSTOM_JAVA_DAEMON_OPTS", Value: "-Xmx4291m -Xms4291m"},
		{Name: "CUSTOM_JAVA_MMS_UI_OPTS", Value: "-Xmx4291m -Xms4291m"},
		{Name: "OM_PROP_mms_adminEmailAddr", Value: "cloud-manager-support@mongodb.com"},
		{Name: "OM_PROP_mms_centralUrl", Value: "http://om-svc"},
	}
	env := sts.Spec.Template.Spec.Containers[0].Env
	assert.Equal(t, expectedVars, env)
}

func TestBuildJvmParamsEnvVars_FromDefaultPodSpec(t *testing.T) {
	om := DefaultOpsManagerBuilder().
		AddConfiguration(util.MmsCentralUrlPropKey, "http://om-svc").
		AddConfiguration("mms.adminEmailAddr", "cloud-manager-support@mongodb.com").
		Build()
	helper := omSetHelperFromResource(om)

	labels := defaultPodLabels(helper.StatefulSetHelperCommon)

	newContainer := corev1.Container{
		Name:  "my-custom-container",
		Image: "my-custom-image",
	}

	template := opsManagerPodTemplate(labels, helper, newContainer)

	envVar, _ := buildJvmParamsEnvVars(om.Spec, template)
	// xmx and xms based calculated from  default container memory, requests.mem=limits.mem=5GB
	assert.Equal(t, "CUSTOM_JAVA_MMS_UI_OPTS", envVar[0].Name)
	assert.Equal(t, "-Xmx4291m -Xms4291m", envVar[0].Value)

	assert.Equal(t, "CUSTOM_JAVA_DAEMON_OPTS", envVar[1].Name)
	assert.Equal(t, "-Xmx4291m -Xms4291m", envVar[1].Value)
}

func TestBuildJvmParamsEnvVars_FromCustomContainerResource(t *testing.T) {
	om := DefaultOpsManagerBuilder().
		AddConfiguration(util.MmsCentralUrlPropKey, "http://om-svc").
		AddConfiguration("mms.adminEmailAddr", "cloud-manager-support@mongodb.com").
		Build()
	om.Spec.JVMParams = []string{"-DFakeOptionEnabled"}
	helper := omSetHelperFromResource(om)

	labels := defaultPodLabels(helper.StatefulSetHelperCommon)

	newContainer := corev1.Container{
		Name:  "my-custom-container",
		Image: "my-custom-image",
	}

	template := opsManagerPodTemplate(labels, helper, newContainer)

	unsetQuantity := *resource.NewQuantity(0, resource.BinarySI)

	template.Spec.Containers[0].Resources.Limits[corev1.ResourceMemory] = *resource.NewQuantity(268435456, resource.BinarySI)
	template.Spec.Containers[0].Resources.Requests[corev1.ResourceMemory] = unsetQuantity
	envVarLimitsOnly, _ := buildJvmParamsEnvVars(om.Spec, template)

	template.Spec.Containers[0].Resources.Requests[corev1.ResourceMemory] = *resource.NewQuantity(218435456, resource.BinarySI)
	envVarLimitsAndReqs, _ := buildJvmParamsEnvVars(om.Spec, template)

	template.Spec.Containers[0].Resources.Limits[corev1.ResourceMemory] = unsetQuantity
	envVarReqsOnly, _ := buildJvmParamsEnvVars(om.Spec, template)

	template.Spec.Containers[0].Resources.Requests[corev1.ResourceMemory] = unsetQuantity
	envVarsNoLimitsOrReqs, _ := buildJvmParamsEnvVars(om.Spec, template)

	// if only memory requests are configured, xms and xmx should be 90% of mem request
	assert.Equal(t, "-DFakeOptionEnabled -Xmx187m -Xms187m", envVarReqsOnly[0].Value)
	// both are configured, xms should be 90% of mem request, and xmx 90% of mem limit
	assert.Equal(t, "-DFakeOptionEnabled -Xmx230m -Xms187m", envVarLimitsAndReqs[0].Value)
	// if only memory limits are configured, xms and xmx should be 90% of mem limits
	assert.Equal(t, "-DFakeOptionEnabled -Xmx230m -Xms230m", envVarLimitsOnly[0].Value)
	// if neither is configured, xms/xmx params should not be added to JVM params, keeping OM defaults
	assert.Equal(t, "-DFakeOptionEnabled", envVarsNoLimitsOrReqs[0].Value)

}

func TestBaseEnvHelper(t *testing.T) {
	envVars := defaultPodVars()
	podEnv := baseEnvFrom(envVars)
	assert.Len(t, podEnv, 5)

	envVars = defaultPodVars()
	envVars.SSLRequireValidMMSServerCertificates = true
	podEnv = baseEnvFrom(envVars)
	assert.Len(t, podEnv, 6)
	assert.Equal(t, podEnv[5], corev1.EnvVar{
		Name:  util.EnvVarSSLRequireValidMMSCertificates,
		Value: "true",
	})

	envVars = defaultPodVars()
	envVars.SSLMMSCAConfigMap = "custom-ca"
	trustedCACertLocation := path.Join(CaCertMountPath, CaCertMMS)
	podEnv = baseEnvFrom(envVars)
	assert.Len(t, podEnv, 6)
	assert.Equal(t, podEnv[5], corev1.EnvVar{
		Name:  util.EnvVarSSLTrustedMMSServerCertificate,
		Value: trustedCACertLocation,
	})

	envVars = defaultPodVars()
	envVars.SSLRequireValidMMSServerCertificates = true
	envVars.SSLMMSCAConfigMap = "custom-ca"
	podEnv = baseEnvFrom(envVars)
	assert.Len(t, podEnv, 7)
	assert.Equal(t, podEnv[5], corev1.EnvVar{
		Name:  util.EnvVarSSLRequireValidMMSCertificates,
		Value: "true",
	})
	assert.Equal(t, podEnv[6], corev1.EnvVar{
		Name:  util.EnvVarSSLTrustedMMSServerCertificate,
		Value: trustedCACertLocation,
	})
}

// ******************************** Helper methods *******************************************

func baseSetHelper() *StatefulSetHelper {
	st := DefaultStandaloneBuilder().Build()
	mockedClient := mock.NewClient().WithResource(st)
	helper := NewKubeHelper(mockedClient)
	return helper.NewStatefulSetHelper(st)
}

// baseSetHelperDelayed returns a delayed StatefulSetHelper.
// This helper will not get to Success state right away, but will take at least `delay`.
func baseSetHelperDelayed(delay time.Duration) *StatefulSetHelper {
	st := DefaultStandaloneBuilder().Build()
	mockedClient := mock.NewClient().WithResource(st).WithStsCreationDelay(delay)
	helper := NewKubeHelper(mockedClient)
	return helper.NewStatefulSetHelper(st)
}

func defaultSetHelper() *StatefulSetHelper {
	return baseSetHelper().
		SetName("default-sts-name").
		SetPodSpec(mdbv1.NewEmptyPodSpecWrapperBuilder().Build()).
		SetPodVars(defaultPodVars()).
		SetService("test-service").
		SetSecurity(&mdbv1.Security{
			TLSConfig: &mdbv1.TLSConfig{},
			Authentication: &mdbv1.Authentication{
				Modes: []string{},
			},
		})
}

// defaultAppDbSetHelper builds the default statefulset helper for appdb from Ops Manager resource
func defaultAppDbSetHelper() *StatefulSetHelper {
	om := DefaultOpsManagerBuilder().Build()
	mockedClient := mock.NewClient().WithResource(&om)
	helper := NewKubeHelper(mockedClient)
	return helper.NewStatefulSetHelper(&om)
}

func omSetHelperFromResource(om mdbv1.MongoDBOpsManager) OpsManagerStatefulSetHelper {
	mockedClient := mock.NewClient()
	helper := NewKubeHelper(mockedClient)
	return *helper.NewOpsManagerStatefulSetHelper(om)
}

func backupSetHelperFromResource(om mdbv1.MongoDBOpsManager) BackupStatefulSetHelper {
	mockedClient := mock.NewClient()
	helper := NewKubeHelper(mockedClient)
	return *helper.NewBackupStatefulSetHelper(om)
}

func testDefaultOMSetHelper() OpsManagerStatefulSetHelper {
	return omSetHelperFromResource(DefaultOpsManagerBuilder().Build())
}

func testDefaultBackupSetHelper() BackupStatefulSetHelper {
	om := DefaultOpsManagerBuilder().Build()
	return backupSetHelperFromResource(om)
}

func defaultPodVars() *PodVars {
	return &PodVars{AgentAPIKey: "a", BaseURL: "http://localhost:8080", ProjectID: "myProject", User: "user@some.com"}
}

func defaultNodeAffinity() corev1.NodeAffinity {
	return corev1.NodeAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
			NodeSelectorTerms: []corev1.NodeSelectorTerm{{
				MatchExpressions: []corev1.NodeSelectorRequirement{{
					Key:    "dc",
					Values: []string{"US-EAST"},
				}}},
			}},
	}
}
func defaultPodAffinity() corev1.PodAffinity {
	return corev1.PodAffinity{
		PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{{
			Weight: 50,
			PodAffinityTerm: corev1.PodAffinityTerm{
				LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web-server"}},
				TopologyKey:   "rack",
			},
		}}}
}
