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

	ac, err := conn.ReadAutomationConfig()
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

	ac, err := conn.ReadAutomationConfig()
	if err != nil {
		t.Fatal(err)
	}

	assertAuthenticationEnabled(t, ac.Auth)
	assertAuthenticationMechanism(t, ac.Auth, "MONGODB-X509")
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

	ac, err := conn.ReadAutomationConfig()
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

	ac, err := conn.ReadAutomationConfig()
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
	_ = conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		ac.Auth.Enable()
		return nil
	}, zap.S())

	if err := Disable(ctx, kubeClient, conn, Options{MongoDBResource: mongoDBResource}, true, zap.S()); err != nil {
		t.Fatal(err)
	}

	ac, err := conn.ReadAutomationConfig()
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
	_ = conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		ac.Auth.Enable()
		ac.Auth.AutoUser = "mms-automation-agent"
		ac.Auth.AutoPwd = "some-password"
		return nil
	}, zap.S())

	// Disable with deleteUsers=true (deployment deletion)
	if err := Disable(ctx, kubeClient, conn, Options{MongoDBResource: mongoDBResource}, true, zap.S()); err != nil {
		t.Fatal(err)
	}

	ac, err := conn.ReadAutomationConfig()
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
	_ = conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		ac.Auth.Enable()
		ac.Auth.AutoUser = "mms-automation-agent"
		ac.Auth.AutoPwd = "some-password"
		return nil
	}, zap.S())

	// Disable with deleteUsers=false (auth transition)
	if err := Disable(ctx, kubeClient, conn, Options{MongoDBResource: mongoDBResource}, false, zap.S()); err != nil {
		t.Fatal(err)
	}

	ac, err := conn.ReadAutomationConfig()
	if err != nil {
		t.Fatal(err)
	}

	// When deleteUsers=false, AutoUser should be kept for agent validation
	assert.Equal(t, util.AutomationAgentName, ac.Auth.AutoUser, "AutoUser should be set when deleteUsers=false")
	// Password should NOT be cleared during auth transitions - agent needs it to re-authenticate
	assert.Equal(t, "some-password", ac.Auth.AutoPwd, "AutoPwd should be preserved when deleteUsers=false")
}

