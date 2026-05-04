package operator

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apiv1 "github.com/mongodb/mongodb-kubernetes/api/v1"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"
	"github.com/mongodb/mongodb-kubernetes/api/v1/status"
	"github.com/mongodb/mongodb-kubernetes/controllers/searchcontroller"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1/common"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/util/merge"
)

// TODO: Add full Reconcile() integration tests covering:
//   - LB mode transitions (managed -> unmanaged, managed -> disabled)
//   - Envoy Deployment/ConfigMap lifecycle (create, update, cleanup)
//   - Error paths (missing envoy image, unreachable source)
//   - Status updates for the loadBalancer sub-status

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

			route := buildReplicaSetRoute(search)

			assert.Equal(t, "rs", route.Name)
			assert.Equal(t, "rs", route.NameSafe)
			assert.Equal(t, tt.expectedSNI, route.SNIHostname)
			assert.Equal(t, "mdb-search-search-svc.test-ns.svc.cluster.local", route.UpstreamHost)
			assert.Equal(t, int32(27028), route.UpstreamPort)
		})
	}
}

func TestBuildShardRoutes(t *testing.T) {
	shardNames := []string{"mdb-sh-0", "mdb-sh-1"}

	tests := []struct {
		name         string
		endpoint     string
		expectedSNIs []string
	}{
		{
			name:     "no endpoint uses proxy service FQDNs",
			endpoint: "",
			expectedSNIs: []string{
				"mdb-search-search-0-mdb-sh-0-proxy-svc.test-ns.svc.cluster.local",
				"mdb-search-search-0-mdb-sh-1-proxy-svc.test-ns.svc.cluster.local",
			},
		},
		{
			name:     "externalHostname template resolves per shard",
			endpoint: "mongot-{shardName}-ns.apps.example.com",
			expectedSNIs: []string{
				"mongot-mdb-sh-0-ns.apps.example.com",
				"mongot-mdb-sh-1-ns.apps.example.com",
			},
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

			routes := buildShardRoutes(search, shardNames)

			assert.Len(t, routes, 2)
			for i, route := range routes {
				assert.Equal(t, shardNames[i], route.Name)
				assert.Equal(t, tt.expectedSNIs[i], route.SNIHostname)
			}
		})
	}
}

