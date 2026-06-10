package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	searchv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/search"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	"github.com/mongodb/mongodb-kubernetes/controllers/searchcontroller"
	khandler "github.com/mongodb/mongodb-kubernetes/pkg/handler"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/merge"
)

// TODO: Add full Reconcile() integration tests covering:
//   - LB mode transitions (managed -> unmanaged, managed -> disabled)
//   - Envoy Deployment/ConfigMap lifecycle (create, update, cleanup)
//   - Error paths (missing envoy image, unreachable source)
//   - Status updates for the loadBalancer sub-status

// seedSearchStateCM writes a <searchName>-state ConfigMap carrying the given
// clusterName→clusterIndex mapping into c, simulating a previous write by the
// main search controller. Tests that exercise the Envoy reconcile loop must
// seed this CM before constructing the reconciler so the Envoy controller can
// resolve indices without needing the full main-controller reconcile path.
func seedSearchStateCM(t *testing.T, ctx context.Context, c client.Client, searchName, ns string, mapping map[string]int) {
	t.Helper()
	state := SearchDeploymentState{
		CommonDeploymentState: CommonDeploymentState{ClusterMapping: mapping},
	}
	raw, err := json.Marshal(state)
	require.NoError(t, err)
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      searchName + "-state",
			Namespace: ns,
		},
		Data: map[string]string{stateKey: string(raw)},
	}
	require.NoError(t, c.Create(ctx, cm))
}

func TestBuildReplicaSetRoute(t *testing.T) {
	tests := []struct {
		name        string
		endpoint    string
		expectedSNI string
	}{
		{
			name:        "no endpoint uses proxy service FQDN",
			endpoint:    "",
			expectedSNI: "mdb-search-search-0-proxy-svc.test-ns.svc.cluster.local",
		},
		{
			name:        "externalHostname set uses it for SNI",
			endpoint:    "lb.example.com",
			expectedSNI: "lb.example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			search := &searchv1.MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "mdb-search",
					Namespace: "test-ns",
				},
			}
			if tt.endpoint != "" {
				search.Spec.LoadBalancer = &searchv1.LoadBalancerConfig{
					Managed: &searchv1.ManagedLBConfig{ExternalHostname: tt.endpoint},
				}
			}

			route := buildReplicaSetRouteForCluster(search, 0, "")

			assert.Equal(t, "rs", route.Name)
			assert.Equal(t, "rs", route.NameSafe)
			assert.Equal(t, tt.expectedSNI, route.SNIHostname)
			require.Len(t, route.UpstreamHosts, 1)
			assert.Equal(t, "mdb-search-search-0-svc.test-ns.svc.cluster.local", route.UpstreamHosts[0])
			assert.Equal(t, int32(27028), route.UpstreamPort)
		})
	}
}

func TestBuildReplicaSetRouteForCluster_PerClusterPlaceholders(t *testing.T) {
	clusters := []searchv1.ClusterSpec{
		{ClusterName: "kind-e2e-cluster-1"},
		{ClusterName: "kind-e2e-cluster-2"},
	}

	tests := []struct {
		name         string
		endpoint     string
		expectedSNIs []string
	}{
		{
			name:     "no endpoint: per-cluster proxy-svc FQDN",
			endpoint: "",
			expectedSNIs: []string{
				"mdb-search-search-0-proxy-svc.test-ns.svc.cluster.local",
				"mdb-search-search-1-proxy-svc.test-ns.svc.cluster.local",
			},
		},
		{
			name:     "{clusterIndex} placeholder substitutes per cluster",
			endpoint: "mdb-search-search-{clusterIndex}-proxy-svc.test-ns.svc.cluster.local",
			expectedSNIs: []string{
				"mdb-search-search-0-proxy-svc.test-ns.svc.cluster.local",
				"mdb-search-search-1-proxy-svc.test-ns.svc.cluster.local",
			},
		},
		{
			name:     "{clusterName} placeholder substitutes per cluster",
			endpoint: "mongot-{clusterName}.apps.example.com",
			expectedSNIs: []string{
				"mongot-kind-e2e-cluster-1.apps.example.com",
				"mongot-kind-e2e-cluster-2.apps.example.com",
			},
		},
		{
			name:     "{clusterName} and {clusterIndex} both substituted",
			endpoint: "{clusterName}-{clusterIndex}.apps.example.com",
			expectedSNIs: []string{
				"kind-e2e-cluster-1-0.apps.example.com",
				"kind-e2e-cluster-2-1.apps.example.com",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cs := append([]searchv1.ClusterSpec{}, clusters...)
			search := &searchv1.MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "mdb-search",
					Namespace: "test-ns",
				},
			}
			search.Spec.Clusters = cs
			if tt.endpoint != "" {
				search.Spec.LoadBalancer = &searchv1.LoadBalancerConfig{
					Managed: &searchv1.ManagedLBConfig{ExternalHostname: tt.endpoint},
				}
			}

			for i, c := range cs {
				route := buildReplicaSetRouteForCluster(search, i, c.ClusterName)
				assert.Equal(t, tt.expectedSNIs[i], route.SNIHostname,
					"cluster %d (%s)", i, c.ClusterName)
				assert.Equal(t, "rs", route.Name)
				assert.Equal(t, c.ClusterName, route.ClusterID)
				expectedUpstream := fmt.Sprintf("mdb-search-search-%d-svc.test-ns.svc.cluster.local", i)
				require.Len(t, route.UpstreamHosts, 1, "cluster %d (%s) UpstreamHosts length", i, c.ClusterName)
				assert.Equal(t, expectedUpstream, route.UpstreamHosts[0],
					"cluster %d (%s) UpstreamHosts[0]", i, c.ClusterName)
			}
		})
	}
}

// Regression guard: route builders must substitute {clusterIndex} from the
// resolved index (here 7), not the spec.clusters[] array position (0).
func TestBuildReplicaSetRouteForCluster_ResolvedIndexNotArrayPos(t *testing.T) {
	// Spec narrowed to one cluster (array position 0), but reconciled at pinned index 7.
	clusters := []searchv1.ClusterSpec{{ClusterName: "eu-west-k8s"}}
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-search", Namespace: "test-ns"},
		Spec: searchv1.MongoDBSearchSpec{
			Clusters: clusters,
			LoadBalancer: &searchv1.LoadBalancerConfig{
				Managed: &searchv1.ManagedLBConfig{ExternalHostname: "mongot-{clusterIndex}-{clusterName}.apps.example.com"},
			},
		},
	}

	route := buildReplicaSetRouteForCluster(search, 7, "eu-west-k8s")
	assert.Equal(t, "mongot-7-eu-west-k8s.apps.example.com", route.SNIHostname)
	assert.Equal(t, "mdb-search-search-7-svc.test-ns.svc.cluster.local", route.UpstreamHosts[0])
}

func TestBuildShardRoutes(t *testing.T) {
	shardNames := []string{"mdb-sh-0", "mdb-sh-1"}

	tests := []struct {
		name                    string
		endpoint                string
		expectedShardSNIs       []string
		expectedClusterLevelSNI string
	}{
		{
			name:     "no endpoint uses proxy service FQDNs",
			endpoint: "",
			expectedShardSNIs: []string{
				"mdb-search-search-0-mdb-sh-0-proxy-svc.test-ns.svc.cluster.local",
				"mdb-search-search-0-mdb-sh-1-proxy-svc.test-ns.svc.cluster.local",
			},
			// ProxyServiceNamespacedNameForCluster(0) = mdb-search-search-0-proxy-svc
			expectedClusterLevelSNI: "mdb-search-search-0-proxy-svc.test-ns.svc.cluster.local",
		},
		{
			name:     "externalHostname template resolves per shard and strips shardName for cluster-level",
			endpoint: "{shardName}.search.example.com",
			expectedShardSNIs: []string{
				"mdb-sh-0.search.example.com",
				"mdb-sh-1.search.example.com",
			},
			// GetManagedLBEndpointForClusterLevel strips "{shardName}." → "search.example.com"
			expectedClusterLevelSNI: "search.example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			search := &searchv1.MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "mdb-search",
					Namespace: "test-ns",
				},
			}
			if tt.endpoint != "" {
				search.Spec.LoadBalancer = &searchv1.LoadBalancerConfig{
					Managed: &searchv1.ManagedLBConfig{ExternalHostname: tt.endpoint},
				}
			}

			routes := buildShardRoutes(search, shardNames, 0, "")

			// 2 per-shard routes + 1 cluster-level route.
			require.Len(t, routes, 3)
			for i, route := range routes[:2] {
				assert.Equal(t, shardNames[i], route.Name)
				assert.Equal(t, tt.expectedShardSNIs[i], route.SNIHostname)
				require.Len(t, route.UpstreamHosts, 1)
			}
			clusterLevel := routes[2]
			assert.Equal(t, "cluster-level", clusterLevel.Name)
			assert.Equal(t, tt.expectedClusterLevelSNI, clusterLevel.SNIHostname)
			require.Len(t, clusterLevel.UpstreamHosts, 2, "cluster-level route must aggregate all shard upstreams")
		})
	}
}

func TestBuildEnvoyPodSpec_DefaultResources(t *testing.T) {
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "ns"},
	}
	resources := envoyResourceRequirements(search)
	podSpec := buildEnvoyPodSpec(search, 0, nil, false, "envoy:latest", resources, false)

	assert.Len(t, podSpec.Containers, 1)
	assert.Equal(t, "envoy", podSpec.Containers[0].Name)
	assert.Equal(t, resource.MustParse("100m"), podSpec.Containers[0].Resources.Requests[corev1.ResourceCPU])
	assert.Equal(t, resource.MustParse("128Mi"), podSpec.Containers[0].Resources.Requests[corev1.ResourceMemory])

	assert.NotNil(t, podSpec.Affinity.PodAntiAffinity)
	assert.Equal(t, corev1.PodAntiAffinity{
		PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{
			{
				Weight: 100,
				PodAffinityTerm: corev1.PodAffinityTerm{
					LabelSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"app": search.LoadBalancerDeploymentNameForCluster(0),
						},
					},
					TopologyKey: "kubernetes.io/hostname",
				},
			},
		},
	}, *podSpec.Affinity.PodAntiAffinity)
}

// TestBuildEnvoyPodSpec_ConfigMapVolumePerCluster regresses the MC-mode
// volume-name bug where buildEnvoyPodSpec hardcoded LoadBalancerConfigMapName()
// instead of the per-cluster suffixed name. Without this, the Pod template in
// member clusters references a ConfigMap that does not exist (ensureConfigMap
// writes the per-cluster name), so Envoy never starts in MC mode.
func TestBuildEnvoyPodSpec_ConfigMapVolumePerCluster(t *testing.T) {
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-search", Namespace: "ns"},
	}
	resources := envoyResourceRequirements(search)

	cases := []struct {
		name           string
		clusterIndex   int
		expectedCMName string
	}{
		{
			name:           "single-cluster uses index 0",
			clusterIndex:   0,
			expectedCMName: search.LoadBalancerConfigMapNameForCluster(0),
		},
		{
			name:           "MC first cluster uses index 0",
			clusterIndex:   0,
			expectedCMName: search.LoadBalancerConfigMapNameForCluster(0),
		},
		{
			name:           "MC second cluster uses index 1",
			clusterIndex:   1,
			expectedCMName: search.LoadBalancerConfigMapNameForCluster(1),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			podSpec := buildEnvoyPodSpec(search, tc.clusterIndex, nil, false, "envoy:latest", resources, false)

			var found *corev1.Volume
			for i := range podSpec.Volumes {
				if podSpec.Volumes[i].Name == "envoy-config" {
					found = &podSpec.Volumes[i]
					break
				}
			}
			require.NotNil(t, found, "envoy-config volume must be present")
			require.NotNil(t, found.ConfigMap, "envoy-config volume must be a ConfigMap source")
			assert.Equal(t, tc.expectedCMName, found.ConfigMap.Name,
				"per-cluster ConfigMap volume name mismatch — pod will fail to mount")
		})
	}
}

