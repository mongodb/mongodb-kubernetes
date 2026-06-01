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

func TestGetReplicasNilDefaultsToOne(t *testing.T) {
	s := &MongoDBSearch{}
	assert.Equal(t, 1, s.GetReplicas())
}

func TestGetReplicasReturnsExplicitValue(t *testing.T) {
	s := &MongoDBSearch{Spec: MongoDBSearchSpec{Replicas: ptr.To(int32(3))}}
	assert.Equal(t, 3, s.GetReplicas())
}

func TestGetReplicasHonorsExplicitZero(t *testing.T) {
	// spec.replicas=0 is a legitimate value (operator-driven scale-to-0
	// for taking mongot offline via the CR). Distinguishing it from "unset"
	// is the contract callers like search-connectivity tests rely on.
	s := &MongoDBSearch{Spec: MongoDBSearchSpec{Replicas: ptr.To(int32(0))}}
	assert.Equal(t, 0, s.GetReplicas())
}

func TestHasMultipleReplicas(t *testing.T) {
	assert.False(t, (&MongoDBSearch{}).HasMultipleReplicas())
	assert.False(t, (&MongoDBSearch{Spec: MongoDBSearchSpec{Replicas: ptr.To(int32(0))}}).HasMultipleReplicas())
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
				Persistence:              &v1.Persistence{},
				StatefulSetConfiguration: &v1.StatefulSetConfiguration{},
			},
			expected: []ClusterSpec{{
				Replicas:                 ptr.To(int32(2)),
				ResourceRequirements:     &corev1.ResourceRequirements{},
				Persistence:              &v1.Persistence{},
				StatefulSetConfiguration: &v1.StatefulSetConfiguration{},
			}},
		},
		{
			name: "spec.clusters set, no top-level cascade fields — returned unchanged",
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
			got := s.EffectiveClusters()
			assert.Equal(t, tt.expected, got)
		})
	}
}

