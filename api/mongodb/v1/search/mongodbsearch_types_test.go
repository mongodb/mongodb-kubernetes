package search

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
)

func TestGetReplicasForCluster(t *testing.T) {
	t.Run("unset defaults to one", func(t *testing.T) {
		s := &MongoDBSearch{Spec: MongoDBSearchSpec{Clusters: []ClusterSpec{{}}}}
		assert.Equal(t, 1, s.GetReplicasForCluster(""))
	})

	t.Run("explicit value", func(t *testing.T) {
		s := &MongoDBSearch{Spec: MongoDBSearchSpec{Clusters: []ClusterSpec{{Replicas: ptr.To(int32(3))}}}}
		assert.Equal(t, 3, s.GetReplicasForCluster(""))
	})

	t.Run("explicit zero is honored", func(t *testing.T) {
		// clusters[].replicas=0 is a legitimate value (operator-driven scale-to-0
		// for taking mongot offline via the CR). Distinguishing it from "unset"
		// is the contract callers like search-connectivity tests rely on.
		s := &MongoDBSearch{Spec: MongoDBSearchSpec{Clusters: []ClusterSpec{{Replicas: ptr.To(int32(0))}}}}
		assert.Equal(t, 0, s.GetReplicasForCluster(""))
	})

	t.Run("by cluster name", func(t *testing.T) {
		s := &MongoDBSearch{Spec: MongoDBSearchSpec{Clusters: []ClusterSpec{
			{ClusterName: "a", Replicas: ptr.To(int32(2))},
			{ClusterName: "b", Replicas: ptr.To(int32(5))},
		}}}
		assert.Equal(t, 2, s.GetReplicasForCluster("a"))
		assert.Equal(t, 5, s.GetReplicasForCluster("b"))
	})
}

func TestHasMultipleReplicas(t *testing.T) {
	mk := func(cs ...ClusterSpec) *MongoDBSearch {
		return &MongoDBSearch{Spec: MongoDBSearchSpec{Clusters: cs}}
	}
	assert.False(t, mk(ClusterSpec{}).HasMultipleReplicas())
	assert.False(t, mk(ClusterSpec{Replicas: ptr.To(int32(0))}).HasMultipleReplicas())
	assert.False(t, mk(ClusterSpec{Replicas: ptr.To(int32(1))}).HasMultipleReplicas())
	assert.True(t, mk(ClusterSpec{Replicas: ptr.To(int32(2))}).HasMultipleReplicas())
	// Any single cluster over one replica trips the load-balancer requirement.
	assert.True(t, mk(
		ClusterSpec{ClusterName: "a", Replicas: ptr.To(int32(1))},
		ClusterSpec{ClusterName: "b", Replicas: ptr.To(int32(3))},
	).HasMultipleReplicas())
}

func TestEffectiveClusters(t *testing.T) {
	clusters := []ClusterSpec{
		{ClusterName: "us-east", Replicas: ptr.To(int32(2))},
		{ClusterName: "us-west", Replicas: ptr.To(int32(3))},
	}
	s := &MongoDBSearch{Spec: MongoDBSearchSpec{Clusters: clusters}}
	assert.Equal(t, clusters, s.EffectiveClusters())
}

func TestEffectiveClusterFor(t *testing.T) {
	clusterA := ClusterSpec{ClusterName: "cluster-a"}
	resB := &corev1.ResourceRequirements{}
	clusterB := ClusterSpec{ClusterName: "cluster-b", ResourceRequirements: resB}

	t.Run("empty clusterName returns first entry", func(t *testing.T) {
		s := &MongoDBSearch{Spec: MongoDBSearchSpec{Clusters: []ClusterSpec{clusterA, clusterB}}}
		got, err := s.EffectiveClusterFor("")
		require.NoError(t, err)
		assert.Equal(t, clusterA, got)
	})

	t.Run("matches by clusterName", func(t *testing.T) {
		s := &MongoDBSearch{Spec: MongoDBSearchSpec{Clusters: []ClusterSpec{clusterA, clusterB}}}
		gotB, err := s.EffectiveClusterFor("cluster-b")
		require.NoError(t, err)
		assert.Same(t, resB, gotB.ResourceRequirements)
	})

	t.Run("unknown clusterName returns error", func(t *testing.T) {
		s := &MongoDBSearch{Spec: MongoDBSearchSpec{
			Clusters: []ClusterSpec{{ClusterName: "only"}},
		}}
		got, err := s.EffectiveClusterFor("missing")
		require.Error(t, err)
		assert.Equal(t, ClusterSpec{}, got)
	})
}

