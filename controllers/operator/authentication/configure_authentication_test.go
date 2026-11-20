package authentication

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/types"

	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

func init() {
	logger, _ := zap.NewDevelopment()
	zap.ReplaceGlobals(logger)
}

func TestConfigureScramSha256(t *testing.T) {
	ctx := context.Background()
	kubeClient, _ := mock.NewDefaultFakeClient()
	mongoDBResource := types.NamespacedName{Namespace: "test", Name: "test"}

	dep := om.NewDeployment()
	conn := om.NewMockedOmConnection(dep)

	opts := Options{
		AuthoritativeSet: true,
		ProcessNames:     []string{"process-1", "process-2", "process-3"},
		Mechanisms:       []string{"SCRAM"},
		AgentMechanism:   "SCRAM",
		MongoDBResource:  mongoDBResource,
	}

	if err := Configure(ctx, kubeClient, conn, opts, false, zap.S()); err != nil {
		t.Fatal(err)
	}

	ac, err := conn.ReadAutomationConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}

	assertAuthenticationEnabled(t, ac.Auth)
	assertAuthenticationMechanism(t, ac.Auth, "SCRAM-SHA-256")
}

func TestConfigureX509(t *testing.T) {
	ctx := context.Background()
	kubeClient, _ := mock.NewDefaultFakeClient()
	mongoDBResource := types.NamespacedName{Namespace: "test", Name: "test"}

	dep := om.NewDeployment()
	conn := om.NewMockedOmConnection(dep)

	opts := Options{
		AuthoritativeSet:   true,
		ProcessNames:       []string{"process-1", "process-2", "process-3"},
		Mechanisms:         []string{"X509"},
		AgentMechanism:     "X509",
		ClientCertificates: util.RequireClientCertificates,
		UserOptions: UserOptions{
			AutomationSubject: validSubject("automation"),
		},
		MongoDBResource: mongoDBResource,
	}

	if err := Configure(ctx, kubeClient, conn, opts, false, zap.S()); err != nil {
		t.Fatal(err)
	}

	ac, err := conn.ReadAutomationConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}

	assertAuthenticationEnabled(t, ac.Auth)
	assertAuthenticationMechanism(t, ac.Auth, "MONGODB-X509")
}

// x509ClientCertificateModeValidatingOMConnection rejects automation config updates the same way Ops Manager does
// when MONGODB-X509 is the only deployment mechanism and client certificates are not required.
// The embedded mock still stores a rejected change, so state can't be asserted after a rejection.
// Initial setup must not go through it: Configure enables X509 before SCRAM, which is briefly X509-only.
type x509ClientCertificateModeValidatingOMConnection struct {
	*om.MockedOmConnection
}

func (c x509ClientCertificateModeValidatingOMConnection) ReadUpdateAutomationConfig(ctx context.Context, modifyACFunc func(ac *om.AutomationConfig) error, log *zap.SugaredLogger) error {
	return c.MockedOmConnection.ReadUpdateAutomationConfig(ctx, func(ac *om.AutomationConfig) error {
		if err := modifyACFunc(ac); err != nil {
			return err
		}
		x509Only := len(ac.Auth.DeploymentAuthMechanisms) == 1 && ac.Auth.DeploymentAuthMechanisms[0] == string(MongoDBX509)
		if x509Only && (ac.AgentSSL == nil || ac.AgentSSL.ClientCertificateMode != util.RequireClientCertificates) {
			return errors.New(x509RequiresClientCertificatesOMError)
		}
		return nil
	}, log)
}

const x509RequiresClientCertificatesOMError = "Status: 400 (Bad Request), Detail: Invalid config: If auth.deploymentAuthMechanisms is MONGODB-X509 tls.clientCertificateMode must be REQUIRE."