// TestEffectiveClustersCascade verifies the full cascade across all five fields:
//   - Replicas (*int32): REPLACE-if-nil
//   - ResourceRequirements (*corev1.ResourceRequirements): REPLACE-if-nil
//   - Persistence (*v1.Persistence): REPLACE-if-nil
//   - StatefulSetConfiguration (*v1.StatefulSetConfiguration): REPLACE-if-nil
//   - JVMFlags ([]string): REPLACE-if-empty (non-empty cluster slice wins; no append)
func TestEffectiveClustersCascade(t *testing.T) {
	topReplicas := ptr.To(int32(2))
	topPersistence := &v1.Persistence{}
	topResources := &corev1.ResourceRequirements{}
	topSTS := &v1.StatefulSetConfiguration{}
	topJVMFlags := []string{"-Xmx2g", "-XX:+UseG1GC"}

	clusterAPersistence := &v1.Persistence{}
	clusterBResources := &corev1.ResourceRequirements{}
	clusterDReplicas := ptr.To(int32(5))
	clusterEJVMFlags := []string{"-Xmx8g"}

	spec := MongoDBSearchSpec{
		Replicas:                 topReplicas,
		ResourceRequirements:     topResources,
		Persistence:              topPersistence,
		StatefulSetConfiguration: topSTS,
		JVMFlags:                 topJVMFlags,
		Clusters: &[]ClusterSpec{
			{ClusterName: "cluster-a", Persistence: clusterAPersistence},        // override Persistence only
			{ClusterName: "cluster-b", ResourceRequirements: clusterBResources}, // override ResourceRequirements only
			{ClusterName: "cluster-c"},                                          // no overrides — inherit all five top-level defaults
			{ClusterName: "cluster-d", Replicas: clusterDReplicas},              // override Replicas only
			{ClusterName: "cluster-e", JVMFlags: clusterEJVMFlags},              // override JVMFlags only (REPLACE-if-empty: no append)
		},
	}
	s := &MongoDBSearch{Spec: spec}
	got := s.EffectiveClusters()

	require.Len(t, got, 5)

	// cluster-a: own Persistence, inherits the rest
	assert.Same(t, clusterAPersistence, got[0].Persistence, "cluster-a Persistence override wins")
	assert.Same(t, topResources, got[0].ResourceRequirements)
	assert.Same(t, topSTS, got[0].StatefulSetConfiguration)
	assert.Equal(t, topReplicas, got[0].Replicas)
	assert.Equal(t, topJVMFlags, got[0].JVMFlags)

	// cluster-b: own ResourceRequirements, inherits the rest
	assert.Same(t, topPersistence, got[1].Persistence)
	assert.Same(t, clusterBResources, got[1].ResourceRequirements, "cluster-b ResourceRequirements override wins")
	assert.Same(t, topSTS, got[1].StatefulSetConfiguration)
	assert.Equal(t, topReplicas, got[1].Replicas)
	assert.Equal(t, topJVMFlags, got[1].JVMFlags)

	// cluster-c: all five inherited
	assert.Same(t, topPersistence, got[2].Persistence)
	assert.Same(t, topResources, got[2].ResourceRequirements)
	assert.Same(t, topSTS, got[2].StatefulSetConfiguration)
	assert.Equal(t, topReplicas, got[2].Replicas)
	assert.Equal(t, topJVMFlags, got[2].JVMFlags)

	// cluster-d: own Replicas, inherits the rest
	assert.Equal(t, clusterDReplicas, got[3].Replicas, "cluster-d Replicas override wins")
	assert.Equal(t, topJVMFlags, got[3].JVMFlags)

	// cluster-e: own JVMFlags (REPLACE — does NOT append top-level "-XX:+UseG1GC")
	assert.Equal(t, clusterEJVMFlags, got[4].JVMFlags, "cluster-e JVMFlags override wins atomically")
	assert.Equal(t, topReplicas, got[4].Replicas)

	// Cascade must NOT mutate the original spec.
	assert.Same(t, topPersistence, spec.Persistence)
	assert.Same(t, topResources, spec.ResourceRequirements)
	assert.Same(t, topSTS, spec.StatefulSetConfiguration)
	assert.Equal(t, topReplicas, spec.Replicas)
	assert.Equal(t, topJVMFlags, spec.JVMFlags)
	assert.Same(t, clusterAPersistence, (*spec.Clusters)[0].Persistence)
	assert.Nil(t, (*spec.Clusters)[0].ResourceRequirements)
	assert.Nil(t, (*spec.Clusters)[2].Persistence)
	assert.Nil(t, (*spec.Clusters)[2].Replicas)
	assert.Empty(t, (*spec.Clusters)[2].JVMFlags)
}

func TestEffectiveClusterFor(t *testing.T) {
	topResources := &corev1.ResourceRequirements{}
	clusterBResources := &corev1.ResourceRequirements{}

	t.Run("empty clusterName returns first auto-promoted entry", func(t *testing.T) {
		s := &MongoDBSearch{Spec: MongoDBSearchSpec{ResourceRequirements: topResources}}
		got, err := s.EffectiveClusterFor("")
		require.NoError(t, err)
		assert.Same(t, topResources, got.ResourceRequirements)
	})

	t.Run("matches by clusterName with cascade applied", func(t *testing.T) {
		s := &MongoDBSearch{Spec: MongoDBSearchSpec{
			ResourceRequirements: topResources,
			Clusters: &[]ClusterSpec{
				{ClusterName: "cluster-a"},
				{ClusterName: "cluster-b", ResourceRequirements: clusterBResources},
			},
		}}
		gotA, err := s.EffectiveClusterFor("cluster-a")
		require.NoError(t, err)
		assert.Same(t, topResources, gotA.ResourceRequirements)
		gotB, err := s.EffectiveClusterFor("cluster-b")
		require.NoError(t, err)
		assert.Same(t, clusterBResources, gotB.ResourceRequirements)
	})

	t.Run("unknown clusterName returns error", func(t *testing.T) {
		s := &MongoDBSearch{Spec: MongoDBSearchSpec{
			Clusters: &[]ClusterSpec{{ClusterName: "only"}},
		}}
		got, err := s.EffectiveClusterFor("missing")
		require.Error(t, err)
		assert.Equal(t, ClusterSpec{}, got)
	})
}

