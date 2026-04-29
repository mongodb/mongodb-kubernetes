package operator

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"
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
	podSpec := buildEnvoyPodSpec(search, nil, false, "envoy:latest", resources, false)

	assert.Len(t, podSpec.Containers, 1)
	assert.Equal(t, "envoy", podSpec.Containers[0].Name)
	assert.Equal(t, resource.MustParse("100m"), podSpec.Containers[0].Resources.Requests[corev1.ResourceCPU])
	assert.Equal(t, resource.MustParse("128Mi"), podSpec.Containers[0].Resources.Requests[corev1.ResourceMemory])
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
	podSpec := buildEnvoyPodSpec(search, nil, false, "envoy:latest", resources, false)

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

	podSpec := buildEnvoyPodSpec(search, nil, false, "envoy:latest", resources, false)

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

// --- B16 per-cluster route renderer tests -------------------------------------

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

// mockShardedSourceForEnvoy is a minimal SearchSourceShardedDeployment double for B16 tests.
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

// --- B16 reconciler constructor with member-cluster client maps ---------------

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

// --- B16 selectEnvoyClient ----------------------------------------------------

func TestSelectEnvoyClient(t *testing.T) {
	central := kubernetesClient.NewClient(fake.NewClientBuilder().Build())
	memberA := kubernetesClient.NewClient(fake.NewClientBuilder().Build())
	members := map[string]kubernetesClient.Client{"a": memberA}

	// Empty cluster name → central (single-cluster path)
	assert.Equal(t, central, selectEnvoyClient("", central, members))
	// Known member name → that member
	assert.Equal(t, memberA, selectEnvoyClient("a", central, members))
	// Unknown member name → silent fallback to central (mirrors B1 selectClusterClient)
	assert.Equal(t, central, selectEnvoyClient("zzz", central, members))
}

// --- B16 per-cluster name helpers + cross-cluster enqueue labels --------------

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

// --- B16 per-cluster replicas defaulting -------------------------------------

func TestEnvoyReplicasForCluster_SingleCluster_DefaultsTo1(t *testing.T) {
	search := &searchv1.MongoDBSearch{ObjectMeta: metav1.ObjectMeta{Name: "mdb-search", Namespace: "ns"}}
	assert.Equal(t, int32(1), envoyReplicasForCluster(search, ""))
}

func TestEnvoyReplicasForCluster_TopLevelOnly(t *testing.T) {
	three := int32(3)
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-search", Namespace: "ns"},
		Spec: searchv1.MongoDBSearchSpec{
			LoadBalancer: &searchv1.LoadBalancerConfig{
				Managed: &searchv1.ManagedLBConfig{Replicas: &three},
			},
		},
	}
	assert.Equal(t, int32(3), envoyReplicasForCluster(search, ""))
	assert.Equal(t, int32(3), envoyReplicasForCluster(search, "us-east-k8s"))
}

func TestEnvoyReplicasForCluster_PerClusterOverride(t *testing.T) {
	top := int32(2)
	override := int32(5)
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-search", Namespace: "ns"},
		Spec: searchv1.MongoDBSearchSpec{
			LoadBalancer: &searchv1.LoadBalancerConfig{
				Managed: &searchv1.ManagedLBConfig{Replicas: &top},
			},
			Clusters: &[]searchv1.ClusterSpec{
				{
					ClusterName: "us-east-k8s",
					LoadBalancer: &searchv1.PerClusterLoadBalancerConfig{
						Managed: &searchv1.ManagedLBConfig{Replicas: &override},
					},
				},
				{ClusterName: "eu-west-k8s"}, // inherits top-level (2)
			},
		},
	}
	assert.Equal(t, int32(5), envoyReplicasForCluster(search, "us-east-k8s"))
	assert.Equal(t, int32(2), envoyReplicasForCluster(search, "eu-west-k8s"))
}
