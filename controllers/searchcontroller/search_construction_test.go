package searchcontroller

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/ptr"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	searchv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/search"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/construct"
	"github.com/mongodb/mongodb-kubernetes/pkg/statefulset"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

func resolvedSizing(t *testing.T, s *searchv1.MongoDBSearch, clusterName, shardName string) searchv1.ClusterSpec {
	t.Helper()
	sizing, err := s.ResolveSizingForClusterShard(clusterName, shardName)
	require.NoError(t, err)
	return sizing
}

func TestCreateSearchStatefulSetFunc_JVMFlags(t *testing.T) {
	testCases := []struct {
		name                 string
		userProvidedJVMFlags []string
		userProvidedMemory   string
		expectedJVMFlags     string
	}{
		// Default memory (4G), varying user JVM flags
		{
			name:                 "no jvm flags - default heap from 4Gi memory",
			userProvidedJVMFlags: nil,
			expectedJVMFlags:     `--jvm-flags "-Xmx2048m -Xms2048m"`,
		},
		{
			name:                 "user provides -Xmx only - jvm heap flag is derived from that",
			userProvidedJVMFlags: []string{"-Xmx2g"},
			expectedJVMFlags:     `--jvm-flags "-Xmx2g"`,
		},
		{
			name:                 "user provides -Xms only - jvm heap flag is derived from that",
			userProvidedJVMFlags: []string{"-Xms1g"},
			expectedJVMFlags:     `--jvm-flags "-Xms1g"`,
		},
		{
			name:                 "user provides both -Xmx and -Xms - jvm heap flags are derived from that",
			userProvidedJVMFlags: []string{"-Xmx2g", "-Xms512m"},
			expectedJVMFlags:     `--jvm-flags "-Xmx2g -Xms512m"`,
		},
		{
			name:                 "user provides non-heap flags only - jvm heap flags are derived from default memory req",
			userProvidedJVMFlags: []string{"-XX:+UseG1GC"},
			expectedJVMFlags:     `--jvm-flags "-Xmx2048m -Xms2048m -XX:+UseG1GC"`,
		},
		{
			name:                 "user provides heap and non-heap flags - jvm heap flags are derived from that",
			userProvidedJVMFlags: []string{"-Xmx2g", "-Xms512m", "-XX:+UseG1GC"},
			expectedJVMFlags:     `--jvm-flags "-Xmx2g -Xms512m -XX:+UseG1GC"`,
		},
		// Custom memory, no user JVM flags
		{
			name:               "4Gi memory - half is 2048m",
			userProvidedMemory: "4Gi",
			expectedJVMFlags:   `--jvm-flags "-Xmx2048m -Xms2048m"`,
		},
		{
			name:               "4G memory - half is 1907m",
			userProvidedMemory: "4G",
			expectedJVMFlags:   `--jvm-flags "-Xmx1907m -Xms1907m"`,
		},
		{
			name:               "512Mi memory - half is 256m",
			userProvidedMemory: "512Mi",
			expectedJVMFlags:   `--jvm-flags "-Xmx256m -Xms256m"`,
		},
		{
			name:               "8Gi memory - half is 4096m",
			userProvidedMemory: "8Gi",
			expectedJVMFlags:   `--jvm-flags "-Xmx4096m -Xms4096m"`,
		},
		{
			name:               "60Gi memory - half is exactly the 30GB cap",
			userProvidedMemory: "60Gi",
			expectedJVMFlags:   `--jvm-flags "-Xmx30720m -Xms30720m"`,
		},
		{
			name:               "128Gi memory - auto heap capped at 30GB",
			userProvidedMemory: "128Gi",
			expectedJVMFlags:   `--jvm-flags "-Xmx30720m -Xms30720m"`,
		},
		{
			name:                 "128Gi memory with user heap flags above the cap - not capped",
			userProvidedJVMFlags: []string{"-Xmx64g", "-Xms64g"},
			userProvidedMemory:   "128Gi",
			expectedJVMFlags:     `--jvm-flags "-Xmx64g -Xms64g"`,
		},
		// Custom memory + user JVM flags combined
		{
			name:                 "8Gi memory with non-heap user flags - auto heap from custom memory",
			userProvidedJVMFlags: []string{"-XX:+UseG1GC"},
			userProvidedMemory:   "8Gi",
			expectedJVMFlags:     `--jvm-flags "-Xmx4096m -Xms4096m -XX:+UseG1GC"`,
		},
		{
			name:                 "8Gi memory with user heap flags - auto heap suppressed",
			userProvidedJVMFlags: []string{"-Xmx2g"},
			userProvidedMemory:   "8Gi",
			expectedJVMFlags:     `--jvm-flags "-Xmx2g"`,
		},
		{
			name:                 "512Mi memory with user heap and non-heap flags - auto heap suppressed",
			userProvidedJVMFlags: []string{"-Xmx256m", "-Xms128m", "-XX:+UseG1GC"},
			userProvidedMemory:   "512Mi",
			expectedJVMFlags:     `--jvm-flags "-Xmx256m -Xms128m -XX:+UseG1GC"`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			search := newTestMongoDBSearch("test-search", "default", func(s *searchv1.MongoDBSearch) {
				cluster := searchv1.ClusterSpec{JVMFlags: tc.userProvidedJVMFlags}
				if tc.userProvidedMemory != "" {
					cluster.ResourceRequirements = &corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceMemory: resource.MustParse(tc.userProvidedMemory),
							corev1.ResourceCPU:    resource.MustParse("2"),
						},
					}
				}
				s.Spec.Clusters = []searchv1.ClusterSpec{cluster}
			})

			stsModification := CreateSearchStatefulSetFunc(search, resolvedSizing(t, search, "", ""), "", "", "", "", nil, "mongot:latest", false)
			sts := statefulset.New(stsModification)

			// Find the mongot container
			var mongotContainer *containerInfo
			for i := range sts.Spec.Template.Spec.Containers {
				if sts.Spec.Template.Spec.Containers[i].Name == MongotContainerName {
					mongotContainer = &containerInfo{
						args: sts.Spec.Template.Spec.Containers[i].Args,
					}
					break
				}
			}

			require.NotNil(t, mongotContainer, "mongot container not found in StatefulSet")
			require.Len(t, mongotContainer.args, 2, "the args are of form ['-c', '<script>'], that's why 2 is expected")

			script := mongotContainer.args[1]

			if tc.expectedJVMFlags != "" {
				assert.True(t, strings.Contains(script, tc.expectedJVMFlags),
					"expected args to contain %q, got %q", tc.expectedJVMFlags, script)
			}
		})
	}
}