func TestEffectiveClustersDoesNotMutate(t *testing.T) {
	// Independent invariant — a pure function must not mutate spec.
	s := &MongoDBSearch{Spec: MongoDBSearchSpec{Replicas: ptr.To(int32(7))}}
	_ = s.EffectiveClusters()
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
		name         string
		template     string
		clusterIndex int
		clusterName  string
		want         string
	}{
		{
			name:         "clusterName substitution first cluster",
			template:     "{clusterName}.search-lb.example.com:443",
			clusterIndex: 0,
			clusterName:  "us-east-k8s",
			want:         "us-east-k8s.search-lb.example.com:443",
		},
		{
			name:         "clusterIndex substitution second cluster",
			template:     "search-{clusterIndex}.lb.example.com:443",
			clusterIndex: 1,
			clusterName:  "eu-west-k8s",
			want:         "search-1.lb.example.com:443",
		},
		{
			name:         "both placeholders present",
			template:     "{clusterName}-{clusterIndex}.lb.example.com:443",
			clusterIndex: 1,
			clusterName:  "eu-west-k8s",
			want:         "eu-west-k8s-1.lb.example.com:443",
		},
		{
			name:         "no placeholders left untouched",
			template:     "static.lb.example.com:443",
			clusterIndex: 0,
			clusterName:  "us-east-k8s",
			want:         "static.lb.example.com:443",
		},
		{
			name:         "legacy empty clusterName substitutes clusterName placeholder to empty",
			template:     "search-{clusterIndex}.lb.example.com:443",
			clusterIndex: 0,
			clusterName:  "",
			want:         "search-0.lb.example.com:443",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &MongoDBSearch{
				Spec: MongoDBSearchSpec{
					LoadBalancer: &LoadBalancerConfig{Managed: &ManagedLBConfig{ExternalHostname: tc.template}},
				},
			}
			assert.Equal(t, tc.want, s.GetManagedLBEndpointForCluster(tc.clusterIndex, tc.clusterName))
		})
	}
}

func TestGetManagedLBEndpointForCluster_NotManaged(t *testing.T) {
	s := &MongoDBSearch{Spec: MongoDBSearchSpec{}}
	assert.Equal(t, "", s.GetManagedLBEndpointForCluster(0, "us-east-k8s"))

	s.Spec.LoadBalancer = &LoadBalancerConfig{Managed: &ManagedLBConfig{ExternalHostname: ""}}
	assert.Equal(t, "", s.GetManagedLBEndpointForCluster(0, "us-east-k8s"))
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
			got[s.GetManagedLBEndpointForClusterShard(i, clusters[i].ClusterName, sh)] = struct{}{}
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
	assert.Equal(t, "search-0.shard-a.lb.example.com:443", s.GetManagedLBEndpointForClusterShard(0, "us-east-k8s", "shard-a"))
	assert.Equal(t, "search-1.shard-b.lb.example.com:443", s.GetManagedLBEndpointForClusterShard(1, "eu-west-k8s", "shard-b"))
}

// Regression guard: {clusterIndex} substitution uses exactly the resolved index
// the caller supplies (the user-pinned ClusterIndex in simulated-MC; the mapping
// index in legacy MC) — never the spec-array position. The helper no longer
// indexes into spec.clusters[]; pin resolution lives in the callers. Here we
// prove the helper renders whatever index it is handed, so a caller threading a
// pinned value (e.g. 7) renders "7" regardless of array layout.
func TestGetManagedLBEndpoint_ClusterIndexUsesResolvedIndex(t *testing.T) {
	mk := func(tmpl string) *MongoDBSearch {
		return &MongoDBSearch{Spec: MongoDBSearchSpec{
			LoadBalancer: &LoadBalancerConfig{Managed: &ManagedLBConfig{ExternalHostname: tmpl}},
		}}
	}
	// Resolved index 7 (e.g. a pinned ClusterIndex) → "7" verbatim.
	assert.Equal(t, "mongot-7.example.com",
		mk("mongot-{clusterIndex}.example.com").GetManagedLBEndpointForCluster(7, "us-east"))
	// Resolved index 0 → "0".
	assert.Equal(t, "mongot-0.example.com",
		mk("mongot-{clusterIndex}.example.com").GetManagedLBEndpointForCluster(0, "us-east"))
	// ClusterLevel path renders the resolved index too (strips leading {shardName}. prefix).
	assert.Equal(t, "search-7.lb.example.com:443",
		mk("{shardName}.search-{clusterIndex}.lb.example.com:443").GetManagedLBEndpointForClusterLevel(7, "us-east"))
}

