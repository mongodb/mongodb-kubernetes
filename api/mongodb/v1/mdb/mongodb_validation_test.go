package mdb

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	corev1 "k8s.io/api/core/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
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

func TestMongoDB_ProcessValidations_InvalidHorizonAddress(t *testing.T) {
	tests := []struct {
		name           string
		invalidAddress string
	}{
		{
			name:           "Empty address",
			invalidAddress: ":27018",
		},
		{
			name:           "Invalid characters in hostname",
			invalidAddress: "my_db.com:27018",
		},
		{
			name:           "Hostname too long",
			invalidAddress: strings.Repeat("a", 256) + ":27018",
		},
		{
			name:           "Label starts with hyphen",
			invalidAddress: "-mydb.com:27018",
		},
		{
			name:           "Label ends with hyphen",
			invalidAddress: "mydb-.com:27018",
		},
		{
			name:           "Uppercase letters in hostname",
			invalidAddress: "MyDB.com:27018",
		},
		{
			name:           "Consecutive dots",
			invalidAddress: "my..db.com:27018",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			replicaSetHorizons := []MongoDBHorizonConfig{
				{"my-horizon": tt.invalidAddress},
			}
			rs := NewDefaultReplicaSetBuilder().SetSecurityTLSEnabled().SetMembers(1).Build()
			rs.Spec.Connectivity = &MongoDBConnectivity{}
			rs.Spec.Connectivity.ReplicaSetHorizons = replicaSetHorizons
			err := rs.ProcessValidationsOnReconcile(nil)
			assert.Equal(t, "Horizons must have valid domain names", err.Error())
		})
	}
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
	_, err := validator.ValidateCreate(ctx, rs)
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
	_, err := validator.ValidateCreate(ctx, rs)
	assert.Errorf(t, err, "spec.security.authentication.agents.mode must be specified if more than one entry is present in spec.security.authentication.modes")
}

func TestMongoDB_ResourceTypeImmutable(t *testing.T) {
	newRs := NewReplicaSetBuilder().Build()
	oldRs := NewReplicaSetBuilder().setType(ShardedCluster).Build()
	_, err := validator.ValidateUpdate(ctx, oldRs, newRs)
	assert.Errorf(t, err, "'resourceType' cannot be changed once created")
}