func TestCreateSearchResourceRequirements(t *testing.T) {
	defaultCPU := construct.ParseQuantityOrZero("2")
	defaultMemory := construct.ParseQuantityOrZero("4Gi")

	testCases := []struct {
		name             string
		userRequirements *corev1.ResourceRequirements
		expectedCPU      resource.Quantity
		expectedMemory   resource.Quantity
	}{
		{
			name:             "nil requirements - full defaults",
			userRequirements: nil,
			expectedCPU:      defaultCPU,
			expectedMemory:   defaultMemory,
		},
		{
			name: "nil Requests - full defaults",
			userRequirements: &corev1.ResourceRequirements{
				Requests: nil,
			},
			expectedCPU:    defaultCPU,
			expectedMemory: defaultMemory,
		},
		{
			name: "only CPU set - memory defaults",
			userRequirements: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("4"),
				},
			},
			expectedCPU:    resource.MustParse("4"),
			expectedMemory: defaultMemory,
		},
		{
			name: "only memory set - CPU defaults",
			userRequirements: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceMemory: resource.MustParse("8Gi"),
				},
			},
			expectedCPU:    defaultCPU,
			expectedMemory: resource.MustParse("8Gi"),
		},
		{
			name: "both CPU and memory set - no defaults applied",
			userRequirements: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("8"),
					corev1.ResourceMemory: resource.MustParse("16Gi"),
				},
			},
			expectedCPU:    resource.MustParse("8"),
			expectedMemory: resource.MustParse("16Gi"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := createSearchResourceRequirements(tc.userRequirements)

			assert.True(t, result.Requests.Cpu().Equal(tc.expectedCPU),
				"expected CPU %s, got %s", tc.expectedCPU.String(), result.Requests.Cpu().String())
			assert.True(t, result.Requests.Memory().Equal(tc.expectedMemory),
				"expected memory %s, got %s", tc.expectedMemory.String(), result.Requests.Memory().String())
		})
	}

	t.Run("input requirements are not mutated", func(t *testing.T) {
		// userRequirements may point into the live CR spec (cluster or
		// shardOverride entry); defaulting must not write through it.
		user := &corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("8Gi")},
		}
		result := createSearchResourceRequirements(user)
		assert.True(t, result.Requests.Cpu().Equal(defaultCPU))
		_, hasCPU := user.Requests[corev1.ResourceCPU]
		assert.False(t, hasCPU, "default CPU written into the caller's requirements")

		nilRequests := &corev1.ResourceRequirements{}
		result = createSearchResourceRequirements(nilRequests)
		assert.True(t, result.Requests.Cpu().Equal(defaultCPU))
		assert.Nil(t, nilRequests.Requests, "defaults map assigned into the caller's requirements")
	})
}