func TestBuildEnvoyPodSpec_WithDeploymentConfigurationOverride(t *testing.T) {
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "ns"},
		Spec: searchv1.MongoDBSearchSpec{
			LoadBalancer: &searchv1.LoadBalancerConfig{
				Managed: &searchv1.ManagedLBConfig{
					Deployment: &v1.DeploymentConfiguration{
						SpecWrapper: v1.DeploymentSpecWrapper{
							Spec: appsv1.DeploymentSpec{
								Template: corev1.PodTemplateSpec{
									Spec: corev1.PodSpec{
										Tolerations: []corev1.Toleration{
											{Key: "dedicated", Value: "search", Effect: corev1.TaintEffectNoSchedule},
										},
										NodeSelector: map[string]string{"node-type": "search"},
									},
								},
							},
						},
						MetadataWrapper: v1.DeploymentMetadataWrapper{
							Labels:      map[string]string{"custom-label": "value"},
							Annotations: map[string]string{"custom-annotation": "value"},
						},
					},
				},
			},
		},
	}

	// Build the base pod spec as the controller would
	resources := envoyResourceRequirements(search)
	podSpec := buildEnvoyPodSpec(search, 0, nil, false, "envoy:latest", resources, false)

	// Verify base spec has no tolerations
	assert.Empty(t, podSpec.Tolerations)

	// Now simulate what ensureDeployment does: build dep.Spec then apply override
	dep := appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:   search.LoadBalancerDeploymentName(),
			Labels: map[string]string{"app": "envoy"},
		},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: podSpec,
			},
		},
	}

	depCfg := search.Spec.LoadBalancer.Managed.Deployment
	dep.Spec = merge.DeploymentSpecs(dep.Spec, depCfg.SpecWrapper.Spec)
	dep.Labels = merge.StringToStringMap(dep.Labels, depCfg.MetadataWrapper.Labels)
	dep.Annotations = merge.StringToStringMap(dep.Annotations, depCfg.MetadataWrapper.Annotations)

	// Tolerations and node selector applied
	assert.Len(t, dep.Spec.Template.Spec.Tolerations, 1)
	assert.Equal(t, "dedicated", dep.Spec.Template.Spec.Tolerations[0].Key)
	assert.Equal(t, map[string]string{"node-type": "search"}, dep.Spec.Template.Spec.NodeSelector)

	// Labels and annotations merged
	assert.Equal(t, "value", dep.Labels["custom-label"])
	assert.Equal(t, "envoy", dep.Labels["app"])
	assert.Equal(t, "value", dep.Annotations["custom-annotation"])

	// Original container preserved
	assert.Len(t, dep.Spec.Template.Spec.Containers, 1)
	assert.Equal(t, "envoy:latest", dep.Spec.Template.Spec.Containers[0].Image)
}

func TestDeploymentConfigurationOverride_ResourceRequirementsComposition(t *testing.T) {
	// resourceRequirements sets 200m, deployment override sets 500m
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "ns"},
		Spec: searchv1.MongoDBSearchSpec{
			LoadBalancer: &searchv1.LoadBalancerConfig{
				Managed: &searchv1.ManagedLBConfig{
					ResourceRequirements: &corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("200m"),
						},
					},
					Deployment: &v1.DeploymentConfiguration{
						SpecWrapper: v1.DeploymentSpecWrapper{
							Spec: appsv1.DeploymentSpec{
								Template: corev1.PodTemplateSpec{
									Spec: corev1.PodSpec{
										Containers: []corev1.Container{
											{
												Name: "envoy",
												Resources: corev1.ResourceRequirements{
													Requests: corev1.ResourceList{
														corev1.ResourceCPU: resource.MustParse("500m"),
													},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	// Build base with resourceRequirements (200m)
	resources := envoyResourceRequirements(search)
	assert.Equal(t, resource.MustParse("200m"), resources.Requests[corev1.ResourceCPU])

	podSpec := buildEnvoyPodSpec(search, 0, nil, false, "envoy:latest", resources, false)

	dep := appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{Spec: podSpec},
		},
	}

	// Apply deployment override
	depCfg := search.Spec.LoadBalancer.Managed.Deployment
	dep.Spec = merge.DeploymentSpecs(dep.Spec, depCfg.SpecWrapper.Spec)

	// deployment override wins (500m)
	assert.Equal(t, resource.MustParse("500m"), dep.Spec.Template.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU])
}

// --- per-cluster route renderer tests -----------------------------------------

func TestBuildRoutesForCluster_RS_TemplateSubstitutesClusterName(t *testing.T) {
	one := int32(1)
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-search", Namespace: "test-ns"},
		Spec: searchv1.MongoDBSearchSpec{
			Clusters: []searchv1.ClusterSpec{
				{ClusterName: "us-east-k8s", Replicas: &one},
				{ClusterName: "eu-west-k8s", Replicas: &one},
			},
			LoadBalancer: &searchv1.LoadBalancerConfig{
				Managed: &searchv1.ManagedLBConfig{
					ExternalHostname: "mongot-{clusterName}.example.com",
				},
			},
		},
	}

	routes := buildRoutesForCluster(search, nil, 0, "us-east-k8s")
	require.Len(t, routes, 1)
	assert.Equal(t, "rs", routes[0].Name)
	assert.Equal(t, "us-east-k8s", routes[0].ClusterID)
	assert.Equal(t, "mongot-us-east-k8s.example.com", routes[0].SNIHostname)
}

func TestBuildRoutesForCluster_RS_NoTemplateUsesPerClusterProxySvcFQDN(t *testing.T) {
	one := int32(1)
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-search", Namespace: "test-ns"},
		Spec: searchv1.MongoDBSearchSpec{
			Clusters: []searchv1.ClusterSpec{
				{ClusterName: "us-east-k8s", Replicas: &one},
			},
		},
	}

	routes := buildRoutesForCluster(search, nil, 0, "us-east-k8s")
	require.Len(t, routes, 1)
	assert.Equal(t, "us-east-k8s", routes[0].ClusterID)
	assert.Equal(t, "mdb-search-search-0-proxy-svc.test-ns.svc.cluster.local", routes[0].SNIHostname,
		"SNI must be the per-cluster proxy-svc FQDN from ProxyServiceNamespacedNameForCluster")
}

func TestBuildRoutesForCluster_SingleClusterUnchanged(t *testing.T) {
	// clusterName == "" must produce the index-0 RS route (back-compat path).
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-search", Namespace: "test-ns"},
	}

	mc := buildRoutesForCluster(search, nil, 0, "")

	require.Len(t, mc, 1)
	assert.Equal(t, "", mc[0].ClusterID)
	assert.Equal(t, "mdb-search-search-0-proxy-svc.test-ns.svc.cluster.local", mc[0].SNIHostname)
	assert.Equal(t, []string{"mdb-search-search-0-svc.test-ns.svc.cluster.local"}, mc[0].UpstreamHosts)
}

// mockShardedSourceForEnvoy is a minimal SearchSourceShardedDeployment double for tests.
type mockShardedSourceForEnvoy struct {
	shardNames []string
}

func (m *mockShardedSourceForEnvoy) GetShardCount() int      { return len(m.shardNames) }
func (m *mockShardedSourceForEnvoy) GetShardNames() []string { return m.shardNames }
func (m *mockShardedSourceForEnvoy) GetUnmanagedLBEndpointForShard(_ string) string {
	return ""
}
func (m *mockShardedSourceForEnvoy) MongosHostsAndPorts() []string { return nil }
func (m *mockShardedSourceForEnvoy) KeyfileSecretName() string     { return "" }
func (m *mockShardedSourceForEnvoy) TLSConfig() *searchcontroller.TLSSourceConfig {
	return nil
}
func (m *mockShardedSourceForEnvoy) HostSeeds(_ string) ([]string, error) { return nil, nil }
func (m *mockShardedSourceForEnvoy) Validate() error                      { return nil }
func (m *mockShardedSourceForEnvoy) ResourceType() mdbv1.ResourceType {
	return mdbv1.ShardedCluster
}

func TestBuildRoutesForCluster_Sharded_PerClusterShardSNI(t *testing.T) {
	one := int32(1)
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-search", Namespace: "test-ns"},
		Spec: searchv1.MongoDBSearchSpec{
			Clusters: []searchv1.ClusterSpec{
				{ClusterName: "us-east-k8s", Replicas: &one},
				{ClusterName: "eu-west-k8s", Replicas: &one},
			},
			LoadBalancer: &searchv1.LoadBalancerConfig{
				Managed: &searchv1.ManagedLBConfig{
					// Both placeholders present — must substitute both.
					ExternalHostname: "mongot-{clusterName}-{shardName}.example.com",
				},
			},
		},
	}
	src := &mockShardedSourceForEnvoy{shardNames: []string{"mdb-sh-0", "mdb-sh-1"}}

	east := buildRoutesForCluster(search, src, 0, "us-east-k8s")
	west := buildRoutesForCluster(search, src, 1, "eu-west-k8s")

	// 2 per-shard routes + 1 cluster-level route per cluster.
	require.Len(t, east, 3)
	require.Len(t, west, 3)

	// Per-(cluster, shard) SNI emitted with both substitutions.
	assert.Equal(t, "us-east-k8s", east[0].ClusterID)
	assert.Equal(t, "mongot-us-east-k8s-mdb-sh-0.example.com", east[0].SNIHostname)
	assert.Equal(t, "mongot-us-east-k8s-mdb-sh-1.example.com", east[1].SNIHostname)
	assert.Equal(t, "mongot-eu-west-k8s-mdb-sh-0.example.com", west[0].SNIHostname)
	assert.Equal(t, "mongot-eu-west-k8s-mdb-sh-1.example.com", west[1].SNIHostname)

	// Last route in each cluster is the cluster-level route.
	assert.Equal(t, "cluster-level", east[2].Name)
	assert.Equal(t, "cluster-level", west[2].Name)
	require.Len(t, east[2].UpstreamHosts, 2, "cluster-level must aggregate both shard upstreams")
	require.Len(t, west[2].UpstreamHosts, 2, "cluster-level must aggregate both shard upstreams")

	// All 4 per-shard SNIs must be unique.
	all := []string{east[0].SNIHostname, east[1].SNIHostname, west[0].SNIHostname, west[1].SNIHostname}
	seen := map[string]struct{}{}
	for _, s := range all {
		seen[s] = struct{}{}
	}
	assert.Len(t, seen, 4, "per-(cluster, shard) SNIs must all be distinct")
}

// TestBuildShardRoutes_MC_ClusterLevel_NoExternalHostname asserts that for a
// multi-cluster sharded deploy with no externalHostname, buildShardRoutes for
// clusterIndex=1 / clusterName="cluster-b" emits 3 per-shard routes + 1
// cluster-level route whose upstream pool is the union of all shard mongot FQDNs
// in cluster-1 and whose SNI is the cluster-level proxy Service FQDN.
func TestBuildShardRoutes_MC_ClusterLevel_NoExternalHostname(t *testing.T) {
	shardNames := []string{"mdb-sh-0", "mdb-sh-1", "mdb-sh-2"}
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-search", Namespace: "test-ns"},
		Spec: searchv1.MongoDBSearchSpec{
			Clusters: []searchv1.ClusterSpec{
				{ClusterName: "cluster-a"},
				{ClusterName: "cluster-b"},
			},
		},
	}

	routes := buildShardRoutes(search, shardNames, 1, "cluster-b")

	// 3 per-shard + 1 cluster-level.
	require.Len(t, routes, 4)

	// Per-shard routes use clusterIndex=1 naming.
	for i, shardName := range shardNames {
		r := routes[i]
		assert.Equal(t, shardName, r.Name)
		assert.Equal(t, "cluster-b", r.ClusterID)
		expectedUpstream := fmt.Sprintf("mdb-search-search-1-%s-svc.test-ns.svc.cluster.local", shardName)
		require.Len(t, r.UpstreamHosts, 1)
		assert.Equal(t, expectedUpstream, r.UpstreamHosts[0])
		expectedSNI := fmt.Sprintf("mdb-search-search-1-%s-proxy-svc.test-ns.svc.cluster.local", shardName)
		assert.Equal(t, expectedSNI, r.SNIHostname)
	}

	// Cluster-level route.
	cl := routes[3]
	assert.Equal(t, "cluster-level", cl.Name)
	assert.Equal(t, "cluster-b", cl.ClusterID)
	// SNI is ProxyServiceNamespacedNameForCluster(1) FQDN.
	assert.Equal(t, "mdb-search-search-1-proxy-svc.test-ns.svc.cluster.local", cl.SNIHostname)
	// UpstreamHosts must be the union of all 3 per-shard mongot Service FQDNs for cluster-1.
	require.Len(t, cl.UpstreamHosts, 3)
	for _, shardName := range shardNames {
		expected := fmt.Sprintf("mdb-search-search-1-%s-svc.test-ns.svc.cluster.local", shardName)
		assert.Contains(t, cl.UpstreamHosts, expected)
	}
}

