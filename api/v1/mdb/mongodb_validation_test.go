package mdb

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	v1 "github.com/mongodb/mongodb-kubernetes/api/v1"
	"github.com/mongodb/mongodb-kubernetes/api/v1/status"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

func TestMongoDB_ProcessValidations_BadHorizonsMemberCount(t *testing.T) {
	replicaSetHorizons := []MongoDBHorizonConfig{
		{"my-horizon": "my-db.com:12345"},
		{"my-horizon": "my-db.com:12346"},
	}

	rs := NewReplicaSetBuilder().SetSecurityTLSEnabled().Build()
	rs.Spec.Connectivity = &MongoDBConnectivity{}
	rs.Spec.Connectivity.ReplicaSetHorizons = replicaSetHorizons
	err := rs.ProcessValidationsOnReconcile(nil)
	assert.Contains(t, "Number of horizons must be equal to number of members in replica set", err.Error())
}

func TestMongoDB_ProcessValidations_HorizonsWithoutTLS(t *testing.T) {
	replicaSetHorizons := []MongoDBHorizonConfig{
		{"my-horizon": "my-db.com:12345"},
		{"my-horizon": "my-db.com:12342"},
		{"my-horizon": "my-db.com:12346"},
	}

	rs := NewReplicaSetBuilder().Build()
	rs.Spec.Connectivity = &MongoDBConnectivity{}
	rs.Spec.Connectivity.ReplicaSetHorizons = replicaSetHorizons
	err := rs.ProcessValidationsOnReconcile(nil)
	assert.Equal(t, "TLS must be enabled in order to use replica set horizons", err.Error())
}

func TestMongoDB_ProcessValidationsOnReconcile_X509WithoutTls(t *testing.T) {
	rs := NewReplicaSetBuilder().Build()
	rs.Spec.Security.Authentication = &Authentication{Enabled: true, Modes: []AuthMode{"X509"}}
	err := rs.ProcessValidationsOnReconcile(nil)
	assert.Equal(t, "Cannot have a non-tls deployment when x509 authentication is enabled", err.Error())
}

func TestMongoDB_ValidateCreate_Error(t *testing.T) {
	replicaSetHorizons := []MongoDBHorizonConfig{
		{"my-horizon": "my-db.com:12345"},
		{"my-horizon": "my-db.com:12342"},
		{"my-horizon": "my-db.com:12346"},
	}

	rs := NewReplicaSetBuilder().Build()
	rs.Spec.Connectivity = &MongoDBConnectivity{}
	rs.Spec.Connectivity.ReplicaSetHorizons = replicaSetHorizons
	_, err := rs.ValidateCreate()
	assert.Equal(t, "TLS must be enabled in order to use replica set horizons", err.Error())
}

func TestMongoDB_MultipleAuthsButNoAgentAuth_Error(t *testing.T) {
	rs := NewReplicaSetBuilder().SetVersion("4.0.2-ent").Build()
	rs.Spec.Security = &Security{
		TLSConfig: &TLSConfig{Enabled: true},
		Authentication: &Authentication{
			Enabled: true,
			Modes:   []AuthMode{"LDAP", "X509"},
		},
	}
	_, err := rs.ValidateCreate()
	assert.Errorf(t, err, "spec.security.authentication.agents.mode must be specified if more than one entry is present in spec.security.authentication.modes")
}

func TestMongoDB_ResourceTypeImmutable(t *testing.T) {
	newRs := NewReplicaSetBuilder().Build()
	oldRs := NewReplicaSetBuilder().setType(ShardedCluster).Build()
	_, err := newRs.ValidateUpdate(oldRs)
	assert.Errorf(t, err, "'resourceType' cannot be changed once created")
}

func TestSpecProjectOnlyOneValue(t *testing.T) {
	rs := NewReplicaSetBuilder().Build()
	rs.Spec.CloudManagerConfig = &PrivateCloudConfig{
		ConfigMapRef: ConfigMapRef{Name: "cloud-manager"},
	}
	_, err := rs.ValidateCreate()
	assert.NoError(t, err)
}

