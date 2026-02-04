package searchcontroller

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"

	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"
	userv1 "github.com/mongodb/mongodb-kubernetes/api/v1/user"
)

func newShardedExternalSearchSource(spec *searchv1.ExternalMongoDBSource) *ShardedExternalSearchSource {
	return NewShardedExternalSearchSource("test-namespace", spec)
}

func newExternalShardedConfig(mongosHostAndPort string, shards []searchv1.ExternalShardConfig) *searchv1.ExternalShardedConfig {
	return &searchv1.ExternalShardedConfig{
		MongosHostAndPort: mongosHostAndPort,
		Shards:            shards,
	}
}

func TestShardedExternalSearchSource_Validate(t *testing.T) {
	cases := []struct {
		name           string
		spec           *searchv1.ExternalMongoDBSource
		expectError    bool
		expectedErrMsg string
	}{
		{
			name:           "Nil sharded config",
			spec:           &searchv1.ExternalMongoDBSource{},
			expectError:    true,
			expectedErrMsg: "sharded configuration is required",
		},
		{
			name: "Empty mongos host and port",
			spec: &searchv1.ExternalMongoDBSource{
				Sharded: newExternalShardedConfig("", []searchv1.ExternalShardConfig{
					{Name: "shard-0", HostAndPorts: []string{"host:27017"}},
				}),
			},
			expectError:    true,
			expectedErrMsg: "mongosHostAndPort is required",
		},
		{
			name: "Empty shards list",
			spec: &searchv1.ExternalMongoDBSource{
				Sharded: newExternalShardedConfig("mongos.example.com:27017", []searchv1.ExternalShardConfig{}),
			},
			expectError:    true,
			expectedErrMsg: "at least one shard must be configured",
		},
		{
			name: "Shard with empty name",
			spec: &searchv1.ExternalMongoDBSource{
				Sharded: newExternalShardedConfig("mongos.example.com:27017", []searchv1.ExternalShardConfig{
					{Name: "", HostAndPorts: []string{"host:27017"}},
				}),
			},
			expectError:    true,
			expectedErrMsg: "shard[0].name is required",
		},
		{
			name: "Shard with empty host list",
			spec: &searchv1.ExternalMongoDBSource{
				Sharded: newExternalShardedConfig("mongos.example.com:27017", []searchv1.ExternalShardConfig{
					{Name: "shard-0", HostAndPorts: []string{}},
				}),
			},
			expectError:    true,
			expectedErrMsg: "shard[0].hostAndPorts must have at least one host",
		},
		{
			name: "Second shard with empty name",
			spec: &searchv1.ExternalMongoDBSource{
				Sharded: newExternalShardedConfig("mongos.example.com:27017", []searchv1.ExternalShardConfig{
					{Name: "shard-0", HostAndPorts: []string{"host0:27017"}},
					{Name: "", HostAndPorts: []string{"host1:27017"}},
				}),
			},
			expectError:    true,
			expectedErrMsg: "shard[1].name is required",
		},
		{
			name: "Valid single shard config",
			spec: &searchv1.ExternalMongoDBSource{
				Sharded: newExternalShardedConfig("mongos.example.com:27017", []searchv1.ExternalShardConfig{
					{Name: "shard-0", HostAndPorts: []string{"shard0-0.example.com:27017", "shard0-1.example.com:27017"}},
				}),
			},
			expectError: false,
		},
		{
			name: "Valid multi-shard config",
			spec: &searchv1.ExternalMongoDBSource{
				Sharded: newExternalShardedConfig("mongos.example.com:27017", []searchv1.ExternalShardConfig{
					{Name: "shard-0", HostAndPorts: []string{"shard0-0.example.com:27017", "shard0-1.example.com:27017"}},
					{Name: "shard-1", HostAndPorts: []string{"shard1-0.example.com:27017", "shard1-1.example.com:27017"}},
					{Name: "shard-2", HostAndPorts: []string{"shard2-0.example.com:27017"}},
				}),
			},
			expectError: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := newShardedExternalSearchSource(c.spec)
			err := src.Validate()

			if c.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), c.expectedErrMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestShardedExternalSearchSource_GetShardCount(t *testing.T) {
	cases := []struct {
		name     string
		spec     *searchv1.ExternalMongoDBSource
		expected int
	}{
		{
			name:     "Nil sharded config",
			spec:     &searchv1.ExternalMongoDBSource{},
			expected: 0,
		},
		{
			name: "Single shard",
			spec: &searchv1.ExternalMongoDBSource{
				Sharded: newExternalShardedConfig("mongos:27017", []searchv1.ExternalShardConfig{
					{Name: "shard-0", HostAndPorts: []string{"host:27017"}},
				}),
			},
			expected: 1,
		},
		{
			name: "Multiple shards",
			spec: &searchv1.ExternalMongoDBSource{
				Sharded: newExternalShardedConfig("mongos:27017", []searchv1.ExternalShardConfig{
					{Name: "shard-0", HostAndPorts: []string{"host0:27017"}},
					{Name: "shard-1", HostAndPorts: []string{"host1:27017"}},
					{Name: "shard-2", HostAndPorts: []string{"host2:27017"}},
				}),
			},
			expected: 3,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := newShardedExternalSearchSource(c.spec)
			assert.Equal(t, c.expected, src.GetShardCount())
		})
	}
}

func TestShardedExternalSearchSource_GetShardNames(t *testing.T) {
	cases := []struct {
		name     string
		spec     *searchv1.ExternalMongoDBSource
		expected []string
	}{
		{
			name:     "Nil sharded config",
			spec:     &searchv1.ExternalMongoDBSource{},
			expected: nil,
		},
		{
			name: "Single shard",
			spec: &searchv1.ExternalMongoDBSource{
				Sharded: newExternalShardedConfig("mongos:27017", []searchv1.ExternalShardConfig{
					{Name: "my-shard-0", HostAndPorts: []string{"host:27017"}},
				}),
			},
			expected: []string{"my-shard-0"},
		},
		{
			name: "Multiple shards",
			spec: &searchv1.ExternalMongoDBSource{
				Sharded: newExternalShardedConfig("mongos:27017", []searchv1.ExternalShardConfig{
					{Name: "shard-alpha", HostAndPorts: []string{"host0:27017"}},
					{Name: "shard-beta", HostAndPorts: []string{"host1:27017"}},
					{Name: "shard-gamma", HostAndPorts: []string{"host2:27017"}},
				}),
			},
			expected: []string{"shard-alpha", "shard-beta", "shard-gamma"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := newShardedExternalSearchSource(c.spec)
			assert.Equal(t, c.expected, src.GetShardNames())
		})
	}
}

func TestShardedExternalSearchSource_HostSeedsForShard(t *testing.T) {
	spec := &searchv1.ExternalMongoDBSource{
		Sharded: newExternalShardedConfig("mongos:27017", []searchv1.ExternalShardConfig{
			{Name: "shard-0", HostAndPorts: []string{"shard0-0.example.com:27017", "shard0-1.example.com:27017"}},
			{Name: "shard-1", HostAndPorts: []string{"shard1-0.example.com:27017"}},
		}),
	}
	src := newShardedExternalSearchSource(spec)

	cases := []struct {
		name     string
		shardIdx int
		expected []string
	}{
		{
			name:     "First shard",
			shardIdx: 0,
			expected: []string{"shard0-0.example.com:27017", "shard0-1.example.com:27017"},
		},
		{
			name:     "Second shard",
			shardIdx: 1,
			expected: []string{"shard1-0.example.com:27017"},
		},
		{
			name:     "Negative index",
			shardIdx: -1,
			expected: nil,
		},
		{
			name:     "Out of bounds index",
			shardIdx: 5,
			expected: nil,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.expected, src.HostSeedsForShard(c.shardIdx))
		})
	}

	// Test with nil sharded config
	t.Run("Nil sharded config", func(t *testing.T) {
		nilSrc := newShardedExternalSearchSource(&searchv1.ExternalMongoDBSource{})
		assert.Nil(t, nilSrc.HostSeedsForShard(0))
	})
}