func TestBuildEnvoyPodSpec_DefaultResources(t *testing.T) {
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "ns"},
	}
	resources := envoyResourceRequirements(search)
	podSpec := buildEnvoyPodSpec(search, "", nil, false, "envoy:latest", resources, false)

	assert.Len(t, podSpec.Containers, 1)
	assert.Equal(t, "envoy", podSpec.Containers[0].Name)
	assert.Equal(t, resource.MustParse("100m"), podSpec.Containers[0].Resources.Requests[corev1.ResourceCPU])
	assert.Equal(t, resource.MustParse("128Mi"), podSpec.Containers[0].Resources.Requests[corev1.ResourceMemory])
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
		clusterName    string
		expectedCMName string
	}{
		{
			name:           "single-cluster keeps legacy unsuffixed name",
			clusterName:    "",
			expectedCMName: search.LoadBalancerConfigMapName(),
		},
		{
			name:           "MC cluster a uses per-cluster suffixed name",
			clusterName:    "cluster-a",
			expectedCMName: search.LoadBalancerConfigMapNameForCluster("cluster-a"),
		},
		{
			name:           "MC cluster b uses per-cluster suffixed name",
			clusterName:    "cluster-b",
			expectedCMName: search.LoadBalancerConfigMapNameForCluster("cluster-b"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			podSpec := buildEnvoyPodSpec(search, tc.clusterName, nil, false, "envoy:latest", resources, false)

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
					Deployment: &common.DeploymentConfiguration{
						SpecWrapper: common.DeploymentSpecWrapper{
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
						MetadataWrapper: common.DeploymentMetadataWrapper{
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
	podSpec := buildEnvoyPodSpec(search, "", nil, false, "envoy:latest", resources, false)

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
					Deployment: &common.DeploymentConfiguration{
						SpecWrapper: common.DeploymentSpecWrapper{
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

	podSpec := buildEnvoyPodSpec(search, "", nil, false, "envoy:latest", resources, false)

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
			Clusters: &[]searchv1.ClusterSpec{
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

	routes := buildRoutesForCluster(search, nil, "us-east-k8s")
	require.Len(t, routes, 1)
	assert.Equal(t, "rs", routes[0].Name)
	assert.Equal(t, "us-east-k8s", routes[0].ClusterID)
	assert.Equal(t, "mongot-us-east-k8s.example.com", routes[0].SNIHostname)
}

func TestBuildRoutesForCluster_RS_NoTemplateAppendsClusterIDToServiceFQDN(t *testing.T) {
	// No externalHostname template — controller appends -<clusterID> to the service-FQDN base
	// so SNI strings stay distinct per cluster.
	one := int32(1)
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-search", Namespace: "test-ns"},
		Spec: searchv1.MongoDBSearchSpec{
			Clusters: &[]searchv1.ClusterSpec{
				{ClusterName: "us-east-k8s", Replicas: &one},
			},
		},
	}

	routes := buildRoutesForCluster(search, nil, "us-east-k8s")
	require.Len(t, routes, 1)
	assert.Equal(t, "us-east-k8s", routes[0].ClusterID)
	assert.Contains(t, routes[0].SNIHostname, "us-east-k8s",
		"per-cluster SNI must encode the cluster identifier when no externalHostname template is provided")
}

func TestBuildRoutesForCluster_SingleClusterUnchanged(t *testing.T) {
	// clusterName == "" must produce identical routes to buildRoutes (back-compat path).
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-search", Namespace: "test-ns"},
	}

	mc := buildRoutesForCluster(search, nil, "")
	sc := []envoyRoute{buildReplicaSetRoute(search)}

	require.Len(t, mc, 1)
	assert.Equal(t, "", mc[0].ClusterID)
	assert.Equal(t, sc[0].SNIHostname, mc[0].SNIHostname)
	assert.Equal(t, sc[0].UpstreamHost, mc[0].UpstreamHost)
}

// mockShardedSourceForEnvoy is a minimal SearchSourceShardedDeployment double for these tests.
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
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-search", Namespace: "test-ns"},
		Spec: searchv1.MongoDBSearchSpec{
			LoadBalancer: &searchv1.LoadBalancerConfig{
				Managed: &searchv1.ManagedLBConfig{
					// Both placeholders present — must substitute both.
					ExternalHostname: "mongot-{clusterName}-{shardName}.example.com",
				},
			},
		},
	}
	src := &mockShardedSourceForEnvoy{shardNames: []string{"mdb-sh-0", "mdb-sh-1"}}

	east := buildRoutesForCluster(search, src, "us-east-k8s")
	west := buildRoutesForCluster(search, src, "eu-west-k8s")

	require.Len(t, east, 2)
	require.Len(t, west, 2)

	// Per-(cluster, shard) SNI emitted with both substitutions.
	assert.Equal(t, "us-east-k8s", east[0].ClusterID)
	assert.Equal(t, "mongot-us-east-k8s-mdb-sh-0.example.com", east[0].SNIHostname)
	assert.Equal(t, "mongot-us-east-k8s-mdb-sh-1.example.com", east[1].SNIHostname)
	assert.Equal(t, "mongot-eu-west-k8s-mdb-sh-0.example.com", west[0].SNIHostname)
	assert.Equal(t, "mongot-eu-west-k8s-mdb-sh-1.example.com", west[1].SNIHostname)

	// All 4 SNIs must be unique.
	all := []string{east[0].SNIHostname, east[1].SNIHostname, west[0].SNIHostname, west[1].SNIHostname}
	seen := map[string]struct{}{}
	for _, s := range all {
		seen[s] = struct{}{}
	}
	assert.Len(t, seen, 4, "per-(cluster, shard) SNIs must all be distinct")
}

// --- reconciler constructor with member-cluster client maps -------------------

func TestNewMongoDBSearchEnvoyReconciler_AcceptsMemberClusters(t *testing.T) {
	central := fake.NewClientBuilder().Build()
	memberA := fake.NewClientBuilder().Build()
	memberB := fake.NewClientBuilder().Build()
	members := map[string]client.Client{
		"us-east-k8s": memberA,
		"eu-west-k8s": memberB,
	}

	r := newMongoDBSearchEnvoyReconciler(central, "envoy:latest", members)
	require.NotNil(t, r)
	assert.Len(t, r.memberClusterClientsMap, 2)
	assert.NotNil(t, r.memberClusterClientsMap["us-east-k8s"])
	assert.NotNil(t, r.memberClusterClientsMap["eu-west-k8s"])
	assert.Len(t, r.memberClusterSecretClientsMap, 2)
}

func TestNewMongoDBSearchEnvoyReconciler_NilMembersMap(t *testing.T) {
	central := fake.NewClientBuilder().Build()
	r := newMongoDBSearchEnvoyReconciler(central, "envoy:latest", nil)
	require.NotNil(t, r)
	assert.Empty(t, r.memberClusterClientsMap)
	assert.Empty(t, r.memberClusterSecretClientsMap)
}

// --- selectEnvoyClient --------------------------------------------------------

func TestSelectEnvoyClient(t *testing.T) {
	central := kubernetesClient.NewClient(fake.NewClientBuilder().Build())
	memberA := kubernetesClient.NewClient(fake.NewClientBuilder().Build())
	members := map[string]kubernetesClient.Client{"a": memberA}

	// Empty cluster name → central (single-cluster path)
	assert.Equal(t, central, selectEnvoyClient("", central, members))
	// Known member name → that member
	assert.Equal(t, memberA, selectEnvoyClient("a", central, members))
	// Unknown member name → silent fallback to central (mirrors searchcontroller.SelectClusterClient)
	assert.Equal(t, central, selectEnvoyClient("zzz", central, members))
}

// --- per-cluster name helpers + cross-cluster enqueue labels ------------------

func TestLoadBalancerNamesForCluster_SingleClusterUnchanged(t *testing.T) {
	search := &searchv1.MongoDBSearch{ObjectMeta: metav1.ObjectMeta{Name: "mdb-search", Namespace: "ns"}}
	assert.Equal(t, search.LoadBalancerDeploymentName(), search.LoadBalancerDeploymentNameForCluster(""))
	assert.Equal(t, search.LoadBalancerConfigMapName(), search.LoadBalancerConfigMapNameForCluster(""))
}

func TestLoadBalancerNamesForCluster_MultiClusterAppendsClusterID(t *testing.T) {
	search := &searchv1.MongoDBSearch{ObjectMeta: metav1.ObjectMeta{Name: "mdb-search", Namespace: "ns"}}
	dep := search.LoadBalancerDeploymentNameForCluster("us-east-k8s")
	cm := search.LoadBalancerConfigMapNameForCluster("us-east-k8s")
	assert.Contains(t, dep, "us-east-k8s")
	assert.Contains(t, cm, "us-east-k8s")
	assert.NotEqual(t, search.LoadBalancerDeploymentName(), dep)
}

func TestEnvoyLabels_StampsCrossClusterEnqueueLabels(t *testing.T) {
	search := &searchv1.MongoDBSearch{ObjectMeta: metav1.ObjectMeta{Name: "mdb-search", Namespace: "ns"}}

	// Single-cluster: cluster-name label must be absent.
	single := envoyLabelsForCluster(search, "")
	assert.Equal(t, "mdb-search", single[envoyOwnerSearchNameLabel])
	assert.Equal(t, "ns", single[envoyOwnerSearchNamespaceLabel])
	_, hasCluster := single[envoyClusterNameLabel]
	assert.False(t, hasCluster)

	// Multi-cluster: all three labels present.
	mc := envoyLabelsForCluster(search, "us-east-k8s")
	assert.Equal(t, "mdb-search", mc[envoyOwnerSearchNameLabel])
	assert.Equal(t, "ns", mc[envoyOwnerSearchNamespaceLabel])
	assert.Equal(t, "us-east-k8s", mc[envoyClusterNameLabel])
}

// --- envoy replicas defaulting ------------------------------------------

func TestEnvoyReplicas_DefaultsTo1(t *testing.T) {
	search := &searchv1.MongoDBSearch{ObjectMeta: metav1.ObjectMeta{Name: "mdb-search", Namespace: "ns"}}
	assert.Equal(t, int32(1), envoyReplicas(search))
}

func TestEnvoyReplicas_TopLevelOnly(t *testing.T) {
	three := int32(3)
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-search", Namespace: "ns"},
		Spec: searchv1.MongoDBSearchSpec{
			LoadBalancer: &searchv1.LoadBalancerConfig{
				Managed: &searchv1.ManagedLBConfig{Replicas: &three},
			},
		},
	}
	assert.Equal(t, int32(3), envoyReplicas(search))
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
	require.NoError(t, apiv1.AddToScheme(scheme))
	_ = searchv1.AddToScheme // keep import live
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
	})

	search := &searchv1.MongoDBSearch{ObjectMeta: metav1.ObjectMeta{Name: "mdb-search", Namespace: "ns"}}
	require.NoError(t, r.ensureConfigMap(context.Background(), search, `{"x":1}`, "a", zap.S()))

	// Member A has it.
	cmA := &corev1.ConfigMap{}
	require.NoError(t, memberA.Get(context.Background(),
		types.NamespacedName{Name: search.LoadBalancerConfigMapNameForCluster("a"), Namespace: "ns"}, cmA))
	assert.Equal(t, `{"x":1}`, cmA.Data["envoy.json"])
	// Cluster name label stamped.
	assert.Equal(t, "a", cmA.Labels[envoyClusterNameLabel])
	assert.Equal(t, "mdb-search", cmA.Labels[envoyOwnerSearchNameLabel])

	// Central and member B do not.
	cm := &corev1.ConfigMap{}
	err := central.Get(context.Background(),
		types.NamespacedName{Name: search.LoadBalancerConfigMapNameForCluster("a"), Namespace: "ns"}, cm)
	assert.True(t, apierrors.IsNotFound(err))
	err = memberB.Get(context.Background(),
		types.NamespacedName{Name: search.LoadBalancerConfigMapNameForCluster("a"), Namespace: "ns"}, cm)
	assert.True(t, apierrors.IsNotFound(err))
}

func TestEnsureConfigMap_SingleCluster_WritesToCentralWithOwnerRef(t *testing.T) {
	scheme := envoyTestScheme(t)
	central := fake.NewClientBuilder().WithScheme(scheme).Build()

	r := newMongoDBSearchEnvoyReconciler(central, "envoy:latest", nil)

	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-search", Namespace: "ns", UID: "abc"},
	}
	require.NoError(t, r.ensureConfigMap(context.Background(), search, `{"x":1}`, "", zap.S()))

	cm := &corev1.ConfigMap{}
	require.NoError(t, central.Get(context.Background(),
		types.NamespacedName{Name: search.LoadBalancerConfigMapName(), Namespace: "ns"}, cm))

	// Owner ref present in single-cluster path (back-compat).
	require.Len(t, cm.OwnerReferences, 1)
	assert.Equal(t, "mdb-search", cm.OwnerReferences[0].Name)
}

