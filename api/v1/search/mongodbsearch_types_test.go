package search

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/utils/ptr"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mongodb/mongodb-kubernetes/api/v1/status"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1/common"
)

func TestGetReplicasNilDefaultsToOne(t *testing.T) {
	s := &MongoDBSearch{}
	assert.Equal(t, 1, s.GetReplicas())
}

func TestGetReplicasReturnsExplicitValue(t *testing.T) {
	s := &MongoDBSearch{Spec: MongoDBSearchSpec{Replicas: ptr.To(int32(3))}}
	assert.Equal(t, 3, s.GetReplicas())
}

func TestHasMultipleReplicas(t *testing.T) {
	assert.False(t, (&MongoDBSearch{}).HasMultipleReplicas())
	assert.False(t, (&MongoDBSearch{Spec: MongoDBSearchSpec{Replicas: ptr.To(int32(1))}}).HasMultipleReplicas())
	assert.True(t, (&MongoDBSearch{Spec: MongoDBSearchSpec{Replicas: ptr.To(int32(2))}}).HasMultipleReplicas())
}

func TestEffectiveClusters(t *testing.T) {
	tests := []struct {
		name     string
		spec     MongoDBSearchSpec
		expected []ClusterSpec
	}{
		{
			name:     "nil clusters, all top-level unset",
			spec:     MongoDBSearchSpec{},
			expected: []ClusterSpec{{}},
		},
		{
			name:     "nil clusters, only top-level Replicas set",
			spec:     MongoDBSearchSpec{Replicas: ptr.To(int32(3))},
			expected: []ClusterSpec{{Replicas: ptr.To(int32(3))}},
		},
		{
			name: "nil clusters, all four top-level fields set",
			spec: MongoDBSearchSpec{
				Replicas:                 ptr.To(int32(2)),
				ResourceRequirements:     &corev1.ResourceRequirements{},
				Persistence:              &common.Persistence{},
				StatefulSetConfiguration: &common.StatefulSetConfiguration{},
			},
			expected: []ClusterSpec{{
				Replicas:                 ptr.To(int32(2)),
				ResourceRequirements:     &corev1.ResourceRequirements{},
				Persistence:              &common.Persistence{},
				StatefulSetConfiguration: &common.StatefulSetConfiguration{},
			}},
		},
		{
			name: "spec.clusters set, returned unchanged",
			spec: MongoDBSearchSpec{
				Clusters: &[]ClusterSpec{
					{ClusterName: "us-east", Replicas: ptr.To(int32(2))},
					{ClusterName: "us-west", Replicas: ptr.To(int32(3))},
				},
			},
			expected: []ClusterSpec{
				{ClusterName: "us-east", Replicas: ptr.To(int32(2))},
				{ClusterName: "us-west", Replicas: ptr.To(int32(3))},
			},
		},
		{
			name:     "spec.clusters explicitly empty slice — preserved",
			spec:     MongoDBSearchSpec{Clusters: &[]ClusterSpec{}},
			expected: []ClusterSpec{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &MongoDBSearch{Spec: tt.spec}
			got := EffectiveClusters(s)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestEffectiveClustersDoesNotMutate(t *testing.T) {
	// Independent invariant — a pure function must not mutate spec.
	s := &MongoDBSearch{Spec: MongoDBSearchSpec{Replicas: ptr.To(int32(7))}}
	_ = EffectiveClusters(s)
	assert.Nil(t, s.Spec.Clusters)
	assert.Equal(t, int32(7), *s.Spec.Replicas)
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
			if tc.clusters != nil {
				cs := tc.clusters
				s.Spec.Clusters = &cs
			}
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
			Clusters:     &clusters,
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
			Clusters:     &clusters,
		},
	}
	assert.Equal(t, "search-0.shard-a.lb.example.com:443", s.GetManagedLBEndpointForClusterShard(0, "shard-a"))
	assert.Equal(t, "search-1.shard-b.lb.example.com:443", s.GetManagedLBEndpointForClusterShard(1, "shard-b"))
}

func TestGetManagedLBEndpointForClusterShard_NotManaged(t *testing.T) {
	s := &MongoDBSearch{Spec: MongoDBSearchSpec{}}
	assert.Equal(t, "", s.GetManagedLBEndpointForClusterShard(0, "shard-0"))
}

func TestProxyServiceNamespacedNameForCluster(t *testing.T) {
	s := &MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-search", Namespace: "ns"},
	}

	// Single-cluster (clusterIndex=0) preserves the legacy single-cluster name.
	got := s.ProxyServiceNamespacedNameForCluster(0)
	assert.Equal(t, "mdb-search-search-0-proxy-svc", got.Name)
	assert.Equal(t, "ns", got.Namespace)
	// Same as legacy ProxyServiceNamespacedName when index=0.
	assert.Equal(t, s.ProxyServiceNamespacedName(), got)

	// Per-cluster index suffix differs.
	got1 := s.ProxyServiceNamespacedNameForCluster(1)
	assert.Equal(t, "mdb-search-search-1-proxy-svc", got1.Name)

	got2 := s.ProxyServiceNamespacedNameForCluster(2)
	assert.Equal(t, "mdb-search-search-2-proxy-svc", got2.Name)
}

func TestMongotConfigConfigMapNameForCluster(t *testing.T) {
	s := &MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-search", Namespace: "ns"},
	}

	got0 := s.MongotConfigConfigMapNameForCluster(0)
	assert.Equal(t, "mdb-search-search-config", got0.Name) // legacy match for index 0
	got1 := s.MongotConfigConfigMapNameForCluster(1)
	assert.Equal(t, "mdb-search-search-1-config", got1.Name)
}
