package search

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	v1 "github.com/mongodb/mongodb-kubernetes/api/v1"
	userv1 "github.com/mongodb/mongodb-kubernetes/api/v1/user"
)

func TestValidateShardNames(t *testing.T) {
	shard := func(name string) ExternalShardConfig {
		return ExternalShardConfig{ShardName: name, Hosts: []string{"host:27017"}}
	}

	tests := []struct {
		name          string
		search        *MongoDBSearch
		errorContains string
	}{
		// Valid cases
		{
			name:   "valid simple shard name",
			search: newSearch("my-search", []ExternalShardConfig{shard("shard0")}, "", false, false),
		},
		{
			name:   "valid shard name with hyphen",
			search: newSearch("my-search", []ExternalShardConfig{shard("shard-zero")}, "", false, false),
		},
		{
			name:   "valid shard name starts with digit",
			search: newSearch("my-search", []ExternalShardConfig{shard("0shard")}, "", false, false),
		},
		{
			name:   "valid multiple unique shards",
			search: newSearch("my-search", []ExternalShardConfig{shard("shard0"), shard("shard1")}, "", false, false),
		},
		{
			name: "non-sharded config skips validation",
			search: &MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{Name: "test-search", Namespace: "test-namespace"},
				Spec:       MongoDBSearchSpec{Clusters: []SearchClusterSpecItem{{Replicas: 1}}},
			},
		},

		// Invalid cases
		{
			name:          "invalid empty shard name",
			search:        newSearch("my-search", []ExternalShardConfig{shard("")}, "", false, false),
			errorContains: "is required",
		},
		{
			name:          "invalid uppercase shard name",
			search:        newSearch("my-search", []ExternalShardConfig{shard("SHARD")}, "", false, false),
			errorContains: "invalid",
		},
		{
			name:          "invalid shard name with dot",
			search:        newSearch("my-search", []ExternalShardConfig{shard("shard.zero")}, "", false, false),
			errorContains: "must not contain dots",
		},
		{
			name:          "invalid shard name with underscore",
			search:        newSearch("my-search", []ExternalShardConfig{shard("shard_zero")}, "", false, false),
			errorContains: "invalid",
		},
		{
			name:          "invalid duplicate shard names",
			search:        newSearch("my-search", []ExternalShardConfig{shard("shard0"), shard("shard0")}, "", false, false),
			errorContains: "duplicate",
		},
		{
			// StatefulSet name: {name}-search-0-{shardName} must be ≤63 chars (DNS Label)
			// 30 + 10 + 25 = 65 chars > 63
			name:          "invalid StatefulSet name too long",
			search:        newSearch(strings.Repeat("a", 30), []ExternalShardConfig{shard(strings.Repeat("s", 25))}, "", false, false),
			errorContains: "exceeds",
		},
		{
			// TLS Secret name: {prefix}-{name}-search-0-{shardName}-cert uses DNS Subdomain (253 chars)
			// but Service name: {name}-search-0-{shardName}-svc uses DNS Label (63 chars)
			// 20 + 10 + 30 + 4 = 64 chars > 63
			name:          "invalid Service name too long with TLS",
			search:        newSearch(strings.Repeat("a", 20), []ExternalShardConfig{shard(strings.Repeat("s", 30))}, "prefix", true, false),
			errorContains: "exceeds",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.search.ValidateSpec()

			if tt.errorContains != "" {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateX509AuthConfig(t *testing.T) {
	tests := []struct {
		name          string
		searchSource  *MongoDBSource
		errorContains string
	}{
		{
			name:         "no source configured",
			searchSource: nil,
		},
		{
			name:         "no x509 configured",
			searchSource: &MongoDBSource{},
		},
		{
			name: "x509 with valid secret name",
			searchSource: &MongoDBSource{
				X509: &X509Auth{
					ClientCertificateSecret: corev1.LocalObjectReference{Name: "my-cert"},
				},
			},
		},
		{
			name: "x509 with empty secret name",
			searchSource: &MongoDBSource{
				X509: &X509Auth{
					ClientCertificateSecret: corev1.LocalObjectReference{Name: ""},
				},
			},
			errorContains: "must not be empty",
		},
		{
			name: "x509 with passwordSecretRef is mutually exclusive",
			searchSource: &MongoDBSource{
				X509: &X509Auth{
					ClientCertificateSecret: corev1.LocalObjectReference{Name: "my-cert"},
				},
				PasswordSecretRef: &userv1.SecretKeyRef{Name: "my-password"},
			},
			errorContains: "mutually exclusive",
		},
		{
			name: "x509 with username is mutually exclusive",
			searchSource: &MongoDBSource{
				X509: &X509Auth{
					ClientCertificateSecret: corev1.LocalObjectReference{Name: "my-cert"},
				},
				Username: ptr.To("some-user"),
			},
			errorContains: "mutually exclusive",
		},
		{
			name: "x509 with both passwordSecretRef and username",
			searchSource: &MongoDBSource{
				X509: &X509Auth{
					ClientCertificateSecret: corev1.LocalObjectReference{Name: "my-cert"},
				},
				PasswordSecretRef: &userv1.SecretKeyRef{Name: "my-password"},
				Username:          ptr.To("some-user"),
			},
			errorContains: "mutually exclusive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			search := &MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{Name: "test-search", Namespace: "test-ns"},
				Spec:       MongoDBSearchSpec{Source: tt.searchSource},
			}

			result := validateX509AuthConfig(search)

			if tt.errorContains != "" {
				assert.Equal(t, v1.ErrorLevel, result.Level)
				assert.Contains(t, result.Msg, tt.errorContains)
			} else {
				assert.Equal(t, v1.SuccessLevel, result.Level)
			}
		})
	}
}

func TestValidateClustersNotEmpty(t *testing.T) {
	tests := []struct {
		name          string
		clusters      []SearchClusterSpecItem
		errorContains string
	}{
		{
			name:          "nil clusters",
			clusters:      nil,
			errorContains: "spec.clusters must have at least one entry",
		},
		{
			name:          "empty clusters slice",
			clusters:      []SearchClusterSpecItem{},
			errorContains: "spec.clusters must have at least one entry",
		},
		{
			name:     "single cluster",
			clusters: []SearchClusterSpecItem{{Replicas: 1}},
		},
		{
			name:     "multiple clusters",
			clusters: []SearchClusterSpecItem{{ClusterName: "c1", Replicas: 1}, {ClusterName: "c2", Replicas: 1}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			search := &MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{Name: "test-search", Namespace: "test-ns"},
				Spec:       MongoDBSearchSpec{Clusters: tt.clusters},
			}
			result := validateClustersNotEmpty(search)
			if tt.errorContains != "" {
				assert.Equal(t, v1.ErrorLevel, result.Level)
				assert.Contains(t, result.Msg, tt.errorContains)
			} else {
				assert.Equal(t, v1.SuccessLevel, result.Level)
			}
		})
	}
}