func TestEnsureConfigMap_MultiCluster_NoOwnerRef(t *testing.T) {
	scheme := envoyTestScheme(t)
	central := fake.NewClientBuilder().WithScheme(scheme).Build()
	memberA := fake.NewClientBuilder().WithScheme(scheme).Build()

	r := newMongoDBSearchEnvoyReconciler(central, "envoy:latest", map[string]client.Client{"a": memberA})

	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-search", Namespace: "ns", UID: "abc"},
	}
	require.NoError(t, r.ensureConfigMap(context.Background(), search, `{"x":1}`, "a", zap.S()))

	cm := &corev1.ConfigMap{}
	require.NoError(t, memberA.Get(context.Background(),
		types.NamespacedName{Name: search.LoadBalancerConfigMapNameForCluster("a"), Namespace: "ns"}, cm))

	// Cross-cluster: no owner ref (k8s GC does not span clusters).
	assert.Empty(t, cm.OwnerReferences)
}

// --- reconcile loop + per-cluster status --------------------------------------

func TestBuildClusterWorkList_SingleClusterDegenerate(t *testing.T) {
	r := newMongoDBSearchEnvoyReconciler(fake.NewClientBuilder().Build(), "envoy:latest", nil)
	search := &searchv1.MongoDBSearch{}
	wl := r.buildClusterWorkList(search)
	require.Len(t, wl, 1)
	assert.Equal(t, "", wl[0].ClusterName)
}

