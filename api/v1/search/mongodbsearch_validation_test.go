package search

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/utils/ptr"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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

func TestValidateClustersSyncSourceSelector(t *testing.T) {
	tests := []struct {
		name          string
		selector      *SyncSourceSelector
		errorContains string
	}{
		{name: "nil selector", selector: nil},
		{name: "matchTags only", selector: &SyncSourceSelector{MatchTags: map[string]string{"region": "us-east"}}},
		{name: "hosts only", selector: &SyncSourceSelector{Hosts: []string{"mongo-1:27017"}}},
		{
			name:          "both set rejected",
			selector:      &SyncSourceSelector{MatchTags: map[string]string{"region": "us-east"}, Hosts: []string{"mongo-1:27017"}},
			errorContains: "mutually exclusive",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"},
				Spec: MongoDBSearchSpec{
					Clusters: &[]ClusterSpec{{ClusterName: "us-east-k8s", SyncSourceSelector: tt.selector}},
				},
			}
			res := validateClustersSyncSourceSelector(s)
			if tt.errorContains != "" {
				assert.Equal(t, v1.ErrorLevel, res.Level)
				assert.Contains(t, res.Msg, tt.errorContains)
			} else {
				assert.Equal(t, v1.SuccessLevel, res.Level)
			}
		})
	}
}

func TestValidateClustersShardOverrides(t *testing.T) {
	tests := []struct {
		name          string
		overrides     []ShardOverride
		errorContains string
	}{
		{name: "no overrides", overrides: nil},
		{name: "valid single shardName", overrides: []ShardOverride{{ShardNames: []string{"shard-0"}}}},
		{name: "valid multiple shardNames", overrides: []ShardOverride{{ShardNames: []string{"shard-0", "shard-1"}}}},
		{
			name:          "empty shardNames slice",
			overrides:     []ShardOverride{{ShardNames: []string{}}},
			errorContains: "must have at least one entry",
		},
		{
			name:          "nil shardNames slice",
			overrides:     []ShardOverride{{}},
			errorContains: "must have at least one entry",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"},
				Spec: MongoDBSearchSpec{
					Clusters: &[]ClusterSpec{{ClusterName: "us-east-k8s", ShardOverrides: tt.overrides}},
				},
			}
			res := validateClustersShardOverrides(s)
			if tt.errorContains != "" {
				assert.Equal(t, v1.ErrorLevel, res.Level)
				assert.Contains(t, res.Msg, tt.errorContains)
			} else {
				assert.Equal(t, v1.SuccessLevel, res.Level)
			}
		})
	}
}

func TestValidateClustersUniqueClusterName(t *testing.T) {
	tests := []struct {
		name          string
		clusters      []ClusterSpec
		errorContains string
	}{
		{name: "single empty clusterName", clusters: []ClusterSpec{{}}},
		{name: "two unique names", clusters: []ClusterSpec{{ClusterName: "a"}, {ClusterName: "b"}}},
		{
			name:          "duplicate names",
			clusters:      []ClusterSpec{{ClusterName: "a"}, {ClusterName: "a"}},
			errorContains: "duplicate",
		},
		{
			// Empty names are reserved for the single-cluster degenerate case;
			// two empty names is still a duplicate.
			name:          "two empty names",
			clusters:      []ClusterSpec{{}, {}},
			errorContains: "duplicate",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clusters := tt.clusters
			s := &MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"},
				Spec:       MongoDBSearchSpec{Clusters: &clusters},
			}
			res := validateClustersUniqueClusterName(s)
			if tt.errorContains != "" {
				assert.Equal(t, v1.ErrorLevel, res.Level)
				assert.Contains(t, res.Msg, tt.errorContains)
			} else {
				assert.Equal(t, v1.SuccessLevel, res.Level)
			}
		})
	}
}