// TestBuildShardRoutes_MC_ClusterLevel_ManagedLB asserts that with a managed-LB
// externalHostname of "{shardName}.{clusterName}.search.example.com:443", the
// per-shard SNIs resolve per-shard and the cluster-level SNI strips the
// "{shardName}." prefix to produce "{clusterName}.search.example.com:443".
func TestBuildShardRoutes_MC_ClusterLevel_ManagedLB(t *testing.T) {
	shardNames := []string{"mdb-sh-0", "mdb-sh-1", "mdb-sh-2"}
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-search", Namespace: "test-ns"},
		Spec: searchv1.MongoDBSearchSpec{
			Clusters: []searchv1.ClusterSpec{
				{ClusterName: "cluster-a"},
				{ClusterName: "cluster-b"},
			},
			LoadBalancer: &searchv1.LoadBalancerConfig{
				Managed: &searchv1.ManagedLBConfig{
					ExternalHostname: "{shardName}.{clusterName}.search.example.com:443",
				},
			},
		},
	}

	routes := buildShardRoutes(search, shardNames, 1, "cluster-b")

	require.Len(t, routes, 4)

	// Per-shard SNIs must resolve both {clusterName} and {shardName}.
	for i, shardName := range shardNames {
		expected := fmt.Sprintf("%s.cluster-b.search.example.com:443", shardName)
		assert.Equal(t, expected, routes[i].SNIHostname, "per-shard SNI mismatch for shard %s", shardName)
	}

	// Cluster-level SNI strips "{shardName}." → "cluster-b.search.example.com:443".
	assert.Equal(t, "cluster-b.search.example.com:443", routes[3].SNIHostname)
	require.Len(t, routes[3].UpstreamHosts, 3)
}

// Each cluster's filter chain must use its own per-cluster proxy-svc FQDN as
// SNI (no collisions across clusters).
func TestEnvoyFilterChain_PerClusterSNI(t *testing.T) {
	one := int32(1)
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-search", Namespace: "ns"},
		Spec: searchv1.MongoDBSearchSpec{
			Clusters: []searchv1.ClusterSpec{
				{ClusterName: "us-east-k8s", Replicas: &one},
				{ClusterName: "eu-west-k8s", Replicas: &one},
			},
		},
	}

	type expected struct {
		idx      int
		cluster  string
		ownSNI   string
		otherSNI string
	}
	cases := []expected{
		{
			idx:      0,
			cluster:  "us-east-k8s",
			ownSNI:   "mdb-search-search-0-proxy-svc.ns.svc.cluster.local",
			otherSNI: "mdb-search-search-1-proxy-svc.ns.svc.cluster.local",
		},
		{
			idx:      1,
			cluster:  "eu-west-k8s",
			ownSNI:   "mdb-search-search-1-proxy-svc.ns.svc.cluster.local",
			otherSNI: "mdb-search-search-0-proxy-svc.ns.svc.cluster.local",
		},
	}

	for _, c := range cases {
		t.Run(c.cluster, func(t *testing.T) {
			routes := buildRoutesForCluster(search, nil, c.idx, c.cluster)
			require.Len(t, routes, 1)
			assert.Equal(t, c.cluster, routes[0].ClusterID)
			assert.Equal(t, c.ownSNI, routes[0].SNIHostname,
				"cluster %q must use its own per-cluster proxy-svc FQDN as SNI", c.cluster)
			assert.NotEqual(t, c.otherSNI, routes[0].SNIHostname,
				"cluster %q SNI must not collide with another cluster's proxy-svc FQDN", c.cluster)
		})
	}

	allSNIs := map[string]struct{}{}
	for _, c := range cases {
		routes := buildRoutesForCluster(search, nil, c.idx, c.cluster)
		require.Len(t, routes, 1)
		allSNIs[routes[0].SNIHostname] = struct{}{}
	}
	assert.Len(t, allSNIs, len(cases),
		"per-cluster SNIs must all be distinct; collisions break per-cluster TLS routing")
}

// --- reconciler constructor with member-cluster client maps ------------------

func TestNewMongoDBSearchEnvoyReconciler_AcceptsMemberClusters(t *testing.T) {
	central := fake.NewClientBuilder().Build()
	memberA := fake.NewClientBuilder().Build()
	memberB := fake.NewClientBuilder().Build()
	members := map[string]client.Client{
		"us-east-k8s": memberA,
		"eu-west-k8s": memberB,
	}

	r := newMongoDBSearchEnvoyReconciler(central, "envoy:latest", members, "")
	require.NotNil(t, r)
	assert.NotNil(t, r.clientForCluster("us-east-k8s"))
	assert.NotNil(t, r.clientForCluster("eu-west-k8s"))
	assert.Nil(t, r.clientForCluster("unknown"), "unregistered cluster must resolve to nil, not the central client")
}

func TestNewMongoDBSearchEnvoyReconciler_NilMembersMap(t *testing.T) {
	central := fake.NewClientBuilder().Build()
	r := newMongoDBSearchEnvoyReconciler(central, "envoy:latest", nil, "")
	require.NotNil(t, r)
	assert.Equal(t, r.kubeClient, r.clientForCluster("any-cluster"),
		"nil members map: every cluster name must resolve to the local client")
}

// --- clusterWorkItem.Client population ----------------------------------------

func TestBuildClusterWorkList_ClientPopulation(t *testing.T) {
	centralRaw := fake.NewClientBuilder().Build()
	memberARaw := fake.NewClientBuilder().Build()
	r := newMongoDBSearchEnvoyReconciler(centralRaw, "envoy:latest", map[string]client.Client{"a": memberARaw}, "")

	// Single-cluster: Client must be the central client.
	singleSearch := &searchv1.MongoDBSearch{}
	wl := r.buildClusterWorkList(singleSearch, nil)
	require.Len(t, wl, 1)
	assert.Equal(t, r.kubeClient, wl[0].Client, "single-cluster path must use central client")

	// Multi-cluster: known member → member client; unregistered member → nil sentinel
	// (the reconcile loop surfaces it as Pending; it must NOT fall back to central).
	mcSearch := &searchv1.MongoDBSearch{
		Spec: searchv1.MongoDBSearchSpec{
			Clusters: []searchv1.ClusterSpec{
				{ClusterName: "a"},
				{ClusterName: "unknown"},
			},
		},
	}
	mapping := map[string]int{"a": 0, "unknown": 1}
	wl = r.buildClusterWorkList(mcSearch, mapping)
	require.Len(t, wl, 2)
	assert.Equal(t, r.clientForCluster("a"), wl[0].Client, "known member must use member client")
	assert.Nil(t, wl[1].Client, "unregistered member must carry the nil-Client sentinel, not the central client")
}

// --- per-cluster name helpers + cross-cluster enqueue labels ------------------

func TestLoadBalancerNamesForCluster_IndexBased(t *testing.T) {
	search := &searchv1.MongoDBSearch{ObjectMeta: metav1.ObjectMeta{Name: "mdb-search", Namespace: "ns"}}

	// Index 0 (single-cluster path).
	assert.Equal(t, "mdb-search-search-lb-0-0", search.LoadBalancerDeploymentNameForCluster(0))
	assert.Equal(t, "mdb-search-search-lb-0-0-config", search.LoadBalancerConfigMapNameForCluster(0))

	// Index 2 (higher MC index).
	assert.Equal(t, "mdb-search-search-lb-0-2", search.LoadBalancerDeploymentNameForCluster(2))
	assert.Equal(t, "mdb-search-search-lb-0-2-config", search.LoadBalancerConfigMapNameForCluster(2))

	// Different indices produce different names.
	assert.NotEqual(t, search.LoadBalancerDeploymentNameForCluster(0), search.LoadBalancerDeploymentNameForCluster(1))
	assert.NotEqual(t, search.LoadBalancerConfigMapNameForCluster(0), search.LoadBalancerConfigMapNameForCluster(1))
}

