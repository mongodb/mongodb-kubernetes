package authentication

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"

	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

func TestX509EnableAgentAuthentication(t *testing.T) {
	conn, ac := createConnectionAndAutomationConfig()
	options := Options{
		AgentMechanism:     "X509",
		ClientCertificates: util.RequireClientCertificates,
		UserOptions: UserOptions{
			AutomationSubject: validSubject("automation"),
		},
	}
	x := NewConnectionX509(conn, ac, options)
	if err := x.EnableAgentAuthentication(Options{AuthoritativeSet: true}, zap.S()); err != nil {
		t.Fatal(err)
	}

	ac, err := conn.ReadAutomationConfig()
	if err != nil {
		t.Fatal(err)
	}

	assert.Equal(t, ac.Auth.AutoUser, options.AutomationSubject)
	assert.Len(t, ac.Auth.AutoAuthMechanisms, 1)
	assert.Contains(t, ac.Auth.AutoAuthMechanisms, string(MongoDBX509))
	assert.Equal(t, ac.Auth.AutoPwd, util.MergoDelete)
	assert.False(t, ac.Auth.Disabled)
	assert.Len(t, ac.Auth.Users, 0)

	assert.True(t, ac.Auth.AuthoritativeSet)
	assert.NotEmpty(t, ac.Auth.Key)
	assert.NotEmpty(t, ac.Auth.KeyFileWindows)
	assert.NotEmpty(t, ac.Auth.KeyFile)
}

func TestX509_DisableAgentAuthentication(t *testing.T) {
	conn, ac := createConnectionAndAutomationConfig()
	opts := Options{
		UserOptions: UserOptions{
			AutomationSubject: validSubject("automation"),
		},
	}
	x509 := NewConnectionX509(conn, ac, opts)
	assertAgentAuthenticationDisabled(t, x509, opts)
}

func TestX509_DeploymentConfigured(t *testing.T) {
	conn, ac := createConnectionAndAutomationConfig()
	assertDeploymentMechanismsConfigured(t, NewConnectionX509(conn, ac, Options{AgentMechanism: "SCRAM"}))
	assert.Equal(t, ac.AgentSSL.CAFilePath, util.CAFilePathInContainer)
}

func validSubject(o string) string {
	return fmt.Sprintf("CN=mms-automation-agent,OU=MongoDB Kubernetes Operator,O=%s,L=NY,ST=NY,C=US", o)
}