func TestCreateSearchStatefulSetFunc_DefaultAntiAffinity(t *testing.T) {
	search := newTestMongoDBSearch("test-search", "default")
	labels := map[string]string{appLabelKey: "test-search-svc"}

	stsMod := CreateSearchStatefulSetFunc(search, resolvedSizing(t, search, "", ""), "test-search-db", "default", "test-search-svc", "cm", labels, "mongot:latest", false)
	sts := statefulset.New(stsMod)

	// Reclaim the index PVC immediately on both CR delete and scale-down.
	require.NotNil(t, sts.Spec.PersistentVolumeClaimRetentionPolicy)
	assert.Equal(t, appsv1.DeletePersistentVolumeClaimRetentionPolicyType, sts.Spec.PersistentVolumeClaimRetentionPolicy.WhenDeleted)
	assert.Equal(t, appsv1.DeletePersistentVolumeClaimRetentionPolicyType, sts.Spec.PersistentVolumeClaimRetentionPolicy.WhenScaled)

	affinity := sts.Spec.Template.Spec.Affinity
	require.NotNil(t, affinity)
	require.NotNil(t, affinity.PodAntiAffinity)

	// The affinity should be preferred, not required
	assert.Empty(t, affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution)
	terms := affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution
	require.Len(t, terms, 1)
	assert.Equal(t, int32(100), terms[0].Weight)
	assert.Equal(t, util.DefaultAntiAffinityTopologyKey, terms[0].PodAffinityTerm.TopologyKey)
	require.NotNil(t, terms[0].PodAffinityTerm.LabelSelector)
	// Selects this StatefulSet's own pods, so the term spreads them across hosts.
	assert.Equal(t, map[string]string{appLabelKey: "test-search-svc"}, terms[0].PodAffinityTerm.LabelSelector.MatchLabels)
}

func TestCreateSearchStatefulSetFunc_StatefulSetOverrideReplacesAntiAffinity(t *testing.T) {
	customAntiAffinity := &corev1.PodAntiAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
			TopologyKey:   "topology.kubernetes.io/zone",
			LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"custom": "selector"}},
		}},
	}
	search := newTestMongoDBSearch("test-search", "default", func(s *searchv1.MongoDBSearch) {
		s.Spec.Clusters = []searchv1.ClusterSpec{{
			Name: "cluster-1",
			StatefulSetConfiguration: &v1.StatefulSetConfiguration{
				SpecWrapper: v1.StatefulSetSpecWrapper{
					Spec: appsv1.StatefulSetSpec{
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Affinity: &corev1.Affinity{PodAntiAffinity: customAntiAffinity},
							},
						},
					},
				},
			},
		}}
	})
	labels := map[string]string{appLabelKey: "test-search-svc"}

	sizing := resolvedSizing(t, search, "cluster-1", "")
	stsMod := CreateSearchStatefulSetFunc(search, sizing, "test-search-db", "default", "test-search-svc", "cm", labels, "mongot:latest", false)
	overrideMod := StatefulSetOverrideModification(sizing.StatefulSetConfiguration)
	// The override is applied last in the reconcile pipeline, after all other modifications.
	sts := statefulset.New(stsMod, overrideMod)

	pa := sts.Spec.Template.Spec.Affinity.PodAntiAffinity
	require.NotNil(t, pa)
	// The override's PodAntiAffinity replaces the default term wholesale.
	assert.Empty(t, pa.PreferredDuringSchedulingIgnoredDuringExecution)
	require.Len(t, pa.RequiredDuringSchedulingIgnoredDuringExecution, 1)
	assert.Equal(t, "topology.kubernetes.io/zone", pa.RequiredDuringSchedulingIgnoredDuringExecution[0].TopologyKey)
	assert.Equal(t, map[string]string{"custom": "selector"}, pa.RequiredDuringSchedulingIgnoredDuringExecution[0].LabelSelector.MatchLabels)
}

func testNodeAffinity(key string) *corev1.NodeAffinity {
	return &corev1.NodeAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
			NodeSelectorTerms: []corev1.NodeSelectorTerm{{
				MatchExpressions: []corev1.NodeSelectorRequirement{{
					Key:      key,
					Operator: corev1.NodeSelectorOpIn,
					Values:   []string{"true"},
				}},
			}},
		},
	}
}

