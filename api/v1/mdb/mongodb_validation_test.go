package mdb

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	v1 "github.com/mongodb/mongodb-kubernetes/api/v1"
	"github.com/mongodb/mongodb-kubernetes/api/v1/status"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1/common"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/automationconfig"
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

func TestMongoDB_ProcessValidationsOnReconcile_AgentAutoPEMKeyFilePath(t *testing.T) {
	clientCertAgents := AgentAuthentication{
		Mode: "X509",
		ClientCertificateSecretRefWrap: common.ClientCertificateSecretRefWrapper{
			ClientCertificateSecretRef: corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "agent-tls"},
				Key:                  corev1.TLSCertKey,
			},
		},
	}

	t.Run("requires clientCertificateSecretRef", func(t *testing.T) {
		rs := NewReplicaSetBuilder().AddDummyOpsManagerConfig().Build()
		rs.Spec.Security.TLSConfig = &TLSConfig{Enabled: true}
		rs.Spec.Security.Authentication = &Authentication{
			Enabled: true,
			Modes:   []AuthMode{"X509"},
			Agents: AgentAuthentication{
				Mode:               "X509",
				AutoPEMKeyFilePath: "/var/lib/mongodb-mms-automation/certs/agent.pem",
			},
		}
		err := rs.ProcessValidationsOnReconcile(nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "clientCertificateSecretRef")
	})

	t.Run("rejects non-absolute path", func(t *testing.T) {
		rs := NewReplicaSetBuilder().AddDummyOpsManagerConfig().Build()
		rs.Spec.Security.TLSConfig = &TLSConfig{Enabled: true}
		a := clientCertAgents
		a.AutoPEMKeyFilePath = "relative/pem.pem"
		rs.Spec.Security.Authentication = &Authentication{Enabled: true, Modes: []AuthMode{"X509"}, Agents: a}
		err := rs.ProcessValidationsOnReconcile(nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "absolute path")
	})

	t.Run("rejects dot-dot in path", func(t *testing.T) {
		rs := NewReplicaSetBuilder().AddDummyOpsManagerConfig().Build()
		rs.Spec.Security.TLSConfig = &TLSConfig{Enabled: true}
		a := clientCertAgents
		a.AutoPEMKeyFilePath = "/safe/../etc/passwd"
		rs.Spec.Security.Authentication = &Authentication{Enabled: true, Modes: []AuthMode{"X509"}, Agents: a}
		err := rs.ProcessValidationsOnReconcile(nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "..")
	})

	t.Run("accepts absolute path with client cert ref", func(t *testing.T) {
		rs := NewReplicaSetBuilder().AddDummyOpsManagerConfig().Build()
		rs.Spec.Security.TLSConfig = &TLSConfig{Enabled: true}
		a := clientCertAgents
		a.AutoPEMKeyFilePath = "/var/lib/mongodb-mms-automation/certs/agent.pem"
		rs.Spec.Security.Authentication = &Authentication{Enabled: true, Modes: []AuthMode{"X509"}, Agents: a}
		err := rs.ProcessValidationsOnReconcile(nil)
		assert.NoError(t, err)
	})
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

