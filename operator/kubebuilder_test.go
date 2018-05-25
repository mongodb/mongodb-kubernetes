package operator

import (
	"testing"

	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1beta1"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestBuildStatefulSet_PersistentFlag(t *testing.T) {
	set := buildStatefulSet(&mongodb.MongoDbStandalone{}, "s", "p", "ns", "c", "a", 1, nil, podSpec())
	assert.Len(t, set.Spec.VolumeClaimTemplates, 1)
	assert.Len(t, set.Spec.Template.Spec.Containers[0].VolumeMounts, 1)

	set = buildStatefulSet(&mongodb.MongoDbStandalone{}, "s", "p", "ns", "c", "a", 1, BooleanRef(true), podSpec())
	assert.Len(t, set.Spec.VolumeClaimTemplates, 1)
	assert.Len(t, set.Spec.Template.Spec.Containers[0].VolumeMounts, 1)

	set = buildStatefulSet(&mongodb.MongoDbStandalone{}, "s", "p", "ns", "c", "a", 1, BooleanRef(false), podSpec())
	assert.Len(t, set.Spec.VolumeClaimTemplates, 0)
	assert.Len(t, set.Spec.Template.Spec.Containers[0].VolumeMounts, 0)
}

func TestBuildStatefulSet_PersistentVolumeClaim(t *testing.T) {
	podSpec := mongodb.PodSpecWrapper{
		mongodb.MongoDbPodSpec{
			mongodb.MongoDbPodSpecStandalone{StorageClass: "fast", Storage: "5G"}, ""},
		NewDefaultPodSpec()}
	set := buildStatefulSet(&mongodb.MongoDbStandalone{}, "s", "p", "ns", "c", "a", 1, nil, podSpec)

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

	spec := basePodSpec("s", "c", "k", BooleanRef(false), podSpec)

	assert.Equal(t, nodeAffinity, *spec.Affinity.NodeAffinity)
	assert.Equal(t, podAffinity, *spec.Affinity.PodAffinity)
	assert.Len(t, spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution, 1)
	assert.Len(t, spec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution, 0)
	term := spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution[0]
	assert.Equal(t, int32(100), term.Weight)
	assert.Equal(t, map[string]string{APP_LABEL_KEY: "s"}, term.PodAffinityTerm.LabelSelector.MatchLabels)
	assert.Equal(t, "nodeId", term.PodAffinityTerm.TopologyKey)
}

// TestBasePodSpec_AntiAffinitySkipped checks that pod anti affinity rule is not created if topology key is not provided
func TestBasePodSpec_AntiAffinitySkipped(t *testing.T) {
	podSpec := mongodb.PodSpecWrapper{mongodb.MongoDbPodSpec{}, NewDefaultPodSpec()}
	spec := basePodSpec("s", "c", "k", BooleanRef(false), podSpec)
	assert.Nil(t, spec.Affinity.PodAntiAffinity)
}

func podSpec() mongodb.PodSpecWrapper {
	return mongodb.PodSpecWrapper{mongodb.MongoDbPodSpec{PodAntiAffinityTopologyKey: "nodeId"}, NewDefaultPodSpec()}
}