func TestMongoDB_ProcessValidations(t *testing.T) {
	rs := NewReplicaSetBuilder().Build()
	assert.Error(t, rs.ProcessValidationsOnReconcile(nil), nil)
}

func TestMongoDB_ValidateAdditionalMongodConfig(t *testing.T) {
	t.Run("No sharded cluster additional config for replica set", func(t *testing.T) {
		rs := NewReplicaSetBuilder().SetConfigSrvAdditionalConfig(NewAdditionalMongodConfig("systemLog.verbosity", 5)).Build()
		_, err := rs.ValidateCreate()
		require.Error(t, err)
		assert.Equal(t, "'spec.mongos', 'spec.configSrv', 'spec.shard' cannot be specified if type of MongoDB is ReplicaSet", err.Error())
	})
	t.Run("No sharded cluster additional config for standalone", func(t *testing.T) {
		rs := NewStandaloneBuilder().SetMongosAdditionalConfig(NewAdditionalMongodConfig("systemLog.verbosity", 5)).Build()
		_, err := rs.ValidateCreate()
		require.Error(t, err)
		assert.Equal(t, "'spec.mongos', 'spec.configSrv', 'spec.shard' cannot be specified if type of MongoDB is Standalone", err.Error())
	})
	t.Run("No replica set additional config for sharded cluster", func(t *testing.T) {
		rs := NewClusterBuilder().SetAdditionalConfig(NewAdditionalMongodConfig("systemLog.verbosity", 5)).Build()
		_, err := rs.ValidateCreate()
		require.Error(t, err)
		assert.Equal(t, "'spec.additionalMongodConfig' cannot be specified if type of MongoDB is ShardedCluster", err.Error())
	})
}

func TestScramSha1AuthValidation(t *testing.T) {
	type TestConfig struct {
		MongoDB       *MongoDB
		ErrorExpected bool
	}
	tests := map[string]TestConfig{
		"Valid MongoDB with Authentication": {
			MongoDB:       NewReplicaSetBuilder().EnableAuth([]AuthMode{util.SCRAMSHA1}).Build(),
			ErrorExpected: true,
		},
		"Valid MongoDB with SCRAM-SHA-1": {
			MongoDB:       NewReplicaSetBuilder().EnableAuth([]AuthMode{util.SCRAMSHA1, util.MONGODBCR}).EnableAgentAuth(util.MONGODBCR).Build(),
			ErrorExpected: false,
		},
	}
	for testName, testConfig := range tests {
		t.Run(testName, func(t *testing.T) {
			validationResult := scramSha1AuthValidation(testConfig.MongoDB.Spec.DbCommonSpec)
			assert.Equal(t, testConfig.ErrorExpected, v1.ValidationSuccess() != validationResult, "Expected %v, got %v", testConfig.ErrorExpected, validationResult)
		})
	}
}

func TestReplicasetMemberIsSpecified(t *testing.T) {
	rs := NewDefaultReplicaSetBuilder().Build()
	err := rs.ProcessValidationsOnReconcile(nil)
	require.Error(t, err)
	assert.Errorf(t, err, "'spec.members' must be specified if type of MongoDB is ReplicaSet")

	rs = NewReplicaSetBuilder().Build()
	rs.Spec.CloudManagerConfig = &PrivateCloudConfig{
		ConfigMapRef: ConfigMapRef{Name: "cloud-manager"},
	}
	require.NoError(t, rs.ProcessValidationsOnReconcile(nil))
}