func TestMongoDB_DownloadBaseImmutable(t *testing.T) {
	t.Run("Changing downloadBase is rejected", func(t *testing.T) {
		oldRs := NewReplicaSetBuilder().AddDummyOpsManagerConfig().Build()
		oldRs.Spec.DownloadBase = "/var/lib/mongodb-mms-automation"
		newRs := NewReplicaSetBuilder().AddDummyOpsManagerConfig().Build()
		newRs.Spec.DownloadBase = "/data/mongodb-mms-automation"
		_, err := validator.ValidateUpdate(ctx, oldRs, newRs)
		assert.EqualError(t, err, "'spec.downloadBase' cannot be changed once created")
	})

	t.Run("Keeping downloadBase unchanged is allowed", func(t *testing.T) {
		oldRs := NewReplicaSetBuilder().AddDummyOpsManagerConfig().Build()
		oldRs.Spec.DownloadBase = "/var/lib/mongodb-mms-automation"
		newRs := NewReplicaSetBuilder().AddDummyOpsManagerConfig().Build()
		newRs.Spec.DownloadBase = "/var/lib/mongodb-mms-automation"
		_, err := validator.ValidateUpdate(ctx, oldRs, newRs)
		assert.NoError(t, err)
	})
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

func TestCountMemberConfigChangesForExistingMembers(t *testing.T) {
	votes0 := 0
	votes1 := 1
	prio0 := "0"
	prio1 := "1"

	tests := []struct {
		name            string
		oldConf         []automationconfig.MemberOptions
		newConf         []automationconfig.MemberOptions
		existingMembers int
		want            int
	}{
		{
			name:            "both nil — no change",
			existingMembers: 3,
			want:            0,
		},
		{
			name:            "identical non-nil — no change",
			oldConf:         []automationconfig.MemberOptions{{Votes: &votes1, Priority: &prio1}, {Votes: &votes1, Priority: &prio1}},
			newConf:         []automationconfig.MemberOptions{{Votes: &votes1, Priority: &prio1}, {Votes: &votes1, Priority: &prio1}},
			existingMembers: 2,
			want:            0,
		},
		{
			name:            "nil votes same as explicit 1 — no change",
			oldConf:         []automationconfig.MemberOptions{{Votes: nil}},
			newConf:         []automationconfig.MemberOptions{{Votes: &votes1}},
			existingMembers: 1,
			want:            0,
		},
		{
			name:            "nil priority same as explicit 1 — no change",
			oldConf:         []automationconfig.MemberOptions{{Priority: nil}},
			newConf:         []automationconfig.MemberOptions{{Priority: &prio1}},
			existingMembers: 1,
			want:            0,
		},
		{
			name:            "one votes change",
			oldConf:         []automationconfig.MemberOptions{{Votes: &votes1}, {Votes: &votes1}},
			newConf:         []automationconfig.MemberOptions{{Votes: &votes0}, {Votes: &votes1}},
			existingMembers: 2,
			want:            1,
		},
		{
			name:            "one priority change",
			oldConf:         []automationconfig.MemberOptions{{Priority: &prio1}, {Priority: &prio1}},
			newConf:         []automationconfig.MemberOptions{{Priority: &prio0}, {Priority: &prio1}},
			existingMembers: 2,
			want:            1,
		},
		{
			name:            "two changes",
			oldConf:         []automationconfig.MemberOptions{{Votes: &votes1}, {Votes: &votes1}},
			newConf:         []automationconfig.MemberOptions{{Votes: &votes0}, {Votes: &votes0}},
			existingMembers: 2,
			want:            2,
		},
		{
			name:            "one member with both votes and priority changed — counts as 1",
			oldConf:         []automationconfig.MemberOptions{{Votes: &votes1, Priority: &prio1}},
			newConf:         []automationconfig.MemberOptions{{Votes: &votes0, Priority: &prio0}},
			existingMembers: 1,
			want:            1,
		},
		{
			name:            "new member appended — not counted as a change",
			oldConf:         []automationconfig.MemberOptions{{Votes: &votes1}},
			newConf:         []automationconfig.MemberOptions{{Votes: &votes1}, {Votes: &votes0, Priority: &prio0}},
			existingMembers: 1, // only 1 pre-existing k8s member
			want:            0,
		},
		{
			name:            "old entry removed — counted as change (back to default)",
			oldConf:         []automationconfig.MemberOptions{{Votes: &votes0}},
			newConf:         []automationconfig.MemberOptions{},
			existingMembers: 1,
			want:            1, // was 0 votes, now implicitly default (1)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := countMemberConfigChangesForExistingMembers(tt.newConf, tt.oldConf, tt.existingMembers)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestAtMostOneMigrationChangeAtATime(t *testing.T) {
	baseExternalMembers := []ExternalMember{
		{ProcessName: "vm-rs-0", Hostname: "vm0.example.com:27017", Type: "mongod"},
		{ProcessName: "vm-rs-1", Hostname: "vm1.example.com:27017", Type: "mongod"},
		{ProcessName: "vm-rs-2", Hostname: "vm2.example.com:27017", Type: "mongod"},
	}

	votes0 := 0
	votes1 := 1
	prio0 := "0"
	prio1 := "1"

	tests := []struct {
		name        string
		oldSpec     MongoDbSpec
		newSpec     MongoDbSpec
		expectError bool
		errorMsg    string
	}{
		{
			name:        "no external members — validator skipped",
			oldSpec:     MongoDbSpec{Members: 3},
			newSpec:     MongoDbSpec{Members: 5},
			expectError: false,
		},
		{
			name:        "no change — allowed",
			oldSpec:     MongoDbSpec{Members: 3, ExternalMembers: baseExternalMembers},
			newSpec:     MongoDbSpec{Members: 3, ExternalMembers: baseExternalMembers},
			expectError: false,
		},
		{
			name:        "adding one k8s member — allowed",
			oldSpec:     MongoDbSpec{Members: 1, ExternalMembers: baseExternalMembers},
			newSpec:     MongoDbSpec{Members: 2, ExternalMembers: baseExternalMembers},
			expectError: false,
		},
		{
			name:        "removing one VM member — allowed",
			oldSpec:     MongoDbSpec{Members: 3, ExternalMembers: baseExternalMembers},
			newSpec:     MongoDbSpec{Members: 3, ExternalMembers: baseExternalMembers[1:]},
			expectError: false,
		},
		{
			name: "updating one k8s member votes — allowed",
			oldSpec: MongoDbSpec{
				Members:         2,
				ExternalMembers: baseExternalMembers,
				MemberConfig:    []automationconfig.MemberOptions{{Votes: &votes1, Priority: &prio1}, {Votes: &votes1, Priority: &prio1}},
			},
			newSpec: MongoDbSpec{
				Members:         2,
				ExternalMembers: baseExternalMembers,
				MemberConfig:    []automationconfig.MemberOptions{{Votes: &votes0, Priority: &prio0}, {Votes: &votes1, Priority: &prio1}},
			},
			expectError: false,
		},
		{
			name:        "adding two k8s members — allowed",
			oldSpec:     MongoDbSpec{Members: 1, ExternalMembers: baseExternalMembers},
			newSpec:     MongoDbSpec{Members: 3, ExternalMembers: baseExternalMembers},
			expectError: false,
		},
		{
			name:        "removing two VM members — allowed",
			oldSpec:     MongoDbSpec{Members: 3, ExternalMembers: baseExternalMembers},
			newSpec:     MongoDbSpec{Members: 3, ExternalMembers: baseExternalMembers[2:]},
			expectError: false,
		},
		{
			name: "updating two k8s member configs — allowed",
			oldSpec: MongoDbSpec{
				Members:         2,
				ExternalMembers: baseExternalMembers,
				MemberConfig:    []automationconfig.MemberOptions{{Votes: &votes1, Priority: &prio1}, {Votes: &votes1, Priority: &prio1}},
			},
			newSpec: MongoDbSpec{
				Members:         2,
				ExternalMembers: baseExternalMembers,
				MemberConfig:    []automationconfig.MemberOptions{{Votes: &votes0, Priority: &prio0}, {Votes: &votes0, Priority: &prio0}},
			},
			expectError: false,
		},
		{
			name:        "adding k8s member AND removing VM member — rejected",
			oldSpec:     MongoDbSpec{Members: 1, ExternalMembers: baseExternalMembers},
			newSpec:     MongoDbSpec{Members: 2, ExternalMembers: baseExternalMembers[1:]},
			expectError: true,
			errorMsg:    "only one migration change type is allowed per update",
		},
		{
			name: "adding k8s member AND updating member config — rejected",
			oldSpec: MongoDbSpec{
				Members:         1,
				ExternalMembers: baseExternalMembers,
				MemberConfig:    []automationconfig.MemberOptions{{Votes: &votes1, Priority: &prio1}},
			},
			newSpec: MongoDbSpec{
				Members:         2,
				ExternalMembers: baseExternalMembers,
				MemberConfig:    []automationconfig.MemberOptions{{Votes: &votes0, Priority: &prio0}, {Votes: &votes0, Priority: &prio0}},
			},
			expectError: true,
			errorMsg:    "only one migration change type is allowed per update",
		},
		{
			name: "removing VM member AND updating member config — rejected",
			oldSpec: MongoDbSpec{
				Members:         2,
				ExternalMembers: baseExternalMembers,
				MemberConfig:    []automationconfig.MemberOptions{{Votes: &votes1, Priority: &prio1}, {Votes: &votes1, Priority: &prio1}},
			},
			newSpec: MongoDbSpec{
				Members:         2,
				ExternalMembers: baseExternalMembers[1:],
				MemberConfig:    []automationconfig.MemberOptions{{Votes: &votes0, Priority: &prio0}, {Votes: &votes1, Priority: &prio1}},
			},
			expectError: true,
			errorMsg:    "only one migration change type is allowed per update",
		},
		{
			name: "adding k8s member with matching new MemberConfig entry — allowed (new entry not counted)",
			oldSpec: MongoDbSpec{
				Members:         1,
				ExternalMembers: baseExternalMembers,
				MemberConfig:    []automationconfig.MemberOptions{{Votes: &votes1, Priority: &prio1}},
			},
			newSpec: MongoDbSpec{
				Members:         2,
				ExternalMembers: baseExternalMembers,
				// Existing entry unchanged; new entry appended for the new pod
				MemberConfig: []automationconfig.MemberOptions{{Votes: &votes1, Priority: &prio1}, {Votes: &votes0, Priority: &prio0}},
			},
			expectError: false,
		},
		{
			name:        "removing k8s members during migration — rejected",
			oldSpec:     MongoDbSpec{Members: 3, ExternalMembers: baseExternalMembers},
			newSpec:     MongoDbSpec{Members: 2, ExternalMembers: baseExternalMembers},
			expectError: true,
			errorMsg:    "Kubernetes members may not be removed during migration",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := atMostOneMigrationChangeAtATime(tt.newSpec, tt.oldSpec)
			if tt.expectError {
				assert.Equal(t, v1.ErrorLevel, result.Level)
				assert.Contains(t, result.Msg, tt.errorMsg)
			} else {
				assert.Equal(t, v1.ValidationSuccess(), result)
			}
		})
	}
}

func TestAtMostOneMigrationChangeAtATime_WiredIntoWebhook(t *testing.T) {
	externalMembers := []ExternalMember{
		{ProcessName: "vm-rs-0", Hostname: "vm0.example.com:27017", Type: "mongod"},
		{ProcessName: "vm-rs-1", Hostname: "vm1.example.com:27017", Type: "mongod"},
		{ProcessName: "vm-rs-2", Hostname: "vm2.example.com:27017", Type: "mongod"},
	}

	votes0 := 0
	prio0 := "0"

	oldRs := NewReplicaSetBuilder().AddDummyOpsManagerConfig().SetMembers(1).Build()
	oldRs.Spec.ExternalMembers = externalMembers
	votes1 := 1
	prio1 := "1"
	oldRs.Spec.MemberConfig = []automationconfig.MemberOptions{{Votes: &votes1, Priority: &prio1}}

	// Simultaneously add a k8s member AND change member config, two types at once
	newRs := NewReplicaSetBuilder().AddDummyOpsManagerConfig().SetMembers(2).Build()
	newRs.Spec.ExternalMembers = externalMembers
	newRs.Spec.MemberConfig = []automationconfig.MemberOptions{
		{Votes: &votes0, Priority: &prio0},
		{Votes: &votes0, Priority: &prio0},
	}

	_, err := validator.ValidateUpdate(ctx, oldRs, newRs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only one migration change type is allowed per update")
}

func TestNoExternalMembersAdditionOrChanges(t *testing.T) {
	memberA := ExternalMember{ProcessName: "vm-rs-0", Hostname: "vm0.example.com:27017", Type: "mongod"}
	memberB := ExternalMember{ProcessName: "vm-rs-1", Hostname: "vm1.example.com:27017", Type: "mongod"}
	memberC := ExternalMember{ProcessName: "vm-rs-2", Hostname: "vm2.example.com:27017", Type: "mongod"}

	tests := []struct {
		name        string
		oldSpec     MongoDbSpec
		newSpec     MongoDbSpec
		expectError bool
		errorMsg    string
	}{
		// --- success cases ---
		{
			name:        "both empty — success",
			oldSpec:     MongoDbSpec{},
			newSpec:     MongoDbSpec{},
			expectError: false,
		},
		{
			name:        "members unchanged — success",
			oldSpec:     MongoDbSpec{ExternalMembers: []ExternalMember{memberA, memberB, memberC}},
			newSpec:     MongoDbSpec{ExternalMembers: []ExternalMember{memberA, memberB, memberC}},
			expectError: false,
		},
		{
			name:        "one member removed — success",
			oldSpec:     MongoDbSpec{ExternalMembers: []ExternalMember{memberA, memberB, memberC}},
			newSpec:     MongoDbSpec{ExternalMembers: []ExternalMember{memberA, memberB}},
			expectError: false,
		},
		{
			name:        "all members removed — success",
			oldSpec:     MongoDbSpec{ExternalMembers: []ExternalMember{memberA, memberB}},
			newSpec:     MongoDbSpec{ExternalMembers: []ExternalMember{}},
			expectError: false,
		},
		// --- addition errors ---
		{
			name:        "member added to previously empty list — rejected",
			oldSpec:     MongoDbSpec{},
			newSpec:     MongoDbSpec{ExternalMembers: []ExternalMember{memberA}},
			expectError: true,
			errorMsg:    "Cannot add external members to an existing MongoDB resource",
		},
		{
			name:        "member added to existing list — rejected",
			oldSpec:     MongoDbSpec{ExternalMembers: []ExternalMember{memberA, memberB}},
			newSpec:     MongoDbSpec{ExternalMembers: []ExternalMember{memberA, memberB, memberC}},
			expectError: true,
			errorMsg:    "Cannot add external members to an existing MongoDB resource",
		},
		// --- modification errors ---
		{
			name:    "hostname changed — rejected",
			oldSpec: MongoDbSpec{ExternalMembers: []ExternalMember{memberA}},
			newSpec: MongoDbSpec{ExternalMembers: []ExternalMember{
				{ProcessName: "vm-rs-0", Hostname: "new-host.example.com:27017", Type: "mongod"},
			}},
			expectError: true,
			errorMsg:    "Cannot make changes to existing external members",
		},
		{
			name:    "type changed — rejected",
			oldSpec: MongoDbSpec{ExternalMembers: []ExternalMember{memberA}},
			newSpec: MongoDbSpec{ExternalMembers: []ExternalMember{
				{ProcessName: "vm-rs-0", Hostname: "vm0.example.com:27017", Type: "mongos"},
			}},
			expectError: true,
			errorMsg:    "Cannot make changes to existing external members",
		},
		{
			name:    "replicaSetName added — rejected",
			oldSpec: MongoDbSpec{ExternalMembers: []ExternalMember{memberA}},
			newSpec: MongoDbSpec{ExternalMembers: []ExternalMember{
				{ProcessName: "vm-rs-0", Hostname: "vm0.example.com:27017", Type: "mongod", ReplicaSetName: "my-rs"},
			}},
			expectError: true,
			errorMsg:    "Cannot make changes to existing external members",
		},
		{
			name:    "processName changed — treated as new member, rejected",
			oldSpec: MongoDbSpec{ExternalMembers: []ExternalMember{memberA}},
			newSpec: MongoDbSpec{ExternalMembers: []ExternalMember{
				{ProcessName: "vm-rs-0-renamed", Hostname: "vm0.example.com:27017", Type: "mongod"},
			}},
			expectError: true,
			errorMsg:    "Cannot add external members to an existing MongoDB resource",
		},
		{
			name:    "one unchanged, one changed — rejected for changed member",
			oldSpec: MongoDbSpec{ExternalMembers: []ExternalMember{memberA, memberB}},
			newSpec: MongoDbSpec{ExternalMembers: []ExternalMember{
				memberA,
				{ProcessName: "vm-rs-1", Hostname: "new-host.example.com:27017", Type: "mongod"},
			}},
			expectError: true,
			errorMsg:    "Cannot make changes to existing external members",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := noExternalMembersAdditionOrChanges(tt.newSpec, tt.oldSpec)
			if tt.expectError {
				assert.Equal(t, v1.ErrorLevel, result.Level)
				assert.Contains(t, result.Msg, tt.errorMsg)
			} else {
				assert.Equal(t, v1.ValidationSuccess(), result)
			}
		})
	}
}

func TestNoExternalMembersAdditionOrChanges_WiredIntoWebhook(t *testing.T) {
	externalMember := ExternalMember{
		ProcessName: "vm-rs-0",
		Hostname:    "vm0.example.com:27017",
		Type:        "mongod",
	}

	oldRs := NewReplicaSetBuilder().AddDummyOpsManagerConfig().SetMembers(3).Build()
	oldRs.Spec.ExternalMembers = []ExternalMember{externalMember}

	// Attempt to modify an existing external member's hostname
	newRs := NewReplicaSetBuilder().AddDummyOpsManagerConfig().SetMembers(3).Build()
	newRs.Spec.ExternalMembers = []ExternalMember{
		{ProcessName: "vm-rs-0", Hostname: "changed-host.example.com:27017", Type: "mongod"},
	}

	_, err := validator.ValidateUpdate(ctx, oldRs, newRs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Cannot make changes to existing external members")
}

func TestNoExternalMembersAdditionOrChanges_ShardedCluster(t *testing.T) {
	// Sharded cluster external members include both mongod entries (with ReplicaSetName) and mongos entries.
	mongodShard0A := ExternalMember{ProcessName: "shard0-0", Hostname: "vm-shard0-0.example.com:27017", Type: "mongod", ReplicaSetName: "myrs-0"}
	mongodShard0B := ExternalMember{ProcessName: "shard0-1", Hostname: "vm-shard0-1.example.com:27017", Type: "mongod", ReplicaSetName: "myrs-0"}
	mongosA := ExternalMember{ProcessName: "mongos-0", Hostname: "vm-mongos-0.example.com:27017", Type: "mongos"}
	mongosB := ExternalMember{ProcessName: "mongos-1", Hostname: "vm-mongos-1.example.com:27017", Type: "mongos"}

	baseExternalMembers := []ExternalMember{mongodShard0A, mongodShard0B, mongosA, mongosB}

	tests := []struct {
		name        string
		oldSpec     MongoDbSpec
		newSpec     MongoDbSpec
		expectError bool
		errorMsg    string
	}{
		{
			name:        "same entries unchanged — success",
			oldSpec:     MongoDbSpec{ExternalMembers: baseExternalMembers},
			newSpec:     MongoDbSpec{ExternalMembers: baseExternalMembers},
			expectError: false,
		},
		{
			name:        "one mongod entry removed — success",
			oldSpec:     MongoDbSpec{ExternalMembers: baseExternalMembers},
			newSpec:     MongoDbSpec{ExternalMembers: []ExternalMember{mongodShard0A, mongosA, mongosB}},
			expectError: false,
		},
		{
			name:        "one mongos entry removed — success",
			oldSpec:     MongoDbSpec{ExternalMembers: baseExternalMembers},
			newSpec:     MongoDbSpec{ExternalMembers: []ExternalMember{mongodShard0A, mongodShard0B, mongosA}},
			expectError: false,
		},
		{
			name:    "new mongod entry added — rejected",
			oldSpec: MongoDbSpec{ExternalMembers: baseExternalMembers},
			newSpec: MongoDbSpec{ExternalMembers: append(baseExternalMembers,
				ExternalMember{ProcessName: "shard0-2", Hostname: "vm-shard0-2.example.com:27017", Type: "mongod", ReplicaSetName: "myrs-0"},
			)},
			expectError: true,
			errorMsg:    "Cannot add external members to an existing MongoDB resource",
		},
		{
			name:    "new mongos entry added — rejected",
			oldSpec: MongoDbSpec{ExternalMembers: baseExternalMembers},
			newSpec: MongoDbSpec{ExternalMembers: append(baseExternalMembers,
				ExternalMember{ProcessName: "mongos-2", Hostname: "vm-mongos-2.example.com:27017", Type: "mongos"},
			)},
			expectError: true,
			errorMsg:    "Cannot add external members to an existing MongoDB resource",
		},
		{
			name:    "hostname of existing mongod entry changed — rejected",
			oldSpec: MongoDbSpec{ExternalMembers: baseExternalMembers},
			newSpec: MongoDbSpec{ExternalMembers: []ExternalMember{
				{ProcessName: "shard0-0", Hostname: "new-host.example.com:27017", Type: "mongod", ReplicaSetName: "myrs-0"},
				mongodShard0B,
				mongosA,
				mongosB,
			}},
			expectError: true,
			errorMsg:    "Cannot make changes to existing external members",
		},
		{
			name:    "replicaSetName of existing mongod entry changed — rejected",
			oldSpec: MongoDbSpec{ExternalMembers: baseExternalMembers},
			newSpec: MongoDbSpec{ExternalMembers: []ExternalMember{
				{ProcessName: "shard0-0", Hostname: "vm-shard0-0.example.com:27017", Type: "mongod", ReplicaSetName: "different-rs"},
				mongodShard0B,
				mongosA,
				mongosB,
			}},
			expectError: true,
			errorMsg:    "Cannot make changes to existing external members",
		},
		{
			name:    "type of existing entry changed from mongos to mongod — rejected",
			oldSpec: MongoDbSpec{ExternalMembers: baseExternalMembers},
			newSpec: MongoDbSpec{ExternalMembers: []ExternalMember{
				mongodShard0A,
				mongodShard0B,
				{ProcessName: "mongos-0", Hostname: "vm-mongos-0.example.com:27017", Type: "mongod", ReplicaSetName: "myrs-0"},
				mongosB,
			}},
			expectError: true,
			errorMsg:    "Cannot make changes to existing external members",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := noExternalMembersAdditionOrChanges(tt.newSpec, tt.oldSpec)
			if tt.expectError {
				assert.Equal(t, v1.ErrorLevel, result.Level)
				assert.Contains(t, result.Msg, tt.errorMsg)
			} else {
				assert.Equal(t, v1.ValidationSuccess(), result)
			}
		})
	}
}

func TestAtMostOneMigrationChangeAtATime_ShardedCluster_ExternalMembersOnly(t *testing.T) {
	// For a sharded cluster migration, ExternalMembers contains mongod and mongos entries across shards.
	// Members is always 0 for sharded clusters; scaling is detected via MongodsPerShardCount and ShardCount.
	shardedExternalMembers := []ExternalMember{
		{ProcessName: "shard0-0", Hostname: "vm-shard0-0.example.com:27017", Type: "mongod", ReplicaSetName: "myrs-0"},
		{ProcessName: "shard0-1", Hostname: "vm-shard0-1.example.com:27017", Type: "mongod", ReplicaSetName: "myrs-0"},
		{ProcessName: "mongos-0", Hostname: "vm-mongos-0.example.com:27017", Type: "mongos"},
	}

	tests := []struct {
		name        string
		oldSpec     MongoDbSpec
		newSpec     MongoDbSpec
		expectError bool
		errorMsg    string
	}{
		{
			name: "no external members — validator skipped",
			oldSpec: MongoDbSpec{
				MongodbShardedClusterSizeConfig: status.MongodbShardedClusterSizeConfig{ShardCount: 2, MongodsPerShardCount: 3},
			},
			newSpec: MongoDbSpec{
				MongodbShardedClusterSizeConfig: status.MongodbShardedClusterSizeConfig{ShardCount: 3, MongodsPerShardCount: 3},
			},
			expectError: false,
		},
		{
			name: "removing one external member with no other changes — allowed",
			oldSpec: MongoDbSpec{
				ExternalMembers:                 shardedExternalMembers,
				MongodbShardedClusterSizeConfig: status.MongodbShardedClusterSizeConfig{ShardCount: 2, MongodsPerShardCount: 3},
			},
			newSpec: MongoDbSpec{
				ExternalMembers:                 shardedExternalMembers[1:],
				MongodbShardedClusterSizeConfig: status.MongodbShardedClusterSizeConfig{ShardCount: 2, MongodsPerShardCount: 3},
			},
			expectError: false,
		},
		{
			name: "removing all external members — allowed",
			oldSpec: MongoDbSpec{
				ExternalMembers:                 shardedExternalMembers,
				MongodbShardedClusterSizeConfig: status.MongodbShardedClusterSizeConfig{ShardCount: 2, MongodsPerShardCount: 3},
			},
			newSpec: MongoDbSpec{
				ExternalMembers:                 []ExternalMember{},
				MongodbShardedClusterSizeConfig: status.MongodbShardedClusterSizeConfig{ShardCount: 2, MongodsPerShardCount: 3},
			},
			expectError: false,
		},
		{
			name: "adding external members once migration has started — rejected",
			oldSpec: MongoDbSpec{
				ExternalMembers:                 shardedExternalMembers,
				MongodbShardedClusterSizeConfig: status.MongodbShardedClusterSizeConfig{ShardCount: 2, MongodsPerShardCount: 3},
			},
			newSpec: MongoDbSpec{
				ExternalMembers: append(shardedExternalMembers,
					ExternalMember{ProcessName: "mongos-1", Hostname: "vm-mongos-1.example.com:27017", Type: "mongos"},
				),
				MongodbShardedClusterSizeConfig: status.MongodbShardedClusterSizeConfig{ShardCount: 2, MongodsPerShardCount: 3},
			},
			expectError: true,
			errorMsg:    "external members may not be added once migration has started",
		},
		{
			name: "removing external member while also increasing MongodsPerShardCount — rejected",
			oldSpec: MongoDbSpec{
				ExternalMembers:                 shardedExternalMembers,
				MongodbShardedClusterSizeConfig: status.MongodbShardedClusterSizeConfig{ShardCount: 2, MongodsPerShardCount: 3},
			},
			newSpec: MongoDbSpec{
				ExternalMembers:                 shardedExternalMembers[1:],
				MongodbShardedClusterSizeConfig: status.MongodbShardedClusterSizeConfig{ShardCount: 2, MongodsPerShardCount: 4},
			},
			expectError: true,
			errorMsg:    "only one migration change type is allowed per update",
		},
		{
			name: "removing external member while also increasing ShardCount — rejected",
			oldSpec: MongoDbSpec{
				ExternalMembers:                 shardedExternalMembers,
				MongodbShardedClusterSizeConfig: status.MongodbShardedClusterSizeConfig{ShardCount: 2, MongodsPerShardCount: 3},
			},
			newSpec: MongoDbSpec{
				ExternalMembers:                 shardedExternalMembers[1:],
				MongodbShardedClusterSizeConfig: status.MongodbShardedClusterSizeConfig{ShardCount: 3, MongodsPerShardCount: 3},
			},
			expectError: true,
			errorMsg:    "only one migration change type is allowed per update",
		},
		{
			name: "decreasing MongodsPerShardCount during migration — rejected",
			oldSpec: MongoDbSpec{
				ExternalMembers:                 shardedExternalMembers,
				MongodbShardedClusterSizeConfig: status.MongodbShardedClusterSizeConfig{ShardCount: 2, MongodsPerShardCount: 3},
			},
			newSpec: MongoDbSpec{
				ExternalMembers:                 shardedExternalMembers,
				MongodbShardedClusterSizeConfig: status.MongodbShardedClusterSizeConfig{ShardCount: 2, MongodsPerShardCount: 2},
			},
			expectError: true,
			errorMsg:    "Kubernetes members may not be removed during migration",
		},
		{
			name: "decreasing ShardCount during migration — rejected",
			oldSpec: MongoDbSpec{
				ExternalMembers:                 shardedExternalMembers,
				MongodbShardedClusterSizeConfig: status.MongodbShardedClusterSizeConfig{ShardCount: 2, MongodsPerShardCount: 3},
			},
			newSpec: MongoDbSpec{
				ExternalMembers:                 shardedExternalMembers,
				MongodbShardedClusterSizeConfig: status.MongodbShardedClusterSizeConfig{ShardCount: 1, MongodsPerShardCount: 3},
			},
			expectError: true,
			errorMsg:    "Kubernetes members may not be removed during migration",
		},
		{
			name: "updating memberConfig (promoting K8s config server member) alone — allowed",
			oldSpec: MongoDbSpec{
				ExternalMembers:                 shardedExternalMembers,
				MongodbShardedClusterSizeConfig: status.MongodbShardedClusterSizeConfig{ShardCount: 1, MongodsPerShardCount: 3, ConfigServerCount: 3},
				MemberConfig:                    []automationconfig.MemberOptions{{Votes: ptr.To(0), Priority: ptr.To("0")}},
			},
			newSpec: MongoDbSpec{
				ExternalMembers:                 shardedExternalMembers,
				MongodbShardedClusterSizeConfig: status.MongodbShardedClusterSizeConfig{ShardCount: 1, MongodsPerShardCount: 3, ConfigServerCount: 3},
				MemberConfig:                    []automationconfig.MemberOptions{{Votes: ptr.To(1), Priority: ptr.To("1")}},
			},
			expectError: false,
		},
		{
			name: "updating memberConfig while also removing an external member — rejected",
			oldSpec: MongoDbSpec{
				ExternalMembers:                 shardedExternalMembers,
				MongodbShardedClusterSizeConfig: status.MongodbShardedClusterSizeConfig{ShardCount: 1, MongodsPerShardCount: 3, ConfigServerCount: 3},
				MemberConfig:                    []automationconfig.MemberOptions{{Votes: ptr.To(0), Priority: ptr.To("0")}},
			},
			newSpec: MongoDbSpec{
				ExternalMembers:                 shardedExternalMembers[1:],
				MongodbShardedClusterSizeConfig: status.MongodbShardedClusterSizeConfig{ShardCount: 1, MongodsPerShardCount: 3, ConfigServerCount: 3},
				MemberConfig:                    []automationconfig.MemberOptions{{Votes: ptr.To(1), Priority: ptr.To("1")}},
			},
			expectError: true,
			errorMsg:    "only one migration change type is allowed per update",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.oldSpec.ResourceType = ShardedCluster
			tt.newSpec.ResourceType = ShardedCluster
			result := atMostOneMigrationChangeAtATime(tt.newSpec, tt.oldSpec)
			if tt.expectError {
				assert.Equal(t, v1.ErrorLevel, result.Level)
				assert.Contains(t, result.Msg, tt.errorMsg)
			} else {
				assert.Equal(t, v1.ValidationSuccess(), result)
			}
		})
	}
}

func TestNoShardNameOverridesAddedOrModified(t *testing.T) {
	initial := []ShardNameOverride{
		{ShardName: "vm-shard-A", ShardId: "vm-id-A", ReplicaSetName: "vm-rs-A"},
		{ShardName: "vm-shard-B"},
	}

	tests := []struct {
		name         string
		oldOverrides []ShardNameOverride
		newOverrides []ShardNameOverride
		expectError  bool
		errorMsg     string
	}{
		{
			name:        "no overrides in either version passes",
			expectError: false,
		},
		{
			name:         "adding overrides to an existing resource that had none fails",
			oldOverrides: nil,
			newOverrides: initial,
			expectError:  true,
			errorMsg:     "Cannot add",
		},
		{
			name:         "identical overrides passes",
			oldOverrides: initial,
			newOverrides: initial,
			expectError:  false,
		},
		{
			name:         "adding a new entry to existing overrides fails",
			oldOverrides: initial[:1],
			newOverrides: initial,
			expectError:  true,
			errorMsg:     "Cannot add",
		},
		{
			name:         "modifying ReplicaSetName in an existing entry fails",
			oldOverrides: initial,
			newOverrides: []ShardNameOverride{
				{ShardName: "vm-shard-A", ShardId: "vm-id-A", ReplicaSetName: "changed-rs"},
				{ShardName: "vm-shard-B"},
			},
			expectError: true,
			errorMsg:    "\"vm-shard-A\" was changed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldSpec := MongoDbSpec{ShardedClusterSpec: ShardedClusterSpec{ShardNameOverrides: tt.oldOverrides}}
			newSpec := MongoDbSpec{ShardedClusterSpec: ShardedClusterSpec{ShardNameOverrides: tt.newOverrides}}
			result := noShardNameOverridesAddedOrModified(newSpec, oldSpec)
			if tt.expectError {
				assert.Equal(t, v1.ErrorLevel, result.Level)
				assert.Contains(t, result.Msg, tt.errorMsg)
			} else {
				assert.Equal(t, v1.ValidationSuccess(), result)
			}
		})
	}
}

