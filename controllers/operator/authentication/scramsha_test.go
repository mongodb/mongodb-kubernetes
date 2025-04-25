package authentication

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"

	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

func TestAgentsAuthentication(t *testing.T) {
	type ConnectionFunction func(om.Connection, *om.AutomationConfig) Mechanism
	type TestConfig struct {
		connection     ConnectionFunction
		mechanismsUsed []MechanismName
	}
	tests := map[string]TestConfig{
		"SCRAM-SHA-1": {
			connection: func(connection om.Connection, config *om.AutomationConfig) Mechanism {
				return NewConnectionScramSha1(connection, config)
			},
			mechanismsUsed: []MechanismName{ScramSha1},
		},
		"SCRAM-SHA-256": {
			connection: func(connection om.Connection, config *om.AutomationConfig) Mechanism {
				return NewConnectionScramSha256(connection, config)
			},
			mechanismsUsed: []MechanismName{ScramSha256},
		},
		"CR": {
			connection: func(connection om.Connection, config *om.AutomationConfig) Mechanism {
				return NewConnectionCR(connection, config)
			},
			mechanismsUsed: []MechanismName{MongoDBCR},
		},
	}
	for testName, testConfig := range tests {
		t.Run(testName, func(t *testing.T) {
			conn, ac := createConnectionAndAutomationConfig()

			s := testConfig.connection(conn, ac)

			err := s.EnableAgentAuthentication(Options{AuthoritativeSet: true}, zap.S())
			assert.NoError(t, err)

			err = s.EnableDeploymentAuthentication(Options{CAFilePath: util.CAFilePathInContainer})
			assert.NoError(t, err)

			ac, err = conn.ReadAutomationConfig()
			assert.NoError(t, err)

			assertAuthenticationEnabled(t, ac.Auth)
			assert.Equal(t, ac.Auth.AutoUser, util.AutomationAgentName)
			assert.Len(t, ac.Auth.AutoAuthMechanisms, 1)
			for _, mech := range testConfig.mechanismsUsed {
				assert.Contains(t, ac.Auth.AutoAuthMechanisms, string(mech))
			}
			assert.NotEmpty(t, ac.Auth.AutoPwd)
			assert.True(t, s.IsAgentAuthenticationConfigured())
			assert.True(t, s.IsDeploymentAuthenticationConfigured())
		})
	}
}

func TestScramSha1_DisableAgentAuthentication(t *testing.T) {
	conn, ac := createConnectionAndAutomationConfig()
	assertAgentAuthenticationDisabled(t, NewConnectionScramSha1(conn, ac), Options{})
}

func TestScramSha256_DisableAgentAuthentication(t *testing.T) {
	conn, ac := createConnectionAndAutomationConfig()
	assertAgentAuthenticationDisabled(t, NewConnectionScramSha256(conn, ac), Options{})
}