func TestValidateClusterNames(t *testing.T) {
	tests := []struct {
		name          string
		clusters      []SearchClusterSpecItem
		errorContains string
	}{
		{
			name:     "single cluster no name required",
			clusters: []SearchClusterSpecItem{{Replicas: 1}},
		},
		{
			name:     "multi-cluster all names present and unique",
			clusters: []SearchClusterSpecItem{{ClusterName: "east", Replicas: 1}, {ClusterName: "west", Replicas: 1}},
		},
		{
			name:          "multi-cluster missing clusterName",
			clusters:      []SearchClusterSpecItem{{ClusterName: "east", Replicas: 1}, {Replicas: 1}},
			errorContains: "clusterName is required",
		},
		{
			name:          "multi-cluster duplicate clusterName",
			clusters:      []SearchClusterSpecItem{{ClusterName: "east", Replicas: 1}, {ClusterName: "east", Replicas: 1}},
			errorContains: "duplicate clusterName",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			search := &MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{Name: "test-search", Namespace: "test-ns"},
				Spec:       MongoDBSearchSpec{Clusters: tt.clusters},
			}
			result := validateClusterNames(search)
			if tt.errorContains != "" {
				assert.Equal(t, v1.ErrorLevel, result.Level)
				assert.Contains(t, result.Msg, tt.errorContains)
			} else {
				assert.Equal(t, v1.SuccessLevel, result.Level)
			}
		})
	}
}

