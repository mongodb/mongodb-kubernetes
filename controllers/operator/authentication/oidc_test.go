package authentication

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"k8s.io/utils/ptr"

	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/oidc"
)

var mongoDBOIDCMechanism = getMechanismByName(MongoDBOIDC)

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
			ClientId:              ptr.To("client1"),
			RequestedScopes:       []string{"openid", "profile"},
			UserClaim:             "sub",
			SupportsHumanFlows:    true,
			UseAuthorizationClaim: false,
		},
		{
			AuthNamePrefix:        "congito",
			Audience:              "aud",
			IssuerUri:             "https://congito.mongodb.com",
			ClientId:              ptr.To("client2"),
			UserClaim:             "sub",
			GroupsClaim:           ptr.To("groups"),
			SupportsHumanFlows:    false,
			UseAuthorizationClaim: true,
		},
	}

	opts := Options{
		Mechanisms:          []string{string(MongoDBOIDC)},
		OIDCProviderConfigs: providerConfigs,
	}

	configured := mongoDBOIDCMechanism.IsDeploymentAuthenticationConfigured(ac, opts)
	assert.False(t, configured)

	err = mongoDBOIDCMechanism.EnableDeploymentAuthentication(conn, opts, zap.S())
	require.NoError(t, err)

	ac, err = conn.ReadAutomationConfig()
	require.NoError(t, err)
	assert.Contains(t, ac.Auth.DeploymentAuthMechanisms, string(MongoDBOIDC))
	assert.Equal(t, providerConfigs, ac.OIDCProviderConfigs)

	configured = mongoDBOIDCMechanism.IsDeploymentAuthenticationConfigured(ac, opts)
	assert.True(t, configured)

	err = mongoDBOIDCMechanism.DisableDeploymentAuthentication(conn, zap.S())
	require.NoError(t, err)

	ac, err = conn.ReadAutomationConfig()
	require.NoError(t, err)

	configured = mongoDBOIDCMechanism.IsDeploymentAuthenticationConfigured(ac, opts)
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

	configured := mongoDBOIDCMechanism.IsAgentAuthenticationConfigured(ac, opts)
	assert.False(t, configured)

	err = mongoDBOIDCMechanism.EnableAgentAuthentication(conn, opts, zap.S())
	require.Error(t, err)

	err = mongoDBOIDCMechanism.DisableAgentAuthentication(conn, zap.S())
	require.Error(t, err)
}