// TestConfigureX509_ClientCertificateModeChange_WhenAgentAlreadyConfigured covers changing the client certificate
// mode on a deployment whose agents already authenticate with X509, with and without removing SCRAM.
func TestConfigureX509_ClientCertificateModeChange_WhenAgentAlreadyConfigured(t *testing.T) {
	tests := map[string]struct {
		initialMechanisms          []string
		initialClientCertificates  string
		desiredMechanisms          []string
		desiredClientCertificates  string
		expectedDeploymentMechsLen int
	}{
		"Removing SCRAM leaves X509 as the only mechanism": {
			initialMechanisms:          []string{"X509", "SCRAM"},
			initialClientCertificates:  util.OptionalClientCertficates,
			desiredMechanisms:          []string{"X509"},
			desiredClientCertificates:  util.RequireClientCertificates,
			expectedDeploymentMechsLen: 1,
		},
		"Requiring client certificates with unchanged mechanisms": {
			initialMechanisms:          []string{"X509", "SCRAM"},
			initialClientCertificates:  util.OptionalClientCertficates,
			desiredMechanisms:          []string{"X509", "SCRAM"},
			desiredClientCertificates:  util.RequireClientCertificates,
			expectedDeploymentMechsLen: 2,
		},
		"Making client certificates optional with unchanged mechanisms": {
			initialMechanisms:          []string{"X509", "SCRAM"},
			initialClientCertificates:  util.RequireClientCertificates,
			desiredMechanisms:          []string{"X509", "SCRAM"},
			desiredClientCertificates:  util.OptionalClientCertficates,
			expectedDeploymentMechsLen: 2,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			kubeClient, _ := mock.NewDefaultFakeClient()
			mockedConn := om.NewMockedOmConnection(om.NewDeployment())

			opts := Options{
				AuthoritativeSet:   true,
				ProcessNames:       []string{"process-1", "process-2", "process-3"},
				Mechanisms:         tc.initialMechanisms,
				AgentMechanism:     "X509",
				ClientCertificates: tc.initialClientCertificates,
				UserOptions: UserOptions{
					AutomationSubject: validSubject("automation"),
				},
				MongoDBResource: types.NamespacedName{Namespace: "test", Name: "test"},
			}
			require.NoError(t, Configure(ctx, kubeClient, mockedConn, opts, false, zap.S()))

			ac, err := mockedConn.ReadAutomationConfig(ctx)
			require.NoError(t, err)
			require.Equal(t, tc.initialClientCertificates, ac.AgentSSL.ClientCertificateMode)

			conn := x509ClientCertificateModeValidatingOMConnection{mockedConn}
			opts.Mechanisms = tc.desiredMechanisms
			opts.ClientCertificates = tc.desiredClientCertificates
			require.NoError(t, Configure(ctx, kubeClient, conn, opts, false, zap.S()))

			ac, err = conn.ReadAutomationConfig(ctx)
			require.NoError(t, err)
			assert.Equal(t, tc.desiredClientCertificates, ac.AgentSSL.ClientCertificateMode)
			assert.Len(t, ac.Auth.DeploymentAuthMechanisms, tc.expectedDeploymentMechsLen)
			assert.Contains(t, ac.Auth.DeploymentAuthMechanisms, string(MongoDBX509))
			assert.Equal(t, []string{string(MongoDBX509)}, ac.Auth.AutoAuthMechanisms)

			// once converged, reconfiguring with the same options must not reconfigure the agents again
			mockedConn.CleanHistory()
			require.NoError(t, Configure(ctx, kubeClient, conn, opts, false, zap.S()))
			mockedConn.CheckOperationsDidntHappen(t, reflect.ValueOf(mockedConn.ReadUpdateMonitoringAgentConfig), reflect.ValueOf(mockedConn.ReadUpdateBackupAgentConfig))

			ac, err = conn.ReadAutomationConfig(ctx)
			require.NoError(t, err)
			assert.Equal(t, tc.desiredClientCertificates, ac.AgentSSL.ClientCertificateMode)
		})
	}
}

func TestConfigureScramSha1(t *testing.T) {
	ctx := context.Background()
	kubeClient, _ := mock.NewDefaultFakeClient()
	mongoDBResource := types.NamespacedName{Namespace: "test", Name: "test"}

	dep := om.NewDeployment()
	conn := om.NewMockedOmConnection(dep)

	opts := Options{
		AuthoritativeSet: true,
		ProcessNames:     []string{"process-1", "process-2", "process-3"},
		Mechanisms:       []string{"SCRAM-SHA-1"},
		AgentMechanism:   "SCRAM-SHA-1",
		MongoDBResource:  mongoDBResource,
	}

	if err := Configure(ctx, kubeClient, conn, opts, false, zap.S()); err != nil {
		t.Fatal(err)
	}

	ac, err := conn.ReadAutomationConfig(ctx)
	assert.NoError(t, err)

	assertAuthenticationEnabled(t, ac.Auth)
	assertAuthenticationMechanism(t, ac.Auth, "SCRAM-SHA-1")
}