func TestValidateClusterReplicas(t *testing.T) {
	tests := []struct {
		name          string
		clusters      []SearchClusterSpecItem
		errorContains string
	}{
		{
			name:     "valid replicas",
			clusters: []SearchClusterSpecItem{{Replicas: 1}},
		},
		{
			name:          "zero replicas",
			clusters:      []SearchClusterSpecItem{{Replicas: 0}},
			errorContains: "replicas must be >= 1",
		},
		{
			name:          "negative replicas",
			clusters:      []SearchClusterSpecItem{{Replicas: -1}},
			errorContains: "replicas must be >= 1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			search := &MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{Name: "test-search", Namespace: "test-ns"},
				Spec:       MongoDBSearchSpec{Clusters: tt.clusters},
			}
			result := validateClusterReplicas(search)
			if tt.errorContains != "" {
				assert.Equal(t, v1.ErrorLevel, result.Level)
				assert.Contains(t, result.Msg, tt.errorContains)
			} else {
				assert.Equal(t, v1.SuccessLevel, result.Level)
			}
		})
	}
}

func TestValidateMultiClusterLBRequirements(t *testing.T) {
	mc := []SearchClusterSpecItem{{ClusterName: "east", Replicas: 1}, {ClusterName: "west", Replicas: 1}}
	sc := []SearchClusterSpecItem{{Replicas: 1}}
	tests := []struct {
		name          string
		clusters      []SearchClusterSpecItem
		lb            *LoadBalancerConfig
		errorContains string
	}{
		{
			name:     "single-cluster no LB required",
			clusters: sc,
		},
		{
			name:     "multi-cluster with managed LB",
			clusters: mc,
			lb:       &LoadBalancerConfig{Managed: &ManagedLBConfig{}},
		},
		{
			name:          "multi-cluster without LB",
			clusters:      mc,
			errorContains: "spec.loadBalancer.managed is required",
		},
		{
			name:          "multi-cluster with unmanaged LB only",
			clusters:      mc,
			lb:            &LoadBalancerConfig{Unmanaged: &UnmanagedLBConfig{Endpoint: "lb.example.com"}},
			errorContains: "spec.loadBalancer.managed is required",
		},
		{
			name:          "multi-cluster with both managed and unmanaged",
			clusters:      mc,
			lb:            &LoadBalancerConfig{Managed: &ManagedLBConfig{}, Unmanaged: &UnmanagedLBConfig{Endpoint: "lb.example.com"}},
			errorContains: "spec.loadBalancer.unmanaged is forbidden",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			search := &MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{Name: "test-search", Namespace: "test-ns"},
				Spec:       MongoDBSearchSpec{Clusters: tt.clusters, LoadBalancer: tt.lb},
			}
			result := validateMultiClusterLBRequirements(search)
			if tt.errorContains != "" {
				assert.Equal(t, v1.ErrorLevel, result.Level)
				assert.Contains(t, result.Msg, tt.errorContains)
			} else {
				assert.Equal(t, v1.SuccessLevel, result.Level)
			}
		})
	}
}