func TestNoShardNameOverridesRemovedForActiveShards(t *testing.T) {
	resourceName := "my-mdb"
	initial := []ShardNameOverride{
		{ShardName: "my-mdb-0", ShardId: "vm-id-0", ReplicaSetName: "vm-rs-0"},
		{ShardName: "my-mdb-1"},
	}

	buildMDB := func(shardCount int, overrides []ShardNameOverride) *MongoDB {
		return &MongoDB{
			ObjectMeta: metav1.ObjectMeta{Name: resourceName},
			Spec: MongoDbSpec{
				MongodbShardedClusterSizeConfig: status.MongodbShardedClusterSizeConfig{ShardCount: shardCount},
				ShardedClusterSpec:              ShardedClusterSpec{ShardNameOverrides: overrides},
			},
		}
	}

	tests := []struct {
		name        string
		newMDB      *MongoDB
		oldMDB      *MongoDB
		expectError bool
		errorMsg    string
	}{
		{
			name:        "removing an entry for an active shard fails",
			newMDB:      buildMDB(2, initial[:1]),
			oldMDB:      buildMDB(2, initial),
			expectError: true,
			errorMsg:    "my-mdb-1",
		},
		{
			name:        "removing the wrong entry when scaling down fails",
			newMDB:      buildMDB(1, initial[1:]),
			oldMDB:      buildMDB(2, initial),
			expectError: true,
			errorMsg:    "my-mdb-0",
		},
		{
			name:        "removing the scaled-away entry when scaling down passes",
			newMDB:      buildMDB(1, initial[:1]),
			oldMDB:      buildMDB(2, initial),
			expectError: false,
		},
		{
			name:        "no removal passes",
			newMDB:      buildMDB(2, initial),
			oldMDB:      buildMDB(2, initial),
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.newMDB.noShardNameOverridesRemovedForActiveShards(tt.oldMDB)
			if tt.expectError {
				assert.Equal(t, v1.ErrorLevel, result.Level)
				assert.Contains(t, result.Msg, tt.errorMsg)
			} else {
				assert.Equal(t, v1.ValidationSuccess(), result)
			}
		})
	}
}

