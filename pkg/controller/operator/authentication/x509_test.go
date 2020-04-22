package authentication

import (
	"fmt"
	"testing"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

func TestX509EnableAgentAuthentication(t *testing.T) {
	conn, ac := createConnectionAndAutomationConfig()
	options := Options{
		UserOptions: UserOptions{
			AutomationSubject: validSubject("automation"),
			BackupSubject:     validSubject("backup"),
			MonitoringSubject: validSubject("monitoring"),
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
	assert.Len(t, ac.Auth.Users, 2)

	assert.True(t, ac.Auth.AuthoritativeSet)
	assert.NotEmpty(t, ac.Auth.Key)
	assert.NotEmpty(t, ac.Auth.KeyFileWindows)
	assert.NotEmpty(t, ac.Auth.KeyFile)

	for _, user := range buildX509AgentUsers(UserOptions{
		AutomationSubject: validSubject("automation"),
		BackupSubject:     validSubject("backup"),
		MonitoringSubject: validSubject("monitoring"),
	}) {
		assert.True(t, ac.Auth.HasUser(user.Username, user.Database))
	}

	for _, user := range buildScramAgentUsers("") {
		assert.False(t, ac.Auth.HasUser(user.Username, user.Database))
	}
}

func TestX509_DisableAgentAuthentication(t *testing.T) {
	conn, ac := createConnectionAndAutomationConfig()
	opts := Options{
		UserOptions: UserOptions{
			AutomationSubject: validSubject("automation"),
			BackupSubject:     validSubject("backup"),
			MonitoringSubject: validSubject("monitoring"),
		},
	}
	x509 := NewConnectionX509(conn, ac, opts)
	assertAgentAuthenticationDisabled(t, x509, opts)
}

func TestX509_DeploymentConfigured(t *testing.T) {
	conn, ac := createConnectionAndAutomationConfig()
	assertDeploymentMechanismsConfigured(t, NewConnectionX509(conn, ac, Options{}))
	assert.Equal(t, ac.AgentSSL.CAFilePath, util.CAFilePathInContainer)
}

func validSubject(o string) string {
	return fmt.Sprintf("CN=mms-automation-agent,OU=MongoDB Kubernetes Operator,O=%s,L=NY,ST=NY,C=US", o)
}
