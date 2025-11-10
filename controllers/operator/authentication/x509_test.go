package authentication

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/types"

	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

var mongoDBX509Mechanism = getMechanismByName(MongoDBX509)

func TestX509EnableAgentAuthentication(t *testing.T) {
	ctx := context.Background()
	kubeClient, _ := mock.NewDefaultFakeClient()
	mdbNamespacedName := types.NamespacedName{Namespace: "test", Name: "test"}

	conn := om.NewMockedOmConnection(om.NewDeployment())

	options := Options{
		AgentMechanism:     "X509",
		ClientCertificates: util.RequireClientCertificates,
		UserOptions: UserOptions{
			AutomationSubject: validSubject("automation"),
		},
		AuthoritativeSet: true,
	}
	if err := mongoDBX509Mechanism.EnableAgentAuthentication(ctx, kubeClient, mdbNamespacedName, conn, options, zap.S()); err != nil {
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
	conn := om.NewMockedOmConnection(om.NewDeployment())

	opts := Options{
		UserOptions: UserOptions{
			AutomationSubject: validSubject("automation"),
		},
	}
	assertAgentAuthenticationDisabled(t, mongoDBX509Mechanism, conn, opts)
}

func TestX509_DeploymentConfigured(t *testing.T) {
	conn := om.NewMockedOmConnection(om.NewDeployment())
	opts := Options{AgentMechanism: "SCRAM", CAFilePath: util.CAFilePathInContainer}

	assertDeploymentMechanismsConfigured(t, mongoDBX509Mechanism, conn, opts)

	ac, err := conn.ReadAutomationConfig()
	require.NoError(t, err)
	assert.Equal(t, ac.AgentSSL.CAFilePath, util.CAFilePathInContainer)
}

func validSubject(o string) string {
	return fmt.Sprintf("CN=mms-automation-agent,OU=MongoDB Kubernetes Operator,O=%s,L=NY,ST=NY,C=US", o)
}