func TestValidateMCExternalHostnamePlaceholders(t *testing.T) {
	mkSearch := func(template string, clusters []ClusterSpec, sharded bool) *MongoDBSearch {
		s := &MongoDBSearch{
			ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"},
			Spec: MongoDBSearchSpec{
				LoadBalancer: &LoadBalancerConfig{Managed: &ManagedLBConfig{ExternalHostname: template}},
			},
		}
		if clusters != nil {
			cs := clusters
			s.Spec.Clusters = &cs
		}
		if sharded {
			s.Spec.Source = &MongoDBSource{
				ExternalMongoDBSource: &ExternalMongoDBSource{
					ShardedCluster: &ExternalShardedClusterConfig{
						Router: ExternalRouterConfig{Hosts: []string{"mongos.example.com:27017"}},
						Shards: []ExternalShardConfig{{ShardName: "shard-0", Hosts: []string{"h:27017"}}},
					},
				},
			}
		}
		return s
	}

	tests := []struct {
		name          string
		template      string
		clusters      []ClusterSpec
		sharded       bool
		errorContains string
	}{
		{
			name:     "single-cluster legacy no placeholder",
			template: "static.lb.example.com:443",
			clusters: nil,
		},
		{
			name:     "single-entry clusters does not require placeholder",
			template: "static.lb.example.com:443",
			clusters: []ClusterSpec{{ClusterName: "us-east-k8s"}},
		},
		{
			name:     "MC RS with clusterName placeholder",
			template: "{clusterName}.lb.example.com:443",
			clusters: []ClusterSpec{{ClusterName: "us-east-k8s"}, {ClusterName: "eu-west-k8s"}},
		},
		{
			name:     "MC RS with clusterIndex placeholder",
			template: "search-{clusterIndex}.lb.example.com:443",
			clusters: []ClusterSpec{{ClusterName: "us-east-k8s"}, {ClusterName: "eu-west-k8s"}},
		},
		{
			name:          "MC RS missing both cluster placeholders",
			template:      "static.lb.example.com:443",
			clusters:      []ClusterSpec{{ClusterName: "us-east-k8s"}, {ClusterName: "eu-west-k8s"}},
			errorContains: "{clusterName}",
		},
		{
			name:     "MC sharded with all three placeholders",
			template: "{clusterName}.{shardName}.lb.example.com:443",
			clusters: []ClusterSpec{{ClusterName: "us-east-k8s"}, {ClusterName: "eu-west-k8s"}},
			sharded:  true,
		},
		{
			name:          "MC sharded missing shardName",
			template:      "{clusterName}.lb.example.com:443",
			clusters:      []ClusterSpec{{ClusterName: "us-east-k8s"}, {ClusterName: "eu-west-k8s"}},
			sharded:       true,
			errorContains: "{shardName}",
		},
		{
			name:     "no managed LB returns success",
			template: "",
			clusters: []ClusterSpec{{ClusterName: "us-east-k8s"}, {ClusterName: "eu-west-k8s"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := mkSearch(tt.template, tt.clusters, tt.sharded)
			if tt.template == "" {
				s.Spec.LoadBalancer = nil
			}
			res := validateMCExternalHostnamePlaceholders(s)
			if tt.errorContains != "" {
				assert.Equal(t, v1.ErrorLevel, res.Level, "expected error, got %+v", res)
				assert.Contains(t, res.Msg, tt.errorContains)
			} else {
				assert.Equal(t, v1.SuccessLevel, res.Level, "expected success, got %+v", res)
			}
		})
	}
}

func TestValidateExternalHostnameDNSLength(t *testing.T) {
	mkSearch := func(template string, clusters []ClusterSpec, shardNames []string) *MongoDBSearch {
		s := &MongoDBSearch{
			ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"},
			Spec: MongoDBSearchSpec{
				LoadBalancer: &LoadBalancerConfig{Managed: &ManagedLBConfig{ExternalHostname: template}},
			},
		}
		if clusters != nil {
			cs := clusters
			s.Spec.Clusters = &cs
		}
		if shardNames != nil {
			shards := make([]ExternalShardConfig, 0, len(shardNames))
			for _, sn := range shardNames {
				shards = append(shards, ExternalShardConfig{ShardName: sn, Hosts: []string{"h:27017"}})
			}
			s.Spec.Source = &MongoDBSource{
				ExternalMongoDBSource: &ExternalMongoDBSource{
					ShardedCluster: &ExternalShardedClusterConfig{
						Router: ExternalRouterConfig{Hosts: []string{"mongos.example.com:27017"}},
						Shards: shards,
					},
				},
			}
		}
		return s
	}

	// Build a > 63-char label.
	longLabel := strings.Repeat("a", 64)

	// Build a > 253-char total host: 4 labels of 60 chars each separated by dots = 4*60 + 3 = 243 (<253),
	// so use longer labels to overflow. 5 labels of 60 chars: 5*60 + 4 = 304 > 253.
	longClusterLabel := strings.Repeat("c", 60)

	tests := []struct {
		name          string
		template      string
		clusters      []ClusterSpec
		shardNames    []string
		errorContains string
	}{
		{
			name:     "short hostname RS legacy passes",
			template: "search.lb.example.com:443",
		},
		{
			name:     "short hostname MC RS passes",
			template: "{clusterName}.search-lb.example.com:443",
			clusters: []ClusterSpec{{ClusterName: "us-east-k8s"}, {ClusterName: "eu-west-k8s"}},
		},
		{
			name:       "short hostname MC sharded passes",
			template:   "{clusterName}.{shardName}.lb.example.com:443",
			clusters:   []ClusterSpec{{ClusterName: "us-east-k8s"}, {ClusterName: "eu-west-k8s"}},
			shardNames: []string{"shard-0", "shard-1"},
		},
		{
			name:          "DNS label > 63 after substitution rejected",
			template:      "{clusterName}.lb.example.com:443",
			clusters:      []ClusterSpec{{ClusterName: longLabel}, {ClusterName: "ok"}},
			errorContains: "invalid DNS subdomain",
		},
		{
			// Each label fits 63, but the FQDN exceeds 253 after substitution.
			// 4 x 60-char labels + 4 dots = 244; plus "{clusterName}." (60+1=61) and tail (suffix) bring it well over 253.
			name:     "FQDN > 253 after cross-product rejected",
			template: "{clusterName}." + strings.Repeat("a", 60) + "." + strings.Repeat("b", 60) + "." + strings.Repeat("c", 60) + "." + strings.Repeat("d", 60) + ".lb.example.com:443",
			clusters: []ClusterSpec{
				{ClusterName: longClusterLabel},
				{ClusterName: "ok"},
			},
			errorContains: "invalid DNS subdomain",
		},
		{
			name:     "single-cluster legacy with literal hostname passes",
			template: "search.lb.example.com:443",
			clusters: nil,
		},
		{
			name:     "single-entry clusters substitutes and validates",
			template: "{clusterName}.lb.example.com:443",
			clusters: []ClusterSpec{{ClusterName: "us-east-k8s"}},
		},
		{
			name:     "no managed LB returns success",
			template: "",
			clusters: []ClusterSpec{{ClusterName: "us-east-k8s"}, {ClusterName: "eu-west-k8s"}},
		},
		{
			name:          "empty host after port-stripping rejected",
			template:      ":443",
			clusters:      nil,
			errorContains: "empty host",
		},
		{
			name:     "no port present validates whole string as host",
			template: "{clusterName}.lb.example.com",
			clusters: []ClusterSpec{{ClusterName: "us-east-k8s"}, {ClusterName: "eu-west-k8s"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := mkSearch(tt.template, tt.clusters, tt.shardNames)
			if tt.template == "" {
				s.Spec.LoadBalancer = nil
			}
			res := validateExternalHostnameDNSLength(s)
			if tt.errorContains != "" {
				assert.Equal(t, v1.ErrorLevel, res.Level, "expected error, got %+v", res)
				assert.Contains(t, res.Msg, tt.errorContains)
			} else {
				assert.Equal(t, v1.SuccessLevel, res.Level, "expected success, got %+v", res)
			}
		})
	}
}

func newSearch(name string, shards []ExternalShardConfig, tlsPrefix string, isTLS, isLBManaged bool) *MongoDBSearch {
	search := &MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "test-namespace"},
		Spec: MongoDBSearchSpec{
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