func TestGetManagedLBEndpointForClusterShard_NotManaged(t *testing.T) {
	s := &MongoDBSearch{Spec: MongoDBSearchSpec{}}
	assert.Equal(t, "", s.GetManagedLBEndpointForClusterShard(0, "us-east-k8s", "shard-0"))
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
		clusterIndex int
		clusterName  string
		want         string
	}{
		{"strip prefix, resolve clusterName", "{shardName}.{clusterName}.search.example.com", 0, "us-east-k8s", "us-east-k8s.search.example.com"},
		{"strip prefix, resolve clusterIndex", "{shardName}.search-{clusterIndex}.example.com", 1, "eu-west-k8s", "search-1.example.com"},
		{"strip prefix, single-cluster sharded shape (no cluster placeholders)", "{shardName}.search.example.com", 0, "us-east-k8s", "search.example.com"},
		// {shardName} as a name component (not a leading prefix) is not derivable to a
		// single cluster-level hostname; expect "" so the caller falls back to the
		// cluster-level proxy Service FQDN.
		{"shardName as name component returns empty", "search-{clusterIndex}-{shardName}-proxy.example.com", 0, "us-east-k8s", ""},
		// {clusterName} with an empty name (operator-managed sharded mongos path)
		// is unresolvable; return "" so the caller falls back to the proxy-svc FQDN
		// rather than emitting a malformed ".search.example.com".
		{"empty clusterName with {clusterName} placeholder returns empty", "{shardName}.{clusterName}.search.example.com", 0, "", ""},
		// {clusterIndex} with an empty name still resolves — 0 is well-formed.
		{"empty clusterName with only {clusterIndex} resolves", "{shardName}.search-{clusterIndex}.example.com", 0, "", "search-0.example.com"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, mk(tc.template).GetManagedLBEndpointForClusterLevel(tc.clusterIndex, tc.clusterName))
		})
	}
}

func TestGetManagedLBEndpointForClusterLevel_NotManaged(t *testing.T) {
	s := &MongoDBSearch{Spec: MongoDBSearchSpec{}}
	assert.Equal(t, "", s.GetManagedLBEndpointForClusterLevel(0, "us-east-k8s"))
}

