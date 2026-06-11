package searchcontroller

import (
	"testing"

	"github.com/stretchr/testify/assert"

	corev1 "k8s.io/api/core/v1"

	searchv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/search"
	userv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/user"
)

// newExternalShardedSource returns a minimal external sharded MongoDB source so
// endpoint-template validation takes the sharded ({shardName} required) branch.
func newExternalShardedSource() *searchv1.MongoDBSource {
	return &searchv1.MongoDBSource{
		ExternalMongoDBSource: &searchv1.ExternalMongoDBSource{
			ShardedCluster: &searchv1.ExternalShardedClusterConfig{
				Router: searchv1.ExternalRouterConfig{Hosts: []string{"mongos.example.com:27017"}},
				Shards: []searchv1.ExternalShardConfig{{ShardName: "shard-0", Hosts: []string{"h:27017"}}},
			},
		},
	}
}

func TestValidateLBConfig(t *testing.T) {
	testCases := []struct {
		name          string
		modify        func(s *searchv1.MongoDBSearch)
		expectError   bool
		errorContains string
	}{
		{
			name:        "Valid: no LB config",
			modify:      func(s *searchv1.MongoDBSearch) {},
			expectError: false,
		},
		{
			name: "Valid: managed LB",
			modify: func(s *searchv1.MongoDBSearch) {
				s.Spec.Clusters[0].LoadBalancer = &searchv1.LoadBalancerConfig{
					Managed: &searchv1.ManagedLBConfig{},
				}
			},
			expectError: false,
		},
		{
			name: "Valid: unmanaged LB with endpoint",
			modify: func(s *searchv1.MongoDBSearch) {
				s.Spec.Clusters[0].LoadBalancer = &searchv1.LoadBalancerConfig{
					Unmanaged: &searchv1.UnmanagedLBConfig{Endpoint: "lb.example.com:27028"},
				}
			},
			expectError: false,
		},
		{
			name: "Invalid: both managed and unmanaged",
			modify: func(s *searchv1.MongoDBSearch) {
				s.Spec.Clusters[0].LoadBalancer = &searchv1.LoadBalancerConfig{
					Managed:   &searchv1.ManagedLBConfig{},
					Unmanaged: &searchv1.UnmanagedLBConfig{Endpoint: "lb.example.com:27028"},
				}
			},
			expectError:   true,
			errorContains: "mutually exclusive",
		},
		{
			name: "Invalid: neither managed nor unmanaged",
			modify: func(s *searchv1.MongoDBSearch) {
				s.Spec.Clusters[0].LoadBalancer = &searchv1.LoadBalancerConfig{}
			},
			expectError:   true,
			errorContains: "exactly one",
		},
		{
			name: "Invalid: unmanaged without endpoint",
			modify: func(s *searchv1.MongoDBSearch) {
				s.Spec.Clusters[0].LoadBalancer = &searchv1.LoadBalancerConfig{
					Unmanaged: &searchv1.UnmanagedLBConfig{},
				}
			},
			expectError:   true,
			errorContains: "endpoint must be specified",
		},
		{
			name: "Invalid: managed LB with external source and no hostname",
			modify: func(s *searchv1.MongoDBSearch) {
				s.Spec.Source = &searchv1.MongoDBSource{
					ExternalMongoDBSource: &searchv1.ExternalMongoDBSource{
						HostAndPorts: []string{"host:27017"},
					},
				}
				s.Spec.Clusters[0].LoadBalancer = &searchv1.LoadBalancerConfig{
					Managed: &searchv1.ManagedLBConfig{},
				}
			},
			expectError:   true,
			errorContains: "externalHostname must be specified",
		},
		{
			name: "Valid: managed LB with external source and externalHostname",
			modify: func(s *searchv1.MongoDBSearch) {
				s.Spec.Source = &searchv1.MongoDBSource{
					ExternalMongoDBSource: &searchv1.ExternalMongoDBSource{
						HostAndPorts: []string{"host:27017"},
					},
				}
				s.Spec.Clusters[0].LoadBalancer = &searchv1.LoadBalancerConfig{
					Managed: &searchv1.ManagedLBConfig{ExternalHostname: "lb.example.com"},
				}
			},
			expectError: false,
		},
		{
			name: "Valid: unmanaged LB with shardName template and sharded source",
			modify: func(s *searchv1.MongoDBSearch) {
				s.Spec.Source = newExternalShardedSource()
				s.Spec.Clusters[0].LoadBalancer = &searchv1.LoadBalancerConfig{
					Unmanaged: &searchv1.UnmanagedLBConfig{Endpoint: "lb-{shardName}.example.com:27028"},
				}
			},
			expectError: false,
		},
		{
			name: "Invalid: sharded unmanaged endpoint is only template placeholder",
			modify: func(s *searchv1.MongoDBSearch) {
				s.Spec.Source = newExternalShardedSource()
				s.Spec.Clusters[0].LoadBalancer = &searchv1.LoadBalancerConfig{
					Unmanaged: &searchv1.UnmanagedLBConfig{Endpoint: "{shardName}"},
				}
			},
			expectError:   true,
			errorContains: "must contain more than just",
		},
		{
			name: "Invalid: sharded unmanaged endpoint without shardName template",
			modify: func(s *searchv1.MongoDBSearch) {
				s.Spec.Source = newExternalShardedSource()
				s.Spec.Clusters[0].LoadBalancer = &searchv1.LoadBalancerConfig{
					Unmanaged: &searchv1.UnmanagedLBConfig{Endpoint: "lb.example.com:27028"},
				}
			},
			expectError:   true,
			errorContains: "at least one",
		},
		{
			name: "Invalid: RS external source with shardName template",
			modify: func(s *searchv1.MongoDBSearch) {
				s.Spec.Source = &searchv1.MongoDBSource{
					ExternalMongoDBSource: &searchv1.ExternalMongoDBSource{
						HostAndPorts: []string{"host:27017"},
					},
				}
				s.Spec.Clusters[0].LoadBalancer = &searchv1.LoadBalancerConfig{
					Unmanaged: &searchv1.UnmanagedLBConfig{Endpoint: "lb-{shardName}.example.com:27028"},
				}
			},
			expectError:   true,
			errorContains: "must not contain",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			search := newTestMongoDBSearch("test-search", "default", tc.modify)
			err := search.ValidateSpec()
			if tc.expectError {
				assert.Error(t, err)
				if tc.errorContains != "" {
					assert.Contains(t, err.Error(), tc.errorContains)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateX509AuthConfig(t *testing.T) {
	testCases := []struct {
		name          string
		modify        func(s *searchv1.MongoDBSearch)
		expectError   bool
		errorContains string
	}{
		{
			name:        "Valid: no x509 config",
			modify:      func(s *searchv1.MongoDBSearch) {},
			expectError: false,
		},
		{
			name: "Invalid: x509 with empty client cert secret name",
			modify: func(s *searchv1.MongoDBSearch) {
				s.Spec.Source.X509 = &searchv1.X509Auth{}
			},
			expectError:   true,
			errorContains: "must not be empty",
		},
		{
			name: "Invalid: x509 and password both set",
			modify: func(s *searchv1.MongoDBSearch) {
				s.Spec.Source.X509 = &searchv1.X509Auth{
					ClientCertificateSecret: corev1.LocalObjectReference{Name: "my-cert"},
				}
				s.Spec.Source.PasswordSecretRef = &userv1.SecretKeyRef{Name: "my-password"}
			},
			expectError:   true,
			errorContains: "mutually exclusive",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			search := newTestMongoDBSearch("test-search", "default", tc.modify)
			err := search.ValidateSpec()
			if tc.expectError {
				assert.Error(t, err)
				if tc.errorContains != "" {
					assert.Contains(t, err.Error(), tc.errorContains)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateJVMFlags(t *testing.T) {
	testCases := []struct {
		name          string
		jvmFlags      []string
		expectError   bool
		errorContains string
	}{
		{
			name:        "Valid: -Xmx flag",
			jvmFlags:    []string{"-Xmx2g"},
			expectError: false,
		},
		{
			name:        "Valid: multiple jvm flags",
			jvmFlags:    []string{"-Xmx2g", "-Xms512m", "-XX:+UseG1GC"},
			expectError: false,
		},
		{
			name:        "Valid: -D system property",
			jvmFlags:    []string{"-Dsome.property=value"},
			expectError: false,
		},
		{
			name:        "Valid: -XX flag with numeric value",
			jvmFlags:    []string{"-XX:MaxGCPauseMillis=200"},
			expectError: false,
		},
		{
			name:        "Valid: use nil for jvm flags",
			jvmFlags:    nil,
			expectError: false,
		},
		{
			name:        "Valid: empty slice as jvm flags",
			jvmFlags:    []string{},
			expectError: false,
		},
		{
			name:          "Invalid: empty string as jvm flag",
			jvmFlags:      []string{""},
			expectError:   true,
			errorContains: "must not be empty",
		},
		{
			name:          "Invalid: jvm flag with space",
			jvmFlags:      []string{"-Xmx2g -Xms512m"},
			expectError:   true,
			errorContains: "must not contain spaces",
		},
		{
			name:          "Invalid: jvm flag with invalid prefix",
			jvmFlags:      []string{"-verbose:gc"},
			expectError:   true,
			errorContains: "must start with -X, -XX:, or -D",
		},
		{
			name:          "Invalid: jvm flag doesn't have dash prefix",
			jvmFlags:      []string{"Xmx2g"},
			expectError:   true,
			errorContains: "must start with -X, -XX:, or -D",
		},
		{
			name:          "Invalid: jvm flag has invalid characters",
			jvmFlags:      []string{"-Xmx2g;echo"},
			expectError:   true,
			errorContains: "contains invalid characters",
		},
		{
			name:          "Invalid: run another shell cmd (shell injection attempt) using flag",
			jvmFlags:      []string{"-Xmx2g$(whoami)"},
			expectError:   true,
			errorContains: "contains invalid characters",
		},
		{
			name:          "Invalid: second jvm flag invalid",
			jvmFlags:      []string{"-Xmx2g", "-invalid"},
			expectError:   true,
			errorContains: "must start with -X, -XX:, or -D",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			search := newTestMongoDBSearch("test-search", "default", func(s *searchv1.MongoDBSearch) {
				s.Spec.Clusters = []searchv1.ClusterSpec{{JVMFlags: tc.jvmFlags}}
			})

			err := search.ValidateSpec()
			if tc.expectError {
				assert.Error(t, err)
				if tc.errorContains != "" {
					assert.Contains(t, err.Error(), tc.errorContains)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}

	t.Run("Invalid: shard override jvm flag", func(t *testing.T) {
		search := newTestMongoDBSearch("test-search", "default", func(s *searchv1.MongoDBSearch) {
			s.Spec.Clusters = []searchv1.ClusterSpec{{
				ShardOverrides: []searchv1.ShardOverride{{ShardNames: []string{"sh-0"}, JVMFlags: []string{"-invalid"}}},
			}}
		})

		err := search.ValidateSpec()
		assert.ErrorContains(t, err, "shardOverrides[0].jvmFlags[0] must start with -X")
	})
}