func TestConfigureMultipleAuthenticationMechanisms(t *testing.T) {
	ctx := context.Background()
	kubeClient, _ := mock.NewDefaultFakeClient()
	mongoDBResource := types.NamespacedName{Namespace: "test", Name: "test"}

	dep := om.NewDeployment()
	conn := om.NewMockedOmConnection(dep)

	opts := Options{
		AuthoritativeSet: true,
		ProcessNames:     []string{"process-1", "process-2", "process-3"},
		Mechanisms:       []string{"X509", "SCRAM"},
		AgentMechanism:   "SCRAM",
		UserOptions: UserOptions{
			AutomationSubject: validSubject("automation"),
		},
		MongoDBResource: mongoDBResource,
	}

	if err := Configure(ctx, kubeClient, conn, opts, false, zap.S()); err != nil {
		t.Fatal(err)
	}

	ac, err := conn.ReadAutomationConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}

	assertAuthenticationEnabled(t, ac.Auth)

	assert.Contains(t, ac.Auth.AutoAuthMechanisms, "SCRAM-SHA-256")

	assert.Len(t, ac.Auth.DeploymentAuthMechanisms, 2)
	assert.Len(t, ac.Auth.AutoAuthMechanisms, 1)
	assert.Contains(t, ac.Auth.DeploymentAuthMechanisms, "SCRAM-SHA-256")
	assert.Contains(t, ac.Auth.DeploymentAuthMechanisms, "MONGODB-X509")
}

func TestDisableAuthentication(t *testing.T) {
	ctx := context.Background()
	kubeClient, _ := mock.NewDefaultFakeClient()
	mongoDBResource := types.NamespacedName{Namespace: "test", Name: "test"}

	dep := om.NewDeployment()
	conn := om.NewMockedOmConnection(dep)

	// enable authentication
	_ = conn.ReadUpdateAutomationConfig(ctx, func(ac *om.AutomationConfig) error {
		ac.Auth.Enable()
		return nil
	}, zap.S())

	if err := Disable(ctx, kubeClient, conn, Options{MongoDBResource: mongoDBResource}, true, zap.S()); err != nil {
		t.Fatal(err)
	}

	ac, err := conn.ReadAutomationConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}

	assertAuthenticationDisabled(t, ac.Auth)
}

// TestDisableAuthenticationWithDeleteUsers tests that when deleteUsers=true (deployment deletion),
// the automation agent credentials are cleared to prevent stale credentials from being propagated.
func TestDisableAuthenticationWithDeleteUsers(t *testing.T) {
	ctx := context.Background()
	kubeClient, _ := mock.NewDefaultFakeClient()
	mongoDBResource := types.NamespacedName{Namespace: "test", Name: "test"}

	dep := om.NewDeployment()
	conn := om.NewMockedOmConnection(dep)

	// enable authentication
	_ = conn.ReadUpdateAutomationConfig(ctx, func(ac *om.AutomationConfig) error {
		ac.Auth.Enable()
		ac.Auth.AutoUser = "mms-automation-agent"
		ac.Auth.AutoPwd = "some-password"
		return nil
	}, zap.S())

	// Disable with deleteUsers=true (deployment deletion)
	if err := Disable(ctx, kubeClient, conn, Options{MongoDBResource: mongoDBResource}, true, zap.S()); err != nil {
		t.Fatal(err)
	}

	ac, err := conn.ReadAutomationConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// When deleteUsers=true, credentials should be cleared
	assert.Equal(t, util.MergoDelete, ac.Auth.AutoUser, "AutoUser should be cleared when deleteUsers=true")
	assert.Equal(t, util.MergoDelete, ac.Auth.AutoPwd, "AutoPwd should be cleared when deleteUsers=true")
}

// TestDisableAuthenticationWithoutDeleteUsers tests that when deleteUsers=false (auth transition),
// the AutoUser is kept to satisfy agent validation.
func TestDisableAuthenticationWithoutDeleteUsers(t *testing.T) {
	ctx := context.Background()
	kubeClient, _ := mock.NewDefaultFakeClient()
	mongoDBResource := types.NamespacedName{Namespace: "test", Name: "test"}

	dep := om.NewDeployment()
	conn := om.NewMockedOmConnection(dep)

	// enable authentication
	_ = conn.ReadUpdateAutomationConfig(ctx, func(ac *om.AutomationConfig) error {
		ac.Auth.Enable()
		ac.Auth.AutoUser = "mms-automation-agent"
		ac.Auth.AutoPwd = "some-password"
		return nil
	}, zap.S())

	// Disable with deleteUsers=false (auth transition)
	if err := Disable(ctx, kubeClient, conn, Options{MongoDBResource: mongoDBResource}, false, zap.S()); err != nil {
		t.Fatal(err)
	}

	ac, err := conn.ReadAutomationConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// When deleteUsers=false, AutoUser should be kept for agent validation
	assert.Equal(t, util.AutomationAgentName, ac.Auth.AutoUser, "AutoUser should be set when deleteUsers=false")
	// Password should NOT be cleared during auth transitions - agent needs it to re-authenticate
	assert.Equal(t, "some-password", ac.Auth.AutoPwd, "AutoPwd should be preserved when deleteUsers=false")
}

