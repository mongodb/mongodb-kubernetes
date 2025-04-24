package authentication

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

func TestAgentsAuthentication(t *testing.T) {
	type TestConfig struct {
		mechanism AutomationConfigScramSha
	}
	tests := map[string]TestConfig{
		"SCRAM-SHA-1": {
			mechanism: ScramSha1Mechanism,
		},
		"SCRAM-SHA-256": {
			mechanism: ScramSha256Mechanism,
		},
		"CR": {
			mechanism: MongoDBCRMechanism,
		},
	}
	for testName, testConfig := range tests {
		t.Run(testName, func(t *testing.T) {
			conn := om.NewMockedOmConnection(om.NewDeployment())

			s := testConfig.mechanism

			opts := Options{
				AuthoritativeSet: true,
				CAFilePath:       util.CAFilePathInContainer,
			}

			err := s.EnableAgentAuthentication(conn, opts, zap.S())
			require.NoError(t, err)

			err = s.EnableDeploymentAuthentication(conn, opts, zap.S())
			require.NoError(t, err)

			ac, err := conn.ReadAutomationConfig()
			require.NoError(t, err)

			assertAuthenticationEnabled(t, ac.Auth)
			assert.Equal(t, ac.Auth.AutoUser, util.AutomationAgentName)
			assert.Len(t, ac.Auth.AutoAuthMechanisms, 1)
			for _, mech := range testConfig.mechanism.GetName() {
				assert.Contains(t, ac.Auth.AutoAuthMechanisms, string(mech))
			}
			assert.NotEmpty(t, ac.Auth.AutoPwd)
			assert.True(t, s.IsAgentAuthenticationConfigured(ac, opts))
			assert.True(t, s.IsDeploymentAuthenticationConfigured(ac, opts))
		})
	}
}

func TestScramSha1_DisableAgentAuthentication(t *testing.T) {
	conn := om.NewMockedOmConnection(om.NewDeployment())
	assertAgentAuthenticationDisabled(t, ScramSha1Mechanism, conn, Options{})
}

func TestScramSha256_DisableAgentAuthentication(t *testing.T) {
	conn := om.NewMockedOmConnection(om.NewDeployment())
	assertAgentAuthenticationDisabled(t, ScramSha256Mechanism, conn, Options{})
}