func TestUpdateStatus_MainPath(t *testing.T) {
	s := &MongoDBSearch{}
	s.UpdateStatus(status.PhaseRunning, NewMongoDBSearchVersionOption("1.0"))

	assert.Equal(t, status.PhaseRunning, s.Status.Phase)
	assert.Equal(t, "1.0", s.Status.Version)
	assert.Nil(t, s.Status.LoadBalancer)
}

func TestUpdateStatus_LoadBalancerPath(t *testing.T) {
	s := &MongoDBSearch{}
	partOpt := NewSearchPartOption(SearchPartLoadBalancer)
	s.UpdateStatus(status.PhaseRunning, partOpt, status.NewMessageOption("Envoy ready"))

	assert.NotNil(t, s.Status.LoadBalancer)
	assert.Equal(t, status.PhaseRunning, s.Status.LoadBalancer.Phase)
	assert.Equal(t, "Envoy ready", s.Status.LoadBalancer.Message)
	// Main status untouched
	assert.Equal(t, status.Phase(""), s.Status.Phase)
}

func TestGetStatusPath_Default(t *testing.T) {
	s := &MongoDBSearch{}
	assert.Equal(t, "/status", s.GetStatusPath())
}

func TestGetStatusPath_LoadBalancer(t *testing.T) {
	s := &MongoDBSearch{}
	partOpt := NewSearchPartOption(SearchPartLoadBalancer)
	assert.Equal(t, "/status/loadBalancer", s.GetStatusPath(partOpt))
}

func TestGetStatus_Default(t *testing.T) {
	s := &MongoDBSearch{}
	s.Status.Phase = status.PhaseRunning
	got := s.GetStatus()
	assert.Equal(t, s.Status, got)
}

func TestGetStatus_LoadBalancer(t *testing.T) {
	s := &MongoDBSearch{}
	s.Status.LoadBalancer = &LoadBalancerStatus{Phase: status.PhaseRunning, Message: "ok"}
	partOpt := NewSearchPartOption(SearchPartLoadBalancer)
	got := s.GetStatus(partOpt)
	assert.Equal(t, s.Status.LoadBalancer, got)
}

// nil LB → GetStatus returns nil, used by clearLBStatus to patch null
func TestGetStatus_LoadBalancerNil(t *testing.T) {
	s := &MongoDBSearch{}
	partOpt := NewSearchPartOption(SearchPartLoadBalancer)
	got := s.GetStatus(partOpt)
	assert.Nil(t, got)
}

// Failed→Running transition must not carry over the old error message
func TestUpdateStatus_LoadBalancerClearsStaleMessage(t *testing.T) {
	s := &MongoDBSearch{}
	partOpt := NewSearchPartOption(SearchPartLoadBalancer)

	// Simulate a failure with a message
	s.UpdateStatus(status.PhaseFailed, partOpt, status.NewMessageOption("missing image"))
	assert.Equal(t, "missing image", s.Status.LoadBalancer.Message)

	// Transition to Running without a message — old message must be cleared
	s.UpdateStatus(status.PhaseRunning, partOpt)
	assert.Equal(t, status.PhaseRunning, s.Status.LoadBalancer.Phase)
	assert.Equal(t, "", s.Status.LoadBalancer.Message)
}

func TestIsLoadBalancerReady(t *testing.T) {
	tests := []struct {
		name     string
		search   MongoDBSearch
		expected bool
	}{
		{
			name:     "no LB configured",
			search:   MongoDBSearch{},
			expected: true,
		},
		{
			name: "managed LB, status nil",
			search: MongoDBSearch{
				Spec: MongoDBSearchSpec{
					LoadBalancer: &LoadBalancerConfig{Managed: &ManagedLBConfig{}},
				},
			},
			expected: false,
		},
		{
			name: "managed LB, status Running",
			search: MongoDBSearch{
				Spec: MongoDBSearchSpec{
					LoadBalancer: &LoadBalancerConfig{Managed: &ManagedLBConfig{}},
				},
				Status: MongoDBSearchStatus{
					LoadBalancer: &LoadBalancerStatus{Phase: status.PhaseRunning},
				},
			},
			expected: true,
		},
		{
			name: "managed LB, status Pending",
			search: MongoDBSearch{
				Spec: MongoDBSearchSpec{
					LoadBalancer: &LoadBalancerConfig{Managed: &ManagedLBConfig{}},
				},
				Status: MongoDBSearchStatus{
					LoadBalancer: &LoadBalancerStatus{Phase: status.PhasePending},
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.search.IsLoadBalancerReady())
		})
	}
}

