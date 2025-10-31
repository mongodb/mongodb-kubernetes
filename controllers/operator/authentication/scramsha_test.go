package authentication

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/types"

	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

var (
	mongoDBCRMechanism   = getMechanismByName(MongoDBCR)
	scramSha1Mechanism   = getMechanismByName(ScramSha1)
	scramSha256Mechanism = getMechanismByName(ScramSha256)
)

func TestAgentsAuthentication(t *testing.T) {
	type TestConfig struct {
		mechanism Mechanism
	}
	tests := map[string]TestConfig{
		"SCRAM-SHA-1": {
			mechanism: scramSha1Mechanism,
		},
		"SCRAM-SHA-256": {
			mechanism: scramSha256Mechanism,
		},
		"CR": {
			mechanism: mongoDBCRMechanism,
		},
	}
	for testName, testConfig := range tests {
		t.Run(testName, func(t *testing.T) {
			ctx := context.Background()
			kubeClient, _ := mock.NewDefaultFakeClient()
			mdbNamespacedName := &types.NamespacedName{Namespace: "test", Name: "test"}

			conn := om.NewMockedOmConnection(om.NewDeployment())

			s := testConfig.mechanism

			opts := Options{
				AuthoritativeSet: true,
				CAFilePath:       util.CAFilePathInContainer,
			}

			err := s.EnableAgentAuthentication(kubeClient, ctx, mdbNamespacedName, conn, opts, zap.S())
			require.NoError(t, err)

			err = s.EnableDeploymentAuthentication(conn, opts, zap.S())
			require.NoError(t, err)

			ac, err := conn.ReadAutomationConfig()
			require.NoError(t, err)

			assertAuthenticationEnabled(t, ac.Auth)
			assert.Equal(t, ac.Auth.AutoUser, util.AutomationAgentName)
			assert.Len(t, ac.Auth.AutoAuthMechanisms, 1)
			assert.Contains(t, ac.Auth.AutoAuthMechanisms, string(testConfig.mechanism.GetName()))
			assert.NotEmpty(t, ac.Auth.AutoPwd)
			assert.True(t, s.IsAgentAuthenticationConfigured(ac, opts))
			assert.True(t, s.IsDeploymentAuthenticationConfigured(ac, opts))
		})
	}
}

func TestScramSha1_DisableAgentAuthentication(t *testing.T) {
	conn := om.NewMockedOmConnection(om.NewDeployment())
	assertAgentAuthenticationDisabled(t, scramSha1Mechanism, conn, Options{})
}

func TestScramSha256_DisableAgentAuthentication(t *testing.T) {
	conn := om.NewMockedOmConnection(om.NewDeployment())
	assertAgentAuthenticationDisabled(t, scramSha256Mechanism, conn, Options{})
}
