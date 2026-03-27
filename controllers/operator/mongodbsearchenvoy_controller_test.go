package operator

import (
	"testing"

	"github.com/stretchr/testify/assert"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1/common"
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
