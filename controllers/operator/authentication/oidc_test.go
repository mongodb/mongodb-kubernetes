package authentication

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/oidc"
)

func TestOIDC_EnableDeploymentAuthentication(t *testing.T) {
	conn := om.NewMockedOmConnection(om.NewDeployment())
	ac, err := conn.ReadAutomationConfig()
	require.NoError(t, err)
	assert.Empty(t, ac.OIDCProviderConfigs)
	assert.Empty(t, ac.Auth.DeploymentAuthMechanisms)

	providerConfigs := []oidc.ProviderConfig{
		{
			AuthNamePrefix:        "okta",
			Audience:              "aud",
			IssuerUri:             "https://okta.mongodb.com",
			ClientId:              "client1",
			RequestedScopes:       []string{"openid", "profile"},
			UserClaim:             "sub",
			SupportsHumanFlows:    true,
			UseAuthorizationClaim: false,
		},
		{
			AuthNamePrefix:        "congito",
			Audience:              "aud",
			IssuerUri:             "https://congito.mongodb.com",
			ClientId:              "client2",
			UserClaim:             "sub",
			GroupsClaim:           "groups",
			SupportsHumanFlows:    false,
			UseAuthorizationClaim: true,
		},
	}

	opts := Options{
		Mechanisms:          []string{string(MongoDBOIDC)},
		OIDCProviderConfigs: providerConfigs,
	}

	configured := MongoDBOIDCMechanism.IsDeploymentAuthenticationConfigured(ac, opts)
	assert.False(t, configured)

	err = MongoDBOIDCMechanism.EnableDeploymentAuthentication(conn, opts, zap.S())
	require.NoError(t, err)

	ac, err = conn.ReadAutomationConfig()
	require.NoError(t, err)
	assert.Contains(t, ac.Auth.DeploymentAuthMechanisms, string(MongoDBOIDC))
	assert.Equal(t, providerConfigs, ac.OIDCProviderConfigs)

	configured = MongoDBOIDCMechanism.IsDeploymentAuthenticationConfigured(ac, opts)
	assert.True(t, configured)

	err = MongoDBOIDCMechanism.DisableDeploymentAuthentication(conn, zap.S())
	require.NoError(t, err)

	ac, err = conn.ReadAutomationConfig()
	require.NoError(t, err)

	configured = MongoDBOIDCMechanism.IsDeploymentAuthenticationConfigured(ac, opts)
	assert.False(t, configured)

	assert.NotContains(t, ac.Auth.DeploymentAuthMechanisms, string(MongoDBOIDC))
	assert.Empty(t, ac.OIDCProviderConfigs)
}

func TestOIDC_EnableAgentAuthentication(t *testing.T) {
	conn := om.NewMockedOmConnection(om.NewDeployment())
	opts := Options{
		Mechanisms: []string{string(MongoDBOIDC)},
	}

	ac, err := conn.ReadAutomationConfig()
	require.NoError(t, err)

	configured := MongoDBOIDCMechanism.IsAgentAuthenticationConfigured(ac, opts)
	assert.False(t, configured)

	err = MongoDBOIDCMechanism.EnableAgentAuthentication(conn, opts, zap.S())
	require.Error(t, err)

	err = MongoDBOIDCMechanism.DisableAgentAuthentication(conn, zap.S())
	require.Error(t, err)
}