func TestEnvoyLabels_StampsCrossClusterEnqueueLabels(t *testing.T) {
	search := &searchv1.MongoDBSearch{ObjectMeta: metav1.ObjectMeta{Name: "mdb-search", Namespace: "ns"}}

	// Single-cluster: cluster-name label must be absent; app label uses index 0.
	single := envoyLabelsForCluster(search, "", 0)
	assert.Equal(t, "mdb-search", single[khandler.MongoDBSearchOwnerNameLabel])
	assert.Equal(t, "ns", single[khandler.MongoDBSearchOwnerNamespaceLabel])
	_, hasCluster := single[khandler.MongoDBSearchClusterNameLabel]
	assert.False(t, hasCluster)
	assert.Equal(t, search.LoadBalancerDeploymentNameForCluster(0), single["app"])

	// Multi-cluster: all three labels present; app label uses the provided index.
	mc := envoyLabelsForCluster(search, "us-east-k8s", 3)
	assert.Equal(t, "mdb-search", mc[khandler.MongoDBSearchOwnerNameLabel])
	assert.Equal(t, "ns", mc[khandler.MongoDBSearchOwnerNamespaceLabel])
	assert.Equal(t, "us-east-k8s", mc[khandler.MongoDBSearchClusterNameLabel])
	assert.Equal(t, search.LoadBalancerDeploymentNameForCluster(3), mc["app"])
}

// --- envoy replicas defaulting ------------------------------------------

func TestEnvoyReplicas_DefaultsTo1(t *testing.T) {
	search := &searchv1.MongoDBSearch{ObjectMeta: metav1.ObjectMeta{Name: "mdb-search", Namespace: "ns"}}
	assert.Equal(t, int32(1), envoyReplicas(search))
}

// envoyTestScheme returns a runtime.Scheme registered for the types ensureDeployment/
// ensureConfigMap interact with. Using a per-test scheme avoids depending on the
// global scheme initialization order and keeps these unit tests self-contained.
//
// MongoDBSearch is registered through api/v1's SchemeBuilder, not search-local.
func envoyTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, v1.AddToScheme(scheme))
	return scheme
}

func TestEnsureConfigMap_WritesToCorrectMemberCluster(t *testing.T) {
	scheme := envoyTestScheme(t)
	central := fake.NewClientBuilder().WithScheme(scheme).Build()
	memberA := fake.NewClientBuilder().WithScheme(scheme).Build()
	memberB := fake.NewClientBuilder().WithScheme(scheme).Build()

	r := newMongoDBSearchEnvoyReconciler(central, "envoy:latest", map[string]client.Client{
		"a": memberA,
		"b": memberB,
	}, "")

	search := &searchv1.MongoDBSearch{ObjectMeta: metav1.ObjectMeta{Name: "mdb-search", Namespace: "ns"}}
	// cluster "a" is at index 0 in the mapping.
	require.NoError(t, r.ensureConfigMap(context.Background(), search, `{"bootstrap":1}`, `{"cds":1}`, `{"lds":1}`, "a", 0, r.clientForCluster("a"), zap.S()))

	// Member A has the ConfigMap named with index 0.
	cmA := &corev1.ConfigMap{}
	require.NoError(t, memberA.Get(context.Background(),
		types.NamespacedName{Name: search.LoadBalancerConfigMapNameForCluster(0), Namespace: "ns"}, cmA))
	assert.Equal(t, `{"bootstrap":1}`, cmA.Data["bootstrap.json"])
	assert.Equal(t, `{"cds":1}`, cmA.Data["cds.json"])
	assert.Equal(t, `{"lds":1}`, cmA.Data["lds.json"])
	// Owner labels stamped (name-keyed for cross-cluster enqueue).
	assertSearchOwnerLabels(t, search, "a", cmA)

	// Central and member B do not.
	cm := &corev1.ConfigMap{}
	err := central.Get(context.Background(),
		types.NamespacedName{Name: search.LoadBalancerConfigMapNameForCluster(0), Namespace: "ns"}, cm)
	assert.True(t, apierrors.IsNotFound(err))
	err = memberB.Get(context.Background(),
		types.NamespacedName{Name: search.LoadBalancerConfigMapNameForCluster(0), Namespace: "ns"}, cm)
	assert.True(t, apierrors.IsNotFound(err))
}

func TestEnsureConfigMap_SingleCluster_WritesToCentralWithOwnerRef(t *testing.T) {
	scheme := envoyTestScheme(t)
	central := fake.NewClientBuilder().WithScheme(scheme).Build()

	r := newMongoDBSearchEnvoyReconciler(central, "envoy:latest", nil, "")

	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-search", Namespace: "ns", UID: "abc"},
	}
	// Single-cluster uses index 0.
	require.NoError(t, r.ensureConfigMap(context.Background(), search, `{"bootstrap":1}`, `{"cds":1}`, `{"lds":1}`, "", 0, r.kubeClient, zap.S()))

	cm := &corev1.ConfigMap{}
	require.NoError(t, central.Get(context.Background(),
		types.NamespacedName{Name: search.LoadBalancerConfigMapNameForCluster(0), Namespace: "ns"}, cm))

	// Owner ref present in single-cluster path (central cluster).
	require.Len(t, cm.OwnerReferences, 1)
	assert.Equal(t, "mdb-search", cm.OwnerReferences[0].Name)
}

func TestEnsureConfigMap_MultiCluster_NoOwnerRef(t *testing.T) {
	scheme := envoyTestScheme(t)
	central := fake.NewClientBuilder().WithScheme(scheme).Build()
	memberA := fake.NewClientBuilder().WithScheme(scheme).Build()

	r := newMongoDBSearchEnvoyReconciler(central, "envoy:latest", map[string]client.Client{"a": memberA}, "")

	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-search", Namespace: "ns", UID: "abc"},
	}
	// cluster "a" is at index 0.
	require.NoError(t, r.ensureConfigMap(context.Background(), search, `{"bootstrap":1}`, `{"cds":1}`, `{"lds":1}`, "a", 0, r.clientForCluster("a"), zap.S()))

	cm := &corev1.ConfigMap{}
	require.NoError(t, memberA.Get(context.Background(),
		types.NamespacedName{Name: search.LoadBalancerConfigMapNameForCluster(0), Namespace: "ns"}, cm))

	// Cross-cluster: no owner ref (k8s GC does not span clusters).
	assert.Empty(t, cm.OwnerReferences)
}

// --- reconcile loop + per-cluster status --------------------------------------

func TestBuildClusterWorkList_SingleClusterDegenerate(t *testing.T) {
	r := newMongoDBSearchEnvoyReconciler(fake.NewClientBuilder().Build(), "envoy:latest", nil, "")
	search := &searchv1.MongoDBSearch{}
	wl := r.buildClusterWorkList(search, nil)
	require.Len(t, wl, 1)
	assert.Equal(t, "", wl[0].ClusterName)
	assert.Equal(t, 0, wl[0].ClusterIndex)
}

func TestBuildClusterWorkList_EmptySpecClusters_TreatedAsSingle(t *testing.T) {
	central := fake.NewClientBuilder().Build()
	r := newMongoDBSearchEnvoyReconciler(central, "envoy:latest", map[string]client.Client{
		"a": fake.NewClientBuilder().Build(),
	}, "")
	search := &searchv1.MongoDBSearch{}
	wl := r.buildClusterWorkList(search, nil)
	require.Len(t, wl, 1)
	assert.Equal(t, "", wl[0].ClusterName)
	assert.Equal(t, 0, wl[0].ClusterIndex)
}

func TestBuildClusterWorkList_MultiCluster_OneItemPerSpecEntry(t *testing.T) {
	central := fake.NewClientBuilder().Build()
	r := newMongoDBSearchEnvoyReconciler(central, "envoy:latest", map[string]client.Client{
		"a": fake.NewClientBuilder().Build(),
		"b": fake.NewClientBuilder().Build(),
	}, "")
	search := &searchv1.MongoDBSearch{
		Spec: searchv1.MongoDBSearchSpec{
			Clusters: []searchv1.ClusterSpec{
				{ClusterName: "a"},
				{ClusterName: "b"},
			},
		},
	}
	mapping := map[string]int{"a": 0, "b": 1}
	wl := r.buildClusterWorkList(search, mapping)
	require.Len(t, wl, 2)
	assert.Equal(t, "a", wl[0].ClusterName)
	assert.Equal(t, 0, wl[0].ClusterIndex)
	assert.Equal(t, "b", wl[1].ClusterName)
	assert.Equal(t, 1, wl[1].ClusterIndex)
}

func TestBuildClusterWorkList_SimulatedMC_UsesProjectedIndex(t *testing.T) {
	// Simulated-MC: members map empty; LocalizeToCluster already narrowed
	// spec.Clusters to one entry whose projected clusterIndex must be honoured.
	central := fake.NewClientBuilder().Build()
	// operatorClusterName is "" — this test exercises buildClusterWorkList directly, not Reconcile.
	r := newMongoDBSearchEnvoyReconciler(central, "envoy:latest", nil, "")
	search := &searchv1.MongoDBSearch{
		Spec: searchv1.MongoDBSearchSpec{
			Clusters: []searchv1.ClusterSpec{
				{ClusterName: "kind-e2e-cluster-2", ClusterIndex: ptr.To(int32(1))},
			},
		},
	}
	wl := r.buildClusterWorkList(search, map[string]int{"kind-e2e-cluster-2": 1})
	require.Len(t, wl, 1)
	assert.Equal(t, "kind-e2e-cluster-2", wl[0].ClusterName)
	assert.Equal(t, 1, wl[0].ClusterIndex, "simulated-MC must honour the projected clusterIndex, not 0")
	assert.Equal(t, r.kubeClient, wl[0].Client, "simulated-MC: client must fall back to kubeClient")
}

func TestBuildClusterWorkList_MissingFromMapping_SentinelIndex(t *testing.T) {
	central := fake.NewClientBuilder().Build()
	r := newMongoDBSearchEnvoyReconciler(central, "envoy:latest", map[string]client.Client{
		"a": fake.NewClientBuilder().Build(),
	}, "")
	search := &searchv1.MongoDBSearch{
		Spec: searchv1.MongoDBSearchSpec{
			Clusters: []searchv1.ClusterSpec{{ClusterName: "a"}},
		},
	}
	// Empty mapping simulates first reconcile before main controller writes state.
	wl := r.buildClusterWorkList(search, map[string]int{})
	require.Len(t, wl, 1)
	assert.Equal(t, "a", wl[0].ClusterName)
	assert.Equal(t, -1, wl[0].ClusterIndex, "missing cluster must use sentinel -1")
}