func TestBuildClusterWorkList_EmptySpecClusters_TreatedAsSingle(t *testing.T) {
	central := fake.NewClientBuilder().Build()
	r := newMongoDBSearchEnvoyReconciler(central, "envoy:latest", map[string]client.Client{
		"a": fake.NewClientBuilder().Build(),
	})
	search := &searchv1.MongoDBSearch{}
	wl := r.buildClusterWorkList(search)
	require.Len(t, wl, 1)
	assert.Equal(t, "", wl[0].ClusterName)
}

func TestBuildClusterWorkList_MultiCluster_OneItemPerSpecEntry(t *testing.T) {
	central := fake.NewClientBuilder().Build()
	r := newMongoDBSearchEnvoyReconciler(central, "envoy:latest", map[string]client.Client{
		"a": fake.NewClientBuilder().Build(),
		"b": fake.NewClientBuilder().Build(),
	})
	search := &searchv1.MongoDBSearch{
		Spec: searchv1.MongoDBSearchSpec{
			Clusters: &[]searchv1.ClusterSpec{
				{ClusterName: "a"},
				{ClusterName: "b"},
			},
		},
	}
	wl := r.buildClusterWorkList(search)
	require.Len(t, wl, 2)
	assert.Equal(t, "a", wl[0].ClusterName)
	assert.Equal(t, "b", wl[1].ClusterName)
}