func TestShardNameOverridesValidForm(t *testing.T) {
	mdb := NewDefaultShardedClusterBuilder().Build()
	mdb.Name = "vm-shard"

	tests := []struct {
		name        string
		overrides   []ShardNameOverride
		expectError bool
		errorMsg    string
	}{
		{
			name:        "no overrides passes",
			overrides:   nil,
			expectError: false,
		},
		{
			name:        "brevity form passes",
			overrides:   []ShardNameOverride{{ShardName: "vm-shard-0"}},
			expectError: false,
		},
		{
			name:        "full form passes",
			overrides:   []ShardNameOverride{{ShardName: "vm-shard-0", ShardId: "vm-id-0", ReplicaSetName: "vm-rs-0"}},
			expectError: false,
		},
		{
			name:        "missing shardName fails",
			overrides:   []ShardNameOverride{{ShardId: "vm-id-0", ReplicaSetName: "vm-rs-0"}},
			expectError: true,
			errorMsg:    "[0]: shardName is required",
		},
		{
			name:        "shardId without replicaSetName fails",
			overrides:   []ShardNameOverride{{ShardName: "vm-shard-0", ShardId: "vm-id-0"}},
			expectError: true,
			errorMsg:    "must both be set or both be omitted",
		},
		{
			name:        "replicaSetName without shardId fails",
			overrides:   []ShardNameOverride{{ShardName: "vm-shard-0", ReplicaSetName: "vm-rs-0"}},
			expectError: true,
			errorMsg:    "must both be set or both be omitted",
		},
		{
			name: "duplicate shardName fails",
			overrides: []ShardNameOverride{
				{ShardName: "vm-shard-0"},
				{ShardName: "vm-shard-0"},
			},
			expectError: true,
			errorMsg:    "shardName \"vm-shard-0\" is a duplicate",
		},
		{
			name: "duplicate shardId fails",
			overrides: []ShardNameOverride{
				{ShardName: "vm-shard-0", ShardId: "vm-id-0", ReplicaSetName: "vm-rs-0"},
				{ShardName: "vm-shard-1", ShardId: "vm-id-0", ReplicaSetName: "vm-rs-1"},
			},
			expectError: true,
			errorMsg:    "shardId \"vm-id-0\" is a duplicate",
		},
		{
			name: "duplicate replicaSetName fails",
			overrides: []ShardNameOverride{
				{ShardName: "vm-shard-0", ShardId: "vm-id-0", ReplicaSetName: "vm-rs-0"},
				{ShardName: "vm-shard-1", ShardId: "vm-id-1", ReplicaSetName: "vm-rs-0"},
			},
			expectError: true,
			errorMsg:    "replicaSetName \"vm-rs-0\" is a duplicate",
		},
		{
			name: "unique shardNames pass",
			overrides: []ShardNameOverride{
				{ShardName: "vm-shard-0"},
				{ShardName: "vm-shard-1"},
			},
			expectError: false,
		},
		{
			name: "unique full-form entries pass",
			overrides: []ShardNameOverride{
				{ShardName: "vm-shard-0", ShardId: "vm-id-0", ReplicaSetName: "vm-rs-0"},
				{ShardName: "vm-shard-1", ShardId: "vm-id-1", ReplicaSetName: "vm-rs-1"},
			},
			expectError: false,
		},
		{
			name:        "override replicaSetName colliding with default name of another shard fails",
			overrides:   []ShardNameOverride{{ShardName: "vm-shard-0", ShardId: "vm-shard-1", ReplicaSetName: "vm-shard-1"}},
			expectError: true,
			errorMsg:    "both resolve to AC replicaSetName \"vm-shard-1\"",
		},
		{
			name:        "override shardId colliding with default id of another shard fails",
			overrides:   []ShardNameOverride{{ShardName: "vm-shard-0", ShardId: "vm-shard-1", ReplicaSetName: "vm-rs-0"}},
			expectError: true,
			errorMsg:    "both resolve to AC shard _id \"vm-shard-1\"",
		},
		{
			name:        "override replicaSetName colliding with the config server name fails",
			overrides:   []ShardNameOverride{{ShardName: "vm-shard-0", ShardId: "vm-id-0", ReplicaSetName: "vm-shard-config"}},
			expectError: true,
			errorMsg:    "as the config server",
		},
		{
			// shardCount is 3, so shards are vm-shard-0..2. Keeping an override for vm-shard-3 after a scale
			// down (without removing it) must fail, which forces the override to be dropped with the shard.
			name:        "override for a shard beyond shardCount fails",
			overrides:   []ShardNameOverride{{ShardName: "vm-shard-3"}},
			expectError: true,
			errorMsg:    "must be vm-shard-{index} with index < 3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mdb.Spec.ShardNameOverrides = tt.overrides
			result := shardNameOverridesValidForm(*mdb)
			if tt.expectError {
				assert.Equal(t, v1.ErrorLevel, result.Level)
				assert.Contains(t, result.Msg, tt.errorMsg)
			} else {
				assert.Equal(t, v1.ValidationSuccess(), result)
			}
		})
	}
}

