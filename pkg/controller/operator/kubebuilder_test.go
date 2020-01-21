package operator

import (
	"os"
	"testing"
	"time"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/stretchr/testify/require"

	appsv1 "k8s.io/api/apps/v1"

	"k8s.io/apimachinery/pkg/api/resource"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

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
	selector := &metav1.LabelSelector{MatchLabels: map[string]string{"app": "foo"}}
	// todo add builders to avoid cumbersome structs
	podSpec := mdbv1.PodSpecWrapper{
		MongoDbPodSpec: mdbv1.MongoDbPodSpec{MongoDbPodSpecStandard: mdbv1.MongoDbPodSpecStandard{
			Persistence: &mdbv1.Persistence{SingleConfig: &mdbv1.PersistenceConfig{Storage: "40G", StorageClass: util.StringRef("fast"), LabelSelector: selector}}}, PodAntiAffinityTopologyKey: ""},
		Default: NewDefaultPodSpec()}
	set, _ := defaultSetHelper().SetPodSpec(podSpec).BuildStatefulSet()

	checkPvClaims(t, set, []*corev1.PersistentVolumeClaim{pvClaim(util.PvcNameData, "40G", util.StringRef("fast"), selector)})

	checkMounts(t, set, []*corev1.VolumeMount{
		volMount(util.PvcNameData, util.PvcMountPathData, util.PvcNameData),
		volMount(util.PvcNameData, util.PvcMountPathJournal, util.PvcNameJournal),
		volMount(util.PvcNameData, util.PvcMountPathLogs, util.PvcNameLogs),
	})
}

// TestBuildStatefulSet_PersistentVolumeClaimMultiple checks multiple volumes for multiple mounts. Note, that subpaths
// for mount points are not created (unlike in single mode)
func TestBuildStatefulSet_PersistentVolumeClaimMultiple(t *testing.T) {
	selector := &metav1.LabelSelector{MatchLabels: map[string]string{"app": "bar"}}
	selector2 := &metav1.LabelSelector{MatchLabels: map[string]string{"app": "foo"}}
	// todo add builders to avoid cumbersome structs
	podSpec := mdbv1.PodSpecWrapper{
		MongoDbPodSpec: mdbv1.MongoDbPodSpec{MongoDbPodSpecStandard: mdbv1.MongoDbPodSpecStandard{
			Persistence: &mdbv1.Persistence{MultipleConfig: &mdbv1.MultiplePersistenceConfig{
				Data:    &mdbv1.PersistenceConfig{Storage: "40G", StorageClass: util.StringRef("fast")},
				Logs:    &mdbv1.PersistenceConfig{Storage: "3G", StorageClass: util.StringRef("slow"), LabelSelector: selector},
				Journal: &mdbv1.PersistenceConfig{Storage: "500M", StorageClass: util.StringRef("fast"), LabelSelector: selector2},
			}}}, PodAntiAffinityTopologyKey: ""},
		Default: NewDefaultPodSpec()}
	set, _ := defaultSetHelper().SetPodSpec(podSpec).BuildStatefulSet()

	checkPvClaims(t, set, []*corev1.PersistentVolumeClaim{
		pvClaim(util.PvcNameData, "40G", util.StringRef("fast"), nil),
		pvClaim(util.PvcNameJournal, "500M", util.StringRef("fast"), selector2),
		pvClaim(util.PvcNameLogs, "3G", util.StringRef("slow"), selector),
	})

	checkMounts(t, set, []*corev1.VolumeMount{
		volMount(util.PvcNameData, util.PvcMountPathData, ""),
		volMount(util.PvcNameJournal, util.PvcMountPathJournal, ""),
		volMount(util.PvcNameLogs, util.PvcMountPathLogs, ""),
	})
}

// TestBuildStatefulSet_PersistentVolumeClaimMultipleDefaults checks the scenario when storage is provided only for one
// mount point. Default values are expected to be used for two others
func TestBuildStatefulSet_PersistentVolumeClaimMultipleDefaults(t *testing.T) {
	podSpec := mdbv1.PodSpecWrapper{
		MongoDbPodSpec: mdbv1.MongoDbPodSpec{MongoDbPodSpecStandard: mdbv1.MongoDbPodSpecStandard{
			Persistence: &mdbv1.Persistence{MultipleConfig: &mdbv1.MultiplePersistenceConfig{
				Data: &mdbv1.PersistenceConfig{Storage: "40G", StorageClass: util.StringRef("fast")},
			}}}, PodAntiAffinityTopologyKey: ""},
		Default: NewDefaultPodSpec()}
	set, _ := defaultSetHelper().SetPodSpec(podSpec).BuildStatefulSet()

	checkPvClaims(t, set, []*corev1.PersistentVolumeClaim{
		pvClaim(util.PvcNameData, "40G", util.StringRef("fast"), nil),
		pvClaim(util.PvcNameJournal, util.DefaultJournalStorageSize, nil, nil),
		pvClaim(util.PvcNameLogs, util.DefaultLogsStorageSize, nil, nil),
	})

	checkMounts(t, set, []*corev1.VolumeMount{
		volMount(util.PvcNameData, util.PvcMountPathData, ""),
		volMount(util.PvcNameJournal, util.PvcMountPathJournal, ""),
		volMount(util.PvcNameLogs, util.PvcMountPathLogs, ""),
	})
}

