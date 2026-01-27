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
			mongoDBResource := types.NamespacedName{Namespace: "test", Name: "test"}

			conn := om.NewMockedOmConnection(om.NewDeployment())

			s := testConfig.mechanism

			opts := Options{
				AuthoritativeSet: true,
				CAFilePath:       util.CAFilePathInContainer,
				MongoDBResource:  mongoDBResource,
			}

			err := s.EnableAgentAuthentication(ctx, kubeClient, conn, opts, zap.S())
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

// TestDisableAgentAuthentication_ClearsMonitoringAndBackupCredentials verifies that
// DisableAgentAuthentication properly clears monitoring and backup agent credentials.
// This is critical to prevent SCRAM authentication attempts against deployments
// that don't have auth enabled.
func TestDisableAgentAuthentication_ClearsMonitoringAndBackupCredentials(t *testing.T) {
	ctx := context.Background()
	kubeClient, _ := mock.NewDefaultFakeClient()
	mongoDBResource := types.NamespacedName{Namespace: "test", Name: "test"}

	conn := om.NewMockedOmConnection(om.NewDeployment())

	// First, enable SCRAM authentication which sets credentials
	opts := Options{
		AuthoritativeSet: true,
		CAFilePath:       util.CAFilePathInContainer,
		MongoDBResource:  mongoDBResource,
	}

	err := scramSha256Mechanism.EnableAgentAuthentication(ctx, kubeClient, conn, opts, zap.S())
	require.NoError(t, err)

	// Verify automation config has credentials set
	ac, err := conn.ReadAutomationConfig()
	require.NoError(t, err)
	assert.NotEmpty(t, ac.Auth.AutoUser, "AutoUser should be set after enabling auth")
	assert.NotEmpty(t, ac.Auth.AutoPwd, "AutoPwd should be set after enabling auth")

	// Pre-populate monitoring and backup configs with credentials (simulating project-level contamination)
	monitoringConfig, err := conn.ReadMonitoringAgentConfig()
	require.NoError(t, err)
	monitoringConfig.MonitoringAgentTemplate.Username = "test-user"
	monitoringConfig.MonitoringAgentTemplate.Password = "test-password"

	backupConfig, err := conn.ReadBackupAgentConfig()
	require.NoError(t, err)
	backupConfig.BackupAgentTemplate.Username = "test-user"
	backupConfig.BackupAgentTemplate.Password = "test-password"

	// Now disable SCRAM authentication
	err = scramSha256Mechanism.DisableAgentAuthentication(conn, zap.S())
	require.NoError(t, err)

	// Verify monitoring agent credentials are cleared (set to MergoDelete sentinel)
	monitoringConfig, err = conn.ReadMonitoringAgentConfig()
	require.NoError(t, err)
	assert.Equal(t, util.MergoDelete, monitoringConfig.MonitoringAgentTemplate.Username,
		"Monitoring agent username should be cleared (MergoDelete) after disabling SCRAM auth")
	assert.Equal(t, util.MergoDelete, monitoringConfig.MonitoringAgentTemplate.Password,
		"Monitoring agent password should be cleared (MergoDelete) after disabling SCRAM auth")

	// Verify backup agent credentials are cleared
	backupConfig, err = conn.ReadBackupAgentConfig()
	require.NoError(t, err)
	assert.Equal(t, util.MergoDelete, backupConfig.BackupAgentTemplate.Username,
		"Backup agent username should be cleared (MergoDelete) after disabling SCRAM auth")
	assert.Equal(t, util.MergoDelete, backupConfig.BackupAgentTemplate.Password,
		"Backup agent password should be cleared (MergoDelete) after disabling SCRAM auth")
}
