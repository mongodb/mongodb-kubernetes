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

func newExternalShardedConfig(routerHosts []string, shards []searchv1.ExternalShardConfig) *searchv1.ExternalShardedConfig {
	return &searchv1.ExternalShardedConfig{
		Router: searchv1.ExternalRouterConfig{
			Hosts: routerHosts,
		},
		Shards: shards,
	}
}

func newExternalShardedConfigWithRouterTLS(routerHosts []string, routerTLS *searchv1.ExternalMongodTLS, shards []searchv1.ExternalShardConfig) *searchv1.ExternalShardedConfig {
	return &searchv1.ExternalShardedConfig{
		Router: searchv1.ExternalRouterConfig{
			Hosts: routerHosts,
			TLS:   routerTLS,
		},
		Shards: shards,
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
			name: "Empty router hosts",
			spec: &searchv1.ExternalMongoDBSource{
				Sharded: newExternalShardedConfig([]string{}, []searchv1.ExternalShardConfig{
					{ShardName: "shard-0", Hosts: []string{"host:27017"}},
				}),
			},
			expectError:    true,
			expectedErrMsg: "router.hosts must have at least one host",
		},
		{
			name: "Empty shards list",
			spec: &searchv1.ExternalMongoDBSource{
				Sharded: newExternalShardedConfig([]string{"mongos.example.com:27017"}, []searchv1.ExternalShardConfig{}),
			},
			expectError:    true,
			expectedErrMsg: "at least one shard must be configured",
		},
		{
			name: "Shard with empty shardName",
			spec: &searchv1.ExternalMongoDBSource{
				Sharded: newExternalShardedConfig([]string{"mongos.example.com:27017"}, []searchv1.ExternalShardConfig{
					{ShardName: "", Hosts: []string{"host:27017"}},
				}),
			},
			expectError:    true,
			expectedErrMsg: "shard[0].shardName is required",
		},
		{
			name: "Shard with empty hosts list",
			spec: &searchv1.ExternalMongoDBSource{
				Sharded: newExternalShardedConfig([]string{"mongos.example.com:27017"}, []searchv1.ExternalShardConfig{
					{ShardName: "shard-0", Hosts: []string{}},
				}),
			},
			expectError:    true,
			expectedErrMsg: "shard[0].hosts must have at least one host",
		},
		{
			name: "Second shard with empty shardName",
			spec: &searchv1.ExternalMongoDBSource{
				Sharded: newExternalShardedConfig([]string{"mongos.example.com:27017"}, []searchv1.ExternalShardConfig{
					{ShardName: "shard-0", Hosts: []string{"host0:27017"}},
					{ShardName: "", Hosts: []string{"host1:27017"}},
				}),
			},
			expectError:    true,
			expectedErrMsg: "shard[1].shardName is required",
		},
		{
			name: "Valid single shard config",
			spec: &searchv1.ExternalMongoDBSource{
				Sharded: newExternalShardedConfig([]string{"mongos.example.com:27017"}, []searchv1.ExternalShardConfig{
					{ShardName: "shard-0", Hosts: []string{"shard0-0.example.com:27017", "shard0-1.example.com:27017"}},
				}),
			},
			expectError: false,
		},
		{
			name: "Valid multi-shard config",
			spec: &searchv1.ExternalMongoDBSource{
				Sharded: newExternalShardedConfig([]string{"mongos.example.com:27017"}, []searchv1.ExternalShardConfig{
					{ShardName: "shard-0", Hosts: []string{"shard0-0.example.com:27017", "shard0-1.example.com:27017"}},
					{ShardName: "shard-1", Hosts: []string{"shard1-0.example.com:27017", "shard1-1.example.com:27017"}},
					{ShardName: "shard-2", Hosts: []string{"shard2-0.example.com:27017"}},
				}),
			},
			expectError: false,
		},
		{
			name: "Valid config with multiple router hosts",
			spec: &searchv1.ExternalMongoDBSource{
				Sharded: newExternalShardedConfig([]string{"mongos1.example.com:27017", "mongos2.example.com:27017"}, []searchv1.ExternalShardConfig{
					{ShardName: "shard-0", Hosts: []string{"shard0-0.example.com:27017"}},
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
				Sharded: newExternalShardedConfig([]string{"mongos:27017"}, []searchv1.ExternalShardConfig{
					{ShardName: "shard-0", Hosts: []string{"host:27017"}},
				}),
			},
			expected: 1,
		},
		{
			name: "Multiple shards",
			spec: &searchv1.ExternalMongoDBSource{
				Sharded: newExternalShardedConfig([]string{"mongos:27017"}, []searchv1.ExternalShardConfig{
					{ShardName: "shard-0", Hosts: []string{"host0:27017"}},
					{ShardName: "shard-1", Hosts: []string{"host1:27017"}},
					{ShardName: "shard-2", Hosts: []string{"host2:27017"}},
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
				Sharded: newExternalShardedConfig([]string{"mongos:27017"}, []searchv1.ExternalShardConfig{
					{ShardName: "my-shard-0", Hosts: []string{"host:27017"}},
				}),
			},
			expected: []string{"my-shard-0"},
		},
		{
			name: "Multiple shards",
			spec: &searchv1.ExternalMongoDBSource{
				Sharded: newExternalShardedConfig([]string{"mongos:27017"}, []searchv1.ExternalShardConfig{
					{ShardName: "shard-alpha", Hosts: []string{"host0:27017"}},
					{ShardName: "shard-beta", Hosts: []string{"host1:27017"}},
					{ShardName: "shard-gamma", Hosts: []string{"host2:27017"}},
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
		Sharded: newExternalShardedConfig([]string{"mongos:27017"}, []searchv1.ExternalShardConfig{
			{ShardName: "shard-0", Hosts: []string{"shard0-0.example.com:27017", "shard0-1.example.com:27017"}},
			{ShardName: "shard-1", Hosts: []string{"shard1-0.example.com:27017"}},
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
				Sharded: newExternalShardedConfig([]string{"mongos:27017"}, []searchv1.ExternalShardConfig{
					{ShardName: "shard-0", Hosts: []string{"first-0.example.com:27017", "first-1.example.com:27017"}},
					{ShardName: "shard-1", Hosts: []string{"second-0.example.com:27017"}},
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
			name: "Single router host",
			spec: &searchv1.ExternalMongoDBSource{
				Sharded: newExternalShardedConfig([]string{"mongos.example.com:27017"}, []searchv1.ExternalShardConfig{
					{ShardName: "shard-0", Hosts: []string{"host:27017"}},
				}),
			},
			expected: "mongos.example.com:27017",
		},
		{
			name: "Multiple router hosts returns first",
			spec: &searchv1.ExternalMongoDBSource{
				Sharded: newExternalShardedConfig([]string{"mongos1.example.com:27017", "mongos2.example.com:27017"}, []searchv1.ExternalShardConfig{
					{ShardName: "shard-0", Hosts: []string{"host:27017"}},
				}),
			},
			expected: "mongos1.example.com:27017",
		},
		{
			name: "Empty router hosts",
			spec: &searchv1.ExternalMongoDBSource{
				Sharded: newExternalShardedConfig([]string{}, []searchv1.ExternalShardConfig{
					{ShardName: "shard-0", Hosts: []string{"host:27017"}},
				}),
			},
			expected: "",
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

	t.Run("Top-level TLS configured", func(t *testing.T) {
		spec := &searchv1.ExternalMongoDBSource{
			TLS: &searchv1.ExternalMongodTLS{
				CA: &corev1.LocalObjectReference{
					Name: "top-level-ca-secret",
				},
			},
		}
		src := newShardedExternalSearchSource(spec)
		tlsConfig := src.TLSConfig()

		assert.NotNil(t, tlsConfig)
		assert.Equal(t, "ca.crt", tlsConfig.CAFileName)
		assert.Equal(t, "ca", tlsConfig.CAVolume.Name)
		assert.Equal(t, "top-level-ca-secret", tlsConfig.CAVolume.VolumeSource.Secret.SecretName)
		assert.NotNil(t, tlsConfig.ResourcesToWatch)
	})

	t.Run("Router-specific TLS overrides top-level TLS", func(t *testing.T) {
		spec := &searchv1.ExternalMongoDBSource{
			Sharded: newExternalShardedConfigWithRouterTLS(
				[]string{"mongos:27017"},
				&searchv1.ExternalMongodTLS{
					CA: &corev1.LocalObjectReference{
						Name: "router-ca-secret",
					},
				},
				[]searchv1.ExternalShardConfig{
					{ShardName: "shard-0", Hosts: []string{"host:27017"}},
				},
			),
			TLS: &searchv1.ExternalMongodTLS{
				CA: &corev1.LocalObjectReference{
					Name: "top-level-ca-secret",
				},
			},
		}
		src := newShardedExternalSearchSource(spec)
		tlsConfig := src.TLSConfig()

		assert.NotNil(t, tlsConfig)
		assert.Equal(t, "ca.crt", tlsConfig.CAFileName)
		assert.Equal(t, "router-ca-secret", tlsConfig.CAVolume.VolumeSource.Secret.SecretName)
	})

	t.Run("Router TLS without top-level TLS", func(t *testing.T) {
		spec := &searchv1.ExternalMongoDBSource{
			Sharded: newExternalShardedConfigWithRouterTLS(
				[]string{"mongos:27017"},
				&searchv1.ExternalMongodTLS{
					CA: &corev1.LocalObjectReference{
						Name: "router-only-ca-secret",
					},
				},
				[]searchv1.ExternalShardConfig{
					{ShardName: "shard-0", Hosts: []string{"host:27017"}},
				},
			),
		}
		src := newShardedExternalSearchSource(spec)
		tlsConfig := src.TLSConfig()

		assert.NotNil(t, tlsConfig)
		assert.Equal(t, "router-only-ca-secret", tlsConfig.CAVolume.VolumeSource.Secret.SecretName)
	})

	t.Run("Falls back to top-level TLS when router TLS not specified", func(t *testing.T) {
		spec := &searchv1.ExternalMongoDBSource{
			Sharded: newExternalShardedConfig([]string{"mongos:27017"}, []searchv1.ExternalShardConfig{
				{ShardName: "shard-0", Hosts: []string{"host:27017"}},
			}),
			TLS: &searchv1.ExternalMongodTLS{
				CA: &corev1.LocalObjectReference{
					Name: "fallback-ca-secret",
				},
			},
		}
		src := newShardedExternalSearchSource(spec)
		tlsConfig := src.TLSConfig()

		assert.NotNil(t, tlsConfig)
		assert.Equal(t, "fallback-ca-secret", tlsConfig.CAVolume.VolumeSource.Secret.SecretName)
	})
}

