package search

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
		search.Spec.LoadBalancer = &LoadBalancerConfig{Mode: LBModeManaged}
	}
	return search
}