func TestCreateSearchStatefulSetFunc_NodeAffinity(t *testing.T) {
	clusterNodeAffinity := testNodeAffinity("cluster-node")
	shardNodeAffinity := testNodeAffinity("shard-node")
	overrideNodeAffinity := testNodeAffinity("override-node")

	// statefulSetOverride, when non-nil, is applied last in the reconcile
	// pipeline (StatefulSetOverrideModification) and must win over the field.
	statefulSetNodeAffinityOverride := func(na *corev1.NodeAffinity) *v1.StatefulSetConfiguration {
		return &v1.StatefulSetConfiguration{
			SpecWrapper: v1.StatefulSetSpecWrapper{
				Spec: appsv1.StatefulSetSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Affinity: &corev1.Affinity{NodeAffinity: na},
						},
					},
				},
			},
		}
	}

	testCases := []struct {
		name        string
		cluster     searchv1.ClusterSpec
		clusterName string
		shardName   string
		expected    *corev1.NodeAffinity
	}{
		{
			name:        "cluster-level nodeAffinity set as it is",
			cluster:     searchv1.ClusterSpec{Name: "cluster-1", NodeAffinity: clusterNodeAffinity},
			clusterName: "cluster-1",
			expected:    clusterNodeAffinity,
		},
		{
			name:        "unset leaves nodeAffinity nil",
			cluster:     searchv1.ClusterSpec{Name: "cluster-1"},
			clusterName: "cluster-1",
			expected:    nil,
		},
		{
			name: "shard override replaces cluster nodeAffinity",
			cluster: searchv1.ClusterSpec{
				NodeAffinity:   clusterNodeAffinity,
				ShardOverrides: []searchv1.ShardOverride{{ShardNames: []string{"shard-1"}, NodeAffinity: shardNodeAffinity}},
			},
			shardName: "shard-1",
			expected:  shardNodeAffinity,
		},
		{
			name: "shard without override inherits cluster nodeAffinity",
			cluster: searchv1.ClusterSpec{
				NodeAffinity:   clusterNodeAffinity,
				ShardOverrides: []searchv1.ShardOverride{{ShardNames: []string{"shard-1"}, NodeAffinity: shardNodeAffinity}},
			},
			shardName: "shard-0",
			expected:  clusterNodeAffinity,
		},
		{
			name: "statefulSet override wins over nodeAffinity field",
			cluster: searchv1.ClusterSpec{
				Name:                     "cluster-1",
				NodeAffinity:             clusterNodeAffinity,
				StatefulSetConfiguration: statefulSetNodeAffinityOverride(overrideNodeAffinity),
			},
			clusterName: "cluster-1",
			expected:    overrideNodeAffinity,
		},
	}

	labels := map[string]string{appLabelKey: "test-search-svc"}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			search := newTestMongoDBSearch("test-search", "default", func(s *searchv1.MongoDBSearch) {
				s.Spec.Clusters = []searchv1.ClusterSpec{tc.cluster}
			})

			sizing := resolvedSizing(t, search, tc.clusterName, tc.shardName)
			stsMod := CreateSearchStatefulSetFunc(search, sizing, "test-search-db", "default", "test-search-svc", "cm", labels, "mongot:latest", false)
			// The override is applied last in the reconcile pipeline; a NOOP when unset.
			sts := statefulset.New(stsMod, StatefulSetOverrideModification(sizing.StatefulSetConfiguration))

			affinity := sts.Spec.Template.Spec.Affinity
			require.NotNil(t, affinity)
			assert.Equal(t, tc.expected, affinity.NodeAffinity)
			// The default pod anti-affinity term is left untouched in every case.
			require.NotNil(t, affinity.PodAntiAffinity)
			require.Len(t, affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution, 1)
		})
	}
}

func TestCreateSearchStatefulSetFunc_ShardOverrideReplicas(t *testing.T) {
	search := newTestMongoDBSearch("test-search", "default", func(s *searchv1.MongoDBSearch) {
		s.Spec.Clusters = []searchv1.ClusterSpec{{
			Replicas: ptr.To(int32(1)),
			ShardOverrides: []searchv1.ShardOverride{
				{ShardNames: []string{"shard-1"}, Replicas: ptr.To(int32(3))},
			},
		}}
	})
	labels := map[string]string{appLabelKey: "test-search-svc"}

	// The overridden shard's StatefulSet uses the override replica count.
	stsMod := CreateSearchStatefulSetFunc(search, resolvedSizing(t, search, "", "shard-1"), "test-search-db", "default", "test-search-svc", "cm", labels, "mongot:latest", false)
	sts := statefulset.New(stsMod)
	require.NotNil(t, sts.Spec.Replicas)
	assert.Equal(t, int32(3), *sts.Spec.Replicas)

	// A shard without an override keeps the cluster default.
	stsMod = CreateSearchStatefulSetFunc(search, resolvedSizing(t, search, "", "shard-0"), "test-search-db", "default", "test-search-svc", "cm", labels, "mongot:latest", false)
	sts = statefulset.New(stsMod)
	require.NotNil(t, sts.Spec.Replicas)
	assert.Equal(t, int32(1), *sts.Spec.Replicas)
}

type containerInfo struct {
	args []string
}