func TestGetManagedLBEndpointForCluster(t *testing.T) {
	tests := []struct {
		name         string
		template     string
		clusterName  string
		clusterIndex int
		want         string
	}{
		{
			name:         "clusterName substitution",
			template:     "{clusterName}.search-lb.example.com:443",
			clusterName:  "us-east-k8s",
			clusterIndex: 0,
			want:         "us-east-k8s.search-lb.example.com:443",
		},
		{
			name:         "clusterIndex substitution",
			template:     "search-{clusterIndex}.lb.example.com:443",
			clusterName:  "eu-west-k8s",
			clusterIndex: 1,
			want:         "search-1.lb.example.com:443",
		},
		{
			name:         "both placeholders present",
			template:     "{clusterName}-{clusterIndex}.lb.example.com:443",
			clusterName:  "eu-west-k8s",
			clusterIndex: 1,
			want:         "eu-west-k8s-1.lb.example.com:443",
		},
		{
			name:         "no placeholders left untouched",
			template:     "static.lb.example.com:443",
			clusterName:  "us-east-k8s",
			clusterIndex: 0,
			want:         "static.lb.example.com:443",
		},
		{
			// Persisted index 2 survives a non-last cluster removal (mapping
			// {a:0,b:1,c:2}, spec.clusters [a,c]). The old positional impl would
			// either index Spec.Clusters[2] out of range (raw template) or
			// substitute the wrong cluster — the name+index args fix that.
			name:         "persisted index past current spec length still substitutes",
			template:     "{clusterName}-{clusterIndex}.lb.example.com:443",
			clusterName:  "c",
			clusterIndex: 2,
			want:         "c-2.lb.example.com:443",
		},
		{
			// Legacy single-cluster path: the envoy controller passes name "".
			// {clusterName} stays literal (no name was ever assigned); {clusterIndex}
			// still resolves so the index-suffixed form is intact.
			name:         "empty clusterName leaves clusterName placeholder literal",
			template:     "search-{clusterIndex}-{clusterName}.lb.example.com:443",
			clusterName:  "",
			clusterIndex: 0,
			want:         "search-0-{clusterName}.lb.example.com:443",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &MongoDBSearch{
				Spec: MongoDBSearchSpec{
					LoadBalancer: &LoadBalancerConfig{Managed: &ManagedLBConfig{ExternalHostname: tc.template}},
				},
			}
			assert.Equal(t, tc.want, s.GetManagedLBEndpointForCluster(tc.clusterName, tc.clusterIndex))
		})
	}
}

func TestGetManagedLBEndpointForCluster_NotManaged(t *testing.T) {
	s := &MongoDBSearch{Spec: MongoDBSearchSpec{}}
	assert.Equal(t, "", s.GetManagedLBEndpointForCluster("us-east-k8s", 0))

	s.Spec.LoadBalancer = &LoadBalancerConfig{Managed: &ManagedLBConfig{ExternalHostname: ""}}
	assert.Equal(t, "", s.GetManagedLBEndpointForCluster("us-east-k8s", 0))
}

// TestGetManagedLBEndpointForCluster_NamedSingleCluster pins the named-single-cluster
// path: buildClusterWorkList emits clusterName "" for a single-cluster install even
// when spec.clusters[0] is named, so the resolver is called with ("", 0). {clusterName}
// must still resolve to the spec's single cluster name — not render literally.
func TestGetManagedLBEndpointForCluster_NamedSingleCluster(t *testing.T) {
	s := &MongoDBSearch{
		Spec: MongoDBSearchSpec{
			LoadBalancer: &LoadBalancerConfig{Managed: &ManagedLBConfig{ExternalHostname: "{clusterName}.lb.example.com:443"}},
			Clusters:     []ClusterSpec{{ClusterName: "x"}},
		},
	}
	assert.Equal(t, "x.lb.example.com:443", s.GetManagedLBEndpointForCluster("", 0))
	// ClusterLevel path shares the same recovery.
	s.Spec.LoadBalancer.Managed.ExternalHostname = "{shardName}.{clusterName}.lb.example.com:443"
	assert.Equal(t, "x.lb.example.com:443", s.GetManagedLBEndpointForClusterLevel("", 0))
}

func TestGetManagedLBEndpointForClusterShard_CrossProduct(t *testing.T) {
	template := "{clusterName}.{shardName}.lb.example.com:443"
	clusters := []ClusterSpec{{ClusterName: "us-east-k8s"}, {ClusterName: "eu-west-k8s"}}
	shards := []string{"shard-0", "shard-1"}
	s := &MongoDBSearch{
		Spec: MongoDBSearchSpec{
			LoadBalancer: &LoadBalancerConfig{Managed: &ManagedLBConfig{ExternalHostname: template}},
			Clusters:     clusters,
		},
	}

	got := make(map[string]struct{})
	for i := range clusters {
		for _, sh := range shards {
			got[s.GetManagedLBEndpointForClusterShard(clusters[i].ClusterName, i, sh)] = struct{}{}
		}
	}
	assert.Len(t, got, 4, "2x2 cross-product should yield 4 distinct hostnames")
	assert.Contains(t, got, "us-east-k8s.shard-0.lb.example.com:443")
	assert.Contains(t, got, "us-east-k8s.shard-1.lb.example.com:443")
	assert.Contains(t, got, "eu-west-k8s.shard-0.lb.example.com:443")
	assert.Contains(t, got, "eu-west-k8s.shard-1.lb.example.com:443")
}