func TestShardedExternalSearchSource_HostSeeds(t *testing.T) {
	cases := []struct {
		name     string
		spec     *searchv1.ExternalMongoDBSource
		expected []string
	}{
		{
			name:     "Nil sharded config",
			spec:     &searchv1.ExternalMongoDBSource{},
			expected: nil,
		},
		{
			name: "Returns first shard hosts",
			spec: &searchv1.ExternalMongoDBSource{
				Sharded: newExternalShardedConfig("mongos:27017", []searchv1.ExternalShardConfig{
					{Name: "shard-0", HostAndPorts: []string{"first-0.example.com:27017", "first-1.example.com:27017"}},
					{Name: "shard-1", HostAndPorts: []string{"second-0.example.com:27017"}},
				}),
			},
			expected: []string{"first-0.example.com:27017", "first-1.example.com:27017"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := newShardedExternalSearchSource(c.spec)
			assert.Equal(t, c.expected, src.HostSeeds())
		})
	}
}

func TestShardedExternalSearchSource_MongosHostAndPort(t *testing.T) {
	cases := []struct {
		name     string
		spec     *searchv1.ExternalMongoDBSource
		expected string
	}{
		{
			name:     "Nil sharded config",
			spec:     &searchv1.ExternalMongoDBSource{},
			expected: "",
		},
		{
			name: "Valid mongos endpoint",
			spec: &searchv1.ExternalMongoDBSource{
				Sharded: newExternalShardedConfig("mongos.example.com:27017", []searchv1.ExternalShardConfig{
					{Name: "shard-0", HostAndPorts: []string{"host:27017"}},
				}),
			},
			expected: "mongos.example.com:27017",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := newShardedExternalSearchSource(c.spec)
			assert.Equal(t, c.expected, src.MongosHostAndPort())
		})
	}
}