func TestValidateClusterNamesImmutable(t *testing.T) {
	tests := []struct {
		name          string
		oldClusters   []SearchClusterSpecItem
		newClusters   []SearchClusterSpecItem
		errorContains string
	}{
		{
			name:        "unchanged cluster names",
			oldClusters: []SearchClusterSpecItem{{ClusterName: "east", Replicas: 1}},
			newClusters: []SearchClusterSpecItem{{ClusterName: "east", Replicas: 1}},
		},
		{
			name:        "new cluster added",
			oldClusters: []SearchClusterSpecItem{{ClusterName: "east", Replicas: 1}},
			newClusters: []SearchClusterSpecItem{{ClusterName: "east", Replicas: 1}, {ClusterName: "west", Replicas: 1}},
		},
		{
			name:          "cluster renamed",
			oldClusters:   []SearchClusterSpecItem{{ClusterName: "east", Replicas: 1}},
			newClusters:   []SearchClusterSpecItem{{ClusterName: "renamed", Replicas: 1}},
			errorContains: "cannot be removed or renamed",
		},
		{
			name:          "cluster removed",
			oldClusters:   []SearchClusterSpecItem{{ClusterName: "east", Replicas: 1}, {ClusterName: "west", Replicas: 1}},
			newClusters:   []SearchClusterSpecItem{{ClusterName: "east", Replicas: 1}},
			errorContains: "cannot be removed or renamed",
		},
		{
			name:        "single-cluster no names set, no restriction",
			oldClusters: []SearchClusterSpecItem{{Replicas: 1}},
			newClusters: []SearchClusterSpecItem{{Replicas: 1}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			old := &MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{Name: "test-search", Namespace: "test-ns"},
				Spec:       MongoDBSearchSpec{Clusters: tt.oldClusters},
			}
			newSearch := &MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{Name: "test-search", Namespace: "test-ns"},
				Spec:       MongoDBSearchSpec{Clusters: tt.newClusters},
			}
			result := validateClusterNamesImmutable(newSearch, old)
			if tt.errorContains != "" {
				assert.Equal(t, v1.ErrorLevel, result.Level)
				assert.Contains(t, result.Msg, tt.errorContains)
			} else {
				assert.Equal(t, v1.SuccessLevel, result.Level)
			}
		})
	}
}

func TestValidateMongoDBCommunitySourceForbidden(t *testing.T) {
	tests := []struct {
		name          string
		source        *MongoDBSource
		clusters      []SearchClusterSpecItem
		errorContains string
	}{
		{
			name:     "no source",
			clusters: []SearchClusterSpecItem{{Replicas: 1}},
		},
		{
			name:     "MongoDB kind allowed",
			clusters: []SearchClusterSpecItem{{Replicas: 1}},
			source:   &MongoDBSource{MongoDBResourceRef: &MongoDBSearchSourceRef{Name: "my-rs", Kind: "MongoDB"}},
		},
		{
			name:     "MongoDBMultiCluster kind allowed",
			clusters: []SearchClusterSpecItem{{ClusterName: "c1", Replicas: 1}, {ClusterName: "c2", Replicas: 1}},
			source:   &MongoDBSource{MongoDBResourceRef: &MongoDBSearchSourceRef{Name: "my-mc", Kind: "MongoDBMultiCluster"}},
		},
		{
			name:     "no kind set",
			clusters: []SearchClusterSpecItem{{Replicas: 1}},
			source:   &MongoDBSource{MongoDBResourceRef: &MongoDBSearchSourceRef{Name: "my-rs"}},
		},
		{
			name:          "MongoDBCommunity kind rejected",
			clusters:      []SearchClusterSpecItem{{Replicas: 1}},
			source:        &MongoDBSource{MongoDBResourceRef: &MongoDBSearchSourceRef{Name: "my-mdb", Kind: "MongoDBCommunity"}},
			errorContains: "MongoDBCommunity is not supported",
		},
		{
			name:          "MongoDBCommunity kind + multi-cluster",
			clusters:      []SearchClusterSpecItem{{ClusterName: "c1", Replicas: 1}, {ClusterName: "c2", Replicas: 1}},
			source:        &MongoDBSource{MongoDBResourceRef: &MongoDBSearchSourceRef{Name: "my-mdb", Kind: "MongoDBCommunity"}},
			errorContains: "forbidden with multi-cluster",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clusters := tt.clusters
			if clusters == nil {
				clusters = []SearchClusterSpecItem{{Replicas: 1}}
			}
			search := &MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{Name: "test-search", Namespace: "test-ns"},
				Spec:       MongoDBSearchSpec{Clusters: clusters, Source: tt.source},
			}
			result := validateMongoDBCommunitySourceForbidden(search)
			if tt.errorContains != "" {
				assert.Equal(t, v1.ErrorLevel, result.Level)
				assert.Contains(t, result.Msg, tt.errorContains)
			} else {
				assert.Equal(t, v1.SuccessLevel, result.Level)
			}
		})
	}
}