func TestBuildAppDbStatefulSetDefault(t *testing.T) {
	appDbSts, _ := defaultSetHelper().BuildAppDBStatefulSet()
	podSpecTemplate := appDbSts.Spec.Template.Spec
	assert.Len(t, podSpecTemplate.Containers, 1, "Should have only the db")
	assert.Equal(t, "mongodb-enterprise-appdb", podSpecTemplate.Containers[0].Name, "Database container should always be first")
}

func TestBuildAppDbStatefulSetWithSideCar(t *testing.T) {
	appDbSts, _ := defaultSetHelper().SetPodTemplateSpec(&corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			NodeName: "some-node-name",
			Hostname: "some-host-name",
			Containers: []corev1.Container{{
				Name:  "my-custom-container",
				Image: "my-custom-image",
				VolumeMounts: []corev1.VolumeMount{{
					Name: "my-volume-mount",
				}},
			}},
			RestartPolicy: corev1.RestartPolicyAlways,
		},
	}).BuildAppDBStatefulSet()
	podSpecTemplate := appDbSts.Spec.Template.Spec
	assert.Len(t, podSpecTemplate.Containers, 2, "Should have 2 containers now")
	assert.Equal(t, "mongodb-enterprise-appdb", podSpecTemplate.Containers[0].Name, "Database container should always be first")
	assert.Equal(t, "my-custom-container", podSpecTemplate.Containers[1].Name, "Custom container to be second")
}

func TestBasePodSpec_Affinity(t *testing.T) {
	nodeAffinity := corev1.NodeAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
			NodeSelectorTerms: []corev1.NodeSelectorTerm{{
				MatchExpressions: []corev1.NodeSelectorRequirement{{
					Key:    "dc",
					Values: []string{"US-EAST"},
				}}},
			}},
	}
	podAffinity := corev1.PodAffinity{
		PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{{
			Weight: 50,
			PodAffinityTerm: corev1.PodAffinityTerm{
				LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web-server"}},
				TopologyKey:   "rack",
			},
		}}}
	podSpec := mdbv1.PodSpecWrapper{
		MongoDbPodSpec: mdbv1.MongoDbPodSpec{
			MongoDbPodSpecStandard: mdbv1.MongoDbPodSpecStandard{
				NodeAffinity: &nodeAffinity,
				PodAffinity:  &podAffinity,
			},
			PodAntiAffinityTopologyKey: "nodeId",
		},
		Default: NewDefaultPodSpec()}

	spec := getDefaultPodSpecTemplate("s", podSpec, defaultPodVars(), map[string]string{}, map[string]string{}, []corev1.Container{}).Spec
	assert.Equal(t, nodeAffinity, *spec.Affinity.NodeAffinity)
	assert.Equal(t, podAffinity, *spec.Affinity.PodAffinity)
	assert.Len(t, spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution, 1)
	assert.Len(t, spec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution, 0)
	term := spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution[0]
	assert.Equal(t, int32(100), term.Weight)
	assert.Equal(t, map[string]string{PodAntiAffinityLabelKey: "s"}, term.PodAffinityTerm.LabelSelector.MatchLabels)
	assert.Equal(t, "nodeId", term.PodAffinityTerm.TopologyKey)
}

// TestBasePodSpec_AntiAffinityDefaultTopology checks that the default topology key is created if the topology key is
// not specified
func TestBasePodSpec_AntiAffinityDefaultTopology(t *testing.T) {
	podSpec := mdbv1.PodSpecWrapper{MongoDbPodSpec: mdbv1.MongoDbPodSpec{}, Default: NewDefaultPodSpec()}
	spec := getDefaultPodSpecTemplate("s", podSpec, defaultPodVars(), map[string]string{}, map[string]string{}, []corev1.Container{}).Spec

	term := spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution[0]
	assert.Equal(t, int32(100), term.Weight)
	assert.Equal(t, map[string]string{PodAntiAffinityLabelKey: "s"}, term.PodAffinityTerm.LabelSelector.MatchLabels)
	assert.Equal(t, util.DefaultAntiAffinityTopologyKey, term.PodAffinityTerm.TopologyKey)
}