// TestNameOverrideValidators_AddChangeRemove verifies that configServerNameOverride and
// shardedClusterNameOverride may neither be added to an existing resource nor changed or removed once set.
func TestNameOverrideValidators_AddChangeRemove(t *testing.T) {
	tests := []struct {
		name      string
		validator func(newObj, oldObj MongoDbSpec) v1.ValidationResult
		oldSpec   MongoDbSpec
		newSpec   MongoDbSpec
		errorMsg  string
	}{
		{
			name:      "configServerNameOverride unchanged passes",
			validator: noConfigServerNameOverrideChanges,
			oldSpec:   MongoDbSpec{ShardedClusterSpec: ShardedClusterSpec{ConfigServerNameOverride: "vm-config"}},
			newSpec:   MongoDbSpec{ShardedClusterSpec: ShardedClusterSpec{ConfigServerNameOverride: "vm-config"}},
		},
		{
			name:      "adding configServerNameOverride fails",
			validator: noConfigServerNameOverrideChanges,
			oldSpec:   MongoDbSpec{},
			newSpec:   MongoDbSpec{ShardedClusterSpec: ShardedClusterSpec{ConfigServerNameOverride: "vm-config"}},
			errorMsg:  "Cannot add configServerNameOverride to an existing MongoDB resource.",
		},
		{
			name:      "changing configServerNameOverride fails",
			validator: noConfigServerNameOverrideChanges,
			oldSpec:   MongoDbSpec{ShardedClusterSpec: ShardedClusterSpec{ConfigServerNameOverride: "vm-config"}},
			newSpec:   MongoDbSpec{ShardedClusterSpec: ShardedClusterSpec{ConfigServerNameOverride: "other"}},
			errorMsg:  "Cannot change configServerNameOverride once set.",
		},
		{
			name:      "removing configServerNameOverride fails",
			validator: noConfigServerNameOverrideChanges,
			oldSpec:   MongoDbSpec{ShardedClusterSpec: ShardedClusterSpec{ConfigServerNameOverride: "vm-config"}},
			newSpec:   MongoDbSpec{},
			errorMsg:  "Cannot change configServerNameOverride once set.",
		},
		{
			name:      "adding shardedClusterNameOverride fails",
			validator: noShardedClusterNameOverrideChanges,
			oldSpec:   MongoDbSpec{},
			newSpec:   MongoDbSpec{ShardedClusterSpec: ShardedClusterSpec{ShardedClusterNameOverride: "vm-cluster"}},
			errorMsg:  "Cannot add shardedClusterNameOverride to an existing MongoDB resource.",
		},
		{
			name:      "changing shardedClusterNameOverride fails",
			validator: noShardedClusterNameOverrideChanges,
			oldSpec:   MongoDbSpec{ShardedClusterSpec: ShardedClusterSpec{ShardedClusterNameOverride: "vm-cluster"}},
			newSpec:   MongoDbSpec{ShardedClusterSpec: ShardedClusterSpec{ShardedClusterNameOverride: "other"}},
			errorMsg:  "Cannot change shardedClusterNameOverride once set.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.validator(tt.newSpec, tt.oldSpec)
			if tt.errorMsg != "" {
				assert.Equal(t, v1.ErrorLevel, result.Level)
				assert.Contains(t, result.Msg, tt.errorMsg)
			} else {
				assert.Equal(t, v1.ValidationSuccess(), result)
			}
		})
	}
}