func TestReplicasetFCV(t *testing.T) {
	tests := []struct {
		name                 string
		fcv                  *string
		expectError          bool
		expectedErrorMessage string
	}{
		{
			name:                 "Invalid FCV value",
			fcv:                  ptr.To("test"),
			expectError:          true,
			expectedErrorMessage: "invalid feature compatibility version: test, possible values are: 'AlwaysMatchVersion' or 'major.minor'",
		},
		{
			name:        "Valid FCV with specific version",
			fcv:         ptr.To("4.0"),
			expectError: false,
		},
		{
			name:                 "Invalid FCV - not major.minor only",
			fcv:                  ptr.To("4.0.0"),
			expectError:          true,
			expectedErrorMessage: "invalid feature compatibility version: 4.0.0, possible values are: 'AlwaysMatchVersion' or 'major.minor'",
		},
		{
			name:        "Valid FCV with AlwaysMatchVersion",
			fcv:         ptr.To("AlwaysMatchVersion"),
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rs := NewReplicaSetBuilder().Build()
			rs.Spec.CloudManagerConfig = &PrivateCloudConfig{
				ConfigMapRef: ConfigMapRef{Name: "cloud-manager"},
			}
			rs.Spec.FeatureCompatibilityVersion = tt.fcv

			err := rs.ProcessValidationsOnReconcile(nil)

			if tt.expectError {
				require.Error(t, err)
				assert.EqualError(t, err, tt.expectedErrorMessage)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestOIDCAuthValidation(t *testing.T) {
	tests := []struct {
		name                 string
		auth                 *Authentication
		expectedErrorMessage string
		expectedWarning      status.Warning
	}{
		{
			name: "Authentication disabled",
			auth: &Authentication{
				Enabled: false,
			},
		},
		{
			name: "OIDC not enabled",
			auth: &Authentication{
				Enabled: true,
				Modes:   []AuthMode{util.SCRAMSHA256},
			},
		},
		{
			name: "OIDC cannot be only authentication mode enabled",
			auth: &Authentication{
				Enabled: true,
				Modes:   []AuthMode{util.OIDC},
			},
			expectedErrorMessage: "OIDC authentication cannot be used as the only authentication mechanism",
		},
		{
			name: "Agent authentication mode not specified, but required",
			auth: &Authentication{
				Enabled: true,
				Modes:   []AuthMode{util.OIDC, util.SCRAMSHA256},
			},
			expectedErrorMessage: "spec.security.authentication.agents.mode must be specified if more than one entry is present in spec.security.authentication.modes",
		},
		{
			name: "OIDC enabled but without provider configs",
			auth: &Authentication{
				Enabled: true,
				Agents:  AgentAuthentication{Mode: util.SCRAMSHA256},
				Modes:   []AuthMode{util.OIDC, util.SCRAMSHA256},
			},
			expectedErrorMessage: "At least one OIDC provider config needs to be specified when OIDC authentication is enabled",
		},
		{
			name: "Multiple non unique configuration names",
			auth: &Authentication{
				Enabled: true,
				Agents:  AgentAuthentication{Mode: util.SCRAMSHA256},
				Modes:   []AuthMode{util.OIDC, util.SCRAMSHA256},
				OIDCProviderConfigs: []OIDCProviderConfig{
					{
						ConfigurationName:   "provider",
						IssuerURI:           "https://example1.com",
						AuthorizationMethod: OIDCAuthorizationMethodWorkforceIdentityFederation,
						ClientId:            "clientId1",
					},
					{
						ConfigurationName:   "provider",
						IssuerURI:           "https://example2.com",
						AuthorizationMethod: OIDCAuthorizationMethodWorkforceIdentityFederation,
						ClientId:            "clientId2",
					},
				},
			},
			expectedErrorMessage: "OIDC provider config name provider is not unique",
		},
		{
			name: "Multiple Workforce Identity Federation configs",
			auth: &Authentication{
				Enabled: true,
				Agents:  AgentAuthentication{Mode: util.SCRAMSHA256},
				Modes:   []AuthMode{util.OIDC, util.SCRAMSHA256},
				OIDCProviderConfigs: []OIDCProviderConfig{
					{
						ConfigurationName:   "test-provider1",
						IssuerURI:           "https://example1.com",
						AuthorizationMethod: OIDCAuthorizationMethodWorkforceIdentityFederation,
						ClientId:            "clientId1",
					},
					{
						ConfigurationName:   "test-provider2",
						IssuerURI:           "https://example2.com",
						AuthorizationMethod: OIDCAuthorizationMethodWorkforceIdentityFederation,
						ClientId:            "clientId2",
					},
				},
			},
			expectedErrorMessage: "Only one OIDC provider config can be configured with Workforce Identity Federation. The following configs are configured with Workforce Identity Federation: test-provider1, test-provider2",
		},
		{
			name: "Multiple Workload Identity Federation configs",
			auth: &Authentication{
				Enabled: true,
				Agents:  AgentAuthentication{Mode: util.SCRAMSHA256},
				Modes:   []AuthMode{util.OIDC, util.SCRAMSHA256},
				OIDCProviderConfigs: []OIDCProviderConfig{
					{
						ConfigurationName:   "test-provider-workforce1",
						IssuerURI:           "https://example1.com",
						AuthorizationMethod: OIDCAuthorizationMethodWorkforceIdentityFederation,
						ClientId:            "clientId1",
					},
					{
						ConfigurationName:   "test-provider-workload2",
						IssuerURI:           "https://example2.com",
						AuthorizationMethod: OIDCAuthorizationMethodWorkloadIdentityFederation,
					},
					{
						ConfigurationName:   "test-provider-workload3",
						IssuerURI:           "https://example3.com",
						AuthorizationMethod: OIDCAuthorizationMethodWorkloadIdentityFederation,
					},
				},
			},
		},
		{
			name: "Invalid issuer URI",
			auth: &Authentication{
				Enabled: true,
				Agents:  AgentAuthentication{Mode: util.SCRAMSHA256},
				Modes:   []AuthMode{util.OIDC, util.SCRAMSHA256},
				OIDCProviderConfigs: []OIDCProviderConfig{
					{
						ConfigurationName: "test-provider",
						IssuerURI:         "invalid-uri",
					},
				},
			},
			expectedErrorMessage: "Invalid IssuerURI in OIDC provider config \"test-provider\": missing URL scheme: invalid-uri",
		},
		{
			name: "Non-HTTPS issuer URI - warning",
			auth: &Authentication{
				Enabled: true,
				Agents:  AgentAuthentication{Mode: util.SCRAMSHA256},
				Modes:   []AuthMode{util.OIDC, util.SCRAMSHA256},
				OIDCProviderConfigs: []OIDCProviderConfig{
					{
						ConfigurationName: "test-provider",
						IssuerURI:         "http://example.com",
					},
				},
			},
			expectedWarning: "IssuerURI http://example.com in OIDC provider config \"test-provider\" in not secure endpoint",
		},
		{
			name: "Workforce Identity Federation without ClientId",
			auth: &Authentication{
				Enabled: true,
				Agents:  AgentAuthentication{Mode: util.SCRAMSHA256},
				Modes:   []AuthMode{util.OIDC, util.SCRAMSHA256},
				OIDCProviderConfigs: []OIDCProviderConfig{
					{
						ConfigurationName:   "test-provider",
						IssuerURI:           "https://example.com",
						AuthorizationMethod: OIDCAuthorizationMethodWorkforceIdentityFederation,
					},
				},
			},
			expectedErrorMessage: "ClientId has to be specified in OIDC provider config \"test-provider\" with Workforce Identity Federation",
		},
		{
			name: "Workload Identity Federation with ClientId - warning",
			auth: &Authentication{
				Enabled: true,
				Agents:  AgentAuthentication{Mode: util.SCRAMSHA256},
				Modes:   []AuthMode{util.OIDC, util.SCRAMSHA256},
				OIDCProviderConfigs: []OIDCProviderConfig{
					{
						ConfigurationName:   "test-provider",
						IssuerURI:           "https://example.com",
						AuthorizationMethod: OIDCAuthorizationMethodWorkloadIdentityFederation,
						ClientId:            "clientId",
					},
				},
			},
			expectedWarning: "ClientId will be ignored in OIDC provider config \"test-provider\" with Workload Identity Federation",
		},
		{
			name: "Workload Identity Federation with RequestedScopes - warning",
			auth: &Authentication{
				Enabled: true,
				Agents:  AgentAuthentication{Mode: util.SCRAMSHA256},
				Modes:   []AuthMode{util.OIDC, util.SCRAMSHA256},
				OIDCProviderConfigs: []OIDCProviderConfig{
					{
						ConfigurationName:   "test-provider",
						IssuerURI:           "https://example.com",
						AuthorizationMethod: OIDCAuthorizationMethodWorkloadIdentityFederation,
						RequestedScopes:     []string{"openid", "email"},
					},
				},
			},
			expectedWarning: "RequestedScopes will be ignored in OIDC provider config \"test-provider\" with Workload Identity Federation",
		},
		{
			name: "Group Membership authorization without GroupsClaim",
			auth: &Authentication{
				Enabled: true,
				Agents:  AgentAuthentication{Mode: util.SCRAMSHA256},
				Modes:   []AuthMode{util.OIDC, util.SCRAMSHA256},
				OIDCProviderConfigs: []OIDCProviderConfig{
					{
						ConfigurationName: "test-provider1",
						IssuerURI:         "https://example.com",
						AuthorizationType: OIDCAuthorizationTypeGroupMembership,
						GroupsClaim:       "groups",
					},
					{
						ConfigurationName: "test-provider2",
						IssuerURI:         "https://example.com",
						AuthorizationType: OIDCAuthorizationTypeGroupMembership,
					},
				},
			},
			expectedErrorMessage: "GroupsClaim has to be specified in OIDC provider config \"test-provider2\" when using Group Membership authorization",
		},
		{
			name: "User ID authorization with GroupsClaim - warning",
			auth: &Authentication{
				Enabled: true,
				Agents:  AgentAuthentication{Mode: util.SCRAMSHA256},
				Modes:   []AuthMode{util.OIDC, util.SCRAMSHA256},
				OIDCProviderConfigs: []OIDCProviderConfig{
					{
						ConfigurationName: "test-provider1",
						IssuerURI:         "https://example.com",
						AuthorizationType: OIDCAuthorizationTypeUserID,
						GroupsClaim:       "groups",
						UserClaim:         "sub",
					},
					{
						ConfigurationName: "test-provider2",
						IssuerURI:         "https://example.com",
						AuthorizationType: OIDCAuthorizationTypeUserID,
						UserClaim:         "sub",
					},
				},
			},
			expectedWarning: "GroupsClaim will be ignored in OIDC provider config \"test-provider1\" when using User ID authorization",
		},
		{
			name: "Valid OIDC configuration",
			auth: &Authentication{
				Enabled: true,
				Agents:  AgentAuthentication{Mode: util.MONGODBCR},
				Modes:   []AuthMode{util.OIDC, util.MONGODBCR},
				OIDCProviderConfigs: []OIDCProviderConfig{
					{
						ConfigurationName: "test-provider1",
						IssuerURI:         "https://example.com",
						AuthorizationType: OIDCAuthorizationTypeGroupMembership,
						GroupsClaim:       "groups",
					},
					{
						ConfigurationName: "test-provider2",
						IssuerURI:         "https://example.com",
						AuthorizationType: OIDCAuthorizationTypeGroupMembership,
						GroupsClaim:       "groups",
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rs := NewReplicaSetBuilder().
				SetSecurityTLSEnabled().
				SetVersion("8.0.5-ent").
				Build()

			rs.Spec.CloudManagerConfig = &PrivateCloudConfig{
				ConfigMapRef: ConfigMapRef{Name: "cloud-manager"},
			}
			rs.Spec.Security.Authentication = tt.auth

			err := rs.ProcessValidationsOnReconcile(nil)

			if tt.expectedErrorMessage != "" {
				assert.NotNil(t, err)
				assert.Equal(t, tt.expectedErrorMessage, err.Error())
			} else {
				assert.Nil(t, err)
			}

			if tt.expectedWarning != "" {
				warnings := rs.GetStatusWarnings()
				assert.Contains(t, warnings, tt.expectedWarning)
			}
		})
	}
}

func TestOIDCProviderConfigUniqueIssuerURIValidation(t *testing.T) {
	tests := []struct {
		name           string
		mongoVersion   string
		configs        []OIDCProviderConfig
		expectedResult v1.ValidationResult
	}{
		{
			name:         "MongoDB 6.0 with duplicate issuer URIs - error",
			mongoVersion: "6.0.0",
			configs: []OIDCProviderConfig{
				{
					ConfigurationName: "config1",
					IssuerURI:         "https://provider.com",
					Audience:          "audience1",
				},
				{
					ConfigurationName: "config2",
					IssuerURI:         "https://provider.com",
					Audience:          "audience2",
				},
			},
			expectedResult: v1.ValidationError("OIDC provider configs %q and %q have duplicate IssuerURI: %s",
				"config1", "config2", "https://provider.com"),
		},
		{
			name:         "MongoDB 7.0 with unique issuer+audience combinations",
			mongoVersion: "7.0.0",
			configs: []OIDCProviderConfig{
				{
					ConfigurationName: "config1",
					IssuerURI:         "https://provider.com",
					Audience:          "audience1",
				},
				{
					ConfigurationName: "config2",
					IssuerURI:         "https://provider.com",
					Audience:          "audience2",
				},
			},
			expectedResult: v1.ValidationSuccess(),
		},
		{
			name:         "MongoDB 7.0 with duplicate issuer+audience combinations - warning",
			mongoVersion: "7.0.0",
			configs: []OIDCProviderConfig{
				{
					ConfigurationName: "config1",
					IssuerURI:         "https://provider.com",
					Audience:          "audience1",
				},
				{
					ConfigurationName: "config2",
					IssuerURI:         "https://provider.com",
					Audience:          "audience1",
				},
			},
			expectedResult: v1.ValidationWarning("OIDC provider configs %q and %q have duplicate IssuerURI and Audience combination",
				"config1", "config2"),
		},
		{
			name:         "MongoDB 7.3 with unique issuer+audience combinations",
			mongoVersion: "7.3.0",
			configs: []OIDCProviderConfig{
				{
					ConfigurationName: "config1",
					IssuerURI:         "https://provider.com",
					Audience:          "audience1",
				},
				{
					ConfigurationName: "config2",
					IssuerURI:         "https://provider.com",
					Audience:          "audience2",
				},
			},
			expectedResult: v1.ValidationSuccess(),
		},
		{
			name:         "MongoDB 8.0 with unique issuer+audience combinations",
			mongoVersion: "8.0.0",
			configs: []OIDCProviderConfig{
				{
					ConfigurationName: "config1",
					IssuerURI:         "https://provider.com",
					Audience:          "audience1",
				},
				{
					ConfigurationName: "config2",
					IssuerURI:         "https://provider.com",
					Audience:          "audience2",
				},
			},
			expectedResult: v1.ValidationSuccess(),
		},
		{
			name:         "MongoDB enterprise version with -ent suffix",
			mongoVersion: "7.0.0-ent",
			configs: []OIDCProviderConfig{
				{
					ConfigurationName: "config1",
					IssuerURI:         "https://provider.com",
					Audience:          "audience1",
				},
				{
					ConfigurationName: "config2",
					IssuerURI:         "https://provider.com",
					Audience:          "audience2",
				},
			},
			expectedResult: v1.ValidationSuccess(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			validationFunc := oidcProviderConfigUniqueIssuerURIValidation(tt.configs)

			dbSpec := DbCommonSpec{
				Version: tt.mongoVersion,
			}

			result := validationFunc(dbSpec)

			if tt.expectedResult.Level == 0 {
				assert.Equal(t, v1.ValidationSuccess().Level, result.Level)
			} else {
				assert.Equal(t, tt.expectedResult.Level, result.Level)
				assert.Equal(t, tt.expectedResult.Msg, result.Msg)
			}
		})
	}
}