func TestGetCorrectAuthMechanismFromVersion(t *testing.T) {
	conn := om.NewMockedOmConnection(om.NewDeployment())
	ac, err := conn.ReadAutomationConfig()
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

// TestConfigureX509ToScramTransition tests the full X509→SCRAM transition (3 steps):
// Step 1: Start with X509 only
// Step 2: Add SCRAM alongside X509 as deployment mechanisms, switch agent to SCRAM
//         (AutoAuthMechanisms is overwritten to just SCRAM — OpsManager rejects X509+SCRAM combo)
// Step 3: Remove X509 from deployment mechanisms (SCRAM only)
func TestConfigureX509ToScramTransition(t *testing.T) {
	ctx := context.Background()
	kubeClient, _ := mock.NewDefaultFakeClient()
	mongoDBResource := types.NamespacedName{Namespace: "test", Name: "test"}

	dep := om.NewDeployment()
	conn := om.NewMockedOmConnection(dep)

	// Step 1: Configure X509 first
	x509Opts := Options{
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

	err := Configure(ctx, kubeClient, conn, x509Opts, false, zap.S())
	require.NoError(t, err)

	ac, err := conn.ReadAutomationConfig()
	require.NoError(t, err)
	assertAuthenticationEnabled(t, ac.Auth)
	assertAuthenticationMechanism(t, ac.Auth, "MONGODB-X509")
	assert.True(t, isValidX509Subject(ac.Auth.AutoUser), "AutoUser should be X509 subject after X509 setup")

	// Step 2: Add SCRAM alongside X509, switch agent to SCRAM
	scramOpts := Options{
		AuthoritativeSet: true,
		ProcessNames:     []string{"process-1", "process-2", "process-3"},
		Mechanisms:       []string{"X509", "SCRAM"},
		AgentMechanism:   "SCRAM",
		UserOptions: UserOptions{
			AutomationSubject: validSubject("automation"),
		},
		MongoDBResource: mongoDBResource,
	}

	err = Configure(ctx, kubeClient, conn, scramOpts, false, zap.S())
	require.NoError(t, err)

	ac, err = conn.ReadAutomationConfig()
	require.NoError(t, err)

	// Agent mechanism should be SCRAM only (OpsManager rejects X509+SCRAM in AutoAuthMechanisms)
	assert.Contains(t, ac.Auth.AutoAuthMechanisms, "SCRAM-SHA-256", "SCRAM should be the agent mechanism")
	assert.NotContains(t, ac.Auth.AutoAuthMechanisms, "MONGODB-X509", "X509 must not be in agent mechanisms (OpsManager rejects this)")
	assert.Len(t, ac.Auth.AutoAuthMechanisms, 1, "Only SCRAM in agent mechanisms")

	// AutoUser must be the SCRAM username, not the X509 subject
	assert.Equal(t, util.AutomationAgentName, ac.Auth.AutoUser, "AutoUser should be SCRAM agent name after transition")

	// Both deployment mechanisms should be enabled
	assert.Len(t, ac.Auth.DeploymentAuthMechanisms, 2, "Both deployment mechanisms should be enabled")
	assert.Contains(t, ac.Auth.DeploymentAuthMechanisms, "SCRAM-SHA-256")
	assert.Contains(t, ac.Auth.DeploymentAuthMechanisms, "MONGODB-X509")

	assert.False(t, ac.Auth.Disabled, "Auth should be enabled")

	// Step 3: Remove X509 from deployment mechanisms (SCRAM only)
	scramOnlyOpts := Options{
		AuthoritativeSet: true,
		ProcessNames:     []string{"process-1", "process-2", "process-3"},
		Mechanisms:       []string{"SCRAM"},
		AgentMechanism:   "SCRAM",
		MongoDBResource:  mongoDBResource,
	}

	err = Configure(ctx, kubeClient, conn, scramOnlyOpts, false, zap.S())
	require.NoError(t, err)

	ac, err = conn.ReadAutomationConfig()
	require.NoError(t, err)

	// X509 should be removed from deployment mechanisms too
	assert.Contains(t, ac.Auth.AutoAuthMechanisms, "SCRAM-SHA-256", "SCRAM should be the agent mechanism")
	assert.NotContains(t, ac.Auth.AutoAuthMechanisms, "MONGODB-X509", "X509 should be removed from agent mechanisms")
	assert.Len(t, ac.Auth.AutoAuthMechanisms, 1, "Only SCRAM should remain")

	assert.Contains(t, ac.Auth.DeploymentAuthMechanisms, "SCRAM-SHA-256")
	assert.NotContains(t, ac.Auth.DeploymentAuthMechanisms, "MONGODB-X509", "X509 should be removed from deployment mechanisms")
	assert.Len(t, ac.Auth.DeploymentAuthMechanisms, 1, "Only SCRAM deployment mechanism should remain")

	assert.Equal(t, util.AutomationAgentName, ac.Auth.AutoUser)
	assert.False(t, ac.Auth.Disabled, "Auth should be enabled")
}

// TestScramOverwritesAgentMechanism verifies that EnableAgentAuthentication
// overwrites AutoAuthMechanisms with only the SCRAM mechanism (OpsManager rejects
// MONGODB-X509 + SCRAM-SHA-256 in AutoAuthMechanisms).
func TestScramOverwritesAgentMechanism(t *testing.T) {
	ctx := context.Background()
	kubeClient, _ := mock.NewDefaultFakeClient()
	mongoDBResource := types.NamespacedName{Namespace: "test", Name: "test"}

	dep := om.NewDeployment()
	conn := om.NewMockedOmConnection(dep)

	// Pre-configure X509 in AutoAuthMechanisms (simulates existing X509 agent auth)
	_ = conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		ac.Auth.AutoAuthMechanisms = []string{"MONGODB-X509"}
		ac.Auth.AutoUser = validSubject("automation")
		ac.Auth.Disabled = false
		return nil
	}, zap.S())

	scramMechanism := &automationConfigScramSha{MechanismName: ScramSha256}
	err := scramMechanism.EnableAgentAuthentication(ctx, kubeClient, conn, Options{
		MongoDBResource: mongoDBResource,
	}, zap.S())
	require.NoError(t, err)

	ac, err := conn.ReadAutomationConfig()
	require.NoError(t, err)

	// SCRAM should REPLACE X509 (OpsManager rejects the combination)
	assert.Contains(t, ac.Auth.AutoAuthMechanisms, "SCRAM-SHA-256", "SCRAM should be set")
	assert.NotContains(t, ac.Auth.AutoAuthMechanisms, "MONGODB-X509", "X509 should be replaced")
	assert.Len(t, ac.Auth.AutoAuthMechanisms, 1, "Only SCRAM should be present")

	// AutoUser should be set to SCRAM agent name
	assert.Equal(t, util.AutomationAgentName, ac.Auth.AutoUser, "AutoUser should be SCRAM agent name")
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
	err := authMechanism.EnableDeploymentAuthentication(conn, opts, zap.S())
	require.NoError(t, err)

	ac, err := conn.ReadAutomationConfig()
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

	ac, err := conn.ReadAutomationConfig()
	require.NoError(t, err)
	assert.True(t, authMechanism.IsAgentAuthenticationConfigured(ac, opts))

	err = authMechanism.DisableAgentAuthentication(conn, zap.S())
	require.NoError(t, err)

	ac, err = conn.ReadAutomationConfig()
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