func TestGetCorrectAuthMechanismFromVersion(t *testing.T) {
	ctx := t.Context()
	conn := om.NewMockedOmConnection(om.NewDeployment())
	ac, err := conn.ReadAutomationConfig(ctx)
	require.NoError(t, err)

	mechanismList := convertToMechanismList([]string{"X509"}, ac)

	assert.Len(t, mechanismList, 1)
	assert.Contains(t, mechanismList, mongoDBX509Mechanism)

	mechanismList = convertToMechanismList([]string{"SCRAM", "X509"}, ac)

	assert.Contains(t, mechanismList, scramSha256Mechanism)
	assert.Contains(t, mechanismList, mongoDBX509Mechanism)

	// enable MONGODB-CR
	ac.Auth.AutoAuthMechanism = "MONGODB-CR"
	ac.Auth.Enable()

	mechanismList = convertToMechanismList([]string{"SCRAM", "X509"}, ac)

	assert.Contains(t, mechanismList, mongoDBCRMechanism)
	assert.Contains(t, mechanismList, mongoDBX509Mechanism)
}

func assertAuthenticationEnabled(t *testing.T, auth *om.Auth) {
	assertAuthenticationEnabledWithUsers(t, auth, 0)
}

func assertAuthenticationEnabledWithUsers(t *testing.T, auth *om.Auth, numUsers int) {
	assert.True(t, auth.AuthoritativeSet)
	assert.False(t, auth.Disabled)
	assert.NotEmpty(t, auth.Key)
	assert.NotEmpty(t, auth.KeyFileWindows)
	assert.NotEmpty(t, auth.KeyFile)
	assert.Len(t, auth.Users, numUsers)
	assert.True(t, noneNil(auth.Users))
}

func assertAuthenticationDisabled(t *testing.T, auth *om.Auth) {
	assert.True(t, auth.Disabled)
	assert.Empty(t, auth.DeploymentAuthMechanisms)
	assert.Empty(t, auth.AutoAuthMechanisms)
	// When deleteUsers=true (deployment deletion), credentials are cleared to prevent
	// stale credentials from being propagated to monitoring/backup agents
	assert.Equal(t, util.MergoDelete, auth.AutoUser)
	assert.Equal(t, util.MergoDelete, auth.AutoPwd)
	assert.NotEmpty(t, auth.Key)
	assert.True(t, len(auth.Users) == 0 || allNil(auth.Users))
}

func assertAuthenticationMechanism(t *testing.T, auth *om.Auth, mechanism string) {
	assert.Len(t, auth.DeploymentAuthMechanisms, 1)
	assert.Len(t, auth.AutoAuthMechanisms, 1)
	assert.Len(t, auth.Users, 0)
	assert.Contains(t, auth.DeploymentAuthMechanisms, mechanism)
	assert.Contains(t, auth.AutoAuthMechanisms, mechanism)
}

func assertDeploymentMechanismsConfigured(t *testing.T, authMechanism Mechanism, conn om.Connection, opts Options) {
	ctx := t.Context()
	err := authMechanism.EnableDeploymentAuthentication(ctx, conn, opts, zap.S())
	require.NoError(t, err)

	ac, err := conn.ReadAutomationConfig(ctx)
	require.NoError(t, err)
	assert.True(t, authMechanism.IsDeploymentAuthenticationConfigured(ac, opts))
}

func assertAgentAuthenticationDisabled(t *testing.T, authMechanism Mechanism, conn om.Connection, opts Options) {
	ctx := context.Background()
	kubeClient, _ := mock.NewDefaultFakeClient()
	mongoDBResource := types.NamespacedName{Namespace: "test", Name: "test"}
	opts.MongoDBResource = mongoDBResource

	err := authMechanism.EnableAgentAuthentication(ctx, kubeClient, conn, opts, zap.S())
	require.NoError(t, err)

	ac, err := conn.ReadAutomationConfig(ctx)
	require.NoError(t, err)
	assert.True(t, authMechanism.IsAgentAuthenticationConfigured(ac, opts))

	err = authMechanism.DisableAgentAuthentication(ctx, conn, zap.S())
	require.NoError(t, err)

	ac, err = conn.ReadAutomationConfig(ctx)
	require.NoError(t, err)
	assert.False(t, authMechanism.IsAgentAuthenticationConfigured(ac, opts))
}

func noneNil(users []*om.MongoDBUser) bool {
	for i := range users {
		if users[i] == nil {
			return false
		}
	}
	return true
}

func allNil(users []*om.MongoDBUser) bool {
	for i := range users {
		if users[i] != nil {
			return false
		}
	}
	return true
}