// TestAtMostOneMigrationChangeAtATime_ShardedCluster_ConfigSrvAndMongosCounts verifies that config
// server and mongos count changes participate in the one change at a time rule during migration.
func TestAtMostOneMigrationChangeAtATime_ShardedCluster_ConfigSrvAndMongosCounts(t *testing.T) {
	externalMembers := []ExternalMember{
		{ProcessName: "cfg-0", Hostname: "vm-cfg-0.example.com:27017", Type: "mongod", ReplicaSetName: "myrs-config"},
		{ProcessName: "mongos-0", Hostname: "vm-mongos-0.example.com:27017", Type: "mongos"},
	}
	baseSize := status.MongodbShardedClusterSizeConfig{ShardCount: 1, MongodsPerShardCount: 3, ConfigServerCount: 3, MongosCount: 2}

	tests := []struct {
		name        string
		oldSize     status.MongodbShardedClusterSizeConfig
		newSize     status.MongodbShardedClusterSizeConfig
		newExternal []ExternalMember
		expectError bool
		errorMsg    string
	}{
		{
			name:        "increasing configServerCount alone is allowed",
			oldSize:     baseSize,
			newSize:     status.MongodbShardedClusterSizeConfig{ShardCount: 1, MongodsPerShardCount: 3, ConfigServerCount: 4, MongosCount: 2},
			newExternal: externalMembers,
		},
		{
			name:        "increasing configServerCount while removing an external member is rejected",
			oldSize:     baseSize,
			newSize:     status.MongodbShardedClusterSizeConfig{ShardCount: 1, MongodsPerShardCount: 3, ConfigServerCount: 4, MongosCount: 2},
			newExternal: externalMembers[1:],
			expectError: true,
			errorMsg:    "only one migration change type is allowed per update",
		},
		{
			name:        "decreasing configServerCount during migration is rejected",
			oldSize:     baseSize,
			newSize:     status.MongodbShardedClusterSizeConfig{ShardCount: 1, MongodsPerShardCount: 3, ConfigServerCount: 2, MongosCount: 2},
			newExternal: externalMembers,
			expectError: true,
			errorMsg:    "Kubernetes members may not be removed during migration",
		},
		{
			name:        "increasing mongosCount while removing an external member is rejected",
			oldSize:     baseSize,
			newSize:     status.MongodbShardedClusterSizeConfig{ShardCount: 1, MongodsPerShardCount: 3, ConfigServerCount: 3, MongosCount: 3},
			newExternal: externalMembers[:1],
			expectError: true,
			errorMsg:    "only one migration change type is allowed per update",
		},
		{
			name:        "decreasing mongosCount during migration is rejected",
			oldSize:     baseSize,
			newSize:     status.MongodbShardedClusterSizeConfig{ShardCount: 1, MongodsPerShardCount: 3, ConfigServerCount: 3, MongosCount: 1},
			newExternal: externalMembers,
			expectError: true,
			errorMsg:    "Kubernetes members may not be removed during migration",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldSpec := MongoDbSpec{DbCommonSpec: DbCommonSpec{ResourceType: ShardedCluster}, ExternalMembers: externalMembers, MongodbShardedClusterSizeConfig: tt.oldSize}
			newSpec := MongoDbSpec{DbCommonSpec: DbCommonSpec{ResourceType: ShardedCluster}, ExternalMembers: tt.newExternal, MongodbShardedClusterSizeConfig: tt.newSize}
			result := atMostOneMigrationChangeAtATime(newSpec, oldSpec)
			if tt.expectError {
				assert.Equal(t, v1.ErrorLevel, result.Level)
				assert.Contains(t, result.Msg, tt.errorMsg)
			} else {
				assert.Equal(t, v1.ValidationSuccess(), result)
			}
		})
	}
}