// TestWorstOfClusterPhases regresses the canonical invariant declared on
// LoadBalancerStatus: top-level Phase must equal WorstOfPhase across the
// per-cluster Clusters[*].Phase entries when len(Clusters) > 0.
//
// This replaces the previous TestIsWorsePhase, which exercised a partial
// rank function (only Failed/Pending/Running were ordered). The new helper
// delegates to searchv1.WorstOfPhase, picking up the full Phase ordering
// (Failed > Pending > Running > Updated > Disabled > Unsupported) from a
// single source of truth.
func TestWorstOfClusterPhases(t *testing.T) {
	cases := []struct {
		name string
		in   []searchv1.ClusterLoadBalancerStatus
		want status.Phase
	}{
		{
			name: "empty slice — single-cluster degenerate path",
			in:   nil,
			want: status.PhaseRunning,
		},
		{
			name: "single Running",
			in:   []searchv1.ClusterLoadBalancerStatus{{Phase: status.PhaseRunning}},
			want: status.PhaseRunning,
		},
		{
			name: "Failed beats Running",
			in: []searchv1.ClusterLoadBalancerStatus{
				{Phase: status.PhaseRunning},
				{Phase: status.PhaseFailed},
			},
			want: status.PhaseFailed,
		},
		{
			name: "Pending beats Running",
			in: []searchv1.ClusterLoadBalancerStatus{
				{Phase: status.PhaseRunning},
				{Phase: status.PhasePending},
				{Phase: status.PhaseRunning},
			},
			want: status.PhasePending,
		},
		{
			name: "Failed beats Pending",
			in: []searchv1.ClusterLoadBalancerStatus{
				{Phase: status.PhasePending},
				{Phase: status.PhaseFailed},
				{Phase: status.PhaseRunning},
			},
			want: status.PhaseFailed,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, worstOfClusterPhases(tc.in))
		})
	}
}

func TestReconcileForCluster_UnknownClusterPending(t *testing.T) {
	scheme := envoyTestScheme(t)
	central := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := newMongoDBSearchEnvoyReconciler(central, "envoy:latest", map[string]client.Client{
		"a": fake.NewClientBuilder().WithScheme(scheme).Build(),
	})
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-search", Namespace: "ns"},
		Spec: searchv1.MongoDBSearchSpec{
			Clusters: &[]searchv1.ClusterSpec{{ClusterName: "missing-cluster"}},
		},
	}
	st := r.reconcileForCluster(context.Background(), search, nil, false, nil, "missing-cluster", zap.S())
	assert.Equal(t, status.PhasePending, st.Phase())
	assert.Contains(t, searchcontroller.MessageFromStatus(st), "missing-cluster")
}

func TestMapEnvoyObjectToSearch(t *testing.T) {
	// Object with both labels → reconcile request returned.
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mdb-search-search-lb-0-a-config",
			Namespace: "ns",
			Labels: map[string]string{
				envoyOwnerSearchNameLabel:      "mdb-search",
				envoyOwnerSearchNamespaceLabel: "ns",
				envoyClusterNameLabel:          "a",
			},
		},
	}
	reqs := mapEnvoyObjectToSearch(context.Background(), cm)
	require.Len(t, reqs, 1)
	assert.Equal(t, "mdb-search", reqs[0].Name)
	assert.Equal(t, "ns", reqs[0].Namespace)

	// Object missing labels → no enqueue.
	cmNoLabels := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "ns"}}
	assert.Empty(t, mapEnvoyObjectToSearch(context.Background(), cmNoLabels))

	// Partial labels → no enqueue (both required).
	cmPartial := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "x",
			Namespace: "ns",
			Labels:    map[string]string{envoyOwnerSearchNameLabel: "mdb-search"},
		},
	}
	assert.Empty(t, mapEnvoyObjectToSearch(context.Background(), cmPartial))
}

