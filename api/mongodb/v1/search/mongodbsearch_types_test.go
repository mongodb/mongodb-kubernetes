package search

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
)

func TestReplicasOrDefault(t *testing.T) {
	t.Run("unset defaults to one", func(t *testing.T) {
		assert.Equal(t, 1, ClusterSpec{}.ReplicasOrDefault())
	})

	t.Run("explicit value", func(t *testing.T) {
		assert.Equal(t, 3, ClusterSpec{Replicas: ptr.To(int32(3))}.ReplicasOrDefault())
	})

	t.Run("explicit zero is honored", func(t *testing.T) {
		// clusters[].replicas=0 is a legitimate value (operator-driven scale-to-0
		// for taking mongot offline via the CR). Distinguishing it from "unset"
		// is the contract callers like search-connectivity tests rely on.
		assert.Equal(t, 0, ClusterSpec{Replicas: ptr.To(int32(0))}.ReplicasOrDefault())
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
		ClusterSpec{Name: "a", Replicas: ptr.To(int32(1))},
		ClusterSpec{Name: "b", Replicas: ptr.To(int32(3))},
	).HasMultipleReplicas())
}

func TestEffectiveClusters(t *testing.T) {
	clusters := []ClusterSpec{
		{Name: "us-east", Replicas: ptr.To(int32(2))},
		{Name: "us-west", Replicas: ptr.To(int32(3))},
	}
	s := &MongoDBSearch{Spec: MongoDBSearchSpec{Clusters: clusters}}
	assert.Equal(t, clusters, s.EffectiveClusters())
}