// TestShardNameOverridesValidForm_ConfigServerOverrideCollision verifies the resolved name collision
// check also runs when only configServerNameOverride is set, without any shardNameOverrides.
func TestShardNameOverridesValidForm_ConfigServerOverrideCollision(t *testing.T) {
	mdb := NewDefaultShardedClusterBuilder().Build()
	mdb.Name = "vm-shard"
	mdb.Spec.ConfigServerNameOverride = "vm-shard-0"
	result := shardNameOverridesValidForm(*mdb)
	assert.Equal(t, v1.ErrorLevel, result.Level)
	assert.Contains(t, result.Msg, "as the config server")
}

// TestNoReplicaSetNameOverrideChanges verifies replicaSetNameOverride is immutable like the sharded overrides.
func TestNoReplicaSetNameOverrideChanges(t *testing.T) {
	set := MongoDbSpec{ReplicaSetNameOverride: "vm-rs"}
	empty := MongoDbSpec{}

	assert.Equal(t, v1.ValidationSuccess(), noReplicaSetNameOverrideChanges(set, set))

	result := noReplicaSetNameOverrideChanges(set, empty)
	assert.Equal(t, v1.ErrorLevel, result.Level)
	assert.Contains(t, result.Msg, "Cannot add replicaSetNameOverride to an existing MongoDB resource.")

	result = noReplicaSetNameOverrideChanges(MongoDbSpec{ReplicaSetNameOverride: "other"}, set)
	assert.Equal(t, v1.ErrorLevel, result.Level)
	assert.Contains(t, result.Msg, "Cannot change replicaSetNameOverride once set.")

	result = noReplicaSetNameOverrideChanges(empty, set)
	assert.Equal(t, v1.ErrorLevel, result.Level)
	assert.Contains(t, result.Msg, "Cannot change replicaSetNameOverride once set.")
}