func TestBuildRoutesForCluster_SimulatedMC_Sharded_PerShardSNIUsesProjectedIndex(t *testing.T) {
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-search", Namespace: "ns"},
		Spec: searchv1.MongoDBSearchSpec{
			LoadBalancer: &searchv1.LoadBalancerConfig{
				Managed: &searchv1.ManagedLBConfig{
					ExternalHostname: "{clusterIndex}-{shardName}.example.com",
				},
			},
			Clusters: []searchv1.ClusterSpec{
				{ClusterName: "kind-e2e-cluster-2", ClusterIndex: ptr.To(int32(1))},
			},
			Source: &searchv1.MongoDBSource{
				ExternalMongoDBSource: &searchv1.ExternalMongoDBSource{
					ShardedCluster: &searchv1.ExternalShardedClusterConfig{
						Router: searchv1.ExternalRouterConfig{Hosts: []string{"mongos.example:27017"}},
						Shards: []searchv1.ExternalShardConfig{
							{ShardName: "sh-0", Hosts: []string{"sh-0-a.example:27017"}},
							{ShardName: "sh-1", Hosts: []string{"sh-1-a.example:27017"}},
						},
					},
				},
			},
		},
	}
	source := &mockShardedSourceForEnvoy{shardNames: []string{"sh-0", "sh-1"}}

	routes := buildRoutesForCluster(search, source, 1, "kind-e2e-cluster-2")
	require.Len(t, routes, 3, "2 per-shard routes + 1 cluster-level route")

	byName := map[string]envoyRoute{}
	for _, r := range routes {
		byName[r.Name] = r
	}
	require.Contains(t, byName, "sh-0")
	require.Contains(t, byName, "sh-1")
	require.Contains(t, byName, "cluster-level")

	assert.Equal(t, "1-sh-0.example.com", byName["sh-0"].SNIHostname,
		"per-shard SNI must resolve {clusterIndex}=1 via the pinned ClusterSpec.ClusterIndex")
	assert.Equal(t, "1-sh-1.example.com", byName["sh-1"].SNIHostname,
		"per-shard SNI must resolve {clusterIndex}=1 via the pinned ClusterSpec.ClusterIndex")
	// Cluster-level route's SNI must be fully resolved (no literal placeholders).
	clusterLevel := byName["cluster-level"]
	assert.NotContains(t, clusterLevel.SNIHostname, "{")
	assert.NotContains(t, clusterLevel.SNIHostname, "}")
	assert.Len(t, clusterLevel.UpstreamHosts, 2, "cluster-level route aggregates per-shard upstream FQDNs")
}

func TestReconcileForCluster_SimulatedMC_ShardedSource_RendersToProvidedClient(t *testing.T) {
	scheme := envoyTestScheme(t)
	central := fake.NewClientBuilder().WithScheme(scheme).Build()
	// members map is nil (simulated-MC: one operator per member cluster).
	r := newMongoDBSearchEnvoyReconciler(central, "envoy:latest", nil, "")

	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-search", Namespace: "ns"},
		Spec: searchv1.MongoDBSearchSpec{
			LoadBalancer: &searchv1.LoadBalancerConfig{
				Managed: &searchv1.ManagedLBConfig{ExternalHostname: "{shardName}.{clusterName}.example.com"},
			},
			Clusters: []searchv1.ClusterSpec{{ClusterName: "kind-e2e-cluster-1"}},
		},
	}
	source := &mockShardedSourceForEnvoy{shardNames: []string{"sh-0", "sh-1"}}

	// Drive reconcileForCluster directly with the projected cluster name and
	// r.kubeClient — the simulated-MC path that buildClusterWorkList wires.
	st := r.reconcileForCluster(context.Background(), search, source, false, nil, "kind-e2e-cluster-1", 0, r.kubeClient, zap.S())
	require.True(t, st.IsOK(), "simulated-MC sharded reconcile should succeed, got %s: %s",
		st.Phase(), searchcontroller.MessageFromStatus(st))

	// Envoy Deployment + ConfigMap landed in kubeClient at index 0.
	dep := &appsv1.Deployment{}
	require.NoError(t, central.Get(context.Background(),
		types.NamespacedName{Name: search.LoadBalancerDeploymentNameForCluster(0), Namespace: "ns"}, dep),
		"Envoy Deployment must land on r.kubeClient for the projected cluster index")
	cm := &corev1.ConfigMap{}
	require.NoError(t, central.Get(context.Background(),
		types.NamespacedName{Name: search.LoadBalancerConfigMapNameForCluster(0), Namespace: "ns"}, cm),
		"Envoy ConfigMap must land on r.kubeClient for the projected cluster index")
}

func TestEnqueueMemberClusterObjectToSearch(t *testing.T) {
	// Object with both labels → reconcile request returned.
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mdb-search-search-lb-0-a-config",
			Namespace: "ns",
			Labels: map[string]string{
				khandler.MongoDBSearchOwnerNameLabel:      "mdb-search",
				khandler.MongoDBSearchOwnerNamespaceLabel: "ns",
				khandler.MongoDBSearchClusterNameLabel:    "a",
			},
		},
	}
	reqs := khandler.EnqueueMemberClusterObjectToSearch(context.Background(), cm)
	require.Len(t, reqs, 1)
	assert.Equal(t, "mdb-search", reqs[0].Name)
	assert.Equal(t, "ns", reqs[0].Namespace)

	// Object missing labels → no enqueue.
	cmNoLabels := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "ns"}}
	assert.Empty(t, khandler.EnqueueMemberClusterObjectToSearch(context.Background(), cmNoLabels))

	// Partial labels → no enqueue (both required).
	cmPartial := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "x",
			Namespace: "ns",
			Labels:    map[string]string{khandler.MongoDBSearchOwnerNameLabel: "mdb-search"},
		},
	}
	assert.Empty(t, khandler.EnqueueMemberClusterObjectToSearch(context.Background(), cmPartial))
}

func TestReconcileForCluster_RendersInMemberCluster(t *testing.T) {
	scheme := envoyTestScheme(t)
	central := fake.NewClientBuilder().WithScheme(scheme).Build()
	memberA := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := newMongoDBSearchEnvoyReconciler(central, "envoy:latest", map[string]client.Client{"a": memberA}, "")

	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-search", Namespace: "ns"},
		Spec: searchv1.MongoDBSearchSpec{
			LoadBalancer: &searchv1.LoadBalancerConfig{
				Managed: &searchv1.ManagedLBConfig{ExternalHostname: "mongot-{clusterName}.example.com"},
			},
			Clusters: []searchv1.ClusterSpec{{ClusterName: "a"}},
		},
	}

	// cluster "a" is at index 0 in the mapping.
	st := r.reconcileForCluster(context.Background(), search, nil, false, nil, "a", 0, r.clientForCluster("a"), zap.S())
	require.True(t, st.IsOK(), "expected OK, got %s: %s", st.Phase(), searchcontroller.MessageFromStatus(st))

	// Member cluster has Deployment + ConfigMap; central does not.
	dep := &appsv1.Deployment{}
	require.NoError(t, memberA.Get(context.Background(),
		types.NamespacedName{Name: search.LoadBalancerDeploymentNameForCluster(0), Namespace: "ns"}, dep))
	cm := &corev1.ConfigMap{}
	require.NoError(t, memberA.Get(context.Background(),
		types.NamespacedName{Name: search.LoadBalancerConfigMapNameForCluster(0), Namespace: "ns"}, cm))

	err := central.Get(context.Background(),
		types.NamespacedName{Name: search.LoadBalancerDeploymentNameForCluster(0), Namespace: "ns"}, &appsv1.Deployment{})
	assert.True(t, apierrors.IsNotFound(err))
}

func TestEnsureDeployment_Replicas(t *testing.T) {
	scheme := envoyTestScheme(t)
	central := fake.NewClientBuilder().WithScheme(scheme).Build()
	memberA := fake.NewClientBuilder().WithScheme(scheme).Build()

	r := newMongoDBSearchEnvoyReconciler(central, "envoy:latest", map[string]client.Client{"a": memberA}, "")

	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-search", Namespace: "ns"},
		Spec: searchv1.MongoDBSearchSpec{
			LoadBalancer: &searchv1.LoadBalancerConfig{Managed: &searchv1.ManagedLBConfig{}},
			Clusters:     []searchv1.ClusterSpec{{ClusterName: "a"}},
		},
	}
	for _, tc := range []struct {
		lbReplicas           *int32
		expectedDeplReplicas int32
	}{
		// defaults to 1 if replicas is not set in search resource
		{
			expectedDeplReplicas: 1,
		},
		{
			lbReplicas:           ptr.To(int32(3)),
			expectedDeplReplicas: 3,
		},
		{
			lbReplicas:           ptr.To(int32(4)),
			expectedDeplReplicas: 4,
		},
	} {
		search.Spec.LoadBalancer.Managed.Replicas = tc.lbReplicas
		// cluster "a" is at index 0.
		require.NoError(t, r.ensureDeployment(context.Background(), search, `{"x":1}`, "a", 0, r.clientForCluster("a"), nil, zap.S()))

		dep := &appsv1.Deployment{}
		require.NoError(t, memberA.Get(context.Background(),
			types.NamespacedName{Name: search.LoadBalancerDeploymentNameForCluster(0), Namespace: "ns"}, dep))
		require.NotNil(t, dep.Spec.Replicas)
		assert.Equal(t, tc.expectedDeplReplicas, *dep.Spec.Replicas, "envoy replicas must be set to the same value configured in search resource")

		memberA.Delete(context.Background(), &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
			Name:      dep.Name,
			Namespace: dep.Namespace,
		}})
	}
}

// --- end-to-end Reconcile + status aggregation -------------------------------

// newMCEnvoySearch builds the external-RS + managed-LB MC Envoy fixture. The Envoy
// reconciler resolves indices from the seeded state CM, never from the spec pins.
func newMCEnvoySearch(name, namespace, uid string, clusters ...searchv1.ClusterSpec) *searchv1.MongoDBSearch {
	return &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, UID: types.UID(uid)},
		Spec: searchv1.MongoDBSearchSpec{
			Source: &searchv1.MongoDBSource{
				ExternalMongoDBSource: &searchv1.ExternalMongoDBSource{HostAndPorts: []string{"mongo-0:27017"}},
			},
			LoadBalancer: &searchv1.LoadBalancerConfig{
				Managed: &searchv1.ManagedLBConfig{ExternalHostname: "mongot-{clusterName}.example.com"},
			},
			Clusters: clusters,
		},
	}
}

// TestReconcile_WorstOfPhase_Aggregated exercises the full Reconcile path:
// two clusters in spec.clusters[]; one is a member registered with the operator
// (succeeds), the other isn't (Pending). The top-level Phase must be the
// worst-of across both clusters (Pending here).
func TestReconcile_WorstOfPhase_Aggregated(t *testing.T) {
	ctx := context.Background()
	scheme := envoyTestScheme(t)
	memberA := fake.NewClientBuilder().WithScheme(scheme).Build()

	search := newMCEnvoySearch("mdb-search", "ns", "abc", pinnedCluster("a", 0), pinnedCluster("missing", 1))

	central := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&searchv1.MongoDBSearch{}).WithObjects(search).Build()
	// "missing" is absent from the mapping → sentinel -1 → Pending.
	seedSearchStateCM(t, ctx, central, "mdb-search", "ns", map[string]int{"a": 0})

	r := newMongoDBSearchEnvoyReconciler(central, "envoy:latest", map[string]client.Client{"a": memberA}, "")

	res, err := r.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "mdb-search", Namespace: "ns"},
	})
	require.NoError(t, err)
	assert.False(t, res.Requeue)

	// Re-fetch to see the patched status.
	patched := &searchv1.MongoDBSearch{}
	require.NoError(t, central.Get(ctx,
		types.NamespacedName{Name: "mdb-search", Namespace: "ns"}, patched))
	require.NotNil(t, patched.Status.LoadBalancer, "status.loadBalancer must be populated")
	assert.Equal(t, status.PhasePending, patched.Status.LoadBalancer.Phase, "worst-of (Running, Pending) is Pending")

	// Cluster "a" (index 0) got its Deployment + ConfigMap in the member-cluster client.
	require.NoError(t, memberA.Get(ctx,
		types.NamespacedName{Name: search.LoadBalancerDeploymentNameForCluster(0), Namespace: "ns"}, &appsv1.Deployment{}))
	require.NoError(t, memberA.Get(ctx,
		types.NamespacedName{Name: search.LoadBalancerConfigMapNameForCluster(0), Namespace: "ns"}, &corev1.ConfigMap{}))
}