func TestReconcileForCluster_RendersInMemberCluster(t *testing.T) {
	scheme := envoyTestScheme(t)
	central := fake.NewClientBuilder().WithScheme(scheme).Build()
	memberA := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := newMongoDBSearchEnvoyReconciler(central, "envoy:latest", map[string]client.Client{"a": memberA})

	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-search", Namespace: "ns"},
		Spec: searchv1.MongoDBSearchSpec{
			LoadBalancer: &searchv1.LoadBalancerConfig{
				Managed: &searchv1.ManagedLBConfig{ExternalHostname: "mongot-{clusterName}.example.com"},
			},
			Clusters: &[]searchv1.ClusterSpec{{ClusterName: "a"}},
		},
	}

	st := r.reconcileForCluster(context.Background(), search, nil, false, nil, "a", zap.S())
	require.True(t, st.IsOK(), "expected OK, got %s: %s", st.Phase(), searchcontroller.MessageFromStatus(st))

	// Member cluster has Deployment + ConfigMap; central does not.
	dep := &appsv1.Deployment{}
	require.NoError(t, memberA.Get(context.Background(),
		types.NamespacedName{Name: search.LoadBalancerDeploymentNameForCluster("a"), Namespace: "ns"}, dep))
	cm := &corev1.ConfigMap{}
	require.NoError(t, memberA.Get(context.Background(),
		types.NamespacedName{Name: search.LoadBalancerConfigMapNameForCluster("a"), Namespace: "ns"}, cm))

	err := central.Get(context.Background(),
		types.NamespacedName{Name: search.LoadBalancerDeploymentNameForCluster("a"), Namespace: "ns"}, &appsv1.Deployment{})
	assert.True(t, apierrors.IsNotFound(err))
}

func TestEnsureDeployment_Replicas_DefaultsTo1(t *testing.T) {
	scheme := envoyTestScheme(t)
	central := fake.NewClientBuilder().WithScheme(scheme).Build()
	memberA := fake.NewClientBuilder().WithScheme(scheme).Build()

	r := newMongoDBSearchEnvoyReconciler(central, "envoy:latest", map[string]client.Client{"a": memberA})

	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-search", Namespace: "ns"},
		Spec: searchv1.MongoDBSearchSpec{
			LoadBalancer: &searchv1.LoadBalancerConfig{Managed: &searchv1.ManagedLBConfig{}},
			Clusters:     &[]searchv1.ClusterSpec{{ClusterName: "a"}},
		},
	}
	require.NoError(t, r.ensureDeployment(context.Background(), search, `{"x":1}`, "a", nil, zap.S()))

	dep := &appsv1.Deployment{}
	require.NoError(t, memberA.Get(context.Background(),
		types.NamespacedName{Name: search.LoadBalancerDeploymentNameForCluster("a"), Namespace: "ns"}, dep))
	require.NotNil(t, dep.Spec.Replicas)
	assert.Equal(t, int32(1), *dep.Spec.Replicas, "envoy replicas must default to 1 when unset")
}

// --- end-to-end Reconcile + status aggregation -------------------------------