func TestGetManagedLBEndpointForClusterShard_ClusterIndex(t *testing.T) {
	template := "search-{clusterIndex}.{shardName}.lb.example.com:443"
	s := &MongoDBSearch{
		Spec: MongoDBSearchSpec{
			LoadBalancer: &LoadBalancerConfig{Managed: &ManagedLBConfig{ExternalHostname: template}},
		},
	}
	assert.Equal(t, "search-0.shard-a.lb.example.com:443", s.GetManagedLBEndpointForClusterShard("us-east-k8s", 0, "shard-a"))
	assert.Equal(t, "search-1.shard-b.lb.example.com:443", s.GetManagedLBEndpointForClusterShard("eu-west-k8s", 1, "shard-b"))
}

func TestGetManagedLBEndpointForClusterShard_NotManaged(t *testing.T) {
	s := &MongoDBSearch{Spec: MongoDBSearchSpec{}}
	assert.Equal(t, "", s.GetManagedLBEndpointForClusterShard("us-east-k8s", 0, "shard-0"))
}

func TestGetManagedLBEndpointForClusterLevel(t *testing.T) {
	mk := func(tmpl string) *MongoDBSearch {
		return &MongoDBSearch{
			Spec: MongoDBSearchSpec{
				LoadBalancer: &LoadBalancerConfig{Managed: &ManagedLBConfig{ExternalHostname: tmpl}},
			},
		}
	}
	tests := []struct {
		name         string
		template     string
		clusterName  string
		clusterIndex int
		want         string
	}{
		{"strip prefix, resolve clusterName", "{shardName}.{clusterName}.search.example.com", "us-east-k8s", 0, "us-east-k8s.search.example.com"},
		{"strip prefix, resolve clusterIndex", "{shardName}.search-{clusterIndex}.example.com", "eu-west-k8s", 1, "search-1.example.com"},
		{"strip prefix, single-cluster sharded shape (no cluster placeholders)", "{shardName}.search.example.com", "us-east-k8s", 0, "search.example.com"},
		// Persisted index 2 surviving a non-last removal must still resolve.
		{"persisted index past current spec length still substitutes", "{shardName}.{clusterName}-{clusterIndex}.example.com", "c", 2, "c-2.example.com"},
		// {shardName} as a name component (not a leading prefix) is not derivable to a
		// single cluster-level hostname; expect "" so the caller falls back to the
		// cluster-level proxy Service FQDN.
		{"shardName as name component returns empty", "search-{clusterIndex}-{shardName}-proxy.example.com", "us-east-k8s", 0, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, mk(tc.template).GetManagedLBEndpointForClusterLevel(tc.clusterName, tc.clusterIndex))
		})
	}
}

func TestGetManagedLBEndpointForClusterLevel_NotManaged(t *testing.T) {
	s := &MongoDBSearch{Spec: MongoDBSearchSpec{}}
	assert.Equal(t, "", s.GetManagedLBEndpointForClusterLevel("us-east-k8s", 0))
}

func TestProxyServiceNamespacedNameForCluster(t *testing.T) {
	s := &MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-search", Namespace: "ns"},
	}

	got := s.ProxyServiceNamespacedNameForCluster(0)
	assert.Equal(t, "mdb-search-search-0-proxy-svc", got.Name)
	assert.Equal(t, "ns", got.Namespace)
	assert.Equal(t, s.ProxyServiceNamespacedName(), got, "index 0 must match legacy ProxyServiceNamespacedName")

	got1 := s.ProxyServiceNamespacedNameForCluster(1)
	assert.Equal(t, "mdb-search-search-1-proxy-svc", got1.Name)
}

func TestMongotConfigConfigMapNameForCluster(t *testing.T) {
	s := &MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-search", Namespace: "ns"},
	}

	got0 := s.MongotConfigConfigMapNameForCluster(0)
	assert.Equal(t, "mdb-search-search-0-config", got0.Name)
	got1 := s.MongotConfigConfigMapNameForCluster(1)
	assert.Equal(t, "mdb-search-search-1-config", got1.Name)
}