// TestReconcile_AllClustersFailed_TopLevelPhaseIsFailed asserts that when a
// per-cluster reconcile returns workflow.Failed, the aggregated top-level
// phase patched onto status.loadBalancer is Failed (not Pending). Without
// this guard, all errors would be downgraded to Pending in the final write.
func TestReconcile_AllClustersFailed_TopLevelPhaseIsFailed(t *testing.T) {
	ctx := context.Background()
	scheme := envoyTestScheme(t)

	search := newMCEnvoySearch("mdb-search", "ns", "", pinnedCluster("a", 0))
	central := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&searchv1.MongoDBSearch{}).WithObjects(search).Build()
	// Seed state CM so the Envoy controller can resolve "a"→0.
	seedSearchStateCM(t, ctx, central, "mdb-search", "ns", map[string]int{"a": 0})

	// Member client is a fake that fails every write — drives Failed.
	memberA := failingWriteClient{Client: fake.NewClientBuilder().WithScheme(scheme).Build()}

	r := newMongoDBSearchEnvoyReconciler(central, "envoy:latest", map[string]client.Client{"a": memberA}, "")

	_, err := r.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "mdb-search", Namespace: "ns"},
	})
	require.NoError(t, err)

	patched := &searchv1.MongoDBSearch{}
	require.NoError(t, central.Get(ctx,
		types.NamespacedName{Name: "mdb-search", Namespace: "ns"}, patched))
	require.NotNil(t, patched.Status.LoadBalancer)
	assert.Equal(t, status.PhaseFailed, patched.Status.LoadBalancer.Phase,
		"all-Failed clusters must aggregate to top-level Failed, not Pending")
}

// --- index-based naming reconcile loop tests ----------------------------------

// TestReconcile_NoStateCM_ClustersPending asserts that when no <name>-state
// ConfigMap exists (first reconcile before main controller runs), every
// per-cluster status is Pending and no Envoy resources are created.
func TestReconcile_NoStateCM_ClustersPending(t *testing.T) {
	ctx := context.Background()
	scheme := envoyTestScheme(t)
	memberA := fake.NewClientBuilder().WithScheme(scheme).Build()

	search := newMCEnvoySearch("mdb-search", "ns", "abc", pinnedCluster("cluster-a", 0), pinnedCluster("cluster-b", 1))

	// No state CM seeded — first reconcile race.
	central := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&searchv1.MongoDBSearch{}).WithObjects(search).Build()
	r := newMongoDBSearchEnvoyReconciler(central, "envoy:latest", map[string]client.Client{"cluster-a": memberA}, "")

	_, err := r.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "mdb-search", Namespace: "ns"},
	})
	require.NoError(t, err)

	patched := &searchv1.MongoDBSearch{}
	require.NoError(t, central.Get(ctx, types.NamespacedName{Name: "mdb-search", Namespace: "ns"}, patched))
	require.NotNil(t, patched.Status.LoadBalancer)
	assert.Equal(t, status.PhasePending, patched.Status.LoadBalancer.Phase, "no state CM must produce Pending")

	// No Envoy Deployments should have been created.
	depList := &appsv1.DeploymentList{}
	require.NoError(t, memberA.List(ctx, depList))
	assert.Empty(t, depList.Items, "no Envoy Deployment must be created when clusters are not yet in state mapping")
}

// TestReconcile_UsesIndicesFromStateMapping asserts that Envoy resources are
// named with the indices stored in the state ConfigMap, not the cluster names.
func TestReconcile_UsesIndicesFromStateMapping(t *testing.T) {
	ctx := context.Background()
	scheme := envoyTestScheme(t)
	memberA := fake.NewClientBuilder().WithScheme(scheme).Build()
	memberB := fake.NewClientBuilder().WithScheme(scheme).Build()

	// Pins match the seeded mapping below.
	search := newMCEnvoySearch("mdb-search", "ns", "abc", pinnedCluster("cluster-a", 5), pinnedCluster("cluster-b", 7))

	central := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&searchv1.MongoDBSearch{}).WithObjects(search).Build()
	// Seed non-trivial indices: cluster-a→5, cluster-b→7.
	seedSearchStateCM(t, ctx, central, "mdb-search", "ns", map[string]int{"cluster-a": 5, "cluster-b": 7})

	r := newMongoDBSearchEnvoyReconciler(central, "envoy:latest", map[string]client.Client{
		"cluster-a": memberA,
		"cluster-b": memberB,
	}, "")

	_, err := r.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "mdb-search", Namespace: "ns"},
	})
	require.NoError(t, err)

	// cluster-a (index 5) — resources must use index 5.
	require.NoError(t, memberA.Get(ctx,
		types.NamespacedName{Name: search.LoadBalancerDeploymentNameForCluster(5), Namespace: "ns"}, &appsv1.Deployment{}),
		"Envoy Deployment for cluster-a must use index 5")
	require.NoError(t, memberA.Get(ctx,
		types.NamespacedName{Name: search.LoadBalancerConfigMapNameForCluster(5), Namespace: "ns"}, &corev1.ConfigMap{}),
		"Envoy ConfigMap for cluster-a must use index 5")

	// cluster-b (index 7) — resources must use index 7.
	require.NoError(t, memberB.Get(ctx,
		types.NamespacedName{Name: search.LoadBalancerDeploymentNameForCluster(7), Namespace: "ns"}, &appsv1.Deployment{}),
		"Envoy Deployment for cluster-b must use index 7")
	require.NoError(t, memberB.Get(ctx,
		types.NamespacedName{Name: search.LoadBalancerConfigMapNameForCluster(7), Namespace: "ns"}, &corev1.ConfigMap{}),
		"Envoy ConfigMap for cluster-b must use index 7")

	// Cluster names must not appear in any resource name.
	for _, name := range []string{
		search.LoadBalancerDeploymentNameForCluster(5),
		search.LoadBalancerDeploymentNameForCluster(7),
		search.LoadBalancerConfigMapNameForCluster(5),
		search.LoadBalancerConfigMapNameForCluster(7),
	} {
		assert.NotContains(t, name, "cluster-a", "resource names must not encode cluster names")
		assert.NotContains(t, name, "cluster-b", "resource names must not encode cluster names")
	}
}

// TestReconcile_StableIndexAcrossClusterRemovals asserts that removing a
// cluster from spec.clusters does not shift the indices of remaining clusters.
// "b" at index 1 must still be index 1 after "a" is removed.
func TestReconcile_StableIndexAcrossClusterRemovals(t *testing.T) {
	ctx := context.Background()
	scheme := envoyTestScheme(t)
	memberB := fake.NewClientBuilder().WithScheme(scheme).Build()

	mkSearch := func(clusters ...searchv1.ClusterSpec) *searchv1.MongoDBSearch {
		return newMCEnvoySearch("mdb-search", "ns", "abc", clusters...)
	}

	// Start with both clusters; "a"→0, "b"→1.
	search := mkSearch(pinnedCluster("a", 0), pinnedCluster("b", 1))
	memberA := fake.NewClientBuilder().WithScheme(scheme).Build()

	central := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&searchv1.MongoDBSearch{}).WithObjects(search).Build()
	seedSearchStateCM(t, ctx, central, "mdb-search", "ns", map[string]int{"a": 0, "b": 1})

	r := newMongoDBSearchEnvoyReconciler(central, "envoy:latest", map[string]client.Client{
		"a": memberA,
		"b": memberB,
	}, "")

	_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "mdb-search", Namespace: "ns"}})
	require.NoError(t, err)

	// First reconcile: "b" Deployment is at index 1.
	require.NoError(t, memberB.Get(ctx,
		types.NamespacedName{Name: search.LoadBalancerDeploymentNameForCluster(1), Namespace: "ns"}, &appsv1.Deployment{}),
		"b Deployment must be at index 1 on first reconcile")

	// Remove "a" from spec.clusters; state CM still holds "a"→0, "b"→1.
	latest := &searchv1.MongoDBSearch{}
	require.NoError(t, central.Get(ctx, types.NamespacedName{Name: "mdb-search", Namespace: "ns"}, latest))
	updated := mkSearch(pinnedCluster("b", 1))
	updated.ResourceVersion = latest.ResourceVersion
	updated.UID = latest.UID
	require.NoError(t, central.Update(ctx, updated))

	_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "mdb-search", Namespace: "ns"}})
	require.NoError(t, err)

	// "b" must still use index 1 — index must not shift to 0 after "a" is removed.
	require.NoError(t, memberB.Get(ctx,
		types.NamespacedName{Name: search.LoadBalancerDeploymentNameForCluster(1), Namespace: "ns"}, &appsv1.Deployment{}),
		"b Deployment must retain index 1 after a is removed from spec.clusters")
}

func TestDeleteEnvoyResources_MCFanOut(t *testing.T) {
	ctx := context.Background()
	scheme := envoyTestScheme(t)
	central := fake.NewClientBuilder().WithScheme(scheme).Build()

	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-search", Namespace: "ns"},
	}
	depA := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: search.LoadBalancerDeploymentNameForCluster(0), Namespace: "ns"}}
	cmA := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: search.LoadBalancerConfigMapNameForCluster(0), Namespace: "ns"}}
	depB := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: search.LoadBalancerDeploymentNameForCluster(1), Namespace: "ns"}}
	cmB := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: search.LoadBalancerConfigMapNameForCluster(1), Namespace: "ns"}}
	memberA := fake.NewClientBuilder().WithScheme(scheme).WithObjects(depA, cmA).Build()
	memberB := fake.NewClientBuilder().WithScheme(scheme).WithObjects(depB, cmB).Build()

	r := newMongoDBSearchEnvoyReconciler(central, "envoy:latest", map[string]client.Client{"a": memberA, "b": memberB}, "")

	workList := []clusterWorkItem{
		{ClusterName: "a", ClusterIndex: 0, Client: r.clientForCluster("a")},
		{ClusterName: "b", ClusterIndex: 1, Client: r.clientForCluster("b")},
	}
	r.deleteEnvoyResources(ctx, search, workList, zap.S())

	// Both member clusters: Deployment + ConfigMap gone at their respective indices.
	assert.True(t, apierrors.IsNotFound(memberA.Get(ctx,
		types.NamespacedName{Name: search.LoadBalancerDeploymentNameForCluster(0), Namespace: "ns"}, &appsv1.Deployment{})))
	assert.True(t, apierrors.IsNotFound(memberA.Get(ctx,
		types.NamespacedName{Name: search.LoadBalancerConfigMapNameForCluster(0), Namespace: "ns"}, &corev1.ConfigMap{})))
	assert.True(t, apierrors.IsNotFound(memberB.Get(ctx,
		types.NamespacedName{Name: search.LoadBalancerDeploymentNameForCluster(1), Namespace: "ns"}, &appsv1.Deployment{})))
	assert.True(t, apierrors.IsNotFound(memberB.Get(ctx,
		types.NamespacedName{Name: search.LoadBalancerConfigMapNameForCluster(1), Namespace: "ns"}, &corev1.ConfigMap{})))
}