func TestShardedExternalSearchSource_KeyfileSecretName(t *testing.T) {
	cases := []struct {
		name     string
		spec     *searchv1.ExternalMongoDBSource
		expected string
	}{
		{
			name:     "No keyfile configured",
			spec:     &searchv1.ExternalMongoDBSource{},
			expected: "",
		},
		{
			name: "Keyfile configured",
			spec: &searchv1.ExternalMongoDBSource{
				KeyFileSecretKeyRef: &userv1.SecretKeyRef{
					Name: "my-keyfile-secret",
				},
			},
			expected: "my-keyfile-secret",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := newShardedExternalSearchSource(c.spec)
			assert.Equal(t, c.expected, src.KeyfileSecretName())
		})
	}
}

func TestShardedExternalSearchSource_TLSConfig(t *testing.T) {
	t.Run("No TLS configured", func(t *testing.T) {
		spec := &searchv1.ExternalMongoDBSource{}
		src := newShardedExternalSearchSource(spec)
		assert.Nil(t, src.TLSConfig())
	})

	t.Run("TLS configured", func(t *testing.T) {
		spec := &searchv1.ExternalMongoDBSource{
			TLS: &searchv1.ExternalMongodTLS{
				CA: &corev1.LocalObjectReference{
					Name: "ca-secret",
				},
			},
		}
		src := newShardedExternalSearchSource(spec)
		tlsConfig := src.TLSConfig()

		assert.NotNil(t, tlsConfig)
		assert.Equal(t, "ca.crt", tlsConfig.CAFileName)
		assert.Equal(t, "ca", tlsConfig.CAVolume.Name)
		assert.NotNil(t, tlsConfig.ResourcesToWatch)
	})
}