func TestEffectiveClusterFor(t *testing.T) {
	clusterA := ClusterSpec{Name: "cluster-a"}
	resB := &corev1.ResourceRequirements{}
	clusterB := ClusterSpec{Name: "cluster-b", ResourceRequirements: resB}

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
			Clusters: []ClusterSpec{{Name: "only"}},
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
					Clusters: []ClusterSpec{{LoadBalancer: &LoadBalancerConfig{Managed: &ManagedLBConfig{}}}},
				},
			},
			expected: false,
		},
		{
			name: "managed LB, status Running",
			search: MongoDBSearch{
				Spec: MongoDBSearchSpec{
					Clusters: []ClusterSpec{{LoadBalancer: &LoadBalancerConfig{Managed: &ManagedLBConfig{}}}},
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
					Clusters: []ClusterSpec{{LoadBalancer: &LoadBalancerConfig{Managed: &ManagedLBConfig{}}}},
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
	managed := func(hostname string) *LoadBalancerConfig {
		return &LoadBalancerConfig{Managed: &ManagedLBConfig{ExternalHostname: hostname}}
	}
	s := &MongoDBSearch{
		Spec: MongoDBSearchSpec{
			Clusters: []ClusterSpec{
				{Name: "us-east-k8s", LoadBalancer: managed("us-east.search-lb.example.com:443")},
				{Name: "eu-west-k8s", LoadBalancer: managed("eu-west.search-lb.example.com:443")},
				{Name: "ap-south-k8s"},
			},
		},
	}
	assert.Equal(t, "us-east.search-lb.example.com:443", s.GetManagedLBEndpointForCluster("us-east-k8s"))
	assert.Equal(t, "eu-west.search-lb.example.com:443", s.GetManagedLBEndpointForCluster("eu-west-k8s"))
	assert.Equal(t, "us-east.search-lb.example.com:443", s.GetManagedLBEndpointForCluster(""), "empty name means first cluster")
	assert.Equal(t, "", s.GetManagedLBEndpointForCluster("ap-south-k8s"), "cluster without LB")
	assert.Equal(t, "", s.GetManagedLBEndpointForCluster("unknown"), "unknown cluster name")
}

func TestGetManagedLBEndpointForCluster_NotManaged(t *testing.T) {
	s := &MongoDBSearch{Spec: MongoDBSearchSpec{}}
	assert.Equal(t, "", s.GetManagedLBEndpointForCluster(""))

	s.Spec.Clusters = []ClusterSpec{{LoadBalancer: &LoadBalancerConfig{Managed: &ManagedLBConfig{ExternalHostname: ""}}}}
	assert.Equal(t, "", s.GetManagedLBEndpointForCluster(""))
}

func TestGetManagedLBEndpointForClusterShard_CrossProduct(t *testing.T) {
	managed := func(hostname string) *LoadBalancerConfig {
		return &LoadBalancerConfig{Managed: &ManagedLBConfig{ExternalHostname: hostname}}
	}
	clusters := []ClusterSpec{
		{Name: "us-east-k8s", LoadBalancer: managed("us-east-k8s.{shardName}.lb.example.com:443")},
		{Name: "eu-west-k8s", LoadBalancer: managed("eu-west-k8s.{shardName}.lb.example.com:443")},
	}
	shards := []string{"shard-0", "shard-1"}
	s := &MongoDBSearch{Spec: MongoDBSearchSpec{Clusters: clusters}}

	got := make(map[string]struct{})
	for _, c := range clusters {
		for _, sh := range shards {
			got[s.GetManagedLBEndpointForClusterShard(c.Name, sh)] = struct{}{}
		}
	}
	assert.Len(t, got, 4, "2x2 cross-product should yield 4 distinct hostnames")
	assert.Contains(t, got, "us-east-k8s.shard-0.lb.example.com:443")
	assert.Contains(t, got, "us-east-k8s.shard-1.lb.example.com:443")
	assert.Contains(t, got, "eu-west-k8s.shard-0.lb.example.com:443")
	assert.Contains(t, got, "eu-west-k8s.shard-1.lb.example.com:443")
}

func TestGetManagedLBEndpointForClusterShard_NotManaged(t *testing.T) {
	s := &MongoDBSearch{Spec: MongoDBSearchSpec{}}
	assert.Equal(t, "", s.GetManagedLBEndpointForClusterShard("", "shard-0"))
}

func TestGetRouterHostnameForCluster(t *testing.T) {
	mk := func(router string) *MongoDBSearch {
		return &MongoDBSearch{
			Spec: MongoDBSearchSpec{
				Clusters: []ClusterSpec{
					{Name: "us-east-k8s", LoadBalancer: &LoadBalancerConfig{Managed: &ManagedLBConfig{RouterHostname: router}}},
				},
			},
		}
	}
	// routerHostname is used verbatim — no trimming, even if a {shardName} placeholder is present
	// (validation rejects that separately).
	tests := []struct {
		name   string
		router string
		want   string
	}{
		{"returned verbatim", "search.example.com:443", "search.example.com:443"},
		{"placeholder not trimmed (used as-is)", "search-{shardName}-proxy.example.com", "search-{shardName}-proxy.example.com"},
		{"empty when unset", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, mk(tc.router).GetRouterHostnameForCluster("us-east-k8s"))
		})
	}
}

func TestGetRouterHostnameForCluster_NotManaged(t *testing.T) {
	s := &MongoDBSearch{Spec: MongoDBSearchSpec{}}
	assert.Equal(t, "", s.GetRouterHostnameForCluster(""))
}

