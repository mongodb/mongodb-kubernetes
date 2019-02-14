package operator

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "k8s.io/api/apps/v1"

	"k8s.io/apimachinery/pkg/api/resource"

	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestBuildStatefulSet_PersistentFlag(t *testing.T) {
	set := defaultSetHelper().SetPersistence(nil).BuildStatefulSet()
	assert.Len(t, set.Spec.VolumeClaimTemplates, 1)
	assert.Len(t, set.Spec.Template.Spec.Containers[0].VolumeMounts, 3)

	set = defaultSetHelper().SetPersistence(util.BooleanRef(true)).BuildStatefulSet()
	assert.Len(t, set.Spec.VolumeClaimTemplates, 1)
	assert.Len(t, set.Spec.Template.Spec.Containers[0].VolumeMounts, 3)

	set = defaultSetHelper().SetPersistence(util.BooleanRef(false)).BuildStatefulSet()
	assert.Len(t, set.Spec.VolumeClaimTemplates, 0)
	assert.Len(t, set.Spec.Template.Spec.Containers[0].VolumeMounts, 0)
}

// Test for backward compatibility, works the same as TestBuildStatefulSet_PersistentVolumeClaimSingle except for
// absent label selector
func TestBuildStatefulSet_PersistentVolumeClaimDeprecated(t *testing.T) {
	podSpec := mongodb.PodSpecWrapper{
		MongoDbPodSpec: mongodb.MongoDbPodSpec{
			MongoDbPodSpecStandard: mongodb.MongoDbPodSpecStandard{StorageClass: "fast", Storage: "5G"}, PodAntiAffinityTopologyKey: ""},
		Default: NewDefaultPodSpec()}
	set := defaultSetHelper().SetPodSpec(podSpec).BuildStatefulSet()

	checkPvClaims(t, set, []*corev1.PersistentVolumeClaim{pvClaim(util.PvcNameData, "5G", util.StringRef("fast"), nil)})

	checkMounts(t, set, []*corev1.VolumeMount{
		volMount(util.PvcNameData, util.PvcMountPathData, util.PvcNameData),
		volMount(util.PvcNameData, util.PvcMountPathJournal, util.PvcNameJournal),
		volMount(util.PvcNameData, util.PvcMountPathLogs, util.PvcNameLogs),
	})
}

// TestBuildStatefulSet_PersistentVolumeClaimSingle checks that one persistent volume claim is created that is mounted by
// 3 points
func TestBuildStatefulSet_PersistentVolumeClaimSingle(t *testing.T) {
	selector := &metav1.LabelSelector{MatchLabels: map[string]string{"app": "foo"}}
	// todo add builders to avoid cumbersome structs
	podSpec := mongodb.PodSpecWrapper{
		MongoDbPodSpec: mongodb.MongoDbPodSpec{MongoDbPodSpecStandard: mongodb.MongoDbPodSpecStandard{
			Persistence: &mongodb.Persistence{SingleConfig: &mongodb.PersistenceConfig{Storage: "40G", StorageClass: util.StringRef("fast"), LabelSelector: selector}}}, PodAntiAffinityTopologyKey: ""},
		Default: NewDefaultPodSpec()}
	set := defaultSetHelper().SetPodSpec(podSpec).BuildStatefulSet()

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
	podSpec := mongodb.PodSpecWrapper{
		MongoDbPodSpec: mongodb.MongoDbPodSpec{MongoDbPodSpecStandard: mongodb.MongoDbPodSpecStandard{
			Persistence: &mongodb.Persistence{MultipleConfig: &mongodb.MultiplePersistenceConfig{
				Data:    &mongodb.PersistenceConfig{Storage: "40G", StorageClass: util.StringRef("fast")},
				Logs:    &mongodb.PersistenceConfig{Storage: "3G", StorageClass: util.StringRef("slow"), LabelSelector: selector},
				Journal: &mongodb.PersistenceConfig{Storage: "500M", StorageClass: util.StringRef("fast"), LabelSelector: selector2},
			}}}, PodAntiAffinityTopologyKey: ""},
		Default: NewDefaultPodSpec()}
	set := defaultSetHelper().SetPodSpec(podSpec).BuildStatefulSet()

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
	podSpec := mongodb.PodSpecWrapper{
		MongoDbPodSpec: mongodb.MongoDbPodSpec{MongoDbPodSpecStandard: mongodb.MongoDbPodSpecStandard{
			Persistence: &mongodb.Persistence{MultipleConfig: &mongodb.MultiplePersistenceConfig{
				Data: &mongodb.PersistenceConfig{Storage: "40G", StorageClass: util.StringRef("fast")},
			}}}, PodAntiAffinityTopologyKey: ""},
		Default: NewDefaultPodSpec()}
	set := defaultSetHelper().SetPodSpec(podSpec).BuildStatefulSet()

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
	podSpec := mongodb.PodSpecWrapper{
		MongoDbPodSpec: mongodb.MongoDbPodSpec{
			MongoDbPodSpecStandard: mongodb.MongoDbPodSpecStandard{
				NodeAffinity: &nodeAffinity,
				PodAffinity:  &podAffinity,
			},
			PodAntiAffinityTopologyKey: "nodeId",
		},
		Default: NewDefaultPodSpec()}

	spec := basePodSpec("s", podSpec, defaultPodVars())

	assert.Equal(t, nodeAffinity, *spec.Affinity.NodeAffinity)
	assert.Equal(t, podAffinity, *spec.Affinity.PodAffinity)
	assert.Len(t, spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution, 1)
	assert.Len(t, spec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution, 0)
	term := spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution[0]
	assert.Equal(t, int32(100), term.Weight)
	assert.Equal(t, map[string]string{POD_ANTI_AFFINITY_LABEL_KEY: "s"}, term.PodAffinityTerm.LabelSelector.MatchLabels)
	assert.Equal(t, "nodeId", term.PodAffinityTerm.TopologyKey)
}

