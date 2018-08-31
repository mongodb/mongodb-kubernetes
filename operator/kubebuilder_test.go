package operator

import (
	"testing"

	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/util"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestBuildStatefulSet_PersistentFlag(t *testing.T) {
	set := defaultSetHelper().SetPersistence(nil).BuildStatefulSet()
	assert.Len(t, set.Spec.VolumeClaimTemplates, 1)
	assert.Len(t, set.Spec.Template.Spec.Containers[0].VolumeMounts, 1)

	set = defaultSetHelper().SetPersistence(util.BooleanRef(true)).BuildStatefulSet()
	assert.Len(t, set.Spec.VolumeClaimTemplates, 1)
	assert.Len(t, set.Spec.Template.Spec.Containers[0].VolumeMounts, 1)

	set = defaultSetHelper().SetPersistence(util.BooleanRef(false)).BuildStatefulSet()
	assert.Len(t, set.Spec.VolumeClaimTemplates, 0)
	assert.Len(t, set.Spec.Template.Spec.Containers[0].VolumeMounts, 0)
}

func TestBuildStatefulSet_PersistentVolumeClaim(t *testing.T) {
	podSpec := mongodb.PodSpecWrapper{
		mongodb.MongoDbPodSpec{
			mongodb.MongoDbPodSpecStandalone{StorageClass: "fast", Storage: "5G"}, ""},
		NewDefaultPodSpec()}
	set := defaultSetHelper().SetPodSpec(podSpec).BuildStatefulSet()

	assert.Len(t, set.Spec.VolumeClaimTemplates, 1)
	claim := set.Spec.VolumeClaimTemplates[0]

	assert.Equal(t, PersistentVolumeClaimName, claim.ObjectMeta.Name)
	assert.Equal(t, []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, claim.Spec.AccessModes)
	assert.Equal(t, "fast", *claim.Spec.StorageClassName)
	assert.Len(t, claim.Spec.Resources.Requests, 1)
	quantity := claim.Spec.Resources.Requests[corev1.ResourceStorage]
	assert.Equal(t, int64(5000000000), (&quantity).Value())

	assert.Nil(t, claim.Spec.Selector)
	assert.Nil(t, claim.Spec.VolumeMode)
	assert.Equal(t, "", claim.Spec.VolumeName)
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
			MongoDbPodSpecStandalone: mongodb.MongoDbPodSpecStandalone{
				NodeAffinity: &nodeAffinity,
				PodAffinity:  &podAffinity,
			},
			PodAntiAffinityTopologyKey: "nodeId",
		},
		Default: NewDefaultPodSpec()}

	spec := basePodSpec("s", util.BooleanRef(false), podSpec, defaultPodVars())

	assert.Equal(t, nodeAffinity, *spec.Affinity.NodeAffinity)
	assert.Equal(t, podAffinity, *spec.Affinity.PodAffinity)
	assert.Len(t, spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution, 1)
	assert.Len(t, spec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution, 0)
	term := spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution[0]
	assert.Equal(t, int32(100), term.Weight)
	assert.Equal(t, map[string]string{APP_LABEL_KEY: "s"}, term.PodAffinityTerm.LabelSelector.MatchLabels)
	assert.Equal(t, "nodeId", term.PodAffinityTerm.TopologyKey)
}

// TestBasePodSpec_AntiAffinityDefaultTopology checks that the default topology key is created if the topology key is
// not specified
func TestBasePodSpec_AntiAffinityDefaultTopology(t *testing.T) {
	podSpec := mongodb.PodSpecWrapper{mongodb.MongoDbPodSpec{}, NewDefaultPodSpec()}
	spec := basePodSpec("s", util.BooleanRef(false), podSpec, defaultPodVars())

	term := spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution[0]
	assert.Equal(t, int32(100), term.Weight)
	assert.Equal(t, map[string]string{APP_LABEL_KEY: "s"}, term.PodAffinityTerm.LabelSelector.MatchLabels)
	assert.Equal(t, DefaultAntiAffinityTopologyKey, term.PodAffinityTerm.TopologyKey)
}

func baseSetHelper() *StatefulSetHelper {
	return (&KubeHelper{newMockedKubeApi()}).NewStatefulSetHelper(DefaultStandaloneBuilder().Build())
}

func defaultPodSpec() mongodb.PodSpecWrapper {
	return mongodb.PodSpecWrapper{mongodb.MongoDbPodSpec{PodAntiAffinityTopologyKey: "nodeId"}, NewDefaultPodSpec()}
}

func defaultSetHelper() *StatefulSetHelper {
	return baseSetHelper().SetLogger(zap.S()).SetPodSpec(defaultPodSpec()).SetPodVars(defaultPodVars()).SetService("test-service")
}

func defaultPodVars() *PodVars {
	return &PodVars{AgentApiKey: "a", BaseUrl: "http://localhost:8080", ProjectId: "myProject", User: "user@some.com"}
}