func TestValidateSourceNamespace(t *testing.T) {
	tests := []struct {
		name          string
		sourceNS      string
		metaNS        string
		errorContains string
	}{
		{
			name:     "namespace omitted → success",
			sourceNS: "",
			metaNS:   "test-ns",
		},
		{
			name:     "namespace equals metadata.namespace → success",
			sourceNS: "test-ns",
			metaNS:   "test-ns",
		},
		{
			name:          "namespace differs from metadata.namespace → error",
			sourceNS:      "other-ns",
			metaNS:        "test-ns",
			errorContains: "cross-namespace source references are not supported",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			search := &MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: tt.metaNS},
				Spec: MongoDBSearchSpec{
					Clusters: []SearchClusterSpecItem{{Replicas: 1}},
					Source: &MongoDBSource{
						MongoDBResourceRef: &MongoDBSearchSourceRef{Name: "src", Namespace: tt.sourceNS},
					},
				},
			}
			result := validateSourceNamespace(search)
			if tt.errorContains != "" {
				assert.Equal(t, v1.ErrorLevel, result.Level)
				assert.Contains(t, result.Msg, tt.errorContains)
			} else {
				assert.Equal(t, v1.SuccessLevel, result.Level)
			}
		})
	}
}

func TestValidateShardedManagedLBHostnamePlaceholder(t *testing.T) {
	multiShards := []ExternalShardConfig{
		{ShardName: "s0", Hosts: []string{"h0:27017"}},
		{ShardName: "s1", Hosts: []string{"h1:27017"}},
	}
	singleShard := multiShards[:1]

	tests := []struct {
		name          string
		shards        []ExternalShardConfig
		hostname      string
		errorContains string
	}{
		{
			name:     "no sharded source → success",
			shards:   nil,
			hostname: "lb.example.com",
		},
		{
			name:     "single shard with no placeholder → success",
			shards:   singleShard,
			hostname: "lb.example.com",
		},
		{
			name:     "multiple shards, hostname contains {shardName} → success",
			shards:   multiShards,
			hostname: "mongot-{shardName}.example.com",
		},
		{
			name:          "multiple shards, hostname lacks {shardName} → error",
			shards:        multiShards,
			hostname:      "lb.example.com",
			errorContains: ShardNamePlaceholder,
		},
		{
			name:     "multiple shards, no managed LB → success",
			shards:   multiShards,
			hostname: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var source *MongoDBSource
			if tt.shards != nil {
				source = &MongoDBSource{
					ExternalMongoDBSource: &ExternalMongoDBSource{
						ShardedCluster: &ExternalShardedClusterConfig{
							Router: ExternalRouterConfig{Hosts: []string{"mongos:27017"}},
							Shards: tt.shards,
						},
					},
				}
			}
			search := &MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "ns"},
				Spec: MongoDBSearchSpec{
					Clusters: []SearchClusterSpecItem{{Replicas: 1}},
					Source:   source,
					LoadBalancer: &LoadBalancerConfig{
						Managed: &ManagedLBConfig{ExternalHostname: tt.hostname},
					},
				},
			}
			result := validateShardedManagedLBHostnamePlaceholder(search)
			if tt.errorContains != "" {
				assert.Equal(t, v1.ErrorLevel, result.Level)
				assert.Contains(t, result.Msg, tt.errorContains)
			} else {
				assert.Equal(t, v1.SuccessLevel, result.Level)
			}
		})
	}
}

func newSearch(name string, shards []ExternalShardConfig, tlsPrefix string, isTLS, isLBManaged bool) *MongoDBSearch {
	search := &MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "test-namespace"},
		Spec: MongoDBSearchSpec{
			Clusters: []SearchClusterSpecItem{{Replicas: 1}},
			Source: &MongoDBSource{
				ExternalMongoDBSource: &ExternalMongoDBSource{
					ShardedCluster: &ExternalShardedClusterConfig{
						Router: ExternalRouterConfig{Hosts: []string{"mongos.example.com:27017"}},
						Shards: shards,
					},
				},
			},
		},
	}
	if isTLS {
		search.Spec.Security.TLS = &TLS{CertsSecretPrefix: tlsPrefix}
	}
	if isLBManaged {
		search.Spec.LoadBalancer = &LoadBalancerConfig{Managed: &ManagedLBConfig{}}
	}
	return search
}