func TestMongoDB_NoSimultaneousTLSDisablingAndScaling(t *testing.T) {
	tests := []struct {
		name                 string
		oldTLSEnabled        bool
		oldMembers           int
		newTLSEnabled        bool
		newMembers           int
		expectError          bool
		expectedErrorMessage string
	}{
		{
			name:                 "Simultaneous TLS disabling and scaling is blocked",
			oldTLSEnabled:        true,
			oldMembers:           3,
			newTLSEnabled:        false,
			newMembers:           5,
			expectError:          true,
			expectedErrorMessage: "Cannot disable TLS and change member count simultaneously. Please apply these changes separately.",
		},
		{
			name:          "TLS disabling without scaling is allowed",
			oldTLSEnabled: true,
			oldMembers:    3,
			newTLSEnabled: false,
			newMembers:    3,
			expectError:   false,
		},
		{
			name:          "Scaling without TLS changes is allowed",
			oldTLSEnabled: true,
			oldMembers:    3,
			newTLSEnabled: true,
			newMembers:    5,
			expectError:   false,
		},
		{
			name:          "TLS enabling with scaling is allowed",
			oldTLSEnabled: false,
			oldMembers:    3,
			newTLSEnabled: true,
			newMembers:    5,
			expectError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Build old ReplicaSet
			oldBuilder := NewReplicaSetBuilder()
			if tt.oldTLSEnabled {
				oldBuilder = oldBuilder.SetSecurityTLSEnabled()
			}
			oldRs := oldBuilder.Build()
			oldRs.Spec.CloudManagerConfig = &PrivateCloudConfig{
				ConfigMapRef: ConfigMapRef{Name: "cloud-manager"},
			}
			oldRs.Spec.Members = tt.oldMembers

			// Build new ReplicaSet
			newBuilder := NewReplicaSetBuilder()
			if tt.newTLSEnabled {
				newBuilder = newBuilder.SetSecurityTLSEnabled()
			}
			newRs := newBuilder.Build()
			newRs.Spec.CloudManagerConfig = &PrivateCloudConfig{
				ConfigMapRef: ConfigMapRef{Name: "cloud-manager"},
			}
			newRs.Spec.Members = tt.newMembers

			// Validate
			_, err := validator.ValidateUpdate(ctx, oldRs, newRs)

			if tt.expectError {
				require.Error(t, err)
				assert.Equal(t, tt.expectedErrorMessage, err.Error())
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestSpecProjectOnlyOneValue(t *testing.T) {
	rs := NewReplicaSetBuilder().Build()
	rs.Spec.CloudManagerConfig = &PrivateCloudConfig{
		ConfigMapRef: ConfigMapRef{Name: "cloud-manager"},
	}
	_, err := validator.ValidateCreate(ctx, rs)
	assert.NoError(t, err)
}

func TestMongoDB_ProcessValidations(t *testing.T) {
	rs := NewReplicaSetBuilder().Build()
	assert.Error(t, rs.ProcessValidationsOnReconcile(nil), nil)
}

func TestMongoDB_ValidateAdditionalMongodConfig(t *testing.T) {
	t.Run("No sharded cluster additional config for replica set", func(t *testing.T) {
		rs := NewReplicaSetBuilder().SetConfigSrvAdditionalConfig(NewAdditionalMongodConfig("systemLog.verbosity", 5)).Build()
		_, err := validator.ValidateCreate(ctx, rs)
		require.Error(t, err)
		assert.Equal(t, "'spec.mongos', 'spec.configSrv', 'spec.shard' cannot be specified if type of MongoDB is ReplicaSet", err.Error())
	})
	t.Run("No sharded cluster additional config for standalone", func(t *testing.T) {
		rs := NewStandaloneBuilder().SetMongosAdditionalConfig(NewAdditionalMongodConfig("systemLog.verbosity", 5)).Build()
		_, err := validator.ValidateCreate(ctx, rs)
		require.Error(t, err)
		assert.Equal(t, "'spec.mongos', 'spec.configSrv', 'spec.shard' cannot be specified if type of MongoDB is Standalone", err.Error())
	})
	t.Run("No replica set additional config for sharded cluster", func(t *testing.T) {
		rs := NewClusterBuilder().SetAdditionalConfig(NewAdditionalMongodConfig("systemLog.verbosity", 5)).Build()
		_, err := validator.ValidateCreate(ctx, rs)
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
			expectedErrorMessage: "invalid feature compatibility version \"test\", possible values are: 'AlwaysMatchVersion' or 'major.minor'",
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
			expectedErrorMessage: "invalid feature compatibility version \"4.0.0\", possible values are: 'AlwaysMatchVersion' or 'major.minor'",
		},
		{
			name:                 "Invalid FCV - not major leading 0",
			fcv:                  ptr.To("4.01"),
			expectError:          true,
			expectedErrorMessage: "invalid feature compatibility version \"4.01\": Minor number must not contain leading zeroes \"01\"",
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
						ClientId:            ptr.To("clientId1"),
					},
					{
						ConfigurationName:   "provider",
						IssuerURI:           "https://example2.com",
						AuthorizationMethod: OIDCAuthorizationMethodWorkforceIdentityFederation,
						ClientId:            ptr.To("clientId2"),
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
						ClientId:            ptr.To("clientId1"),
					},
					{
						ConfigurationName:   "test-provider2",
						IssuerURI:           "https://example2.com",
						AuthorizationMethod: OIDCAuthorizationMethodWorkforceIdentityFederation,
						ClientId:            ptr.To("clientId2"),
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
						ClientId:            ptr.To("clientId1"),
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
						ClientId:            ptr.To("clientId"),
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
						GroupsClaim:       ptr.To("groups"),
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
						GroupsClaim:       ptr.To("groups"),
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
						GroupsClaim:       ptr.To("groups"),
					},
					{
						ConfigurationName: "test-provider2",
						IssuerURI:         "https://example.com",
						AuthorizationType: OIDCAuthorizationTypeGroupMembership,
						GroupsClaim:       ptr.To("groups"),
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
			name:         "MongoDB 7.0.11 with duplicate issuer URIs - error",
			mongoVersion: "7.0.11",
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
			name:         "MongoDB 8.0 with duplicate issuer+audience combinations - warning",
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
					Audience:          "audience1",
				},
			},
			expectedResult: v1.ValidationWarning("OIDC provider configs %q and %q have duplicate IssuerURI and Audience combination",
				"config1", "config2"),
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
			mongoVersion: "7.0.11-ent",
			configs: []OIDCProviderConfig{
				{
					ConfigurationName: "config1",
					IssuerURI:         "https://provider-1.com",
					Audience:          "audience1",
				},
				{
					ConfigurationName: "config2",
					IssuerURI:         "https://provider-2.com",
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

			assert.Equal(t, tt.expectedResult, result)
		})
	}
}

func TestMongoDB_MonarchConfigRequired(t *testing.T) {
	tests := []struct {
		name        string
		monarch     *MonarchSpec
		expectError bool
		errorMsg    string
	}{
		{
			name:        "No monarch spec",
			monarch:     nil,
			expectError: false,
		},
		{
			name: "Missing image",
			monarch: &MonarchSpec{
				Role: MonarchRoleActive,
				S3: MonarchS3Config{
					Bucket:               "bucket",
					Region:               "us-east-1",
					CredentialsSecretRef: corev1.LocalObjectReference{Name: "creds"},
				},
			},
			expectError: true,
			errorMsg:    "spec.monarch.image is required",
		},
		{
			name: "Missing bucket",
			monarch: &MonarchSpec{
				Role:  MonarchRoleActive,
				Image: "quay.io/mongodb/monarch:0.1.1",
				S3: MonarchS3Config{
					Region: "us-east-1",
				},
			},
			expectError: true,
			errorMsg:    "spec.monarch.s3.bucket is required",
		},
		{
			name: "Missing region",
			monarch: &MonarchSpec{
				Role:  MonarchRoleActive,
				Image: "quay.io/mongodb/monarch:0.1.1",
				S3: MonarchS3Config{
					Bucket: "bucket",
				},
			},
			expectError: true,
			errorMsg:    "spec.monarch.s3.region is required",
		},
		{
			name: "Valid active config",
			monarch: &MonarchSpec{
				Role:  MonarchRoleActive,
				Image: "quay.io/mongodb/monarch:0.1.1",
				S3: MonarchS3Config{
					Bucket:               "bucket",
					Region:               "us-east-1",
					CredentialsSecretRef: corev1.LocalObjectReference{Name: "creds"},
				},
			},
			expectError: false,
		},
		{
			name: "Valid standby config",
			monarch: &MonarchSpec{
				Role:  MonarchRoleStandby,
				Image: "quay.io/mongodb/monarch:0.1.1",
				S3: MonarchS3Config{
					Bucket:               "bucket",
					Region:               "us-east-1",
					CredentialsSecretRef: corev1.LocalObjectReference{Name: "creds"},
				},
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := MongoDbSpec{Monarch: tt.monarch}
			result := monarchConfigRequired(spec)
			if tt.expectError {
				assert.Equal(t, v1.ErrorLevel, result.Level)
				assert.Equal(t, tt.errorMsg, result.Msg)
			} else {
				assert.Equal(t, v1.ValidationSuccess(), result)
			}
		})
	}
}

func TestMongoDB_MonarchRoleNoDemotion(t *testing.T) {
	tests := []struct {
		name        string
		oldRole     MonarchRole
		newRole     MonarchRole
		oldMonarch  bool // false means oldObj.Monarch is nil
		newMonarch  bool // false means newObj.Monarch is nil
		expectError bool
	}{
		{name: "active → standby is rejected", oldRole: MonarchRoleActive, newRole: MonarchRoleStandby, oldMonarch: true, newMonarch: true, expectError: true},
		{name: "standby → active is allowed (failover)", oldRole: MonarchRoleStandby, newRole: MonarchRoleActive, oldMonarch: true, newMonarch: true, expectError: false},
		{name: "active → active no-op is allowed", oldRole: MonarchRoleActive, newRole: MonarchRoleActive, oldMonarch: true, newMonarch: true, expectError: false},
		{name: "standby → standby no-op is allowed", oldRole: MonarchRoleStandby, newRole: MonarchRoleStandby, oldMonarch: true, newMonarch: true, expectError: false},
		{name: "no Monarch on old (initial creation) is allowed", oldRole: "", newRole: MonarchRoleStandby, oldMonarch: false, newMonarch: true, expectError: false},
		{name: "Monarch removed from new is allowed", oldRole: MonarchRoleActive, newRole: "", oldMonarch: true, newMonarch: false, expectError: false},
	}

	build := func(role MonarchRole, present bool) MongoDbSpec {
		if !present {
			return MongoDbSpec{}
		}
		return MongoDbSpec{Monarch: &MonarchSpec{Role: role, Image: "img", S3: MonarchS3Config{Bucket: "b", Region: "r", CredentialsSecretRef: corev1.LocalObjectReference{Name: "s"}}}}
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldSpec := build(tt.oldRole, tt.oldMonarch)
			newSpec := build(tt.newRole, tt.newMonarch)
			result := monarchRoleNoDemotion(newSpec, oldSpec)
			if tt.expectError {
				assert.Equal(t, v1.ErrorLevel, result.Level)
				assert.Contains(t, result.Msg, "active")
				assert.Contains(t, result.Msg, "standby")
			} else {
				assert.Equal(t, v1.ValidationSuccess(), result)
			}
		})
	}
}

func TestMonarchRequiresMinMongoDBVersion(t *testing.T) {
	tests := []struct {
		name        string
		monarch     bool
		version     string
		expectError bool
	}{
		{name: "no monarch — any version ok", monarch: false, version: "7.0.0", expectError: false},
		{name: "no monarch — empty version ok", monarch: false, version: "", expectError: false},
		{name: "8.0.16 ok", monarch: true, version: "8.0.16", expectError: false},
		{name: "8.0.20 ok", monarch: true, version: "8.0.20", expectError: false},
		{name: "9.0.0 ok", monarch: true, version: "9.0.0", expectError: false},
		{name: "8.0.15 rejected", monarch: true, version: "8.0.15", expectError: true},
		{name: "7.0.0 rejected", monarch: true, version: "7.0.0", expectError: true},
		{name: "empty version with monarch rejected", monarch: true, version: "", expectError: true},
		{name: "non-semver rejected", monarch: true, version: "not-a-version", expectError: true},
	}

	build := func(version string, monarch bool) MongoDbSpec {
		spec := MongoDbSpec{DbCommonSpec: DbCommonSpec{Version: version}}
		if monarch {
			spec.Monarch = &MonarchSpec{Role: MonarchRoleStandby, Image: "img", S3: MonarchS3Config{Bucket: "b", Region: "r", CredentialsSecretRef: corev1.LocalObjectReference{Name: "s"}}}
		}
		return spec
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := monarchRequiresMinMongoDBVersion(build(tt.version, tt.monarch))
			if tt.expectError {
				assert.Equal(t, v1.ErrorLevel, result.Level, "expected error, got: %+v", result)
			} else {
				assert.Equal(t, v1.ValidationSuccess(), result)
			}
		})
	}
}