func TestValidateOperatorPerClusterIndices(t *testing.T) {
	tests := []struct {
		name     string
		clusters []ClusterSpec
		wantErr  bool
	}{
		{
			name:     "nil clusters is invalid",
			clusters: nil,
			wantErr:  true,
		},
		{
			name:     "empty clusters is invalid",
			clusters: []ClusterSpec{},
			wantErr:  true,
		},
		{
			name: "all entries pin clusterIndex is ok",
			clusters: []ClusterSpec{
				{Name: "us-east", Index: ptr.To(int32(0))},
				{Name: "eu-west", Index: ptr.To(int32(1))},
			},
			wantErr: false,
		},
		{
			name: "one entry missing clusterIndex is invalid",
			clusters: []ClusterSpec{
				{Name: "us-east", Index: ptr.To(int32(0))},
				{Name: "eu-west"},
			},
			wantErr: true,
		},
		{
			name: "duplicate clusterIndex is invalid",
			clusters: []ClusterSpec{
				{Name: "us-east", Index: ptr.To(int32(0))},
				{Name: "eu-west", Index: ptr.To(int32(0))},
			},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &MongoDBSearch{Spec: MongoDBSearchSpec{Clusters: tc.clusters}}
			err := s.ValidateOperatorPerClusterIndices()
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
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

func TestMongoDBSearch_LocalizeToCluster(t *testing.T) {
	twoClusters := func() []ClusterSpec {
		// LocalizeToCluster is the operator-per-cluster with unified CR narrowing function, so clusterIndex
		// is mandatory and must be unique on every entry.
		return []ClusterSpec{
			{Name: "us-east", Index: ptr.To(int32(0))},
			{Name: "us-west", Index: ptr.To(int32(1))},
		}
	}
	tests := []struct {
		name      string
		clusters  []ClusterSpec
		localize  string
		wantOK    bool
		wantNil   bool // expect spec.Clusters to remain nil
		wantLen   int  // expected len(spec.Clusters) when non-nil
		wantFirst string
	}{
		{name: "nil slice is a no-op", clusters: nil, localize: "us-east", wantOK: true, wantNil: true},
		{name: "empty slice is a no-op", clusters: []ClusterSpec{}, localize: "us-east", wantOK: true, wantLen: 0},
		{name: "match narrows to 1-element slice", clusters: twoClusters(), localize: "us-west", wantOK: true, wantLen: 1, wantFirst: "us-west"},
		{name: "no match leaves slice unmodified", clusters: twoClusters(), localize: "ap-south", wantOK: false, wantLen: 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &MongoDBSearch{Spec: MongoDBSearchSpec{Clusters: tc.clusters}}
			assert.Equal(t, tc.wantOK, s.LocalizeToCluster(tc.localize))
			if tc.wantNil {
				assert.Nil(t, s.Spec.Clusters)
				return
			}
			require.NotNil(t, s.Spec.Clusters)
			require.Len(t, s.Spec.Clusters, tc.wantLen)
			if tc.wantFirst != "" {
				assert.Equal(t, tc.wantFirst, s.Spec.Clusters[0].Name)
			}
		})
	}

	// Sharded source must be preserved by identity on every code path: LocalizeToCluster
	// narrows spec.Clusters only, leaving spec.Source untouched so the downstream sharded
	// plan still sees router + every shard.
	sharded := &ExternalShardedClusterConfig{
		Router: ExternalRouterConfig{Hosts: []string{"mongos.example:27017"}},
		Shards: []ExternalShardConfig{
			{ShardName: "sh-0", Hosts: []string{"sh-0-a.example:27017"}},
			{ShardName: "sh-1", Hosts: []string{"sh-1-a.example:27017"}},
		},
	}
	for _, tc := range []struct {
		name     string
		localize string
	}{
		{name: "on match", localize: "us-east"},
		{name: "on no-match", localize: "ap-south"},
	} {
		t.Run("sharded source preserved "+tc.name, func(t *testing.T) {
			s := &MongoDBSearch{Spec: MongoDBSearchSpec{
				Source: &MongoDBSource{ExternalMongoDBSource: &ExternalMongoDBSource{ShardedCluster: sharded}},
				Clusters: []ClusterSpec{
					{Name: "us-east", Index: ptr.To(int32(0))},
					{Name: "us-west", Index: ptr.To(int32(1))},
				},
			}}
			s.LocalizeToCluster(tc.localize)
			require.NotNil(t, s.Spec.Source)
			require.NotNil(t, s.Spec.Source.ExternalMongoDBSource)
			assert.Same(t, sharded, s.Spec.Source.ExternalMongoDBSource.ShardedCluster,
				"sharded source must be preserved by identity after LocalizeToCluster")
		})
	}
}

func TestResolveSizingForClusterShard(t *testing.T) {
	clusterRes := &corev1.ResourceRequirements{}
	overrideRes := &corev1.ResourceRequirements{}
	clusterPersistence := &v1.Persistence{}
	overridePersistence := &v1.Persistence{}

	cluster := ClusterSpec{
		Name:                 "east",
		Replicas:             ptr.To(int32(2)),
		ResourceRequirements: clusterRes,
		Persistence:          clusterPersistence,
		JVMFlags:             []string{"-Xmx2g"},
		ShardOverrides: []ShardOverride{
			{
				ShardNames:           []string{"shard-1"},
				Replicas:             ptr.To(int32(4)),
				ResourceRequirements: overrideRes,
				JVMFlags:             []string{"-Xmx8g"},
			},
			{
				ShardNames:  []string{"shard-2"},
				Persistence: overridePersistence,
			},
		},
	}
	s := &MongoDBSearch{Spec: MongoDBSearchSpec{Clusters: []ClusterSpec{cluster}}}

	replicasFor := func(t *testing.T, s *MongoDBSearch, clusterName, shardName string) int {
		t.Helper()
		c, err := s.ResolveSizingForClusterShard(clusterName, shardName)
		require.NoError(t, err)
		return c.ReplicasOrDefault()
	}

	t.Run("shard without override inherits cluster values", func(t *testing.T) {
		got, err := s.ResolveSizingForClusterShard("east", "shard-0")
		require.NoError(t, err)
		assert.Equal(t, ptr.To(int32(2)), got.Replicas)
		assert.Same(t, clusterRes, got.ResourceRequirements)
		assert.Same(t, clusterPersistence, got.Persistence)
		assert.Nil(t, got.ShardOverrides)
	})

	t.Run("override replaces replicas, resources and jvm flags, inherits persistence", func(t *testing.T) {
		got, err := s.ResolveSizingForClusterShard("east", "shard-1")
		require.NoError(t, err)
		assert.Equal(t, ptr.To(int32(4)), got.Replicas)
		assert.Same(t, overrideRes, got.ResourceRequirements)
		assert.Equal(t, []string{"-Xmx8g"}, got.JVMFlags)
		assert.Same(t, clusterPersistence, got.Persistence, "unset override field inherits cluster value")
	})

	t.Run("empty override jvm flags inherit cluster value", func(t *testing.T) {
		got, err := s.ResolveSizingForClusterShard("east", "shard-2")
		require.NoError(t, err)
		assert.Equal(t, []string{"-Xmx2g"}, got.JVMFlags)
	})

	t.Run("override replaces only persistence", func(t *testing.T) {
		got, err := s.ResolveSizingForClusterShard("east", "shard-2")
		require.NoError(t, err)
		assert.Equal(t, ptr.To(int32(2)), got.Replicas, "unset override replicas inherits cluster value")
		assert.Same(t, overridePersistence, got.Persistence)
	})

	t.Run("empty shardName returns cluster spec unchanged", func(t *testing.T) {
		got, err := s.ResolveSizingForClusterShard("east", "")
		require.NoError(t, err)
		assert.Equal(t, ptr.To(int32(2)), got.Replicas)
	})

	t.Run("resolved replicas honor override and zero", func(t *testing.T) {
		s := &MongoDBSearch{Spec: MongoDBSearchSpec{Clusters: []ClusterSpec{{
			Replicas: ptr.To(int32(2)),
			ShardOverrides: []ShardOverride{
				{ShardNames: []string{"shard-off"}, Replicas: ptr.To(int32(0))},
			},
		}}}}
		assert.Equal(t, 2, replicasFor(t, s, "", "shard-other"))
		assert.Equal(t, 0, replicasFor(t, s, "", "shard-off"))
	})

	t.Run("every shard in a multi-name override entry resolves to the override", func(t *testing.T) {
		s := &MongoDBSearch{Spec: MongoDBSearchSpec{Clusters: []ClusterSpec{{
			Replicas: ptr.To(int32(1)),
			ShardOverrides: []ShardOverride{
				{ShardNames: []string{"shard-a", "shard-b"}, Replicas: ptr.To(int32(5))},
			},
		}}}}
		assert.Equal(t, 5, replicasFor(t, s, "", "shard-a"))
		assert.Equal(t, 5, replicasFor(t, s, "", "shard-b"))
		assert.Equal(t, 1, replicasFor(t, s, "", "shard-c"))
	})

	t.Run("override in one cluster does not leak into another cluster", func(t *testing.T) {
		s := &MongoDBSearch{Spec: MongoDBSearchSpec{Clusters: []ClusterSpec{
			{Name: "east", Replicas: ptr.To(int32(1)), ShardOverrides: []ShardOverride{
				{ShardNames: []string{"shard-0"}, Replicas: ptr.To(int32(4))},
			}},
			{Name: "west", Replicas: ptr.To(int32(2))},
		}}}
		assert.Equal(t, 4, replicasFor(t, s, "east", "shard-0"))
		assert.Equal(t, 2, replicasFor(t, s, "west", "shard-0"))
	})
}

func TestResolveSizingForClusterShard_StatefulSetDeepMerge(t *testing.T) {
	clusterSTS := &v1.StatefulSetConfiguration{}
	clusterSTS.SpecWrapper.Spec.ServiceName = "cluster-svc"
	clusterSTS.SpecWrapper.Spec.RevisionHistoryLimit = ptr.To(int32(7))

	overrideSTS := &v1.StatefulSetConfiguration{}
	overrideSTS.SpecWrapper.Spec.ServiceName = "override-svc"

	s := &MongoDBSearch{Spec: MongoDBSearchSpec{Clusters: []ClusterSpec{{
		StatefulSetConfiguration: clusterSTS,
		ShardOverrides: []ShardOverride{
			{ShardNames: []string{"shard-0"}, StatefulSetConfiguration: overrideSTS},
		},
	}}}}

	got, err := s.ResolveSizingForClusterShard("", "shard-0")
	require.NoError(t, err)
	require.NotNil(t, got.StatefulSetConfiguration)
	// override wins for ServiceName; cluster-only fields survive the deep-merge.
	assert.Equal(t, "override-svc", got.StatefulSetConfiguration.SpecWrapper.Spec.ServiceName)
	require.NotNil(t, got.StatefulSetConfiguration.SpecWrapper.Spec.RevisionHistoryLimit)
	assert.Equal(t, int32(7), *got.StatefulSetConfiguration.SpecWrapper.Spec.RevisionHistoryLimit)
	// original cluster config is not mutated.
	assert.Equal(t, "cluster-svc", clusterSTS.SpecWrapper.Spec.ServiceName)

	t.Run("nil cluster config returns a copy of the override", func(t *testing.T) {
		s := &MongoDBSearch{Spec: MongoDBSearchSpec{Clusters: []ClusterSpec{{
			ShardOverrides: []ShardOverride{
				{ShardNames: []string{"shard-0"}, StatefulSetConfiguration: overrideSTS},
			},
		}}}}

		got, err := s.ResolveSizingForClusterShard("", "shard-0")
		require.NoError(t, err)
		require.NotNil(t, got.StatefulSetConfiguration)
		assert.Equal(t, "override-svc", got.StatefulSetConfiguration.SpecWrapper.Spec.ServiceName)
		// the result must not alias the spec's override object.
		assert.NotSame(t, overrideSTS, got.StatefulSetConfiguration)
	})
}

func TestUpdateStatus_MetricsForwarderPath(t *testing.T) {
	s := &MongoDBSearch{}
	partOpt := NewSearchPartOption(SearchPartMetricsForwarder)
	s.UpdateStatus(status.PhaseRunning, partOpt, status.NewMessageOption("forwarder ready"))

	assert.NotNil(t, s.Status.MetricsForwarder)
	assert.Equal(t, status.PhaseRunning, s.Status.MetricsForwarder.Phase)
	assert.Equal(t, "forwarder ready", s.Status.MetricsForwarder.Message)
	// Main status untouched
	assert.Equal(t, status.Phase(""), s.Status.Phase)
}

func TestGetStatusPath_MetricsForwarder(t *testing.T) {
	s := &MongoDBSearch{}
	partOpt := NewSearchPartOption(SearchPartMetricsForwarder)
	assert.Equal(t, "/status/metricsForwarder", s.GetStatusPath(partOpt))
}

func TestGetStatus_MetricsForwarder(t *testing.T) {
	s := &MongoDBSearch{}
	s.Status.MetricsForwarder = &MetricsForwarderStatus{Phase: status.PhaseRunning, Message: "ok"}
	partOpt := NewSearchPartOption(SearchPartMetricsForwarder)
	got := s.GetStatus(partOpt)
	assert.Equal(t, s.Status.MetricsForwarder, got)
}

// nil MetricsForwarder → GetStatus returns nil
func TestGetStatus_MetricsForwarderNil(t *testing.T) {
	s := &MongoDBSearch{}
	partOpt := NewSearchPartOption(SearchPartMetricsForwarder)
	got := s.GetStatus(partOpt)
	assert.Nil(t, got)
}

// Failed→Running transition must not carry over the old error message
func TestUpdateStatus_MetricsForwarderClearsStaleMessage(t *testing.T) {
	s := &MongoDBSearch{}
	partOpt := NewSearchPartOption(SearchPartMetricsForwarder)

	s.UpdateStatus(status.PhaseFailed, partOpt, status.NewMessageOption("scrape error"))
	assert.Equal(t, "scrape error", s.Status.MetricsForwarder.Message)

	s.UpdateStatus(status.PhaseRunning, partOpt)
	assert.Equal(t, status.PhaseRunning, s.Status.MetricsForwarder.Phase)
	assert.Equal(t, "", s.Status.MetricsForwarder.Message)
}

func TestIsMetricsForwarderEnabled(t *testing.T) {
	externalSource := &MongoDBSource{
		ExternalMongoDBSource: &ExternalMongoDBSource{
			HostAndPorts: []string{"host:27017"},
		},
	}
	opsManagerConfig := &MetricsForwarderOpsManagerConfig{
		AgentCredentials:    corev1.LocalObjectReference{Name: "agent-creds"},
		ProjectConfigMapRef: corev1.LocalObjectReference{Name: "project-cm"},
	}

	tests := []struct {
		name     string
		search   MongoDBSearch
		expected bool
	}{
		{
			name: "mode enabled",
			search: MongoDBSearch{
				Spec: MongoDBSearchSpec{
					Observability: ObservabilityConfig{
						MetricsForwarder: MetricsForwarderConfig{Mode: MetricsForwarderModeEnabled},
					},
				},
			},
			expected: true,
		},
		{
			name: "mode disabled",
			search: MongoDBSearch{
				Spec: MongoDBSearchSpec{
					Observability: ObservabilityConfig{
						MetricsForwarder: MetricsForwarderConfig{Mode: MetricsForwarderModeDisabled},
					},
				},
			},
			expected: false,
		},
		{
			name: "mode auto, external source, no OpsManager config → disabled",
			search: MongoDBSearch{
				Spec: MongoDBSearchSpec{
					Source: externalSource,
					Observability: ObservabilityConfig{
						MetricsForwarder: MetricsForwarderConfig{Mode: MetricsForwarderModeAuto},
					},
				},
			},
			expected: false,
		},
		{
			name: "mode auto, external source, with OpsManager config → enabled",
			search: MongoDBSearch{
				Spec: MongoDBSearchSpec{
					Source: externalSource,
					Observability: ObservabilityConfig{
						MetricsForwarder: MetricsForwarderConfig{
							Mode:       MetricsForwarderModeAuto,
							OpsManager: opsManagerConfig,
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "mode auto, internal source → enabled",
			search: MongoDBSearch{
				Spec: MongoDBSearchSpec{
					Observability: ObservabilityConfig{
						MetricsForwarder: MetricsForwarderConfig{Mode: MetricsForwarderModeAuto},
					},
				},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.search.IsMetricsForwarderEnabled())
		})
	}
}