// TestBasePodSpec_ImagePullSecrets verifies that 'spec.ImagePullSecrets' is created only if env variable
// IMAGE_PULL_SECRETS is initialized
func TestBasePodSpec_ImagePullSecrets(t *testing.T) {
	podSpec := mdbv1.PodSpecWrapper{MongoDbPodSpec: mdbv1.MongoDbPodSpec{}, Default: NewDefaultPodSpec()}
	spec := getDefaultPodSpecTemplate("s", podSpec, defaultPodVars(), map[string]string{}, map[string]string{}, []corev1.Container{}).Spec
	assert.Nil(t, spec.ImagePullSecrets)

	_ = os.Setenv(util.AutomationAgentPullSecrets, "foo")

	rs := DefaultReplicaSetBuilder().Build()
	kubeManager := newMockedManager(rs)
	addKubernetesTlsResources(kubeManager.client, rs)
	reconciler := newReplicaSetReconciler(kubeManager, om.NewEmptyMockedOmConnection)
	stsHelper := *reconciler.kubeHelper.NewStatefulSetHelper(rs).SetPodVars(defaultPodVars())

	podSpecTemplate, _ := getMergedDefaultPodSpecTemplate(stsHelper, map[string]string{}, []corev1.Container{})
	assert.Equal(t, []corev1.LocalObjectReference{{Name: "foo"}}, podSpecTemplate.Spec.ImagePullSecrets)

	// Cleaning the state (there is no tear down in go test :( )
	InitDefaultEnvVariables()
}

// TestBasePodSpec_TerminationGracePeriodSeconds verifies that the TerminationGracePeriodSeconds is set to 600 seconds
func TestBasePodSpec_TerminationGracePeriodSeconds(t *testing.T) {
	podSpec := mdbv1.PodSpecWrapper{MongoDbPodSpec: mdbv1.MongoDbPodSpec{}, Default: NewDefaultPodSpec()}
	spec := getDefaultPodSpecTemplate("s", podSpec, defaultPodVars(), map[string]string{}, map[string]string{}, []corev1.Container{}).Spec
	assert.Equal(t, util.Int64Ref(600), spec.TerminationGracePeriodSeconds)
}

func checkPvClaims(t *testing.T, set *appsv1.StatefulSet, expectedClaims []*corev1.PersistentVolumeClaim) {
	assert.Len(t, set.Spec.VolumeClaimTemplates, len(expectedClaims))

	for i, c := range expectedClaims {
		assert.Equal(t, c, &set.Spec.VolumeClaimTemplates[i])
	}
}
func checkMounts(t *testing.T, set *appsv1.StatefulSet, expectedMounts []*corev1.VolumeMount) {
	assert.Len(t, set.Spec.Template.Spec.Containers[0].VolumeMounts, len(expectedMounts))

	for i, c := range expectedMounts {
		assert.Equal(t, c, &set.Spec.Template.Spec.Containers[0].VolumeMounts[i])
	}
}

func pvClaim(pvName, size string, storageClass *string, selector *metav1.LabelSelector) *corev1.PersistentVolumeClaim {
	quantity, _ := resource.ParseQuantity(size)
	expectedClaim := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: pvName,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: quantity},
			},
			StorageClassName: storageClass,
			Selector:         selector,
		}}
	return expectedClaim
}

func volMount(pvName, mountPath, subPath string) *corev1.VolumeMount {
	return &corev1.VolumeMount{Name: pvName, MountPath: mountPath, SubPath: subPath}
}

func TestDefaultPodSpec_FsGroup(t *testing.T) {
	rs := DefaultReplicaSetBuilder().Build()
	kubeManager := newMockedManager(rs)
	addKubernetesTlsResources(kubeManager.client, rs)
	reconciler := newReplicaSetReconciler(kubeManager, om.NewEmptyMockedOmConnection)
	stsHelper := *reconciler.kubeHelper.NewStatefulSetHelper(rs).SetPodVars(defaultPodVars())

	podSpecTemplate, _ := getMergedDefaultPodSpecTemplate(stsHelper, map[string]string{}, []corev1.Container{})

	spec := podSpecTemplate.Spec
	assert.Len(t, spec.InitContainers, 0)
	require.NotNil(t, spec.SecurityContext)
	assert.Equal(t, util.Int64Ref(util.FsGroup), spec.SecurityContext.FSGroup)
	assert.Equal(t, util.Int64Ref(util.RunAsUser), spec.SecurityContext.RunAsUser)

	os.Setenv(util.ManagedSecurityContextEnv, "true")

	podSpecTemplate, _ = getMergedDefaultPodSpecTemplate(stsHelper, map[string]string{}, []corev1.Container{})
	spec = podSpecTemplate.Spec

	InitDefaultEnvVariables()
}