func TestDeleteEnvoyResources_SkipsUnregisteredCluster(t *testing.T) {
	ctx := context.Background()
	scheme := envoyTestScheme(t)
	central := fake.NewClientBuilder().WithScheme(scheme).Build()

	search := &searchv1.MongoDBSearch{ObjectMeta: metav1.ObjectMeta{Name: "mdb-search", Namespace: "ns"}}

	// Seed LB resources for BOTH a registered (index 0) and an unregistered (sentinel -1) cluster.
	memberA := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: search.LoadBalancerDeploymentNameForCluster(0), Namespace: "ns"}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: search.LoadBalancerConfigMapNameForCluster(0), Namespace: "ns"}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: search.LoadBalancerDeploymentNameForCluster(-1), Namespace: "ns"}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: search.LoadBalancerConfigMapNameForCluster(-1), Namespace: "ns"}},
	).Build()

	r := newMongoDBSearchEnvoyReconciler(central, "envoy:latest", map[string]client.Client{"a": memberA}, "")
	r.deleteEnvoyResources(ctx, search, []clusterWorkItem{
		{ClusterName: "registered", ClusterIndex: 0, Client: r.clientForCluster("a")},
		{ClusterName: "unregistered", ClusterIndex: -1, Client: r.clientForCluster("a")},
		// nil-Client sentinel (cluster named in spec but not registered): must be
		// skipped without panicking.
		{ClusterName: "no-client", ClusterIndex: 2, Client: nil},
	}, zap.S())

	// Registered cluster (index 0): resources deleted.
	err := memberA.Get(ctx, types.NamespacedName{Name: search.LoadBalancerDeploymentNameForCluster(0), Namespace: "ns"}, &appsv1.Deployment{})
	assert.True(t, apierrors.IsNotFound(err), "registered cluster's Envoy Deployment must be deleted")

	// Unregistered cluster (sentinel -1): resources skipped (survive).
	require.NoError(t, memberA.Get(ctx, types.NamespacedName{Name: search.LoadBalancerDeploymentNameForCluster(-1), Namespace: "ns"}, &appsv1.Deployment{}), "sentinel -1 Deployment must NOT be deleted")
	require.NoError(t, memberA.Get(ctx, types.NamespacedName{Name: search.LoadBalancerConfigMapNameForCluster(-1), Namespace: "ns"}, &corev1.ConfigMap{}), "sentinel -1 ConfigMap must NOT be deleted")
}

// Failing cluster ordered FIRST: a "worstPhase = last-seen" regression would
// downgrade to b's OK/Pending, so this guards proper cross-cluster worst-of.
func TestEnvoyReconcile_MultiCluster_FailedFirstThenOK_AggregatesFailed(t *testing.T) {
	ctx := context.Background()
	scheme := envoyTestScheme(t)

	// "a" first so the Failed phase is seen before the OK phase.
	search := newMCEnvoySearch("mdb-search", "ns", "", pinnedCluster("a", 0), pinnedCluster("b", 1))
	central := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&searchv1.MongoDBSearch{}).WithObjects(search).Build()
	seedSearchStateCM(t, ctx, central, "mdb-search", "ns", map[string]int{"a": 0, "b": 1})

	// Cluster "a" rejects every write → Failed; cluster "b" succeeds → OK.
	memberA := failingWriteClient{Client: fake.NewClientBuilder().WithScheme(scheme).Build()}
	memberB := fake.NewClientBuilder().WithScheme(scheme).Build()

	r := newMongoDBSearchEnvoyReconciler(central, "envoy:latest", map[string]client.Client{"a": memberA, "b": memberB}, "")

	_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "mdb-search", Namespace: "ns"}})
	require.NoError(t, err)

	patched := &searchv1.MongoDBSearch{}
	require.NoError(t, central.Get(ctx, types.NamespacedName{Name: "mdb-search", Namespace: "ns"}, patched))
	require.NotNil(t, patched.Status.LoadBalancer)
	assert.Equal(t, status.PhaseFailed, patched.Status.LoadBalancer.Phase,
		"a Failed + b OK must aggregate to top-level Failed regardless of cluster order")
}

// Unregistered MC cluster must surface Pending and must NOT use the kubeClient
// fallback (reserved for the empty-clients-map simulated-MC path).
func TestEnvoyReconcile_UnregisteredCluster_PendingAndNoCentralWrites(t *testing.T) {
	ctx := context.Background()
	scheme := envoyTestScheme(t)

	search := newMCEnvoySearch("mdb-search", "ns", "", pinnedCluster("cluster-a", 0), pinnedCluster("cluster-b", 1))
	central := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&searchv1.MongoDBSearch{}).WithObjects(search).Build()
	seedSearchStateCM(t, ctx, central, "mdb-search", "ns", map[string]int{"cluster-a": 0, "cluster-b": 1})

	// Only cluster-a is registered; cluster-b is in spec + mapping but has no client.
	memberA := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := newMongoDBSearchEnvoyReconciler(central, "envoy:latest", map[string]client.Client{"cluster-a": memberA}, "")

	_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "mdb-search", Namespace: "ns"}})
	require.NoError(t, err)

	patched := &searchv1.MongoDBSearch{}
	require.NoError(t, central.Get(ctx, types.NamespacedName{Name: "mdb-search", Namespace: "ns"}, patched))
	require.NotNil(t, patched.Status.LoadBalancer)
	assert.Equal(t, status.PhasePending, patched.Status.LoadBalancer.Phase,
		"unregistered cluster must aggregate to Pending")
	assert.Contains(t, patched.Status.LoadBalancer.Message, "not registered with the operator")

	// The registered cluster still reconciles into its member cluster.
	require.NoError(t, memberA.Get(ctx,
		types.NamespacedName{Name: search.LoadBalancerDeploymentNameForCluster(0), Namespace: "ns"}, &appsv1.Deployment{}),
		"registered cluster must still get its Envoy Deployment")

	// The unregistered cluster's resources must NOT land in the central cluster.
	assert.True(t, apierrors.IsNotFound(central.Get(ctx,
		types.NamespacedName{Name: search.LoadBalancerDeploymentNameForCluster(1), Namespace: "ns"}, &appsv1.Deployment{})),
		"unregistered cluster's Envoy Deployment must not be written to the central cluster")
	assert.True(t, apierrors.IsNotFound(central.Get(ctx,
		types.NamespacedName{Name: search.LoadBalancerConfigMapNameForCluster(1), Namespace: "ns"}, &corev1.ConfigMap{})),
		"unregistered cluster's Envoy ConfigMap must not be written to the central cluster")
}

// --- simulated-MC Reconcile() coverage (operatorClusterName != "") ------------

func newSimulatedMCEnvoySearch(name, namespace string, clusterBIndex int32) *searchv1.MongoDBSearch {
	return &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: searchv1.MongoDBSearchSpec{
			Source: &searchv1.MongoDBSource{
				ExternalMongoDBSource: &searchv1.ExternalMongoDBSource{HostAndPorts: []string{"mongo-0:27017"}},
			},
			LoadBalancer: &searchv1.LoadBalancerConfig{
				Managed: &searchv1.ManagedLBConfig{ExternalHostname: "mongot-{clusterName}.example.com"},
			},
			Clusters: []searchv1.ClusterSpec{
				pinnedCluster("cluster-a", 0),
				pinnedCluster("cluster-b", clusterBIndex),
			},
		},
	}
}

func TestEnvoyReconcile_SimulatedMC_NoMatchSilentNoOp(t *testing.T) {
	ctx := context.Background()
	scheme := envoyTestScheme(t)

	// Both entries pinned so the no-op is attributable to LocalizeToCluster, not validation.
	search := newSimulatedMCEnvoySearch("mdb-search", "ns", 1)
	central := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&searchv1.MongoDBSearch{}).WithObjects(search).Build()

	// operatorClusterName="cluster-c" — NOT in spec.clusters[].
	r := newMongoDBSearchEnvoyReconciler(central, "envoy:latest", nil, "cluster-c")

	res, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "mdb-search", Namespace: "ns"}})
	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, res, "no-match reconcile must return zero Result with no error")

	// No Envoy Deployment / ConfigMap at any index on the central client.
	for _, i := range []int{0, 1, 2} {
		assert.True(t, apierrors.IsNotFound(central.Get(ctx,
			types.NamespacedName{Name: search.LoadBalancerDeploymentNameForCluster(i), Namespace: "ns"}, &appsv1.Deployment{})),
			"Envoy Deployment at index %d must not exist", i)
		assert.True(t, apierrors.IsNotFound(central.Get(ctx,
			types.NamespacedName{Name: search.LoadBalancerConfigMapNameForCluster(i), Namespace: "ns"}, &corev1.ConfigMap{})),
			"Envoy ConfigMap at index %d must not exist", i)
	}

	// LB status untouched: the no-op returns before any status patch.
	patched := &searchv1.MongoDBSearch{}
	require.NoError(t, central.Get(ctx, types.NamespacedName{Name: "mdb-search", Namespace: "ns"}, patched))
	assert.Nil(t, patched.Status.LoadBalancer, "no-match reconcile must not write loadBalancer status")
}

// Single-entry unpinned is the only shape reaching the sim-MC gate (multi-entry partial
// pins are caught by ValidateSpec first); the gate runs before LocalizeToCluster, so this
// fails with a Failed loadBalancer status instead of being silently skipped.
func TestEnvoyReconcile_SimulatedMC_MissingClusterIndex_Invalid(t *testing.T) {
	ctx := context.Background()
	scheme := envoyTestScheme(t)

	// Single entry, no pin — built via the generic MC helper because
	// newSimulatedMCEnvoySearch can only produce pinned entries.
	search := newMCEnvoySearch("mdb-search", "ns", "", searchv1.ClusterSpec{ClusterName: "cluster-a"})
	central := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&searchv1.MongoDBSearch{}).WithObjects(search).Build()

	r := newMongoDBSearchEnvoyReconciler(central, "envoy:latest", nil, "cluster-a")

	_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "mdb-search", Namespace: "ns"}})
	require.NoError(t, err, "workflow.Invalid returns nil error; assert via status.loadBalancer")

	patched := &searchv1.MongoDBSearch{}
	require.NoError(t, central.Get(ctx, types.NamespacedName{Name: "mdb-search", Namespace: "ns"}, patched))
	require.NotNil(t, patched.Status.LoadBalancer, "Envoy controller must write loadBalancer status on validation failure")
	assert.Equal(t, status.PhaseFailed, patched.Status.LoadBalancer.Phase,
		"missing clusterIndex must surface as Failed on loadBalancer status")
	// workflow.Invalid capitalizes the first char, so match on the stable substring.
	assert.Contains(t, patched.Status.LoadBalancer.Message,
		"one operator per cluster requires clusterIndex on every spec.clusters[] entry (missing on",
		"message must come from ValidateSimulatedMCClusterIndices")
}