// TestReconcile_PerClusterStatus_Aggregated exercises the full Reconcile path:
// two clusters in spec.clusters[]; one is a member registered with the operator
// (succeeds), the other isn't (Pending). The status.loadBalancer.clusters slice
// must hold one entry per cluster, and the aggregated top-level Phase must be
// the worst-of (Pending here).
func TestReconcile_PerClusterStatus_Aggregated(t *testing.T) {
	scheme := envoyTestScheme(t)
	memberA := fake.NewClientBuilder().WithScheme(scheme).Build()

	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mdb-search",
			Namespace: "ns",
			UID:       "abc",
		},
		Spec: searchv1.MongoDBSearchSpec{
			Source: &searchv1.MongoDBSource{
				ExternalMongoDBSource: &searchv1.ExternalMongoDBSource{
					HostAndPorts: []string{"mongo-0:27017"},
				},
			},
			LoadBalancer: &searchv1.LoadBalancerConfig{
				Managed: &searchv1.ManagedLBConfig{
					ExternalHostname: "mongot-{clusterName}.example.com",
				},
			},
			Clusters: &[]searchv1.ClusterSpec{
				{ClusterName: "a"},
				{ClusterName: "missing"},
			},
		},
	}

	central := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&searchv1.MongoDBSearch{}).WithObjects(search).Build()
	r := newMongoDBSearchEnvoyReconciler(central, "envoy:latest", map[string]client.Client{"a": memberA})

	res, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "mdb-search", Namespace: "ns"},
	})
	require.NoError(t, err)
	assert.False(t, res.Requeue)

	// Re-fetch to see the patched status.
	patched := &searchv1.MongoDBSearch{}
	require.NoError(t, central.Get(context.Background(),
		types.NamespacedName{Name: "mdb-search", Namespace: "ns"}, patched))
	require.NotNil(t, patched.Status.LoadBalancer, "status.loadBalancer must be populated")
	assert.Equal(t, status.PhasePending, patched.Status.LoadBalancer.Phase, "worst-of (Running, Pending) is Pending")
	require.Len(t, patched.Status.LoadBalancer.Clusters, 2, "one per-cluster status entry per spec.clusters[i]")

	byName := map[string]searchv1.ClusterLoadBalancerStatus{}
	for _, c := range patched.Status.LoadBalancer.Clusters {
		byName[c.ClusterName] = c
	}
	assert.Equal(t, status.PhaseRunning, byName["a"].Phase)
	assert.Equal(t, status.PhasePending, byName["missing"].Phase)

	// And cluster "a" actually got its Deployment + ConfigMap in the member-cluster client.
	require.NoError(t, memberA.Get(context.Background(),
		types.NamespacedName{Name: search.LoadBalancerDeploymentNameForCluster("a"), Namespace: "ns"}, &appsv1.Deployment{}))
	require.NoError(t, memberA.Get(context.Background(),
		types.NamespacedName{Name: search.LoadBalancerConfigMapNameForCluster("a"), Namespace: "ns"}, &corev1.ConfigMap{}))
}

// TestReconcile_AllClustersFailed_TopLevelPhaseIsFailed asserts that when a
// per-cluster reconcile returns workflow.Failed, the aggregated top-level
// phase patched onto status.loadBalancer is Failed (not Pending). Without
// this guard, all errors would be downgraded to Pending in the final write.
func TestReconcile_AllClustersFailed_TopLevelPhaseIsFailed(t *testing.T) {
	scheme := envoyTestScheme(t)

	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-search", Namespace: "ns"},
		Spec: searchv1.MongoDBSearchSpec{
			Source: &searchv1.MongoDBSource{
				ExternalMongoDBSource: &searchv1.ExternalMongoDBSource{
					HostAndPorts: []string{"mongo-0:27017"},
				},
			},
			LoadBalancer: &searchv1.LoadBalancerConfig{
				Managed: &searchv1.ManagedLBConfig{
					ExternalHostname: "mongot-{clusterName}.example.com",
				},
			},
			Clusters: &[]searchv1.ClusterSpec{{ClusterName: "a"}},
		},
	}
	central := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&searchv1.MongoDBSearch{}).WithObjects(search).Build()
	// Member client is a fake that fails every write — drives Failed.
	memberA := failingWriteClient{Client: fake.NewClientBuilder().WithScheme(scheme).Build()}

	r := newMongoDBSearchEnvoyReconciler(central, "envoy:latest", map[string]client.Client{"a": memberA})

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "mdb-search", Namespace: "ns"},
	})
	require.NoError(t, err)

	patched := &searchv1.MongoDBSearch{}
	require.NoError(t, central.Get(context.Background(),
		types.NamespacedName{Name: "mdb-search", Namespace: "ns"}, patched))
	require.NotNil(t, patched.Status.LoadBalancer)
	assert.Equal(t, status.PhaseFailed, patched.Status.LoadBalancer.Phase,
		"all-Failed clusters must aggregate to top-level Failed, not Pending")
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