func TestPodSpec_Requirements(t *testing.T) {
	podSpec := mdbv1.PodSpecWrapper{
		MongoDbPodSpec: newMongoDbPodSpec(
			mdbv1.MongoDbPodSpecStandard{CpuRequests: "0.1", MemoryRequests: "512M", Cpu: "0.3", Memory: "1012M"}, "")}

	container := newDatabaseContainer(podSpec, defaultPodVars())
	expectedLimits := corev1.ResourceList{corev1.ResourceCPU: parseQuantityOrZero("0.3"), corev1.ResourceMemory: parseQuantityOrZero("1012M")}
	expectedRequests := corev1.ResourceList{corev1.ResourceCPU: parseQuantityOrZero("0.1"), corev1.ResourceMemory: parseQuantityOrZero("512M")}
	assert.Equal(t, expectedLimits, container.Resources.Limits)
	assert.Equal(t, expectedRequests, container.Resources.Requests)
}

func TestService_merge0(t *testing.T) {
	dst := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "my-service", Namespace: "my-namespace"}}
	src := &corev1.Service{}

	mergeServices(dst, src)
	assert.Equal(t, "my-service", dst.ObjectMeta.Name)
	assert.Equal(t, "my-namespace", dst.ObjectMeta.Namespace)

	// Name and Namespace will not be copied over.
	src = &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "new-service", Namespace: "new-namespace"}}
	mergeServices(dst, src)
	assert.Equal(t, "my-service", dst.ObjectMeta.Name)
	assert.Equal(t, "my-namespace", dst.ObjectMeta.Namespace)
}

func TestService_NodePortIsNotOverwritten(t *testing.T) {
	dst := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "my-service", Namespace: "my-namespace"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{NodePort: 30030}}},
	}
	src := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "my-service", Namespace: "my-namespace"},
		Spec:       corev1.ServiceSpec{},
	}

	mergeServices(dst, src)
	assert.Equal(t, int32(30030), dst.Spec.Ports[0].NodePort)
}

func TestService_NodePortIsNotOverwrittenIfNoNodePortIsSpecified(t *testing.T) {
	dst := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "my-service", Namespace: "my-namespace"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{NodePort: 30030}}},
	}
	src := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "my-service", Namespace: "my-namespace"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{}}},
	}

	mergeServices(dst, src)
	assert.Equal(t, int32(30030), dst.Spec.Ports[0].NodePort)
}

func TestService_NodePortIsKeptWhenChangingServiceType(t *testing.T) {
	dst := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "my-service", Namespace: "my-namespace"},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{NodePort: 30030}},
			Type:  corev1.ServiceTypeLoadBalancer,
		},
	}
	src := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "my-service", Namespace: "my-namespace"},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{NodePort: 30099}},
			Type:  corev1.ServiceTypeNodePort,
		},
	}
	mergeServices(dst, src)
	assert.Equal(t, int32(30099), dst.Spec.Ports[0].NodePort)

	src = &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "my-service", Namespace: "my-namespace"},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{NodePort: 30011}},
			Type:  corev1.ServiceTypeLoadBalancer,
		},
	}

	mergeServices(dst, src)
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

	src := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: annotationsSrc,
		},
	}
	dst := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "my-service", Namespace: "my-namespace",
			Annotations: annotationsDest,
		},
	}
	mergeServices(dst, src)
	assert.Len(t, dst.ObjectMeta.Annotations, 3)
	assert.Equal(t, dst.ObjectMeta.Annotations["annotation0"], "valueXXXX")
}

// ******************************** Helper methods *******************************************

func baseSetHelper() *StatefulSetHelper {
	st := DefaultStandaloneBuilder().Build()
	return (&KubeHelper{newMockedClient(st)}).NewStatefulSetHelper(st)
}

// baseSetHelperDelayed returns a delayed StatefulSetHelper.
// This helper will not get to Success state right away, but will take at least `delay`.
func baseSetHelperDelayed(delay time.Duration) *StatefulSetHelper {
	st := DefaultStandaloneBuilder().Build()
	return (&KubeHelper{newMockedClientDelayed(st, delay)}).NewStatefulSetHelper(st)
}

func defaultPodSpecWrapper() mdbv1.PodSpecWrapper {
	return mdbv1.PodSpecWrapper{MongoDbPodSpec: mdbv1.MongoDbPodSpec{PodAntiAffinityTopologyKey: "nodeId"}, Default: NewDefaultPodSpec()}
}

func defaultSetHelper() *StatefulSetHelper {
	return baseSetHelper().
		SetPodSpec(defaultPodSpecWrapper()).
		SetPodVars(defaultPodVars()).
		SetService("test-service").
		SetSecurity(&mdbv1.Security{
			TLSConfig: &mdbv1.TLSConfig{},
			Authentication: &mdbv1.Authentication{
				Modes: []string{},
			},
		})
}

func defaultPodVars() *PodVars {
	return &PodVars{AgentAPIKey: "a", BaseURL: "http://localhost:8080", ProjectID: "myProject", User: "user@some.com"}
}