// TestAtMostOneMigrationChangeAtATime_ShardMemberConfigCounted verifies memberConfig entries beyond
// configServerCount are counted as changes since they apply to shard members.
func TestAtMostOneMigrationChangeAtATime_ShardMemberConfigCounted(t *testing.T) {
	votes0 := 0
	votes1 := 1
	prio := "0"
	baseConfig := make([]automationconfig.MemberOptions, 5)
	for i := range baseConfig {
		baseConfig[i] = automationconfig.MemberOptions{Votes: &votes0, Priority: &prio}
	}
	changedConfig := make([]automationconfig.MemberOptions, 5)
	copy(changedConfig, baseConfig)
	changedConfig[4] = automationconfig.MemberOptions{Votes: &votes1, Priority: &prio}

	externalMembers := []ExternalMember{
		{ProcessName: "shard0-0", Hostname: "vm0.example.com:27017", Type: "mongod", ReplicaSetName: "myrs-0"},
		{ProcessName: "shard0-1", Hostname: "vm1.example.com:27017", Type: "mongod", ReplicaSetName: "myrs-0"},
	}
	size := status.MongodbShardedClusterSizeConfig{ShardCount: 1, MongodsPerShardCount: 5, ConfigServerCount: 3}

	oldSpec := MongoDbSpec{DbCommonSpec: DbCommonSpec{ResourceType: ShardedCluster}, ExternalMembers: externalMembers, MongodbShardedClusterSizeConfig: size, MemberConfig: baseConfig}

	// Flipping votes for shard member index 4 (beyond configServerCount) alone is one change.
	newSpec := MongoDbSpec{DbCommonSpec: DbCommonSpec{ResourceType: ShardedCluster}, ExternalMembers: externalMembers, MongodbShardedClusterSizeConfig: size, MemberConfig: changedConfig}
	assert.Equal(t, v1.ValidationSuccess(), atMostOneMigrationChangeAtATime(newSpec, oldSpec))

	// Combining it with an external member removal is two changes and is rejected.
	newSpec.ExternalMembers = externalMembers[1:]
	result := atMostOneMigrationChangeAtATime(newSpec, oldSpec)
	assert.Equal(t, v1.ErrorLevel, result.Level)
	assert.Contains(t, result.Msg, "only one migration change type is allowed per update")
}

func TestGeneratedResourceUsedCorrectImportToolVersion(t *testing.T) {
	// The validator compares the migrate-tool-version annotation against the operator's build version,
	// so pin util.OperatorVersion for the duration of the test and restore it afterwards.
	const operatorVersion = "1.42.0"
	original := util.OperatorVersion
	util.OperatorVersion = operatorVersion
	t.Cleanup(func() { util.OperatorVersion = original })

	tests := []struct {
		name        string
		annotations map[string]string
		expectError bool
	}{
		{
			name:        "no annotation is a no-op",
			annotations: nil,
			expectError: false,
		},
		{
			name:        "matching version passes",
			annotations: map[string]string{util.MigrateToolVersionAnnotation: operatorVersion},
			expectError: false,
		},
		{
			name:        "latest is always accepted",
			annotations: map[string]string{util.MigrateToolVersionAnnotation: "latest"},
			expectError: false,
		},
		{
			name:        "mismatching version is rejected",
			annotations: map[string]string{util.MigrateToolVersionAnnotation: "1.41.0"},
			expectError: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rs := NewReplicaSetBuilder().Build()
			rs.Annotations = tc.annotations

			result := generatedResourceUsedCorrectImportToolVersion(rs)

			if tc.expectError {
				assert.Equal(t, v1.ErrorLevel, result.Level)
				assert.Contains(t, result.Msg, "Make sure the version of the import tool matches the operator")
			} else {
				assert.Equal(t, v1.SuccessLevel, result.Level)
			}
		})
	}
}