// A spec-validation failure on a CR with no LB configured must NOT invent a
// /status/loadBalancer sub-status; the main controller surfaces the same failure
// on the phase.
func TestEnvoyReconcile_ValidationFailure_NoLBConfigured_NoLBStatusWrite(t *testing.T) {
	ctx := context.Background()
	scheme := envoyTestScheme(t)

	// Removing the LB from the fully-pinned 2-cluster fixture makes ValidateSpec fail
	// (multi-cluster requires spec.loadBalancer.managed) on a CR with no LB surface.
	search := newSimulatedMCEnvoySearch("mdb-search", "ns", 1)
	search.Spec.LoadBalancer = nil
	central := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&searchv1.MongoDBSearch{}).WithObjects(search).Build()

	r := newMongoDBSearchEnvoyReconciler(central, "envoy:latest", nil, "cluster-a")

	_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "mdb-search", Namespace: "ns"}})
	require.NoError(t, err)

	patched := &searchv1.MongoDBSearch{}
	require.NoError(t, central.Get(ctx, types.NamespacedName{Name: "mdb-search", Namespace: "ns"}, patched))
	assert.Nil(t, patched.Status.LoadBalancer,
		"validation failure on a CR without spec.loadBalancer must not create a loadBalancer sub-status")
}

// Full-Reconcile dual-index guard: operator pinned to non-zero index 7 must render
// at the index-7 name, never array-position 0.
func TestEnvoyReconcile_SimulatedMC_Match_RendersAtPinnedIndex(t *testing.T) {
	ctx := context.Background()
	scheme := envoyTestScheme(t)

	search := newSimulatedMCEnvoySearch("mdb-search", "ns", 7)
	central := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&searchv1.MongoDBSearch{}).WithObjects(search).Build()
	seedSearchStateCM(t, ctx, central, "mdb-search", "ns", map[string]int{"cluster-b": 7})

	// members map empty: simulated-MC falls back to kubeClient (= central).
	r := newMongoDBSearchEnvoyReconciler(central, "envoy:latest", nil, "cluster-b")

	_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "mdb-search", Namespace: "ns"}})
	require.NoError(t, err)

	patched := &searchv1.MongoDBSearch{}
	require.NoError(t, central.Get(ctx, types.NamespacedName{Name: "mdb-search", Namespace: "ns"}, patched))
	require.NotNil(t, patched.Status.LoadBalancer)
	assert.Equal(t, status.PhaseRunning, patched.Status.LoadBalancer.Phase,
		"pinned-index reconcile should succeed; got msg=%q", patched.Status.LoadBalancer.Message)

	// Resources rendered at the pinned index 7 on the central (kubeClient) client.
	dep := &appsv1.Deployment{}
	require.NoError(t, central.Get(ctx,
		types.NamespacedName{Name: search.LoadBalancerDeploymentNameForCluster(7), Namespace: "ns"}, dep),
		"Envoy Deployment must render at pinned index 7")
	cm := &corev1.ConfigMap{}
	require.NoError(t, central.Get(ctx,
		types.NamespacedName{Name: search.LoadBalancerConfigMapNameForCluster(7), Namespace: "ns"}, cm),
		"Envoy ConfigMap must render at pinned index 7")

	assertSearchOwnerLabels(t, search, "cluster-b", dep, cm)

	// Nothing at index 0 — the array-position index must not leak through.
	assert.True(t, apierrors.IsNotFound(central.Get(ctx,
		types.NamespacedName{Name: search.LoadBalancerDeploymentNameForCluster(0), Namespace: "ns"}, &appsv1.Deployment{})),
		"no Envoy Deployment may render at array-position index 0")
	assert.True(t, apierrors.IsNotFound(central.Get(ctx,
		types.NamespacedName{Name: search.LoadBalancerConfigMapNameForCluster(0), Namespace: "ns"}, &corev1.ConfigMap{})),
		"no Envoy ConfigMap may render at array-position index 0")
}

// Registered member client but the cluster is absent from the persisted mapping
// (work item carries Client != nil, ClusterIndex == -1): the Reconcile loop must
// surface Pending with the registration message and must NOT call reconcileForCluster.
func TestEnvoyReconcile_RegisteredButMissingFromMapping_PendingNoReconcile(t *testing.T) {
	ctx := context.Background()
	scheme := envoyTestScheme(t)
	memberA := fake.NewClientBuilder().WithScheme(scheme).Build()

	search := newMCEnvoySearch("mdb-search", "ns", "", pinnedCluster("cluster-a", 0))
	central := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&searchv1.MongoDBSearch{}).WithObjects(search).Build()
	// State CM exists but its mapping OMITS cluster-a → sentinel -1.
	seedSearchStateCM(t, ctx, central, "mdb-search", "ns", map[string]int{"cluster-z": 0})

	r := newMongoDBSearchEnvoyReconciler(central, "envoy:latest", map[string]client.Client{"cluster-a": memberA}, "")

	_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "mdb-search", Namespace: "ns"}})
	require.NoError(t, err)

	patched := &searchv1.MongoDBSearch{}
	require.NoError(t, central.Get(ctx, types.NamespacedName{Name: "mdb-search", Namespace: "ns"}, patched))
	require.NotNil(t, patched.Status.LoadBalancer)
	assert.Equal(t, status.PhasePending, patched.Status.LoadBalancer.Phase)
	assert.Contains(t, patched.Status.LoadBalancer.Message,
		`Waiting for cluster "cluster-a" to be registered in search state`)

	depList := &appsv1.DeploymentList{}
	require.NoError(t, memberA.List(ctx, depList))
	assert.Empty(t, depList.Items, "reconcileForCluster must not run for a sentinel -1 cluster")
}

// Managed→unmanaged transition with a READABLE state CM: the cleanup path must
// build the work list from the persisted mapping (resolving cluster-a→3) and
// delete the per-cluster Envoy resources at that index via deleteEnvoyResources.
func TestEnvoyReconcile_LBCleanup_ReadableState_DeletesAcrossMapping(t *testing.T) {
	ctx := context.Background()
	scheme := envoyTestScheme(t)

	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-search", Namespace: "ns"},
		Spec: searchv1.MongoDBSearchSpec{
			Source:   &searchv1.MongoDBSource{ExternalMongoDBSource: &searchv1.ExternalMongoDBSource{HostAndPorts: []string{"mongo-0:27017"}}},
			Clusters: []searchv1.ClusterSpec{{ClusterName: "cluster-a"}},
		},
		// LB previously managed → status present; spec no longer managed → cleanup branch.
		Status: searchv1.MongoDBSearchStatus{LoadBalancer: &searchv1.LoadBalancerStatus{Phase: status.PhaseRunning}},
	}
	central := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&searchv1.MongoDBSearch{}).WithObjects(search).Build()
	seedSearchStateCM(t, ctx, central, "mdb-search", "ns", map[string]int{"cluster-a": 3})

	// Pre-seed the Envoy resources at the persisted index 3.
	require.NoError(t, central.Create(ctx, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: search.LoadBalancerDeploymentNameForCluster(3), Namespace: "ns"}}))
	require.NoError(t, central.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: search.LoadBalancerConfigMapNameForCluster(3), Namespace: "ns"}}))

	r := newMongoDBSearchEnvoyReconciler(central, "envoy:latest", nil, "")

	_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "mdb-search", Namespace: "ns"}})
	require.NoError(t, err)

	assert.True(t, apierrors.IsNotFound(central.Get(ctx,
		types.NamespacedName{Name: search.LoadBalancerDeploymentNameForCluster(3), Namespace: "ns"}, &appsv1.Deployment{})),
		"Envoy Deployment at the mapped index 3 must be deleted")
	assert.True(t, apierrors.IsNotFound(central.Get(ctx,
		types.NamespacedName{Name: search.LoadBalancerConfigMapNameForCluster(3), Namespace: "ns"}, &corev1.ConfigMap{})),
		"Envoy ConfigMap at the mapped index 3 must be deleted")
}

// State-load ERROR during cleanup (malformed state CM): the cleanup path must
// fall back to a SINGLE central work item (index 0), ignoring the mapping.
func TestEnvoyReconcile_LBCleanup_StateLoadError_CentralOnlyFallback(t *testing.T) {
	ctx := context.Background()
	scheme := envoyTestScheme(t)

	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-search", Namespace: "ns"},
		Spec: searchv1.MongoDBSearchSpec{
			Source:   &searchv1.MongoDBSource{ExternalMongoDBSource: &searchv1.ExternalMongoDBSource{HostAndPorts: []string{"mongo-0:27017"}}},
			Clusters: []searchv1.ClusterSpec{{ClusterName: "cluster-a"}},
		},
		Status: searchv1.MongoDBSearchStatus{LoadBalancer: &searchv1.LoadBalancerStatus{Phase: status.PhaseRunning}},
	}
	central := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&searchv1.MongoDBSearch{}).WithObjects(search).Build()
	// Malformed state CM → loadOrInitSearchState returns a non-NotFound error.
	require.NoError(t, central.Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-search-state", Namespace: "ns"},
		Data:       map[string]string{stateKey: "not-json"},
	}))

	// Seed Envoy resources at BOTH the central-fallback index 0 and a mapping index 3:
	// only index 0 must be deleted, proving the mapping was not consulted.
	require.NoError(t, central.Create(ctx, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: search.LoadBalancerDeploymentNameForCluster(0), Namespace: "ns"}}))
	require.NoError(t, central.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: search.LoadBalancerConfigMapNameForCluster(0), Namespace: "ns"}}))
	require.NoError(t, central.Create(ctx, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: search.LoadBalancerDeploymentNameForCluster(3), Namespace: "ns"}}))

	r := newMongoDBSearchEnvoyReconciler(central, "envoy:latest", nil, "")

	_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "mdb-search", Namespace: "ns"}})
	require.NoError(t, err)

	assert.True(t, apierrors.IsNotFound(central.Get(ctx,
		types.NamespacedName{Name: search.LoadBalancerDeploymentNameForCluster(0), Namespace: "ns"}, &appsv1.Deployment{})),
		"central fallback must delete the index-0 Envoy Deployment")
	assert.True(t, apierrors.IsNotFound(central.Get(ctx,
		types.NamespacedName{Name: search.LoadBalancerConfigMapNameForCluster(0), Namespace: "ns"}, &corev1.ConfigMap{})),
		"central fallback must delete the index-0 Envoy ConfigMap")
	require.NoError(t, central.Get(ctx,
		types.NamespacedName{Name: search.LoadBalancerDeploymentNameForCluster(3), Namespace: "ns"}, &appsv1.Deployment{}),
		"central-only fallback must NOT consult the mapping, so the index-3 Deployment survives")
}

// failingWriteClient wraps a client.Client and rejects every write so we can
// simulate a per-cluster Failed status without needing a real envtest.
type failingWriteClient struct {
	client.Client
}

func (f failingWriteClient) Create(_ context.Context, _ client.Object, _ ...client.CreateOption) error {
	return fmt.Errorf("simulated write failure")
}

func (f failingWriteClient) Update(_ context.Context, _ client.Object, _ ...client.UpdateOption) error {
	return fmt.Errorf("simulated write failure")
}

func (f failingWriteClient) Patch(_ context.Context, _ client.Object, _ client.Patch, _ ...client.PatchOption) error {
	return fmt.Errorf("simulated write failure")
}
