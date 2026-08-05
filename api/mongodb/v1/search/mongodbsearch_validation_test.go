package search

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/utils/ptr"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	userv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/user"
)

func pinnedSpec(name string, idx int32) ClusterSpec {
	return ClusterSpec{Name: name, Index: ptr.To(idx)}
}

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
		{
			// Proxy svc name = name+shard+digits(idx)+19; 20+23+1+19 = 63 ✓ at idx ≤ 9.
			name: "valid MC Proxy Service at borderline with single-digit max index",
			search: func() *MongoDBSearch {
				s := newSearch(strings.Repeat("a", 20), []ExternalShardConfig{shard(strings.Repeat("s", 23))}, "", false, true)
				clusters := make([]ClusterSpec, 10)
				for i := range clusters {
					clusters[i] = pinnedSpec("c"+strconv.Itoa(i), int32(i))
					clusters[i].LoadBalancer = managedLBWithHostname("{shardName}.c" + strconv.Itoa(i) + ".example.com")
				}
				s.Spec.Clusters = clusters
				return s
			}(),
		},
		{
			// At idx 10 digits(idx) becomes 2: 20+23+2+19 = 64 > 63. Guards "valid at idx=0,
			// silently overshoots at ≥11 clusters" — admission validates at the largest index.
			name: "invalid MC Proxy Service overshoots at two-digit max index",
			search: func() *MongoDBSearch {
				s := newSearch(strings.Repeat("a", 20), []ExternalShardConfig{shard(strings.Repeat("s", 23))}, "", false, true)
				clusters := make([]ClusterSpec, 11)
				for i := range clusters {
					clusters[i] = pinnedSpec("c"+strconv.Itoa(i), int32(i))
					clusters[i].LoadBalancer = managedLBWithHostname("{shardName}.c" + strconv.Itoa(i) + ".example.com")
				}
				s.Spec.Clusters = clusters
				return s
			}(),
			errorContains: "exceeds",
		},
		{
			// Proxy svc at pin 999: 38 + len("-search-999-") + len("sh-0") + len("-proxy-svc")
			// = 38+12+4+10 = 64 > 63 — the reconciler names resources with the pin,
			// so admission must validate at the pinned index, not the array position.
			name: "invalid shard Proxy Service at pinned index 999",
			search: func() *MongoDBSearch {
				s := newSearch(strings.Repeat("a", 38), []ExternalShardConfig{shard("sh-0")}, "", false, false)
				s.Spec.Clusters = []ClusterSpec{{Name: "cluster-a", Index: ptr.To(int32(999))}}
				return s
			}(),
			errorContains: "exceeds",
		},
		{
			// Same name unpinned validates at position 0: 38+10+4+10 = 62 ≤ 63.
			name: "valid shard Proxy Service for the same name unpinned",
			search: func() *MongoDBSearch {
				s := newSearch(strings.Repeat("a", 38), []ExternalShardConfig{shard("sh-0")}, "", false, false)
				s.Spec.Clusters = []ClusterSpec{{Name: "cluster-a"}}
				return s
			}(),
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

func TestValidateClustersUniqueClusterName(t *testing.T) {
	tests := []struct {
		name          string
		clusters      []ClusterSpec
		errorContains string
	}{
		{name: "single empty clusterName", clusters: []ClusterSpec{{}}},
		{name: "two unique names", clusters: []ClusterSpec{{Name: "a"}, {Name: "b"}}},
		{
			name:          "duplicate names",
			clusters:      []ClusterSpec{{Name: "a"}, {Name: "a"}},
			errorContains: "duplicate",
		},
		{
			// Empty names are skipped here: validateClustersClusterNameNonEmpty owns
			// the multi-cluster "is required" rule, so two empty names must not be
			// pre-empted with a "duplicate" error.
			name:     "two empty names skipped (non-empty validator owns this)",
			clusters: []ClusterSpec{{}, {}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clusters := tt.clusters
			s := &MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"},
				Spec:       MongoDBSearchSpec{Clusters: clusters},
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

func TestValidateMCExternalHostnames(t *testing.T) {
	mkSearch := func(hostnames []string, sharded bool) *MongoDBSearch {
		clusters := make([]ClusterSpec, 0, len(hostnames))
		for i, hn := range hostnames {
			c := ClusterSpec{Name: "cluster-" + strconv.Itoa(i)}
			if hn != "" {
				c.LoadBalancer = managedLBWithHostname(hn)
			}
			clusters = append(clusters, c)
		}
		s := &MongoDBSearch{
			ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"},
			Spec:       MongoDBSearchSpec{Clusters: clusters},
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
		hostnames     []string
		sharded       bool
		errorContains string
	}{
		{
			name:      "MC RS with distinct hostnames",
			hostnames: []string{"us-east.lb.example.com:443", "eu-west.lb.example.com:443"},
		},
		{
			name:      "MC RS with shared hostname across clusters allowed",
			hostnames: []string{"static.lb.example.com:443", "static.lb.example.com:443"},
		},
		{
			name:      "MC sharded with shardName in each hostname",
			hostnames: []string{"{shardName}.us-east.lb.example.com:443", "{shardName}.eu-west.lb.example.com:443"},
			sharded:   true,
		},
		{
			name:          "MC sharded missing shardName rejected",
			hostnames:     []string{"us-east.lb.example.com:443", "eu-west.lb.example.com:443"},
			sharded:       true,
			errorContains: "{shardName}",
		},
		{
			name:      "MC sharded shardName as name component is allowed",
			hostnames: []string{"search-us-east-{shardName}-proxy.lb.example.com:443", "search-eu-west-{shardName}-proxy.lb.example.com:443"},
			sharded:   true,
		},
		{
			name:      "MC sharded shared hostname across clusters allowed (cross-AZ failover)",
			hostnames: []string{"search-{shardName}-proxy.failover.example.com:443", "search-{shardName}-proxy.failover.example.com:443"},
			sharded:   true,
		},
		{
			name:      "no managed LB returns success",
			hostnames: []string{"", ""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := mkSearch(tt.hostnames, tt.sharded)
			res := validateMCExternalHostnames(s)
			if tt.errorContains != "" {
				assert.Equal(t, v1.ErrorLevel, res.Level, "expected error, got %+v", res)
				assert.Contains(t, res.Msg, tt.errorContains)
			} else {
				assert.Equal(t, v1.SuccessLevel, res.Level, "expected success, got %+v", res)
			}
		})
	}
}

func TestValidateRouterHostname(t *testing.T) {
	// builds a managed-LB cluster spec with the given routerHostnames; sharded toggles whether the
	// source is an external sharded cluster (the only case the validator applies to).
	mkSearch := func(routerHostnames []string, sharded bool) *MongoDBSearch {
		clusters := make([]ClusterSpec, 0, len(routerHostnames))
		for i, rh := range routerHostnames {
			c := ClusterSpec{Name: "cluster-" + strconv.Itoa(i), LoadBalancer: &LoadBalancerConfig{Managed: &ManagedLBConfig{ExternalHostname: "{shardName}.c" + strconv.Itoa(i) + ".example.com", RouterHostname: rh}}}
			clusters = append(clusters, c)
		}
		s := &MongoDBSearch{ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"}, Spec: MongoDBSearchSpec{Clusters: clusters}}
		if sharded {
			s.Spec.Source = &MongoDBSource{ExternalMongoDBSource: &ExternalMongoDBSource{ShardedCluster: &ExternalShardedClusterConfig{
				Router: ExternalRouterConfig{Hosts: []string{"mongos.example.com:27017"}},
				Shards: []ExternalShardConfig{{ShardName: "shard-0", Hosts: []string{"h:27017"}}},
			}}}
		}
		return s
	}

	tests := []struct {
		name            string
		routerHostnames []string
		sharded         bool
		errorContains   string
	}{
		{name: "sharded with routerHostname set", routerHostnames: []string{"search.example.com:443"}, sharded: true},
		{name: "sharded missing routerHostname rejected", routerHostnames: []string{""}, sharded: true, errorContains: "must be specified"},
		{name: "sharded routerHostname with shardName placeholder rejected", routerHostnames: []string{"{shardName}.search.example.com:443"}, sharded: true, errorContains: "must not contain"},
		// Not an external sharded source → validator is a no-op even if unset.
		{name: "non-sharded source ignored", routerHostnames: []string{""}, sharded: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := validateRouterHostname(mkSearch(tt.routerHostnames, tt.sharded))
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
	mkSearch := func(hostnames []string, shardNames []string) *MongoDBSearch {
		clusters := make([]ClusterSpec, 0, len(hostnames))
		for i, hn := range hostnames {
			c := ClusterSpec{Name: "cluster-" + strconv.Itoa(i)}
			if hn != "" {
				c.LoadBalancer = managedLBWithHostname(hn)
			}
			clusters = append(clusters, c)
		}
		s := &MongoDBSearch{
			ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"},
			Spec:       MongoDBSearchSpec{Clusters: clusters},
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

	tests := []struct {
		name          string
		hostnames     []string
		shardNames    []string
		errorContains string
	}{
		{
			name:      "short hostname RS single-cluster passes",
			hostnames: []string{"search.lb.example.com:443"},
		},
		{
			name:      "short hostname MC RS passes",
			hostnames: []string{"us-east.search-lb.example.com:443", "eu-west.search-lb.example.com:443"},
		},
		{
			name:       "short hostname MC sharded passes",
			hostnames:  []string{"us-east.{shardName}.lb.example.com:443", "eu-west.{shardName}.lb.example.com:443"},
			shardNames: []string{"shard-0", "shard-1"},
		},
		{
			name:          "DNS label > 63 rejected",
			hostnames:     []string{longLabel + ".lb.example.com:443", "ok.lb.example.com:443"},
			errorContains: "invalid DNS subdomain",
		},
		{
			// Each label fits 63, but the FQDN exceeds 253.
			// 5 x 60-char labels + 4 dots = 304 > 253.
			name:          "FQDN > 253 rejected",
			hostnames:     []string{strings.Repeat("c", 60) + "." + strings.Repeat("a", 60) + "." + strings.Repeat("b", 60) + "." + strings.Repeat("c", 60) + "." + strings.Repeat("d", 60) + ".lb.example.com:443"},
			errorContains: "invalid DNS subdomain",
		},
		{
			name:       "shard substitution validates each resolved hostname",
			hostnames:  []string{"{shardName}.lb.example.com:443"},
			shardNames: []string{"shard-0"},
		},
		{
			name:          "empty host after port-stripping rejected",
			hostnames:     []string{":443"},
			errorContains: "empty host",
		},
		{
			name:      "no port present validates whole string as host",
			hostnames: []string{"us-east.lb.example.com", "eu-west.lb.example.com"},
		},
		{
			name:          "oversized shard label rejected after substitution",
			hostnames:     []string{"{shardName}.lb.example.com:443"},
			shardNames:    []string{longLabel},
			errorContains: "invalid DNS subdomain",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := mkSearch(tt.hostnames, tt.shardNames)
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

// TestValidateMCRequiresManagedLB covers the multi-cluster LB-mode rule: the
// dispatch scopes this validator to multi-cluster specs, so only the LB shape
// matters here. Both no-LB and unmanaged-LB are rejected; managed is accepted.
func TestValidateMCRequiresManagedLB(t *testing.T) {
	tests := []struct {
		name          string
		lb            *LoadBalancerConfig
		errorContains string
	}{
		{
			name: "MC + managed LB allowed",
			lb:   &LoadBalancerConfig{Managed: &ManagedLBConfig{ExternalHostname: "lb.example.com:443"}},
		},
		{
			name:          "MC + no LB rejected",
			lb:            nil,
			errorContains: "requires a managed load balancer (spec.clusters[0].loadBalancer.managed) at the moment; none is configured",
		},
		{
			name:          "MC + unmanaged LB rejected",
			lb:            &LoadBalancerConfig{Unmanaged: &UnmanagedLBConfig{Endpoint: "lb.example.com:443"}},
			errorContains: "spec.clusters[0].loadBalancer.unmanaged is not supported for multi-cluster",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"},
				Spec: MongoDBSearchSpec{Clusters: []ClusterSpec{
					{Name: "us-east-k8s", LoadBalancer: tt.lb},
					{Name: "eu-west-k8s", LoadBalancer: tt.lb},
				}},
			}
			res := validateMCRequiresManagedLB(s)
			if tt.errorContains != "" {
				assert.Equal(t, v1.ErrorLevel, res.Level)
				assert.Contains(t, res.Msg, tt.errorContains)
			} else {
				assert.Equal(t, v1.SuccessLevel, res.Level)
			}
		})
	}
}

func TestValidateClustersClusterNameNonEmpty(t *testing.T) {
	tests := []struct {
		name          string
		clusters      []ClusterSpec
		errorContains string
	}{
		{
			name:     "single empty clusterName legacy",
			clusters: []ClusterSpec{{}},
		},
		{
			name:     "MC all non-empty",
			clusters: []ClusterSpec{pinnedSpec("us-east-k8s", 0), pinnedSpec("eu-west-k8s", 1)},
		},
		{
			name:          "MC with empty clusterName at index 1",
			clusters:      []ClusterSpec{pinnedSpec("us-east-k8s", 0), {Name: ""}},
			errorContains: "spec.clusters[1].name is required",
		},
		{
			name:          "MC with empty clusterName at index 0",
			clusters:      []ClusterSpec{{Name: ""}, pinnedSpec("eu-west-k8s", 1)},
			errorContains: "spec.clusters[0].name is required",
		},
		{
			// Both names empty must surface the actionable "is required" hint, not
			// "duplicate" — validateClustersUniqueClusterName skips empties so the
			// non-empty rule wins regardless of validator-group ordering.
			name:          "MC with two empty clusterNames reports is-required, not duplicate",
			clusters:      []ClusterSpec{{Name: ""}, {Name: ""}},
			errorContains: "spec.clusters[0].name is required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"},
				Spec:       MongoDBSearchSpec{Clusters: tt.clusters},
			}
			// Multi-cluster specs need an external source to pass
			// validateMCRequiresExternalSource so the clusterName check is the
			// rule under test, not the source check.
			if len(tt.clusters) > 1 {
				s.Spec.Source = &MongoDBSource{
					ExternalMongoDBSource: &ExternalMongoDBSource{HostAndPorts: []string{"h:27017"}},
				}
				for i := range s.Spec.Clusters {
					s.Spec.Clusters[i].LoadBalancer = managedLBWithHostname("c" + strconv.Itoa(i) + ".lb.example.com:443")
				}
			}
			err := s.ValidateSpec()
			if tt.errorContains != "" {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateClustersClusterIndexRequired(t *testing.T) {
	tests := []struct {
		name          string
		clusters      []ClusterSpec
		errorContains string
	}{
		{
			name:     "single-entry unpinned allowed",
			clusters: []ClusterSpec{{Name: "us-east-k8s"}},
		},
		{
			name:     "MC all pinned distinct",
			clusters: []ClusterSpec{pinnedSpec("us-east-k8s", 0), pinnedSpec("eu-west-k8s", 1)},
		},
		{
			name:          "MC missing clusterIndex at index 0",
			clusters:      []ClusterSpec{{Name: "us-east-k8s"}, pinnedSpec("eu-west-k8s", 1)},
			errorContains: "spec.clusters[0].index is required when len(spec.clusters) > 1",
		},
		{
			name:          "MC missing clusterIndex at index 1",
			clusters:      []ClusterSpec{pinnedSpec("us-east-k8s", 0), {Name: "eu-west-k8s"}},
			errorContains: "spec.clusters[1].index is required when len(spec.clusters) > 1",
		},
		{
			name:          "MC duplicate clusterIndex",
			clusters:      []ClusterSpec{pinnedSpec("us-east-k8s", 3), pinnedSpec("eu-west-k8s", 3)},
			errorContains: "index 3 is set on more than one spec.clusters[] entry (entries 0 and 1); pinned indices must be distinct",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clusters := append([]ClusterSpec(nil), tt.clusters...)
			s := &MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"},
				Spec:       MongoDBSearchSpec{Clusters: clusters},
			}
			// Multi-cluster specs need an external source + managed LB on every
			// cluster so the clusterIndex check is the rule under test, not the MC
			// source/LB checks.
			if len(clusters) > 1 {
				s.Spec.Source = &MongoDBSource{
					ExternalMongoDBSource: &ExternalMongoDBSource{HostAndPorts: []string{"h:27017"}},
				}
				for i := range clusters {
					clusters[i].LoadBalancer = managedLBWithHostname("c" + strconv.Itoa(i) + ".lb.example.com:443")
				}
			}
			err := s.ValidateSpec()
			if tt.errorContains != "" {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// managedLBWithHostname is a shorthand for a per-cluster managed LB entry.
func managedLBWithHostname(hostname string) *LoadBalancerConfig {
	// routerHostname must be set for external sharded managed LB, distinct per cluster, and free of
	// {shardName}. Derive it from the (distinct) externalHostname by dropping any "{shardName}."
	// prefix so callers passing distinct externalHostnames get distinct, placeholder-free routers.
	router := strings.ReplaceAll(hostname, ShardNamePlaceholder+".", "")
	return &LoadBalancerConfig{Managed: &ManagedLBConfig{ExternalHostname: hostname, RouterHostname: router}}
}

func newSearch(name string, shards []ExternalShardConfig, tlsPrefix string, isTLS, isLBManaged bool) *MongoDBSearch {
	search := &MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "test-namespace"},
		Spec: MongoDBSearchSpec{
			Clusters: []ClusterSpec{{}},
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
		search.Spec.Clusters[0].LoadBalancer = &LoadBalancerConfig{Managed: &ManagedLBConfig{}}
	}
	return search
}

func TestValidateMCRequiresExternalSource(t *testing.T) {
	mdbBad := &MongoDBSearch{
		Spec: MongoDBSearchSpec{
			Clusters: []ClusterSpec{
				{Name: "cluster-a"},
				{Name: "cluster-b"},
			},
		},
	}
	resBad := validateMCRequiresExternalSource(mdbBad)
	assert.Equal(t, v1.ErrorLevel, resBad.Level, "expected validation error for MC without any external source")
	assert.Contains(t, resBad.Msg, "spec.source.external.hostAndPorts")
	assert.Contains(t, resBad.Msg, "spec.source.external.shardedCluster")
	assert.Contains(t, resBad.Msg, "len(spec.clusters) > 1")

	mdbRS := &MongoDBSearch{
		Spec: MongoDBSearchSpec{
			Clusters: []ClusterSpec{
				{Name: "cluster-a"},
				{Name: "cluster-b"},
			},
			Source: &MongoDBSource{
				ExternalMongoDBSource: &ExternalMongoDBSource{
					HostAndPorts: []string{"a.example:27017"},
				},
			},
		},
	}
	assert.Equal(t, v1.SuccessLevel, validateMCRequiresExternalSource(mdbRS).Level)

	mdbSharded := &MongoDBSearch{
		Spec: MongoDBSearchSpec{
			Clusters: []ClusterSpec{
				{Name: "cluster-a"},
				{Name: "cluster-b"},
			},
			Source: &MongoDBSource{
				ExternalMongoDBSource: &ExternalMongoDBSource{
					ShardedCluster: &ExternalShardedClusterConfig{
						Router: ExternalRouterConfig{Hosts: []string{"mongos.example:27017"}},
						Shards: []ExternalShardConfig{
							{ShardName: "shard-0", Hosts: []string{"shard-0-a.example:27017"}},
						},
					},
				},
			},
		},
	}
	assert.Equal(t, v1.SuccessLevel, validateMCRequiresExternalSource(mdbSharded).Level, "MC sharded source must be accepted")
}

// TestValidateLBConfig covers the per-cluster LB shape rules: exactly one of
// managed/unmanaged per entry, all-or-none presence, and no mode mixing.
func TestValidateLBConfig(t *testing.T) {
	managed := &LoadBalancerConfig{Managed: &ManagedLBConfig{ExternalHostname: "lb.example.com:443"}}
	unmanaged := &LoadBalancerConfig{Unmanaged: &UnmanagedLBConfig{Endpoint: "lb.example.com:443"}}
	tests := []struct {
		name          string
		clusters      []ClusterSpec
		errorContains string
	}{
		{
			name:     "no LB anywhere ok",
			clusters: []ClusterSpec{{Name: "a"}, {Name: "b"}},
		},
		{
			name:     "managed everywhere ok",
			clusters: []ClusterSpec{{Name: "a", LoadBalancer: managed}, {Name: "b", LoadBalancer: managed}},
		},
		{
			name:     "unmanaged everywhere ok",
			clusters: []ClusterSpec{{Name: "a", LoadBalancer: unmanaged}, {Name: "b", LoadBalancer: unmanaged}},
		},
		{
			name:          "neither managed nor unmanaged rejected",
			clusters:      []ClusterSpec{{Name: "a", LoadBalancer: &LoadBalancerConfig{}}},
			errorContains: "exactly one of 'managed' or 'unmanaged'",
		},
		{
			name:          "both managed and unmanaged rejected",
			clusters:      []ClusterSpec{{Name: "a", LoadBalancer: &LoadBalancerConfig{Managed: &ManagedLBConfig{}, Unmanaged: &UnmanagedLBConfig{Endpoint: "e:443"}}}},
			errorContains: "mutually exclusive",
		},
		{
			name:          "partial presence rejected",
			clusters:      []ClusterSpec{{Name: "a", LoadBalancer: managed}, {Name: "b"}},
			errorContains: "every cluster or on none",
		},
		{
			name:          "mixed modes rejected",
			clusters:      []ClusterSpec{{Name: "a", LoadBalancer: managed}, {Name: "b", LoadBalancer: unmanaged}},
			errorContains: "cannot be mixed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"},
				Spec:       MongoDBSearchSpec{Clusters: tt.clusters},
			}
			res := validateLBConfig(s)
			if tt.errorContains != "" {
				assert.Equal(t, v1.ErrorLevel, res.Level)
				assert.Contains(t, res.Msg, tt.errorContains)
			} else {
				assert.Equal(t, v1.SuccessLevel, res.Level)
			}
		})
	}
}

// TestValidateClustersEnvoyResourceNames is the admission check for the
// per-cluster Envoy Deployment + ConfigMap resource names. The Deployment name
// follows DNS-1123 label rules (<=63 chars); the ConfigMap follows DNS-1123
// subdomain rules (<=253). When the search resource name + cluster suffix push
// the result over the limit, validation must reject the spec.
func TestValidateClustersEnvoyResourceNames(t *testing.T) {
	tests := []struct {
		name          string
		searchName    string
		clusters      []ClusterSpec
		errorContains string
	}{
		{
			name:       "short names ok",
			searchName: "s",
			clusters:   []ClusterSpec{{Name: "us-east-k8s"}, {Name: "eu-west-k8s"}},
		},
		{
			name:       "nil clusters ok",
			searchName: "s",
		},
		{
			// Deployment name: <name>-search-lb-<index>
			// suffix "-search-lb-0" is 12 chars; need len(name) > 51 to exceed 63.
			name:          "Deployment name >63 chars rejected",
			searchName:    strings.Repeat("a", 52),
			clusters:      []ClusterSpec{{Name: "us-east-k8s"}},
			errorContains: "exceeds",
		},
		{
			// suffix "-search-lb-999" is 14 chars at pin 999: a 50-char name fits
			// at array position 0 but exceeds 63 at the pinned index the reconciler
			// actually uses for the resource name.
			name:          "pinned index 999 rejected where position 0 would pass",
			searchName:    strings.Repeat("a", 50),
			clusters:      []ClusterSpec{{Name: "us-east-k8s", Index: ptr.To(int32(999))}},
			errorContains: "exceeds",
		},
		{
			name:       "same name unpinned at position 0 passes",
			searchName: strings.Repeat("a", 50),
			clusters:   []ClusterSpec{{Name: "us-east-k8s"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &MongoDBSearch{ObjectMeta: metav1.ObjectMeta{Name: tt.searchName, Namespace: "ns"}}
			s.Spec.Clusters = tt.clusters
			res := validateClustersEnvoyResourceNames(s)
			if tt.errorContains != "" {
				assert.Equal(t, v1.ErrorLevel, res.Level)
				assert.Contains(t, res.Msg, tt.errorContains)
			} else {
				assert.Equal(t, v1.SuccessLevel, res.Level)
			}
		})
	}
}

// TestValidateClustersNonEmpty checks the reconcile backstop to the apiserver
// Required+MinItems=1 rule: an empty spec.clusters surfaces as an error.
func TestValidateClustersNonEmpty(t *testing.T) {
	empty := &MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"},
		Spec:       MongoDBSearchSpec{},
	}
	err := empty.ValidateSpec()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "at least one entry")

	ok := &MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"},
		Spec:       MongoDBSearchSpec{Clusters: []ClusterSpec{{}}},
	}
	assert.NoError(t, ok.ValidateSpec())
}

// TestValidateMultipleReplicasRequireLB covers the spec-tier rule that more than
// one mongot replica in any cluster requires a load balancer.
func TestValidateMultipleReplicasRequireLB(t *testing.T) {
	tests := []struct {
		name          string
		clusters      []ClusterSpec
		lb            *LoadBalancerConfig
		errorContains string
	}{
		{
			name:     "single replica no LB ok",
			clusters: []ClusterSpec{{Replicas: ptr.To(int32(1))}},
		},
		{
			name:          "multiple replicas without LB rejected",
			clusters:      []ClusterSpec{{Replicas: ptr.To(int32(3))}},
			errorContains: "multiple mongot replicas (3) require load balancer",
		},
		{
			name:     "multiple replicas with unmanaged LB ok",
			clusters: []ClusterSpec{{Replicas: ptr.To(int32(3))}},
			lb:       &LoadBalancerConfig{Unmanaged: &UnmanagedLBConfig{Endpoint: "lb.example.com:443"}},
		},
		{
			name:     "multiple replicas with managed LB ok",
			clusters: []ClusterSpec{{Replicas: ptr.To(int32(3))}},
			lb:       &LoadBalancerConfig{Managed: &ManagedLBConfig{}},
		},
		{
			name: "shard override replicas without LB rejected",
			clusters: []ClusterSpec{{
				Replicas:       ptr.To(int32(1)),
				ShardOverrides: []ShardOverride{{ShardNames: []string{"shard-1"}, Replicas: ptr.To(int32(2))}},
			}},
			errorContains: "multiple mongot replicas (2) require load balancer",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clusters := tt.clusters
			for i := range clusters {
				if tt.lb != nil {
					clusters[i].LoadBalancer = tt.lb
				}
			}
			s := &MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"},
				Spec:       MongoDBSearchSpec{Clusters: clusters},
			}
			res := validateMultipleReplicasRequireLB(s)
			if tt.errorContains != "" {
				assert.Equal(t, v1.ErrorLevel, res.Level)
				assert.Contains(t, res.Msg, tt.errorContains)
			} else {
				assert.Equal(t, v1.SuccessLevel, res.Level)
			}
		})
	}
}

// TestValidateUnmanagedEndpointTemplate covers the merged endpoint-template rule:
// a sharded external source requires a {shardName} template; a ReplicaSet source
// must not contain {shardName}. The dispatch guarantees unmanaged LB is set.
func TestValidateUnmanagedEndpointTemplate(t *testing.T) {
	shardedSource := func() *MongoDBSource {
		return &MongoDBSource{
			ExternalMongoDBSource: &ExternalMongoDBSource{
				ShardedCluster: &ExternalShardedClusterConfig{
					Router: ExternalRouterConfig{Hosts: []string{"mongos.example.com:27017"}},
					Shards: []ExternalShardConfig{{ShardName: "shard-0", Hosts: []string{"h:27017"}}},
				},
			},
		}
	}

	tests := []struct {
		name          string
		endpoint      string
		source        *MongoDBSource
		errorContains string
	}{
		{
			name:     "sharded with shardName template ok",
			endpoint: "{shardName}.lb.example.com:443",
			source:   shardedSource(),
		},
		{
			name:          "sharded without shardName template rejected",
			endpoint:      "static.lb.example.com:443",
			source:        shardedSource(),
			errorContains: "at least one",
		},
		{
			name:          "sharded with only placeholder rejected",
			endpoint:      "{shardName}",
			source:        shardedSource(),
			errorContains: "more than just",
		},
		{
			name:     "RS without shardName template ok",
			endpoint: "lb.example.com:443",
			source:   &MongoDBSource{ExternalMongoDBSource: &ExternalMongoDBSource{HostAndPorts: []string{"h:27017"}}},
		},
		{
			name:          "RS with shardName template rejected",
			endpoint:      "{shardName}.lb.example.com:443",
			source:        &MongoDBSource{ExternalMongoDBSource: &ExternalMongoDBSource{HostAndPorts: []string{"h:27017"}}},
			errorContains: "must not contain",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"},
				Spec: MongoDBSearchSpec{
					Source: tt.source,
					Clusters: []ClusterSpec{
						{LoadBalancer: &LoadBalancerConfig{Unmanaged: &UnmanagedLBConfig{Endpoint: tt.endpoint}}},
					},
				},
			}
			res := validateUnmanagedEndpointTemplate(s)
			if tt.errorContains != "" {
				assert.Equal(t, v1.ErrorLevel, res.Level)
				assert.Contains(t, res.Msg, tt.errorContains)
			} else {
				assert.Equal(t, v1.SuccessLevel, res.Level)
			}
		})
	}
}

func TestValidateShardOverrides(t *testing.T) {
	shard := func(name string) ExternalShardConfig {
		return ExternalShardConfig{ShardName: name, Hosts: []string{"host:27017"}}
	}

	t.Run("no overrides passes", func(t *testing.T) {
		s := newSearch("my-search", []ExternalShardConfig{shard("shard-0")}, "", false, false)
		assert.Equal(t, v1.SuccessLevel, validateShardOverrides(s).Level)
	})

	t.Run("override on non-sharded source is rejected", func(t *testing.T) {
		s := &MongoDBSearch{
			ObjectMeta: metav1.ObjectMeta{Name: "my-search", Namespace: "ns"},
			Spec: MongoDBSearchSpec{
				Source: &MongoDBSource{ExternalMongoDBSource: &ExternalMongoDBSource{HostAndPorts: []string{"h:27017"}}},
				Clusters: []ClusterSpec{
					{ShardOverrides: []ShardOverride{{ShardNames: []string{"shard-0"}}}},
				},
			},
		}
		res := validateShardOverrides(s)
		assert.Equal(t, v1.ErrorLevel, res.Level)
		assert.Contains(t, res.Msg, "only supported for external sharded sources")
	})

	t.Run("unknown shardName is rejected", func(t *testing.T) {
		s := newSearch("my-search", []ExternalShardConfig{shard("shard-0")}, "", false, false)
		s.Spec.Clusters = []ClusterSpec{
			{ShardOverrides: []ShardOverride{{ShardNames: []string{"shard-9"}}}},
		}
		res := validateShardOverrides(s)
		assert.Equal(t, v1.ErrorLevel, res.Level)
		assert.Contains(t, res.Msg, "unknown shardName")
	})

	t.Run("shard in two override entries of one cluster is rejected", func(t *testing.T) {
		s := newSearch("my-search", []ExternalShardConfig{shard("shard-0"), shard("shard-1")}, "", false, false)
		s.Spec.Clusters = []ClusterSpec{
			{ShardOverrides: []ShardOverride{
				{ShardNames: []string{"shard-0"}},
				{ShardNames: []string{"shard-0", "shard-1"}},
			}},
		}
		res := validateShardOverrides(s)
		assert.Equal(t, v1.ErrorLevel, res.Level)
		assert.Contains(t, res.Msg, "more than one shardOverrides entry")
	})

	t.Run("same shard overridden in different clusters is allowed", func(t *testing.T) {
		s := newSearch("my-search", []ExternalShardConfig{shard("shard-0")}, "", false, false)
		s.Spec.Clusters = []ClusterSpec{
			{Name: "east", ShardOverrides: []ShardOverride{{ShardNames: []string{"shard-0"}}}},
			{Name: "west", ShardOverrides: []ShardOverride{{ShardNames: []string{"shard-0"}}}},
		}
		assert.Equal(t, v1.SuccessLevel, validateShardOverrides(s).Level)
	})
}