func TestMonarchRequiresScramAuth(t *testing.T) {
	tests := []struct {
		name        string
		monarch     bool
		authEnabled bool
		modes       []AuthMode
		expectError bool
	}{
		{name: "no monarch — any auth ok", monarch: false, authEnabled: true, modes: []AuthMode{"X509"}, expectError: false},
		{name: "monarch + auth disabled ok", monarch: true, authEnabled: false, modes: []AuthMode{"X509"}, expectError: false},
		{name: "monarch + no security ok", monarch: true, authEnabled: false, modes: nil, expectError: false},
		{name: "monarch + SCRAM-SHA-256 ok", monarch: true, authEnabled: true, modes: []AuthMode{"SCRAM-SHA-256"}, expectError: false},
		{name: "monarch + SCRAM ok", monarch: true, authEnabled: true, modes: []AuthMode{"SCRAM"}, expectError: false},
		{name: "monarch + SCRAM-SHA-1 ok", monarch: true, authEnabled: true, modes: []AuthMode{"SCRAM-SHA-1"}, expectError: false},
		{name: "monarch + X509 rejected", monarch: true, authEnabled: true, modes: []AuthMode{"X509"}, expectError: true},
		{name: "monarch + LDAP rejected", monarch: true, authEnabled: true, modes: []AuthMode{"LDAP"}, expectError: true},
		{name: "monarch + OIDC rejected", monarch: true, authEnabled: true, modes: []AuthMode{"OIDC"}, expectError: true},
		{name: "monarch + SCRAM+X509 rejected", monarch: true, authEnabled: true, modes: []AuthMode{"SCRAM-SHA-256", "X509"}, expectError: true},
	}

	build := func(modes []AuthMode, monarch, authEnabled bool) MongoDbSpec {
		spec := MongoDbSpec{
			DbCommonSpec: DbCommonSpec{
				Security: &Security{Authentication: &Authentication{Enabled: authEnabled, Modes: modes}},
			},
		}
		if monarch {
			spec.Monarch = &MonarchSpec{Role: MonarchRoleStandby, Image: "img", S3: MonarchS3Config{Bucket: "b", Region: "r", CredentialsSecretRef: corev1.LocalObjectReference{Name: "s"}}}
		}
		return spec
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := monarchRequiresScramAuth(build(tt.modes, tt.monarch, tt.authEnabled))
			if tt.expectError {
				assert.Equal(t, v1.ErrorLevel, result.Level, "expected error, got: %+v", result)
			} else {
				assert.Equal(t, v1.ValidationSuccess(), result)
			}
		})
	}
}
