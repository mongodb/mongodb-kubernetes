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
		name     string
		template string
		clusters []ClusterSpec
		index    int
		want     string
	}{
		{
			name:     "clusterName substitution first cluster",
			template: "{clusterName}.search-lb.example.com:443",
			clusters: []ClusterSpec{{ClusterName: "us-east-k8s"}, {ClusterName: "eu-west-k8s"}},
			index:    0,
			want:     "us-east-k8s.search-lb.example.com:443",
		},
		{
			name:     "clusterIndex substitution second cluster",
			template: "search-{clusterIndex}.lb.example.com:443",
			clusters: []ClusterSpec{{ClusterName: "us-east-k8s"}, {ClusterName: "eu-west-k8s"}},
			index:    1,
			want:     "search-1.lb.example.com:443",
		},
		{
			name:     "both placeholders present",
			template: "{clusterName}-{clusterIndex}.lb.example.com:443",
			clusters: []ClusterSpec{{ClusterName: "us-east-k8s"}, {ClusterName: "eu-west-k8s"}},
			index:    1,
			want:     "eu-west-k8s-1.lb.example.com:443",
		},
		{
			name:     "no placeholders left untouched",
			template: "static.lb.example.com:443",
			clusters: []ClusterSpec{{ClusterName: "us-east-k8s"}},
			index:    0,
			want:     "static.lb.example.com:443",
		},
		{
			name:     "legacy spec.clusters nil leaves placeholders literal",
			template: "{clusterName}.lb.example.com:443",
			clusters: nil,
			index:    0,
			want:     "{clusterName}.lb.example.com:443",
		},
		{
			name:     "out-of-range index leaves placeholders literal",
			template: "{clusterName}.lb.example.com:443",
			clusters: []ClusterSpec{{ClusterName: "us-east-k8s"}},
			index:    5,
			want:     "{clusterName}.lb.example.com:443",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &MongoDBSearch{
				Spec: MongoDBSearchSpec{
					LoadBalancer: &LoadBalancerConfig{Managed: &ManagedLBConfig{ExternalHostname: tc.template}},
				},
			}
			s.Spec.Clusters = tc.clusters
			assert.Equal(t, tc.want, s.GetManagedLBEndpointForCluster(tc.index))
		})
	}
}

func TestGetManagedLBEndpointForCluster_NotManaged(t *testing.T) {
	s := &MongoDBSearch{Spec: MongoDBSearchSpec{}}
	assert.Equal(t, "", s.GetManagedLBEndpointForCluster(0))

	s.Spec.LoadBalancer = &LoadBalancerConfig{Managed: &ManagedLBConfig{ExternalHostname: ""}}
	assert.Equal(t, "", s.GetManagedLBEndpointForCluster(0))
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
			got[s.GetManagedLBEndpointForClusterShard(i, sh)] = struct{}{}
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
	clusters := []ClusterSpec{{ClusterName: "us-east-k8s"}, {ClusterName: "eu-west-k8s"}}
	s := &MongoDBSearch{
		Spec: MongoDBSearchSpec{
			LoadBalancer: &LoadBalancerConfig{Managed: &ManagedLBConfig{ExternalHostname: template}},
			Clusters:     clusters,
		},
	}
	assert.Equal(t, "search-0.shard-a.lb.example.com:443", s.GetManagedLBEndpointForClusterShard(0, "shard-a"))
	assert.Equal(t, "search-1.shard-b.lb.example.com:443", s.GetManagedLBEndpointForClusterShard(1, "shard-b"))
}

func TestGetManagedLBEndpointForClusterShard_NotManaged(t *testing.T) {
	s := &MongoDBSearch{Spec: MongoDBSearchSpec{}}
	assert.Equal(t, "", s.GetManagedLBEndpointForClusterShard(0, "shard-0"))
}

func TestGetManagedLBEndpointForClusterLevel(t *testing.T) {
	clusters := []ClusterSpec{{ClusterName: "us-east-k8s"}, {ClusterName: "eu-west-k8s"}}
	mk := func(tmpl string) *MongoDBSearch {
		return &MongoDBSearch{
			Spec: MongoDBSearchSpec{
				LoadBalancer: &LoadBalancerConfig{Managed: &ManagedLBConfig{ExternalHostname: tmpl}},
				Clusters:     clusters,
			},
		}
	}
	tests := []struct {
		name     string
		template string
		index    int
		want     string
	}{
		{"strip prefix, resolve clusterName", "{shardName}.{clusterName}.search.example.com", 0, "us-east-k8s.search.example.com"},
		{"strip prefix, resolve clusterIndex", "{shardName}.search-{clusterIndex}.example.com", 1, "search-1.example.com"},
		{"strip prefix, single-cluster sharded shape (no cluster placeholders)", "{shardName}.search.example.com", 0, "search.example.com"},
		// {shardName} as a name component (not a leading prefix) is not derivable to a
		// single cluster-level hostname; expect "" so the caller falls back to the
		// cluster-level proxy Service FQDN.
		{"shardName as name component returns empty", "search-{clusterIndex}-{shardName}-proxy.example.com", 0, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, mk(tc.template).GetManagedLBEndpointForClusterLevel(tc.index))
		})
	}
}

func TestGetManagedLBEndpointForClusterLevel_NotManaged(t *testing.T) {
	s := &MongoDBSearch{Spec: MongoDBSearchSpec{}}
	assert.Equal(t, "", s.GetManagedLBEndpointForClusterLevel(0))
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