func TestValidateSimulatedMCClusterIndices(t *testing.T) {
	tests := []struct {
		name     string
		clusters *[]ClusterSpec
		wantErr  bool
	}{
		{
			name:     "nil clusters is invalid",
			clusters: nil,
			wantErr:  true,
		},
		{
			name:     "empty clusters is invalid",
			clusters: &[]ClusterSpec{},
			wantErr:  true,
		},
		{
			name: "all entries pin clusterIndex is ok",
			clusters: &[]ClusterSpec{
				{ClusterName: "us-east", ClusterIndex: ptr.To(int32(0))},
				{ClusterName: "eu-west", ClusterIndex: ptr.To(int32(1))},
			},
			wantErr: false,
		},
		{
			name: "one entry missing clusterIndex is invalid",
			clusters: &[]ClusterSpec{
				{ClusterName: "us-east", ClusterIndex: ptr.To(int32(0))},
				{ClusterName: "eu-west"},
			},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &MongoDBSearch{Spec: MongoDBSearchSpec{Clusters: tc.clusters}}
			err := s.ValidateSimulatedMCClusterIndices()
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
	// nil slice — no-op, returns true.
	nilSearch := &MongoDBSearch{}
	assert.True(t, nilSearch.LocalizeToCluster("us-east"))
	assert.Nil(t, nilSearch.Spec.Clusters)

	// empty slice — no-op, returns true.
	empty := &MongoDBSearch{Spec: MongoDBSearchSpec{Clusters: &[]ClusterSpec{}}}
	assert.True(t, empty.LocalizeToCluster("us-east"))
	require.NotNil(t, empty.Spec.Clusters)
	assert.Empty(t, *empty.Spec.Clusters)

	// match — projects to 1-element slice, returns true.
	matched := &MongoDBSearch{Spec: MongoDBSearchSpec{Clusters: &[]ClusterSpec{
		{ClusterName: "us-east"}, {ClusterName: "us-west"},
	}}}
	assert.True(t, matched.LocalizeToCluster("us-west"))
	require.Len(t, *matched.Spec.Clusters, 1)
	assert.Equal(t, "us-west", (*matched.Spec.Clusters)[0].ClusterName)

	// no match — returns false, leaves spec.Clusters unmodified.
	unmatched := &MongoDBSearch{Spec: MongoDBSearchSpec{Clusters: &[]ClusterSpec{
		{ClusterName: "us-east"}, {ClusterName: "us-west"},
	}}}
	assert.False(t, unmatched.LocalizeToCluster("ap-south"))
	assert.Len(t, *unmatched.Spec.Clusters, 2)

	// Sharded source must be preserved on every code path: LocalizeToCluster
	// narrows spec.Clusters only — spec.Source is untouched so the sharded plan
	// downstream still sees router + every shard.
	sharded := &ExternalShardedClusterConfig{
		Router: ExternalRouterConfig{Hosts: []string{"mongos.example:27017"}},
		Shards: []ExternalShardConfig{
			{ShardName: "sh-0", Hosts: []string{"sh-0-a.example:27017"}},
			{ShardName: "sh-1", Hosts: []string{"sh-1-a.example:27017"}},
		},
	}

	t.Run("sharded source preserved on match", func(t *testing.T) {
		s := &MongoDBSearch{Spec: MongoDBSearchSpec{
			Source: &MongoDBSource{
				ExternalMongoDBSource: &ExternalMongoDBSource{ShardedCluster: sharded},
			},
			Clusters: &[]ClusterSpec{{ClusterName: "us-east"}, {ClusterName: "us-west"}},
		}}
		assert.True(t, s.LocalizeToCluster("us-east"))
		require.Len(t, *s.Spec.Clusters, 1)
		// Sharded source preserved by identity — LocalizeToCluster MUST NOT touch spec.Source.
		require.NotNil(t, s.Spec.Source)
		require.NotNil(t, s.Spec.Source.ExternalMongoDBSource)
		assert.Same(t, sharded, s.Spec.Source.ExternalMongoDBSource.ShardedCluster,
			"sharded source must be preserved by identity after LocalizeToCluster")
	})

	t.Run("sharded source preserved on no-match", func(t *testing.T) {
		s := &MongoDBSearch{Spec: MongoDBSearchSpec{
			Source: &MongoDBSource{
				ExternalMongoDBSource: &ExternalMongoDBSource{ShardedCluster: sharded},
			},
			Clusters: &[]ClusterSpec{{ClusterName: "us-east"}, {ClusterName: "us-west"}},
		}}
		assert.False(t, s.LocalizeToCluster("ap-south"))
		// Spec.Clusters NOT narrowed when no match — and source remains untouched either way.
		assert.Len(t, *s.Spec.Clusters, 2)
		require.NotNil(t, s.Spec.Source)
		require.NotNil(t, s.Spec.Source.ExternalMongoDBSource)
		assert.Same(t, sharded, s.Spec.Source.ExternalMongoDBSource.ShardedCluster,
			"sharded source must be preserved by identity after LocalizeToCluster no-match")
	})
}