// TestBasePodSpec_AntiAffinityDefaultTopology checks that the default topology key is created if the topology key is
// not specified
func TestBasePodSpec_AntiAffinityDefaultTopology(t *testing.T) {
	podSpec := mongodb.PodSpecWrapper{MongoDbPodSpec: mongodb.MongoDbPodSpec{}, Default: NewDefaultPodSpec()}
	spec := basePodSpec("s", podSpec, defaultPodVars())

	term := spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution[0]
	assert.Equal(t, int32(100), term.Weight)
	assert.Equal(t, map[string]string{POD_ANTI_AFFINITY_LABEL_KEY: "s"}, term.PodAffinityTerm.LabelSelector.MatchLabels)
	assert.Equal(t, util.DefaultAntiAffinityTopologyKey, term.PodAffinityTerm.TopologyKey)
}

// TestBasePodSpec_ImagePullSecrets verifies that 'spec.ImagePullSecrets' is created only if env variable
// IMAGE_PULL_SECRETS is initialized
func TestBasePodSpec_ImagePullSecrets(t *testing.T) {
	podSpec := mongodb.PodSpecWrapper{MongoDbPodSpec: mongodb.MongoDbPodSpec{}, Default: NewDefaultPodSpec()}
	spec := basePodSpec("s", podSpec, defaultPodVars())
	assert.Nil(t, spec.ImagePullSecrets)

	_ = os.Setenv(util.AutomationAgentPullSecrets, "foo")
	spec = basePodSpec("s", podSpec, defaultPodVars())
	assert.Equal(t, []corev1.LocalObjectReference{{Name: "foo"}}, spec.ImagePullSecrets)

	// Cleaning the state (there is no tear down in go test :( )
	InitDefaultEnvVariables()
}

func checkPvClaims(t *testing.T, set *v1.StatefulSet, expectedClaims []*corev1.PersistentVolumeClaim) {
	assert.Len(t, set.Spec.VolumeClaimTemplates, len(expectedClaims))

	for i, c := range expectedClaims {
		assert.Equal(t, c, &set.Spec.VolumeClaimTemplates[i])
	}
}
func checkMounts(t *testing.T, set *v1.StatefulSet, expectedMounts []*corev1.VolumeMount) {
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

func TestBasePodSpec_FsGroup(t *testing.T) {
	spec := basePodSpec("s", defaultPodSpec(), defaultPodVars())
	assert.Len(t, spec.InitContainers, 0)
	require.NotNil(t, spec.SecurityContext)
	assert.Equal(t, util.Int64Ref(util.FsGroup), spec.SecurityContext.FSGroup)
	assert.Equal(t, util.Int64Ref(util.RunAsUser), spec.SecurityContext.RunAsUser)

	os.Setenv(util.ManagedSecurityContextEnv, "true")
	spec = basePodSpec("s", defaultPodSpec(), defaultPodVars())
	require.Nil(t, spec.SecurityContext)

	// restoring
	InitDefaultEnvVariables()
}

func baseSetHelper() *StatefulSetHelper {
	st := DefaultStandaloneBuilder().Build()
	return (&KubeHelper{newMockedClient(st)}).NewStatefulSetHelper(st)
}

func baseSetHelperDelayed(delayMillis time.Duration) *StatefulSetHelper {
	st := DefaultStandaloneBuilder().Build()
	return (&KubeHelper{newMockedClientDelayed(st, delayMillis)}).NewStatefulSetHelper(st)
}

func defaultPodSpec() mongodb.PodSpecWrapper {
	return mongodb.PodSpecWrapper{MongoDbPodSpec: mongodb.MongoDbPodSpec{PodAntiAffinityTopologyKey: "nodeId"}, Default: NewDefaultPodSpec()}
}

func defaultSetHelper() *StatefulSetHelper {
	return baseSetHelper().SetLogger(zap.S()).SetPodSpec(defaultPodSpec()).SetPodVars(defaultPodVars()).SetService("test-service")
}

func defaultPodVars() *PodVars {
	return &PodVars{AgentAPIKey: "a", BaseURL: "http://localhost:8080", ProjectID: "myProject", User: "user@some.com"}
}
