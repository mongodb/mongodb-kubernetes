package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status/pvc"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/agents"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/connectionstring"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/construct"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/construct/scalers"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/create"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/pkg/agentVersionManagement"
	"github.com/mongodb/mongodb-kubernetes/pkg/authentication/scramcredentials"
	"github.com/mongodb/mongodb-kubernetes/pkg/automationconfig"
	"github.com/mongodb/mongodb-kubernetes/pkg/dns"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/secret"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
	"github.com/mongodb/mongodb-kubernetes/pkg/statefulset"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/constants"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
)

func init() {
	mock.InitDefaultEnvVariables()
}

// getReleaseJsonPath searches for a specified target directory by traversing the directory tree backwards from the current working directory
func getReleaseJsonPath() (string, error) {
	releaseFileName := "release.json"

	currentDir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for currentDir != "/" {
		if _, err := os.Stat(filepath.Join(currentDir, releaseFileName)); !errors.Is(err, os.ErrNotExist) {
			return filepath.Join(currentDir, releaseFileName), nil
		}
		currentDir = filepath.Dir(currentDir)
	}
	return currentDir, err
}

// This approach ensures all test methods in this file have properly defined variables.
func TestMain(m *testing.M) {
	path, _ := getReleaseJsonPath()
	_ = os.Setenv(agentVersionManagement.MappingFilePathEnv, path) // nolint:forbidigo
	defer func(key string) {
		_ = os.Unsetenv(key) // nolint:forbidigo
	}(agentVersionManagement.MappingFilePathEnv)
	code := m.Run()
	os.Exit(code)
}

func TestMongoDB_ConnectionURL_DefaultCluster_AppDB(t *testing.T) {
	opsManager := DefaultOpsManagerBuilder().Build()
	appdb := opsManager.Spec.AppDB

	var cnx string
	cnx = appdb.BuildConnectionURL("user", "passwd", connectionstring.SchemeMongoDB, nil, nil)
	assert.Equal(t, "mongodb://user:passwd@test-om-db-0.test-om-db-svc.my-namespace.svc.cluster.local:27017,"+
		"test-om-db-1.test-om-db-svc.my-namespace.svc.cluster.local:27017,test-om-db-2.test-om-db-svc.my-namespace.svc.cluster.local:27017/"+
		"?authMechanism=SCRAM-SHA-256&authSource=admin&connectTimeoutMS=20000&replicaSet=test-om-db&serverSelectionTimeoutMS=20000", cnx)

	// Special symbols in the url
	cnx = appdb.BuildConnectionURL("special/user#", "@passw!", connectionstring.SchemeMongoDB, nil, nil)
	assert.Equal(t, "mongodb://special%2Fuser%23:%40passw%21@test-om-db-0.test-om-db-svc.my-namespace.svc.cluster.local:27017,"+
		"test-om-db-1.test-om-db-svc.my-namespace.svc.cluster.local:27017,test-om-db-2.test-om-db-svc.my-namespace.svc.cluster.local:27017/"+
		"?authMechanism=SCRAM-SHA-256&authSource=admin&connectTimeoutMS=20000&replicaSet=test-om-db&serverSelectionTimeoutMS=20000", cnx)

	// Connection parameters. The default one is overridden
	cnx = appdb.BuildConnectionURL("user", "passwd", connectionstring.SchemeMongoDB, map[string]string{"connectTimeoutMS": "30000", "readPreference": "secondary"}, nil)
	assert.Equal(t, "mongodb://user:passwd@test-om-db-0.test-om-db-svc.my-namespace.svc.cluster.local:27017,"+
		"test-om-db-1.test-om-db-svc.my-namespace.svc.cluster.local:27017,test-om-db-2.test-om-db-svc.my-namespace.svc.cluster.local:27017/"+
		"?authMechanism=SCRAM-SHA-256&authSource=admin&connectTimeoutMS=30000&readPreference=secondary&replicaSet=test-om-db&serverSelectionTimeoutMS=20000",
		cnx)
}

func TestMongoDB_ConnectionURL_OtherCluster_AppDB(t *testing.T) {
	opsManager := DefaultOpsManagerBuilder().SetClusterDomain("my-cluster").Build()
	appdb := opsManager.Spec.AppDB

	var cnx string
	cnx = appdb.BuildConnectionURL("user", "passwd", connectionstring.SchemeMongoDB, nil, nil)
	assert.Equal(t, "mongodb://user:passwd@test-om-db-0.test-om-db-svc.my-namespace.svc.my-cluster:27017,"+
		"test-om-db-1.test-om-db-svc.my-namespace.svc.my-cluster:27017,test-om-db-2.test-om-db-svc.my-namespace.svc.my-cluster:27017/"+
		"?authMechanism=SCRAM-SHA-256&authSource=admin&connectTimeoutMS=20000&replicaSet=test-om-db&serverSelectionTimeoutMS=20000", cnx)

	// Connection parameters. The default one is overridden
	cnx = appdb.BuildConnectionURL("user", "passwd", connectionstring.SchemeMongoDB, map[string]string{"connectTimeoutMS": "30000", "readPreference": "secondary"}, nil)
	assert.Equal(t, "mongodb://user:passwd@test-om-db-0.test-om-db-svc.my-namespace.svc.my-cluster:27017,"+
		"test-om-db-1.test-om-db-svc.my-namespace.svc.my-cluster:27017,test-om-db-2.test-om-db-svc.my-namespace.svc.my-cluster:27017/"+
		"?authMechanism=SCRAM-SHA-256&authSource=admin&connectTimeoutMS=30000&readPreference=secondary&replicaSet=test-om-db&serverSelectionTimeoutMS=20000",
		cnx)
}

// TestAutomationConfig_IsCreatedInSecret verifies that the automation config is created in a secret.
func TestAutomationConfig_IsCreatedInSecret(t *testing.T) {
	ctx := context.Background()
	builder := DefaultOpsManagerBuilder()
	opsManager := builder.Build()
	appdb := opsManager.Spec.AppDB
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(opsManager)
	reconciler, err := newAppDbReconciler(ctx, kubeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
	require.NoError(t, err)

	err = createOpsManagerUserPasswordSecret(ctx, kubeClient, opsManager, "MBPYfkAj5ZM0l9uw6C7ggw")
	assert.NoError(t, err)
	_, err = reconciler.ReconcileAppDB(ctx, opsManager)
	assert.NoError(t, err)

	s := corev1.Secret{}
	err = kubeClient.Get(ctx, kube.ObjectKey(opsManager.Namespace, appdb.AutomationConfigSecretName()), &s)
	assert.NoError(t, err, "The Automation Config was created in a secret.")
	assert.Contains(t, s.Data, automationconfig.ConfigKey)
}

// TestPublishAutomationConfigCreate verifies that the automation config map is created if it doesn't exist
func TestPublishAutomationConfigCreate(t *testing.T) {
	ctx := context.Background()
	builder := DefaultOpsManagerBuilder()
	opsManager := builder.Build()
	appdb := opsManager.Spec.AppDB
	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	kubeClient := mock.NewEmptyFakeClientWithInterceptor(omConnectionFactory)
	reconciler, err := newAppDbReconciler(ctx, kubeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
	require.NoError(t, err)

	memberCluster := multicluster.GetLegacyCentralMemberCluster(opsManager.Spec.Replicas, 0, reconciler.client, reconciler.SecretClient)
	automationConfig, err := buildAutomationConfigForAppDb(ctx, builder, kubeClient, omConnectionFactory.GetConnectionFunc, zap.S())
	assert.NoError(t, err)

	version, err := reconciler.publishAutomationConfig(ctx, opsManager, automationConfig, appdb.AutomationConfigSecretName(), memberCluster.SecretClient)
	assert.NoError(t, err)
	assert.Equal(t, 1, version)

	monitoringAutomationConfig, err := buildAutomationConfigForAppDb(ctx, builder, kubeClient, omConnectionFactory.GetConnectionFunc, zap.S())
	assert.NoError(t, err)
	version, err = reconciler.publishAutomationConfig(ctx, opsManager, monitoringAutomationConfig, monitoringAutomationConfigSecretName(appdb), memberCluster.SecretClient)
	assert.NoError(t, err)
	assert.Equal(t, 1, version)

	// verify the automation config secret for the automation agent
	acSecret := readAutomationConfigSecret(ctx, t, kubeClient, opsManager)
	checkDeploymentEqualToPublished(t, automationConfig, acSecret)

	// verify the automation config secret for the monitoring agent
	acMonitoringSecret := readAutomationConfigMonitoringSecret(ctx, t, kubeClient, opsManager)
	checkDeploymentEqualToPublished(t, monitoringAutomationConfig, acMonitoringSecret)

	assert.Len(t, mock.GetMapForObject(kubeClient, &corev1.Secret{}), 6)
	_, err = kubeClient.GetSecret(ctx, kube.ObjectKey(opsManager.Namespace, appdb.GetOpsManagerUserPasswordSecretName()))
	assert.NoError(t, err)

	_, err = kubeClient.GetSecret(ctx, kube.ObjectKey(opsManager.Namespace, appdb.GetAgentKeyfileSecretNamespacedName().Name))
	assert.NoError(t, err)

	_, err = kubeClient.GetSecret(ctx, kube.ObjectKey(opsManager.Namespace, appdb.GetAgentPasswordSecretNamespacedName().Name))
	assert.NoError(t, err)

	_, err = kubeClient.GetSecret(ctx, kube.ObjectKey(opsManager.Namespace, appdb.OpsManagerUserScramCredentialsName()))
	assert.NoError(t, err)

	_, err = kubeClient.GetSecret(ctx, kube.ObjectKey(opsManager.Namespace, appdb.AutomationConfigSecretName()))
	assert.NoError(t, err)

	_, err = kubeClient.GetSecret(ctx, kube.ObjectKey(opsManager.Namespace, monitoringAutomationConfigSecretName(appdb)))
	assert.NoError(t, err)

	// verifies Users and Roles are created
	assert.Len(t, automationConfig.Auth.Users, 1)

	expectedRoles := []string{"readWriteAnyDatabase", "dbAdminAnyDatabase", "clusterMonitor", "backup", "restore", "hostManager"}
	assert.Len(t, automationConfig.Auth.Users[0].Roles, len(expectedRoles))
	for idx, role := range expectedRoles {
		assert.Equal(t, automationConfig.Auth.Users[0].Roles[idx],
			automationconfig.Role{
				Role:     role,
				Database: "admin",
			})
	}
}

// TestPublishAutomationConfig_Update verifies that the automation config map is updated if it has changed
func TestPublishAutomationConfig_Update(t *testing.T) {
	ctx := context.Background()
	builder := DefaultOpsManagerBuilder()
	opsManager := builder.Build()
	appdb := opsManager.Spec.AppDB
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(opsManager)
	reconciler, err := newAppDbReconciler(ctx, kubeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
	require.NoError(t, err)

	// create
	_ = createOpsManagerUserPasswordSecret(ctx, kubeClient, opsManager, "MBPYfkAj5ZM0l9uw6C7ggw")
	_, err = reconciler.ReconcileAppDB(ctx, opsManager)
	assert.NoError(t, err)

	ac, err := automationconfig.ReadFromSecret(ctx, reconciler.client, kube.ObjectKey(opsManager.Namespace, appdb.AutomationConfigSecretName()))
	assert.NoError(t, err)
	assert.Equal(t, 1, ac.Version)

	// publishing the config without updates should not result in API call
	_, err = reconciler.ReconcileAppDB(ctx, opsManager)
	assert.NoError(t, err)

	ac, err = automationconfig.ReadFromSecret(ctx, reconciler.client, kube.ObjectKey(opsManager.Namespace, appdb.AutomationConfigSecretName()))
	assert.NoError(t, err)
	assert.Equal(t, 1, ac.Version)

	// publishing changed config will result in update
	fcv := "4.4"
	err = reconciler.client.Get(ctx, kube.ObjectKeyFromApiObject(opsManager), opsManager)
	require.NoError(t, err)

	opsManager.Spec.AppDB.FeatureCompatibilityVersion = &fcv
	err = kubeClient.Update(ctx, opsManager)
	assert.NoError(t, err)

	_, err = reconciler.ReconcileAppDB(ctx, opsManager)
	assert.NoError(t, err)

	ac, err = automationconfig.ReadFromSecret(ctx, reconciler.client, kube.ObjectKey(opsManager.Namespace, appdb.AutomationConfigSecretName()))
	assert.NoError(t, err)
	assert.Equal(t, 2, ac.Version)
}

// TestBuildAppDbAutomationConfig checks that the automation config is built correctly
func TestBuildAppDbAutomationConfig(t *testing.T) {
	ctx := context.Background()
	logRotateConfig := &automationconfig.CrdLogRotate{
		SizeThresholdMB: "1",
	}
	builder := DefaultOpsManagerBuilder().
		SetAppDbMembers(2).
		SetAppDbVersion("4.2.11-ent").
		SetAppDbFeatureCompatibility("4.0").
		SetLogRotate(logRotateConfig).
		SetSystemLog(&automationconfig.SystemLog{
			Destination: automationconfig.File,
			Path:        "/tmp/test",
		})

	om := builder.Build()

	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(om)
	err := createOpsManagerUserPasswordSecret(ctx, kubeClient, om, "omPass")
	assert.NoError(t, err)

	automationConfig, err := buildAutomationConfigForAppDb(ctx, builder, kubeClient, omConnectionFactory.GetConnectionFunc, zap.S())
	assert.NoError(t, err)
	// processes
	assert.Len(t, automationConfig.Processes, 2)
	assert.Equal(t, "4.2.11-ent", automationConfig.Processes[0].Version)
	assert.Equal(t, "test-om-db-0.test-om-db-svc.my-namespace.svc.cluster.local", automationConfig.Processes[0].HostName)
	assert.Equal(t, "4.0", automationConfig.Processes[0].FeatureCompatibilityVersion)
	assert.Equal(t, "4.2.11-ent", automationConfig.Processes[1].Version)
	assert.Equal(t, "test-om-db-1.test-om-db-svc.my-namespace.svc.cluster.local", automationConfig.Processes[1].HostName)
	assert.Equal(t, "4.0", automationConfig.Processes[1].FeatureCompatibilityVersion)
	assert.Equal(t, automationconfig.ConvertCrdLogRotateToAC(logRotateConfig), automationConfig.Processes[0].LogRotate)
	assert.Equal(t, "/tmp/test", automationConfig.Processes[0].Args26.Get("systemLog.path").String())
	assert.Equal(t, "file", automationConfig.Processes[0].Args26.Get("systemLog.destination").String())

	// replicasets
	assert.Len(t, automationConfig.ReplicaSets, 1)
	db := builder.Build().Spec.AppDB
	assert.Equal(t, db.Name(), automationConfig.ReplicaSets[0].Id)

	// monitoring agent has been configured
	assert.Len(t, automationConfig.MonitoringVersions, 0)

	// backup agents have not been configured
	assert.Len(t, automationConfig.BackupVersions, 0)

	// options
	assert.Equal(t, automationconfig.Options{DownloadBase: util.AgentDownloadsDir}, automationConfig.Options)
}

func TestBuildAppDbAutomationConfig_MonitoringNotEmbeddedInAC(t *testing.T) {
	// OPS_MANAGER_MONITOR_APPDB defaults true; set explicitly to avoid env-state sensitivity
	t.Setenv(util.OpsManagerMonitorAppDB, "true")
	ctx := context.Background()
	builder := DefaultOpsManagerBuilder()
	opsManager := builder.Build()
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(opsManager)
	reconciler, err := newAppDbReconciler(ctx, kubeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
	require.NoError(t, err)
	_ = createOpsManagerUserPasswordSecret(ctx, kubeClient, opsManager, "my-password")

	podVars := &env.PodEnvVars{
		ProjectID:   "abc123",
		AgentAPIKey: "my-api-key",
	}

	ac, err := reconciler.buildAppDbAutomationConfig(ctx, opsManager, podVars, "", multicluster.LegacyCentralClusterName, zap.S())
	require.NoError(t, err)

	require.NotEmpty(t, ac.MonitoringVersions)
	// Under Option B the credentials are delivered to the agent as CLI flags, not via additionalParams.
	assert.NotContains(t, ac.MonitoringVersions[0].AdditionalParams, "mmsGroupId")
	assert.NotContains(t, ac.MonitoringVersions[0].AdditionalParams, "mmsApiKey")
	require.NotNil(t, ac.MonitoringVersions[0].LogRotate)
	assert.Equal(t, 1000, ac.MonitoringVersions[0].LogRotate.SizeThresholdMB)
	assert.Equal(t, 24, ac.MonitoringVersions[0].LogRotate.TimeThresholdHrs)
	assert.Equal(t, monitoringAgentLogFile, ac.MonitoringVersions[0].LogPath)
}

func TestBuildAppDbAutomationConfig_NoMonitoringWhenDisabled(t *testing.T) {
	t.Setenv(util.OpsManagerMonitorAppDB, "false")
	ctx := context.Background()
	builder := DefaultOpsManagerBuilder()
	opsManager := builder.Build()
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(opsManager)
	reconciler, err := newAppDbReconciler(ctx, kubeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
	require.NoError(t, err)
	_ = createOpsManagerUserPasswordSecret(ctx, kubeClient, opsManager, "my-password")

	// nil podVars => ShouldEnableMonitoring returns false
	ac, err := reconciler.buildAppDbAutomationConfig(ctx, opsManager, nil, "", multicluster.LegacyCentralClusterName, zap.S())
	require.NoError(t, err)

	assert.Empty(t, ac.MonitoringVersions)
}

func TestBuildAppDbAutomationConfig_MonitoringLogRotateCustom(t *testing.T) {
	t.Setenv(util.OpsManagerMonitorAppDB, "true")
	ctx := context.Background()
	builder := DefaultOpsManagerBuilder()
	opsManager := builder.Build()
	opsManager.Spec.AppDB.AutomationAgent.MonitoringAgent.LogRotate = &mdbv1.LogRotateForBackupAndMonitoring{
		SizeThresholdMB:  500,
		TimeThresholdHrs: 12,
	}
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(opsManager)
	reconciler, err := newAppDbReconciler(ctx, kubeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
	require.NoError(t, err)
	_ = createOpsManagerUserPasswordSecret(ctx, kubeClient, opsManager, "my-password")

	podVars := &env.PodEnvVars{
		ProjectID:   "abc123",
		AgentAPIKey: "my-api-key",
	}

	ac, err := reconciler.buildAppDbAutomationConfig(ctx, opsManager, podVars, "", multicluster.LegacyCentralClusterName, zap.S())
	require.NoError(t, err)

	require.NotEmpty(t, ac.MonitoringVersions)
	require.NotNil(t, ac.MonitoringVersions[0].LogRotate)
	assert.Equal(t, 500, ac.MonitoringVersions[0].LogRotate.SizeThresholdMB)
	assert.Equal(t, 12, ac.MonitoringVersions[0].LogRotate.TimeThresholdHrs)
	assert.Equal(t, monitoringAgentLogFile, ac.MonitoringVersions[0].LogPath)
}

func TestMonitoringToggledOff_ClearsMonitoringVersions(t *testing.T) {
	t.Setenv(util.OpsManagerMonitorAppDB, "false")
	ctx := context.Background()
	builder := DefaultOpsManagerBuilder()
	opsManager := builder.Build()
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(opsManager)
	reconciler, err := newAppDbReconciler(ctx, kubeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
	require.NoError(t, err)
	_ = createOpsManagerUserPasswordSecret(ctx, kubeClient, opsManager, "my-password")

	// Non-nil podVars with a project ID — monitoring is disabled via the env var, not nil podVars
	podVars := &env.PodEnvVars{
		ProjectID:   "abc123",
		AgentAPIKey: "some-key",
	}

	ac, err := reconciler.buildAppDbAutomationConfig(ctx, opsManager, podVars, "", multicluster.LegacyCentralClusterName, zap.S())
	require.NoError(t, err)
	assert.Empty(t, ac.MonitoringVersions, "MonitoringVersions must be empty when monitoring env var is false")
}

func TestMonitoringToggledBackOn_PopulatesMonitoringVersions(t *testing.T) {
	ctx := context.Background()
	builder := DefaultOpsManagerBuilder()
	opsManager := builder.Build()
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(opsManager)
	reconciler, err := newAppDbReconciler(ctx, kubeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
	require.NoError(t, err)
	_ = createOpsManagerUserPasswordSecret(ctx, kubeClient, opsManager, "my-password")

	podVars := &env.PodEnvVars{
		ProjectID:   "abc123",
		AgentAPIKey: "re-enabled-key",
	}

	ac, err := reconciler.buildAppDbAutomationConfig(ctx, opsManager, podVars, "", multicluster.LegacyCentralClusterName, zap.S())
	require.NoError(t, err)
	require.NotEmpty(t, ac.MonitoringVersions, "MonitoringVersions must be populated when monitoring is re-enabled")
	assert.Equal(t, "abc123", podVars.ProjectID)
	// Credentials are no longer embedded in additionalParams under Option B.
	assert.NotContains(t, ac.MonitoringVersions[0].AdditionalParams, "mmsGroupId")
	assert.NotContains(t, ac.MonitoringVersions[0].AdditionalParams, "mmsApiKey")
}

func TestRegisterAppDBHostsWithProject(t *testing.T) {
	ctx := context.Background()
	builder := DefaultOpsManagerBuilder()
	opsManager := builder.Build()
	omConnectionFactory := om.NewCachedOMConnectionFactoryWithInitializedConnection(om.NewMockedOmConnection(om.NewDeployment()))
	fakeClient := mock.NewEmptyFakeClientBuilder().Build()
	fakeClient = interceptor.NewClient(fakeClient, interceptor.Funcs{
		Get: func(ctx context.Context, client client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			return mock.GetFakeClientInterceptorGetFunc(omConnectionFactory, true, false)(ctx, client, key, obj, opts...)
		},
	})
	reconciler, err := newAppDbReconciler(ctx, fakeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
	require.NoError(t, err)

	t.Run("Ensure all hosts are added", func(t *testing.T) {
		_, err = reconciler.ReconcileAppDB(ctx, opsManager)

		hostnames := reconciler.getCurrentStatefulsetHostnames(opsManager)
		err = reconciler.registerAppDBHostsWithProject(hostnames, omConnectionFactory.GetConnection(), "password", zap.S())
		assert.NoError(t, err)

		hosts, _ := omConnectionFactory.GetConnection().(*om.MockedOmConnection).GetHosts()
		assert.Len(t, hosts.Results, 3)
	})

	t.Run("Ensure hosts are added when scaled up", func(t *testing.T) {
		opsManager.Spec.AppDB.Members = 5
		_, err = reconciler.ReconcileAppDB(ctx, opsManager)

		hostnames := reconciler.getCurrentStatefulsetHostnames(opsManager)
		err = reconciler.registerAppDBHostsWithProject(hostnames, omConnectionFactory.GetConnection(), "password", zap.S())
		assert.NoError(t, err)

		hosts, _ := omConnectionFactory.GetConnection().GetHosts()
		assert.Len(t, hosts.Results, 5)
	})

	t.Run("Ensure hosts are removed when scaled down", func(t *testing.T) {
		opsManager.Spec.AppDB.Members = 3
		_, err = reconciler.ReconcileAppDB(ctx, opsManager)

		hostnames := reconciler.getCurrentStatefulsetHostnames(opsManager)
		err = reconciler.registerAppDBHostsWithProject(hostnames, omConnectionFactory.GetConnection(), "password", zap.S())
		assert.NoError(t, err)

		// After scale-down, hosts should be removed from monitoring
		hosts, _ := omConnectionFactory.GetConnection().GetHosts()
		assert.Len(t, hosts.Results, 3, "Expected 3 hosts after scaling down from 5 to 3 members")
	})
}

func TestEnsureAppDbAgentApiKey(t *testing.T) {
	ctx := context.Background()
	builder := DefaultOpsManagerBuilder()
	opsManager := builder.Build()
	// we need to pre-initialize connection as we don't call full reconciler in this test and connection is never created by calling connection factory func
	omConnectionFactory := om.NewCachedOMConnectionFactoryWithInitializedConnection(om.NewMockedOmConnection(om.NewDeployment()))
	fakeClient := mock.NewDefaultFakeClientWithOMConnectionFactory(omConnectionFactory)
	reconciler, err := newAppDbReconciler(ctx, fakeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
	require.NoError(t, err)

	omConnectionFactory.GetConnection().(*om.MockedOmConnection).AgentAPIKey = "my-api-key"
	_, err = reconciler.ensureAppDbAgentApiKey(ctx, opsManager, omConnectionFactory.GetConnection(), omConnectionFactory.GetConnection().GroupID(), zap.S())
	assert.NoError(t, err)

	secretName := agents.ApiKeySecretName(omConnectionFactory.GetConnection().GroupID())
	apiKey, err := secret.ReadKey(ctx, reconciler.client, util.OmAgentApiKey, kube.ObjectKey(opsManager.Namespace, secretName))
	assert.NoError(t, err)
	assert.Equal(t, "my-api-key", apiKey)
}

func TestTryConfigureMonitoringInOpsManager(t *testing.T) {
	ctx := context.Background()
	builder := DefaultOpsManagerBuilder()
	opsManager := builder.Build()
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient()
	appdbScaler := scalers.GetAppDBScaler(opsManager, multicluster.LegacyCentralClusterName, 0, nil)
	reconciler, err := newAppDbReconciler(ctx, kubeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
	require.NoError(t, err)

	// attempt configuring monitoring when there is no api key secret
	podVars, err := reconciler.tryConfigureMonitoringInOpsManager(ctx, opsManager, "password", "/fake/agent-cert/path", zap.S())
	assert.NoError(t, err)

	assert.Empty(t, podVars.ProjectID)
	assert.Empty(t, podVars.User)

	opsManager.Spec.AppDB.Members = 5
	appDbSts, err := construct.AppDbStatefulSet(*opsManager, &podVars, construct.AppDBStatefulSetOptions{}, appdbScaler, appsv1.OnDeleteStatefulSetStrategyType, architectures.NonStatic, zap.S())
	assert.NoError(t, err)

	assert.Nil(t, findVolumeByName(appDbSts.Spec.Template.Spec.Volumes, construct.AgentAPIKeyVolumeName))

	_ = kubeClient.Update(ctx, &appDbSts)

	data := map[string]string{
		util.OmPublicApiKey: "publicApiKey",
		util.OmPrivateKey:   "privateApiKey",
	}
	APIKeySecretName, err := opsManager.APIKeySecretName(ctx, secrets.SecretClient{KubeClient: kubeClient}, "")
	assert.NoError(t, err)

	apiKeySecret := secret.Builder().
		SetNamespace(operatorNamespace()).
		SetName(APIKeySecretName).
		SetStringMapToData(data).
		Build()

	err = reconciler.client.CreateSecret(ctx, apiKeySecret)
	assert.NoError(t, err)

	// once the secret exists, monitoring should be fully configured
	podVars, err = reconciler.tryConfigureMonitoringInOpsManager(ctx, opsManager, "password", "/fake/agent-cert/path", zap.S())
	assert.NoError(t, err)

	assert.Equal(t, om.TestGroupID, podVars.ProjectID)
	assert.Equal(t, "publicApiKey", podVars.User)

	expectedHostnames := []string{
		"test-om-db-0.test-om-db-svc.my-namespace.svc.cluster.local",
		"test-om-db-1.test-om-db-svc.my-namespace.svc.cluster.local",
		"test-om-db-2.test-om-db-svc.my-namespace.svc.cluster.local",
		"test-om-db-3.test-om-db-svc.my-namespace.svc.cluster.local",
		"test-om-db-4.test-om-db-svc.my-namespace.svc.cluster.local",
	}

	assertExpectedHostnamesAndPreferred(t, omConnectionFactory.GetConnection().(*om.MockedOmConnection), expectedHostnames)

	appDbSts, err = construct.AppDbStatefulSet(*opsManager, &podVars, construct.AppDBStatefulSetOptions{}, appdbScaler, appsv1.OnDeleteStatefulSetStrategyType, architectures.NonStatic, zap.S())
	assert.NoError(t, err)

	// Monitoring is enabled now (ProjectID populated), so the agent-api-key volume must be mounted
	// to feed the -mmsApiKey CLI flag via ${AGENT_API_KEY}.
	assert.NotNil(t, findVolumeByName(appDbSts.Spec.Template.Spec.Volumes, construct.AgentAPIKeyVolumeName))
}

func TestTryConfigureMonitoring_PopulatesAgentAPIKey(t *testing.T) {
	ctx := context.Background()
	builder := DefaultOpsManagerBuilder()
	opsManager := builder.Build()
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient()
	reconciler, err := newAppDbReconciler(ctx, kubeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
	require.NoError(t, err)

	// Create the OM API key secret (required by tryConfigureMonitoringInOpsManager)
	APIKeySecretName, err := opsManager.APIKeySecretName(ctx, secrets.SecretClient{KubeClient: kubeClient}, "")
	require.NoError(t, err)
	apiKeySecret := secret.Builder().
		SetNamespace(operatorNamespace()).
		SetName(APIKeySecretName).
		SetStringMapToData(map[string]string{util.OmPublicApiKey: "publicApiKey", util.OmPrivateKey: "privateApiKey"}).
		Build()
	err = reconciler.client.CreateSecret(ctx, apiKeySecret)
	require.NoError(t, err)

	podVars, err := reconciler.tryConfigureMonitoringInOpsManager(ctx, opsManager, "password", "/fake/cert", zap.S())
	require.NoError(t, err)
	// MockedOmConnection.AgentAPIKey is om.TestAgentKey by default
	assert.Equal(t, om.TestAgentKey, podVars.AgentAPIKey)
}

// TestTryConfigureMonitoringInOpsManagerWithCustomTemplate runs different scenarios with activating monitoring and pod templates
func TestTryConfigureMonitoringInOpsManagerWithCustomTemplate(t *testing.T) {
	builder := DefaultOpsManagerBuilder()
	opsManager := builder.Build()
	appdbScaler := scalers.GetAppDBScaler(opsManager, multicluster.LegacyCentralClusterName, 0, nil)

	opsManager.Spec.AppDB.PodSpec.PodTemplateWrapper = v1.PodTemplateSpecWrapper{
		PodTemplate: &corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "mongodb-agent",
						Image: "quay.io/mongodb/mongodb-agent:10",
					},
					{
						Name:  "mongod",
						Image: "quay.io/mongodb/mongodb:10",
					},
					{
						Name:  "mongodb-agent-monitoring",
						Image: "quay.io/mongodb/mongodb-agent:20",
					},
				},
			},
		},
	}

	t.Run("do not override images while activating monitoring", func(t *testing.T) {
		podVars := env.PodEnvVars{ProjectID: "something"}
		appDbSts, err := construct.AppDbStatefulSet(*opsManager, &podVars, construct.AppDBStatefulSetOptions{}, appdbScaler, appsv1.OnDeleteStatefulSetStrategyType, architectures.NonStatic, zap.S())
		assert.NoError(t, err)
		assert.NotNil(t, appDbSts)

		foundImages := 0
		for _, c := range appDbSts.Spec.Template.Spec.Containers {
			if c.Name == "mongodb-agent" {
				assert.Equal(t, "quay.io/mongodb/mongodb-agent:10", c.Image)
				foundImages += 1
			}
			if c.Name == "mongod" {
				assert.Equal(t, "quay.io/mongodb/mongodb:10", c.Image)
				foundImages += 1
			}
		}

		// monitoring container is stripped from user-supplied templates; only agent + mongod remain
		assert.Equal(t, 2, foundImages)
		assert.Equal(t, 2, len(appDbSts.Spec.Template.Spec.Containers))
	})

	t.Run("do not override images, but remove monitoring if not activated", func(t *testing.T) {
		podVars := env.PodEnvVars{}
		appDbSts, err := construct.AppDbStatefulSet(*opsManager, &podVars, construct.AppDBStatefulSetOptions{}, appdbScaler, appsv1.OnDeleteStatefulSetStrategyType, architectures.NonStatic, zap.S())
		assert.NoError(t, err)
		assert.NotNil(t, appDbSts)

		foundImages := 0
		for _, c := range appDbSts.Spec.Template.Spec.Containers {
			if c.Name == "mongodb-agent" {
				assert.Equal(t, "quay.io/mongodb/mongodb-agent:10", c.Image)
				foundImages += 1
			}
			if c.Name == "mongod" {
				assert.Equal(t, "quay.io/mongodb/mongodb:10", c.Image)
				foundImages += 1
			}
		}

		assert.Equal(t, 2, foundImages)
		assert.Equal(t, 2, len(appDbSts.Spec.Template.Spec.Containers))
	})
}

func TestTryConfigureMonitoringInOpsManagerWithExternalDomains(t *testing.T) {
	ctx := context.Background()
	opsManager := DefaultOpsManagerBuilder().
		SetAppDbExternalAccess(mdbv1.ExternalAccessConfiguration{
			ExternalDomain: ptr.To("custom.domain"),
		}).Build()
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient()
	appdbScaler := scalers.GetAppDBScaler(opsManager, multicluster.LegacyCentralClusterName, 0, nil)
	reconciler, err := newAppDbReconciler(ctx, kubeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
	require.NoError(t, err)

	// attempt configuring monitoring when there is no api key secret
	podVars, err := reconciler.tryConfigureMonitoringInOpsManager(ctx, opsManager, "password", "/fake/agent-cert/path", zap.S())
	assert.NoError(t, err)

	assert.Empty(t, podVars.ProjectID)
	assert.Empty(t, podVars.User)

	opsManager.Spec.AppDB.Members = 5
	appDbSts, err := construct.AppDbStatefulSet(*opsManager, &podVars, construct.AppDBStatefulSetOptions{}, appdbScaler, appsv1.OnDeleteStatefulSetStrategyType, architectures.NonStatic, zap.S())
	assert.NoError(t, err)

	assert.Nil(t, findVolumeByName(appDbSts.Spec.Template.Spec.Volumes, construct.AgentAPIKeyVolumeName))

	_ = kubeClient.Update(ctx, &appDbSts)

	data := map[string]string{
		util.OmPublicApiKey: "publicApiKey",
		util.OmPrivateKey:   "privateApiKey",
	}
	APIKeySecretName, err := opsManager.APIKeySecretName(ctx, secrets.SecretClient{KubeClient: kubeClient}, "")
	assert.NoError(t, err)

	apiKeySecret := secret.Builder().
		SetNamespace(operatorNamespace()).
		SetName(APIKeySecretName).
		SetStringMapToData(data).
		Build()

	err = reconciler.client.CreateSecret(ctx, apiKeySecret)
	assert.NoError(t, err)

	// once the secret exists, monitoring should be fully configured
	podVars, err = reconciler.tryConfigureMonitoringInOpsManager(ctx, opsManager, "password", "/fake/agent-cert/path", zap.S())
	assert.NoError(t, err)

	assert.Equal(t, om.TestGroupID, podVars.ProjectID)
	assert.Equal(t, "publicApiKey", podVars.User)

	expectedHostnames := []string{
		"test-om-db-0.custom.domain",
		"test-om-db-1.custom.domain",
		"test-om-db-2.custom.domain",
		"test-om-db-3.custom.domain",
		"test-om-db-4.custom.domain",
	}

	assertExpectedHostnamesAndPreferred(t, omConnectionFactory.GetConnection().(*om.MockedOmConnection), expectedHostnames)

	appDbSts, err = construct.AppDbStatefulSet(*opsManager, &podVars, construct.AppDBStatefulSetOptions{}, appdbScaler, appsv1.OnDeleteStatefulSetStrategyType, architectures.NonStatic, zap.S())
	assert.NoError(t, err)

	// Monitoring is enabled now (ProjectID populated), so the agent-api-key volume must be mounted.
	assert.NotNil(t, findVolumeByName(appDbSts.Spec.Template.Spec.Volumes, construct.AgentAPIKeyVolumeName))
}

func TestTryConfigureMonitoringInOpsManagerWithMalformedCredentials(t *testing.T) {
	ctx := context.Background()
	builder := DefaultOpsManagerBuilder().SetAppDbMembers(5)
	opsManager := builder.Build()
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient()
	reconciler, err := newAppDbReconciler(ctx, kubeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
	require.NoError(t, err)

	data := map[string]string{
		util.OmPublicApiKey + "Malformed": "publicApiKey",
		util.OmPrivateKey:                 "privateApiKey",
	}
	APIKeySecretName, err := opsManager.APIKeySecretName(ctx, secrets.SecretClient{KubeClient: kubeClient}, "")
	assert.NoError(t, err)

	apiKeySecret := secret.Builder().
		SetNamespace(operatorNamespace()).
		SetName(APIKeySecretName).
		SetStringMapToData(data).
		Build()

	err = reconciler.client.CreateSecret(ctx, apiKeySecret)
	assert.NoError(t, err)

	// the secret is malformed and tryConfigureMonitoringInOpsManager fails
	podVars, err := reconciler.tryConfigureMonitoringInOpsManager(ctx, opsManager, "password", "/fake/agent-cert/path", zap.S())
	assert.Error(t, err)
	assert.ErrorContains(t, err, "error reading opsManager credentials")
	assert.Empty(t, podVars)

	updatedData := map[string]string{
		util.OmPublicApiKey: "publicApiKey",
		util.OmPrivateKey:   "privateApiKey",
	}

	updatedApiKeySecret := secret.Builder().
		SetNamespace(operatorNamespace()).
		SetName(APIKeySecretName).
		SetStringMapToData(updatedData).
		Build()

	err = reconciler.client.UpdateSecret(ctx, updatedApiKeySecret)
	assert.NoError(t, err)

	// the secret is correct and tryConfigureMonitoringInOpsManager succeeds
	podVars, err = reconciler.tryConfigureMonitoringInOpsManager(ctx, opsManager, "password", "/fake/agent-cert/path", zap.S())
	assert.NoError(t, err)
	assert.NotEmpty(t, podVars)
	assert.Equal(t, om.TestGroupID, podVars.ProjectID)
	assert.Equal(t, "publicApiKey", podVars.User)
}

func TestReadExistingPodVars_ReturnsAgentAPIKey(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-123"
	agentKey := "fallback-agent-key"

	builder := DefaultOpsManagerBuilder()
	opsManager := builder.Build()
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient()
	reconciler, err := newAppDbReconciler(ctx, kubeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
	require.NoError(t, err)

	// 1. Create projectID configmap
	err = reconciler.ensureProjectIDConfigMapForCluster(ctx, opsManager, projectID, reconciler.client)
	require.NoError(t, err)

	// 2. Create OM API key secret (needed by ReadCredentials for User field)
	APIKeySecretName, err := opsManager.APIKeySecretName(ctx, secrets.SecretClient{KubeClient: kubeClient}, "")
	require.NoError(t, err)
	apiKeySecret := secret.Builder().
		SetNamespace(operatorNamespace()).
		SetName(APIKeySecretName).
		SetStringMapToData(map[string]string{util.OmPublicApiKey: "publicApiKey", util.OmPrivateKey: "privateApiKey"}).
		Build()
	err = reconciler.client.CreateSecret(ctx, apiKeySecret)
	require.NoError(t, err)

	// 3. Create agent API key secret
	agentKeySecret := secret.Builder().
		SetNamespace(opsManager.Namespace).
		SetName(agents.ApiKeySecretName(projectID)).
		SetStringMapToData(map[string]string{util.OmAgentApiKey: agentKey}).
		Build()
	err = reconciler.client.CreateSecret(ctx, agentKeySecret)
	require.NoError(t, err)

	podVars, err := reconciler.readExistingPodVars(ctx, opsManager, zap.S())
	require.NoError(t, err)
	assert.Equal(t, agentKey, podVars.AgentAPIKey)
	assert.Equal(t, projectID, podVars.ProjectID)
}

func TestReadExistingPodVars_MissingAgentKeyIsNonFatal(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-123"

	builder := DefaultOpsManagerBuilder()
	opsManager := builder.Build()
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient()
	reconciler, err := newAppDbReconciler(ctx, kubeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
	require.NoError(t, err)

	// projectID configmap exists (so the function gets past the projectId guard)...
	err = reconciler.ensureProjectIDConfigMapForCluster(ctx, opsManager, projectID, reconciler.client)
	require.NoError(t, err)

	// OM API key secret exists (needed by ReadCredentials for the User field)...
	APIKeySecretName, err := opsManager.APIKeySecretName(ctx, secrets.SecretClient{KubeClient: kubeClient}, "")
	require.NoError(t, err)
	apiKeySecret := secret.Builder().
		SetNamespace(operatorNamespace()).
		SetName(APIKeySecretName).
		SetStringMapToData(map[string]string{util.OmPublicApiKey: "publicApiKey", util.OmPrivateKey: "privateApiKey"}).
		Build()
	require.NoError(t, reconciler.client.CreateSecret(ctx, apiKeySecret))

	// ...but the agent API key secret is intentionally NOT created.
	podVars, err := reconciler.readExistingPodVars(ctx, opsManager, zap.S())
	require.NoError(t, err)
	assert.Empty(t, podVars.AgentAPIKey, "missing agent key must not be fatal")
	assert.Equal(t, projectID, podVars.ProjectID)
}

func TestAppDBServiceCreation_WithExternalName(t *testing.T) {
	tests := map[string]struct {
		members                int
		externalAccess         *mdbv1.ExternalAccessConfiguration
		additionalMongodConfig *mdbv1.AdditionalMongodConfig
		result                 map[int]corev1.Service
	}{
		"empty external access configured for one pod": {
			members:        1,
			externalAccess: &mdbv1.ExternalAccessConfiguration{},
			result: map[int]corev1.Service{
				0: {
					ObjectMeta: metav1.ObjectMeta{
						Name:            "test-om-db-0-svc-external",
						Namespace:       "my-namespace",
						ResourceVersion: "1",
						Labels: map[string]string{
							util.OperatorLabelName:         util.OperatorLabelValue,
							appsv1.StatefulSetPodNameLabel: "test-om-db-0",
							omv1.LabelResourceOwner:        "test-om",
						},
					},
					Spec: corev1.ServiceSpec{
						Type:                     "LoadBalancer",
						PublishNotReadyAddresses: true,
						Ports: []corev1.ServicePort{
							{
								Name: "mongodb",
								Port: 27017,
							},
						},
						Selector: map[string]string{
							util.OperatorLabelName:         util.OperatorLabelValue,
							appsv1.StatefulSetPodNameLabel: "test-om-db-0",
						},
					},
				},
			},
		},
		"external access configured for two pods": {
			members: 2,
			externalAccess: &mdbv1.ExternalAccessConfiguration{
				ExternalService: mdbv1.ExternalServiceConfiguration{
					SpecWrapper: &v1.ServiceSpecWrapper{
						Spec: corev1.ServiceSpec{
							Type: "LoadBalancer",
							Ports: []corev1.ServicePort{
								{
									Name: "mongodb",
									Port: 27017,
								},
								{
									Name: "backup",
									Port: 27018,
								},
								{
									Name: "testing2",
									Port: 27019,
								},
							},
						},
					},
				},
			},
			result: map[int]corev1.Service{
				0: {
					ObjectMeta: metav1.ObjectMeta{
						Name:            "test-om-db-0-svc-external",
						Namespace:       "my-namespace",
						ResourceVersion: "1",
						Labels: map[string]string{
							util.OperatorLabelName:         util.OperatorLabelValue,
							appsv1.StatefulSetPodNameLabel: "test-om-db-0",
							omv1.LabelResourceOwner:        "test-om",
						},
					},
					Spec: corev1.ServiceSpec{
						Type:                     "LoadBalancer",
						PublishNotReadyAddresses: true,
						Ports: []corev1.ServicePort{
							{
								Name: "mongodb",
								Port: 27017,
							},
							{
								Name: "backup",
								Port: 27018,
							},
							{
								Name: "testing2",
								Port: 27019,
							},
						},
						Selector: map[string]string{
							util.OperatorLabelName:         util.OperatorLabelValue,
							appsv1.StatefulSetPodNameLabel: "test-om-db-0",
						},
					},
				},
				1: {
					ObjectMeta: metav1.ObjectMeta{
						Name:            "test-om-db-1-svc-external",
						Namespace:       "my-namespace",
						ResourceVersion: "1",
						Labels: map[string]string{
							util.OperatorLabelName:         util.OperatorLabelValue,
							appsv1.StatefulSetPodNameLabel: "test-om-db-1",
							omv1.LabelResourceOwner:        "test-om",
						},
					},
					Spec: corev1.ServiceSpec{
						Type:                     "LoadBalancer",
						PublishNotReadyAddresses: true,
						Ports: []corev1.ServicePort{
							{
								Name: "mongodb",
								Port: 27017,
							},
							{
								Name: "backup",
								Port: 27018,
							},
							{
								Name: "testing2",
								Port: 27019,
							},
						},
						Selector: map[string]string{
							util.OperatorLabelName:         util.OperatorLabelValue,
							appsv1.StatefulSetPodNameLabel: "test-om-db-1",
						},
					},
				},
			},
		},
		"external domain configured for single pod in first cluster": {
			members: 1,
			externalAccess: &mdbv1.ExternalAccessConfiguration{
				ExternalDomain: ptr.To("some.domain"),
			},
			result: map[int]corev1.Service{
				0: {
					ObjectMeta: metav1.ObjectMeta{
						Name:            "test-om-db-0-svc-external",
						Namespace:       "my-namespace",
						ResourceVersion: "1",
						Labels: map[string]string{
							util.OperatorLabelName:         util.OperatorLabelValue,
							appsv1.StatefulSetPodNameLabel: "test-om-db-0",
							omv1.LabelResourceOwner:        "test-om",
						},
					},
					Spec: corev1.ServiceSpec{
						Type:                     "LoadBalancer",
						PublishNotReadyAddresses: true,
						Ports: []corev1.ServicePort{
							{
								Name: "mongodb",
								Port: 27017,
							},
						},
						Selector: map[string]string{
							util.OperatorLabelName:         util.OperatorLabelValue,
							appsv1.StatefulSetPodNameLabel: "test-om-db-0",
						},
					},
				},
			},
		},
		"non default port set in additional mongod config": {
			members:                1,
			externalAccess:         &mdbv1.ExternalAccessConfiguration{},
			additionalMongodConfig: mdbv1.NewAdditionalMongodConfig("net.port", 27027),
			result: map[int]corev1.Service{
				0: {
					ObjectMeta: metav1.ObjectMeta{
						Name:            "test-om-db-0-svc-external",
						Namespace:       "my-namespace",
						ResourceVersion: "1",
						Labels: map[string]string{
							util.OperatorLabelName:         util.OperatorLabelValue,
							appsv1.StatefulSetPodNameLabel: "test-om-db-0",
							omv1.LabelResourceOwner:        "test-om",
						},
					},
					Spec: corev1.ServiceSpec{
						Type:                     "LoadBalancer",
						PublishNotReadyAddresses: true,
						Ports: []corev1.ServicePort{
							{
								Name: "mongodb",
								Port: 27027,
							},
						},
						Selector: map[string]string{
							util.OperatorLabelName:         util.OperatorLabelValue,
							appsv1.StatefulSetPodNameLabel: "test-om-db-0",
						},
					},
				},
			},
		},
		"external service of NodePort type set in first cluster": {
			members: 1,
			externalAccess: &mdbv1.ExternalAccessConfiguration{
				ExternalService: mdbv1.ExternalServiceConfiguration{
					SpecWrapper: &v1.ServiceSpecWrapper{
						Spec: corev1.ServiceSpec{
							Type: "NodePort",
							Ports: []corev1.ServicePort{
								{
									Name:     "mongodb",
									Port:     27017,
									NodePort: 30003,
								},
							},
						},
					},
				},
			},
			result: map[int]corev1.Service{
				0: {
					ObjectMeta: metav1.ObjectMeta{
						Name:            "test-om-db-0-svc-external",
						Namespace:       "my-namespace",
						ResourceVersion: "1",
						Labels: map[string]string{
							util.OperatorLabelName:         util.OperatorLabelValue,
							appsv1.StatefulSetPodNameLabel: "test-om-db-0",
							omv1.LabelResourceOwner:        "test-om",
						},
					},
					Spec: corev1.ServiceSpec{
						Type:                     "NodePort",
						PublishNotReadyAddresses: true,
						Ports: []corev1.ServicePort{
							{
								Name:     "mongodb",
								Port:     27017,
								NodePort: 30003,
							},
						},
						Selector: map[string]string{
							util.OperatorLabelName:         util.OperatorLabelValue,
							appsv1.StatefulSetPodNameLabel: "test-om-db-0",
						},
					},
				},
			},
		},
		"service with annotations with placeholders": {
			members: 2,
			externalAccess: &mdbv1.ExternalAccessConfiguration{
				ExternalService: mdbv1.ExternalServiceConfiguration{
					Annotations: map[string]string{
						create.PlaceholderPodIndex:            "{podIndex}",
						create.PlaceholderNamespace:           "{namespace}",
						create.PlaceholderResourceName:        "{resourceName}",
						create.PlaceholderPodName:             "{podName}",
						create.PlaceholderStatefulSetName:     "{statefulSetName}",
						create.PlaceholderExternalServiceName: "{externalServiceName}",
						create.PlaceholderMongodProcessDomain: "{mongodProcessDomain}",
						create.PlaceholderMongodProcessFQDN:   "{mongodProcessFQDN}",
					},
					SpecWrapper: &v1.ServiceSpecWrapper{
						Spec: corev1.ServiceSpec{
							Type: "LoadBalancer",
							Ports: []corev1.ServicePort{
								{
									Name: "mongodb",
									Port: 27017,
								},
							},
						},
					},
				},
			},
			result: map[int]corev1.Service{
				0: {
					ObjectMeta: metav1.ObjectMeta{
						Name:            "test-om-db-0-svc-external",
						Namespace:       "my-namespace",
						ResourceVersion: "1",
						Labels: map[string]string{
							util.OperatorLabelName:         util.OperatorLabelValue,
							appsv1.StatefulSetPodNameLabel: "test-om-db-0",
							omv1.LabelResourceOwner:        "test-om",
						},
						Annotations: map[string]string{
							create.PlaceholderPodIndex:            "0",
							create.PlaceholderNamespace:           "my-namespace",
							create.PlaceholderResourceName:        "test-om-db",
							create.PlaceholderStatefulSetName:     "test-om-db",
							create.PlaceholderPodName:             "test-om-db-0",
							create.PlaceholderExternalServiceName: "test-om-db-0-svc-external",
							create.PlaceholderMongodProcessDomain: "test-om-db-svc.my-namespace.svc.cluster.local",
							create.PlaceholderMongodProcessFQDN:   "test-om-db-0.test-om-db-svc.my-namespace.svc.cluster.local",
						},
					},
					Spec: corev1.ServiceSpec{
						Type:                     "LoadBalancer",
						PublishNotReadyAddresses: true,
						Ports: []corev1.ServicePort{
							{
								Name: "mongodb",
								Port: 27017,
							},
						},
						Selector: map[string]string{
							util.OperatorLabelName:         util.OperatorLabelValue,
							appsv1.StatefulSetPodNameLabel: "test-om-db-0",
						},
					},
				},
				1: {
					ObjectMeta: metav1.ObjectMeta{
						Name:            "test-om-db-1-svc-external",
						Namespace:       "my-namespace",
						ResourceVersion: "1",
						Labels: map[string]string{
							util.OperatorLabelName:         util.OperatorLabelValue,
							appsv1.StatefulSetPodNameLabel: "test-om-db-1",
							omv1.LabelResourceOwner:        "test-om",
						},
						Annotations: map[string]string{
							create.PlaceholderPodIndex:            "1",
							create.PlaceholderNamespace:           "my-namespace",
							create.PlaceholderResourceName:        "test-om-db",
							create.PlaceholderStatefulSetName:     "test-om-db",
							create.PlaceholderPodName:             "test-om-db-1",
							create.PlaceholderExternalServiceName: "test-om-db-1-svc-external",
							create.PlaceholderMongodProcessDomain: "test-om-db-svc.my-namespace.svc.cluster.local",
							create.PlaceholderMongodProcessFQDN:   "test-om-db-1.test-om-db-svc.my-namespace.svc.cluster.local",
						},
					},
					Spec: corev1.ServiceSpec{
						Type:                     "LoadBalancer",
						PublishNotReadyAddresses: true,
						Ports: []corev1.ServicePort{
							{
								Name: "mongodb",
								Port: 27017,
							},
						},
						Selector: map[string]string{
							util.OperatorLabelName:         util.OperatorLabelValue,
							appsv1.StatefulSetPodNameLabel: "test-om-db-1",
						},
					},
				},
			},
		},
		"service with annotations with placeholders and external domain": {
			members: 2,
			externalAccess: &mdbv1.ExternalAccessConfiguration{
				ExternalDomain: ptr.To("custom.domain"),
				ExternalService: mdbv1.ExternalServiceConfiguration{
					Annotations: map[string]string{
						create.PlaceholderPodIndex:            "{podIndex}",
						create.PlaceholderNamespace:           "{namespace}",
						create.PlaceholderResourceName:        "{resourceName}",
						create.PlaceholderPodName:             "{podName}",
						create.PlaceholderStatefulSetName:     "{statefulSetName}",
						create.PlaceholderExternalServiceName: "{externalServiceName}",
						create.PlaceholderMongodProcessDomain: "{mongodProcessDomain}",
						create.PlaceholderMongodProcessFQDN:   "{mongodProcessFQDN}",
					},
					SpecWrapper: &v1.ServiceSpecWrapper{
						Spec: corev1.ServiceSpec{
							Type: "LoadBalancer",
							Ports: []corev1.ServicePort{
								{
									Name: "mongodb",
									Port: 27017,
								},
							},
						},
					},
				},
			},
			result: map[int]corev1.Service{
				0: {
					ObjectMeta: metav1.ObjectMeta{
						Name:            "test-om-db-0-svc-external",
						Namespace:       "my-namespace",
						ResourceVersion: "1",
						Labels: map[string]string{
							util.OperatorLabelName:         util.OperatorLabelValue,
							appsv1.StatefulSetPodNameLabel: "test-om-db-0",
							omv1.LabelResourceOwner:        "test-om",
						},
						Annotations: map[string]string{
							create.PlaceholderPodIndex:            "0",
							create.PlaceholderNamespace:           "my-namespace",
							create.PlaceholderResourceName:        "test-om-db",
							create.PlaceholderStatefulSetName:     "test-om-db",
							create.PlaceholderPodName:             "test-om-db-0",
							create.PlaceholderExternalServiceName: "test-om-db-0-svc-external",
							create.PlaceholderMongodProcessDomain: "custom.domain",
							create.PlaceholderMongodProcessFQDN:   "test-om-db-0.custom.domain",
						},
					},
					Spec: corev1.ServiceSpec{
						Type:                     "LoadBalancer",
						PublishNotReadyAddresses: true,
						Ports: []corev1.ServicePort{
							{
								Name: "mongodb",
								Port: 27017,
							},
						},
						Selector: map[string]string{
							util.OperatorLabelName:         util.OperatorLabelValue,
							appsv1.StatefulSetPodNameLabel: "test-om-db-0",
						},
					},
				},
				1: {
					ObjectMeta: metav1.ObjectMeta{
						Name:            "test-om-db-1-svc-external",
						Namespace:       "my-namespace",
						ResourceVersion: "1",
						Labels: map[string]string{
							util.OperatorLabelName:         util.OperatorLabelValue,
							appsv1.StatefulSetPodNameLabel: "test-om-db-1",
							omv1.LabelResourceOwner:        "test-om",
						},
						Annotations: map[string]string{
							create.PlaceholderPodIndex:            "1",
							create.PlaceholderNamespace:           "my-namespace",
							create.PlaceholderResourceName:        "test-om-db",
							create.PlaceholderStatefulSetName:     "test-om-db",
							create.PlaceholderPodName:             "test-om-db-1",
							create.PlaceholderExternalServiceName: "test-om-db-1-svc-external",
							create.PlaceholderMongodProcessDomain: "custom.domain",
							create.PlaceholderMongodProcessFQDN:   "test-om-db-1.custom.domain",
						},
					},
					Spec: corev1.ServiceSpec{
						Type:                     "LoadBalancer",
						PublishNotReadyAddresses: true,
						Ports: []corev1.ServicePort{
							{
								Name: "mongodb",
								Port: 27017,
							},
						},
						Selector: map[string]string{
							util.OperatorLabelName:         util.OperatorLabelValue,
							appsv1.StatefulSetPodNameLabel: "test-om-db-1",
						},
					},
				},
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			opsManagerBuilder := DefaultOpsManagerBuilder().
				SetAppDbExternalAccess(*test.externalAccess).
				SetAppDbMembers(test.members).
				SetAdditionalMongodbConfig(test.additionalMongodConfig)

			opsManager := opsManagerBuilder.Build()

			kubeClient, omConnectionFactory := mock.NewDefaultFakeClient()
			reconciler, err := newAppDbReconciler(ctx, kubeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
			require.NoError(t, err)

			err = createOpsManagerUserPasswordSecret(ctx, kubeClient, opsManager, opsManagerUserPassword)
			assert.NoError(t, err)
			reconcileResult, err := reconciler.ReconcileAppDB(ctx, opsManager)
			require.NoError(t, err)

			assert.Equal(t, time.Duration(10000000000), reconcileResult.RequeueAfter)

			for podIdx := 0; podIdx < opsManager.Spec.AppDB.Members; podIdx++ {
				svcName := dns.GetExternalServiceName(opsManager.Spec.AppDB.GetName(), podIdx)
				svcNamespace := opsManager.Namespace

				svcList := corev1.ServiceList{}
				err = kubeClient.List(ctx, &svcList)
				assert.NoError(t, err)

				actualSvc := corev1.Service{}

				err = kubeClient.Get(ctx, kube.ObjectKey(svcNamespace, svcName), &actualSvc)
				assert.NoError(t, err)

				expectedSvc := test.result[podIdx]
				assert.Equal(t, expectedSvc, actualSvc)
			}
		})
	}
}

func findVolumeByName(volumes []corev1.Volume, name string) *corev1.Volume {
	for i := 0; i < len(volumes); i++ {
		if volumes[i].Name == name {
			return &volumes[i]
		}
	}

	return nil
}

func TestAppDBScaleUp_HappensIncrementally(t *testing.T) {
	ctx := context.Background()
	performAppDBScalingTest(ctx, t, 1, 5)
}

func TestAppDBScaleDown_HappensIncrementally(t *testing.T) {
	ctx := context.Background()
	performAppDBScalingTest(ctx, t, 5, 1)
}

func TestAppDBScaleUp_HappensIncrementally_FullOpsManagerReconcile(t *testing.T) {
	ctx := context.Background()

	opsManager := DefaultOpsManagerBuilder().
		SetBackup(omv1.MongoDBOpsManagerBackup{Enabled: false}).
		SetAppDbMembers(1).
		SetVersion("7.0.0").
		Build()
	omConnectionFactory := om.NewCachedOMConnectionFactory(om.NewEmptyMockedOmConnection)
	omReconciler, client, _ := defaultTestOmReconciler(ctx, t, nil, "", "", opsManager, nil, omConnectionFactory, architectures.NonStatic)

	checkOMReconciliationSuccessful(ctx, t, omReconciler, opsManager, client)

	err := client.Get(ctx, types.NamespacedName{Name: opsManager.Name, Namespace: opsManager.Namespace}, opsManager)
	assert.NoError(t, err)

	opsManager.Spec.AppDB.Members = 3

	err = client.Update(ctx, opsManager)
	assert.NoError(t, err)

	checkOMReconciliationPending(ctx, t, omReconciler, opsManager)

	err = client.Get(ctx, types.NamespacedName{Name: opsManager.Name, Namespace: opsManager.Namespace}, opsManager)
	assert.NoError(t, err)

	assert.Equal(t, 2, opsManager.Status.AppDbStatus.Members)

	res, err := omReconciler.Reconcile(ctx, requestFromObject(opsManager))
	assert.NoError(t, err)
	ok, _ := workflow.OK().ReconcileResult()
	assert.Equal(t, ok, res)

	err = client.Get(ctx, types.NamespacedName{Name: opsManager.Name, Namespace: opsManager.Namespace}, opsManager)
	assert.NoError(t, err)

	assert.Equal(t, 3, opsManager.Status.AppDbStatus.Members)
}

func TestAppDbPortIsConfigurable_WithAdditionalMongoConfig(t *testing.T) {
	ctx := context.Background()
	opsManager := DefaultOpsManagerBuilder().
		SetBackup(omv1.MongoDBOpsManagerBackup{Enabled: false}).
		SetAppDbMembers(1).
		SetAdditionalMongodbConfig(mdbv1.NewAdditionalMongodConfig("net.port", 30000)).
		Build()
	omConnectionFactory := om.NewCachedOMConnectionFactory(om.NewEmptyMockedOmConnection)
	omReconciler, client, _ := defaultTestOmReconciler(ctx, t, nil, "", "", opsManager, nil, omConnectionFactory, architectures.NonStatic)

	checkOMReconciliationSuccessful(ctx, t, omReconciler, opsManager, client)

	appdbSvc, err := client.GetService(ctx, kube.ObjectKey(opsManager.Namespace, opsManager.Spec.AppDB.ServiceName()))
	assert.NoError(t, err)
	assert.Equal(t, int32(30000), appdbSvc.Spec.Ports[0].Port)
}

func TestAppDBSkipsReconciliation_IfAnyProcessesAreDisabled(t *testing.T) {
	ctx := context.Background()
	createReconcilerWithAllRequiredSecrets := func(opsManager *omv1.MongoDBOpsManager, createAutomationConfig bool) *ReconcileAppDbReplicaSet {
		kubeClient, omConnectionFactory := mock.NewDefaultFakeClient()
		err := createOpsManagerUserPasswordSecret(ctx, kubeClient, opsManager, "my-password")
		assert.NoError(t, err)
		reconciler, err := newAppDbReconciler(ctx, kubeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
		require.NoError(t, err)
		memberCluster := multicluster.GetLegacyCentralMemberCluster(opsManager.Spec.Replicas, 0, reconciler.client, reconciler.SecretClient)

		// create a pre-existing automation config based on the resource provided.
		// if the automation is not there, we will always want to reconcile. Otherwise, we may not reconcile
		// based on whether or not there are disabled processes.
		if createAutomationConfig {
			ac, err := reconciler.buildAppDbAutomationConfig(ctx, opsManager, nil, UnusedPrometheusConfiguration, multicluster.LegacyCentralClusterName, zap.S())
			assert.NoError(t, err)

			_, err = reconciler.publishAutomationConfig(ctx, opsManager, ac, opsManager.Spec.AppDB.AutomationConfigSecretName(), memberCluster.SecretClient)
			assert.NoError(t, err)
		}
		return reconciler
	}

	t.Run("Reconciliation should happen if we are disabling a process", func(t *testing.T) {
		// In this test, we create an OM + automation config (with no disabled processes),
		// then update OM to have a disabled processes, and we assert that reconciliation should take place.

		omName := "test-om"
		opsManager := DefaultOpsManagerBuilder().SetName(omName).Build()

		reconciler := createReconcilerWithAllRequiredSecrets(opsManager, true)

		opsManager = DefaultOpsManagerBuilder().SetName(omName).SetAppDBAutomationConfigOverride(v1.AutomationConfigOverride{
			Processes: []v1.OverrideProcess{
				{
					// disable the process
					Name:     fmt.Sprintf("%s-db-0", omName),
					Disabled: true,
				},
			},
		}).Build()

		shouldReconcile, err := reconciler.shouldReconcileAppDB(ctx, opsManager, zap.S())
		assert.NoError(t, err)
		assert.True(t, shouldReconcile)
	})

	t.Run("Reconciliation should not happen if a process is disabled", func(t *testing.T) {
		// In this test, we create an OM with a disabled process, and assert that a reconciliation
		// should not take place (since we are not changing a process back from disabled).

		omName := "test-om"
		opsManager := DefaultOpsManagerBuilder().SetName(omName).SetAppDBAutomationConfigOverride(v1.AutomationConfigOverride{
			Processes: []v1.OverrideProcess{
				{
					// disable the process
					Name:     fmt.Sprintf("%s-db-0", omName),
					Disabled: true,
				},
			},
		}).Build()

		reconciler := createReconcilerWithAllRequiredSecrets(opsManager, true)

		shouldReconcile, err := reconciler.shouldReconcileAppDB(ctx, opsManager, zap.S())
		assert.NoError(t, err)
		assert.False(t, shouldReconcile)
	})

	t.Run("Reconciliation should happen if no automation config is present", func(t *testing.T) {
		omName := "test-om"
		opsManager := DefaultOpsManagerBuilder().SetName(omName).SetAppDBAutomationConfigOverride(v1.AutomationConfigOverride{
			Processes: []v1.OverrideProcess{
				{
					// disable the process
					Name:     fmt.Sprintf("%s-db-0", omName),
					Disabled: true,
				},
			},
		}).Build()

		reconciler := createReconcilerWithAllRequiredSecrets(opsManager, false)

		shouldReconcile, err := reconciler.shouldReconcileAppDB(ctx, opsManager, zap.S())
		assert.NoError(t, err)
		assert.True(t, shouldReconcile)
	})

	t.Run("Reconciliation should happen we are re-enabling a process", func(t *testing.T) {
		omName := "test-om"
		opsManager := DefaultOpsManagerBuilder().SetName(omName).SetAppDBAutomationConfigOverride(v1.AutomationConfigOverride{
			Processes: []v1.OverrideProcess{
				{
					// disable the process
					Name:     fmt.Sprintf("%s-db-0", omName),
					Disabled: true,
				},
			},
		}).Build()

		reconciler := createReconcilerWithAllRequiredSecrets(opsManager, true)

		opsManager = DefaultOpsManagerBuilder().SetName(omName).Build()

		shouldReconcile, err := reconciler.shouldReconcileAppDB(ctx, opsManager, zap.S())
		assert.NoError(t, err)
		assert.True(t, shouldReconcile)
	})
}

// appDBStatefulSetLabelsAndServiceName returns extra fields that we have to manually set to the AppDB statefulset
// since we manually create it. Otherwise, the tests will fail as we try to update parts of the sts that we are not
// allowed to change
func appDBStatefulSetLabelsAndServiceName(omResourceName string) (map[string]string, string) {
	appDbName := fmt.Sprintf("%s-db", omResourceName)
	serviceName := fmt.Sprintf("%s-svc", appDbName)
	labels := map[string]string{"app": serviceName, util.OperatorLabelName: util.OperatorLabelValue, "pod-anti-affinity": appDbName}
	return labels, serviceName
}

func appDBStatefulSetVolumeClaimTemplates() []corev1.PersistentVolumeClaim {
	resData, _ := resource.ParseQuantity("16G")
	return []corev1.PersistentVolumeClaim{
		{
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{"ReadWriteOnce"},
				Resources: corev1.VolumeResourceRequirements{
					Requests: map[corev1.ResourceName]resource.Quantity{"storage": resData},
				},
			},
		},
	}
}

func performAppDBScalingTest(ctx context.Context, t *testing.T, startingMembers, finalMembers int) {
	builder := DefaultOpsManagerBuilder().SetAppDbMembers(startingMembers)
	opsManager := builder.Build()
	fakeClient, omConnectionFactory := mock.NewDefaultFakeClient()
	reconciler := createRunningAppDB(ctx, t, startingMembers, fakeClient, opsManager, omConnectionFactory)

	// Scale the AppDB
	opsManager.Spec.AppDB.Members = finalMembers

	if startingMembers < finalMembers {
		for i := startingMembers; i < finalMembers-1; i++ {
			err := fakeClient.Update(ctx, opsManager)
			assert.NoError(t, err)

			res, err := reconciler.ReconcileAppDB(ctx, opsManager)

			assert.NoError(t, err)
			assert.Equal(t, time.Duration(10000000000), res.RequeueAfter)
		}
	} else {
		for i := startingMembers; i > finalMembers+1; i-- {
			err := fakeClient.Update(ctx, opsManager)
			assert.NoError(t, err)

			res, err := reconciler.ReconcileAppDB(ctx, opsManager)

			assert.NoError(t, err)
			assert.Equal(t, time.Duration(10000000000), res.RequeueAfter)
		}
	}

	res, err := reconciler.ReconcileAppDB(ctx, opsManager)
	assert.NoError(t, err)
	ok, _ := workflow.OK().ReconcileResult()
	assert.Equal(t, ok, res)

	err = fakeClient.Get(ctx, types.NamespacedName{Name: opsManager.Name, Namespace: opsManager.Namespace}, opsManager)
	assert.NoError(t, err)

	assert.Equal(t, finalMembers, opsManager.Status.AppDbStatus.Members)
}

func buildAutomationConfigForAppDb(ctx context.Context, builder *omv1.OpsManagerBuilder, kubeClient client.Client, omConnectionFactoryFunc om.ConnectionFactory, log *zap.SugaredLogger) (automationconfig.AutomationConfig, error) {
	opsManager := builder.Build()

	// Ensure the password exists for the Ops Manager User. The Ops Manager controller will have ensured this.
	// We are ignoring this err on purpose since the secret might already exist.
	_ = createOpsManagerUserPasswordSecret(ctx, kubeClient, opsManager, "my-password")
	reconciler, err := newAppDbReconciler(ctx, kubeClient, opsManager, omConnectionFactoryFunc, zap.S())
	if err != nil {
		return automationconfig.AutomationConfig{}, err
	}
	return reconciler.buildAppDbAutomationConfig(ctx, opsManager, nil, UnusedPrometheusConfiguration, multicluster.LegacyCentralClusterName, zap.S())
}

func checkDeploymentEqualToPublished(t *testing.T, expected automationconfig.AutomationConfig, s *corev1.Secret) {
	actual, err := automationconfig.FromBytes(s.Data["cluster-config.json"])
	assert.NoError(t, err)
	expectedBytes, err := json.Marshal(expected)
	assert.NoError(t, err)
	expectedAc := automationconfig.AutomationConfig{}
	err = json.Unmarshal(expectedBytes, &expectedAc)
	assert.NoError(t, err)
	assert.Equal(t, expectedAc, actual)
}

func TestBuildMongoConnectionUrl(t *testing.T) {
	opsManager := DefaultOpsManagerBuilder().Build()

	url := buildMongoConnectionUrl(opsManager, "password", nil)

	opsManager.Spec.AppDB.Members = 5
	assert.NotEqual(t, url, buildMongoConnectionUrl(opsManager, "password", nil),
		"Changing the number of members should result in a different connection string")

	opsManager.Spec.AppDB.Members = 3
	opsManager.Spec.AppDB.Version = "4.2.0"
	assert.Equal(t, url, buildMongoConnectionUrl(opsManager, "password", nil),
		"Changing version should not change the connection string")
}

func TestEnsureResourcesForArchitectureChange(t *testing.T) {
	ctx := context.Background()
	opsManager := DefaultOpsManagerBuilder().Build()

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()

	t.Run("When no automation config is present, there is no error", func(t *testing.T) {
		client := mock.NewDefaultFakeClientWithOMConnectionFactory(omConnectionFactory)
		reconciler, err := newAppDbReconciler(ctx, client, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
		require.NoError(t, err)

		err = reconciler.ensureResourcesForArchitectureChange(ctx, opsManager)
		assert.NoError(t, err)
	})

	t.Run("If User is not present, there is an error", func(t *testing.T) {
		client := mock.NewDefaultFakeClientWithOMConnectionFactory(omConnectionFactory)
		ac, err := automationconfig.NewBuilder().SetAuth(automationconfig.Auth{
			Users: []automationconfig.MongoDBUser{
				{
					Username: "not-ops-manager-user",
				},
			},
		}).Build()

		assert.NoError(t, err)

		acBytes, err := json.Marshal(ac)
		assert.NoError(t, err)

		// create the automation config secret
		err = client.CreateSecret(ctx, secret.Builder().SetNamespace(opsManager.Namespace).SetName(opsManager.Spec.AppDB.AutomationConfigSecretName()).SetField(automationconfig.ConfigKey, string(acBytes)).Build())
		assert.NoError(t, err)

		reconciler, err := newAppDbReconciler(ctx, client, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
		require.NoError(t, err)

		err = reconciler.ensureResourcesForArchitectureChange(ctx, opsManager)
		assert.Error(t, err)
	})

	t.Run("If an automation config is present, all secrets are created with the correct values", func(t *testing.T) {
		client := mock.NewDefaultFakeClientWithOMConnectionFactory(omConnectionFactory)
		ac, err := automationconfig.NewBuilder().SetAuth(automationconfig.Auth{
			AutoPwd: "VrBQgsUZJJs",
			Key:     "Z8PSBtvvjnvds4zcI6iZ",
			Users: []automationconfig.MongoDBUser{
				{
					Username: util.OpsManagerMongoDBUserName,
					ScramSha256Creds: &scramcredentials.ScramCreds{
						Salt:      "sha256-salt-value",
						ServerKey: "sha256-serverkey-value",
						StoredKey: "sha256-storedkey-value",
					},
					ScramSha1Creds: &scramcredentials.ScramCreds{
						Salt:      "sha1-salt-value",
						ServerKey: "sha1-serverkey-value",
						StoredKey: "sha1-storedkey-value",
					},
				},
			},
		}).Build()

		assert.NoError(t, err)

		acBytes, err := json.Marshal(ac)
		assert.NoError(t, err)

		// create the automation config secret
		err = client.CreateSecret(ctx, secret.Builder().SetNamespace(opsManager.Namespace).SetName(opsManager.Spec.AppDB.AutomationConfigSecretName()).SetField(automationconfig.ConfigKey, string(acBytes)).Build())
		assert.NoError(t, err)

		// create the old ops manager user password
		err = client.CreateSecret(ctx, secret.Builder().SetNamespace(opsManager.Namespace).SetName(opsManager.Spec.AppDB.Name()+"-password").SetField("my-password", "jrJP7eUeyn").Build())
		assert.NoError(t, err)

		reconciler, err := newAppDbReconciler(ctx, client, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
		require.NoError(t, err)

		err = reconciler.ensureResourcesForArchitectureChange(ctx, opsManager)
		assert.NoError(t, err)

		t.Run("Scram credentials have been created", func(t *testing.T) {
			ctx := context.Background()
			scramCreds, err := client.GetSecret(ctx, kube.ObjectKey(opsManager.Namespace, opsManager.Spec.AppDB.OpsManagerUserScramCredentialsName()))
			assert.NoError(t, err)

			assert.Equal(t, ac.Auth.Users[0].ScramSha256Creds.Salt, string(scramCreds.Data["sha256-salt"]))
			assert.Equal(t, ac.Auth.Users[0].ScramSha256Creds.StoredKey, string(scramCreds.Data["sha-256-stored-key"]))
			assert.Equal(t, ac.Auth.Users[0].ScramSha256Creds.ServerKey, string(scramCreds.Data["sha-256-server-key"]))

			assert.Equal(t, ac.Auth.Users[0].ScramSha1Creds.Salt, string(scramCreds.Data["sha1-salt"]))
			assert.Equal(t, ac.Auth.Users[0].ScramSha1Creds.StoredKey, string(scramCreds.Data["sha-1-stored-key"]))
			assert.Equal(t, ac.Auth.Users[0].ScramSha1Creds.ServerKey, string(scramCreds.Data["sha-1-server-key"]))
		})

		t.Run("Ops Manager user password has been copied", func(t *testing.T) {
			ctx := context.Background()
			newOpsManagerUserPassword, err := client.GetSecret(ctx, kube.ObjectKey(opsManager.Namespace, opsManager.Spec.AppDB.GetOpsManagerUserPasswordSecretName()))
			assert.NoError(t, err)
			assert.Equal(t, string(newOpsManagerUserPassword.Data["my-password"]), "jrJP7eUeyn")
		})

		t.Run("Agent password has been created", func(t *testing.T) {
			ctx := context.Background()
			agentPasswordSecret, err := client.GetSecret(ctx, opsManager.Spec.AppDB.GetAgentPasswordSecretNamespacedName())
			assert.NoError(t, err)
			assert.Equal(t, ac.Auth.AutoPwd, string(agentPasswordSecret.Data[constants.AgentPasswordKey]))
		})

		t.Run("Keyfile has been created", func(t *testing.T) {
			ctx := context.Background()
			keyFileSecret, err := client.GetSecret(ctx, opsManager.Spec.AppDB.GetAgentKeyfileSecretNamespacedName())
			assert.NoError(t, err)
			assert.Equal(t, ac.Auth.Key, string(keyFileSecret.Data[constants.AgentKeyfileKey]))
		})
	})
}

func newAppDbReconciler(ctx context.Context, c client.Client, opsManager *omv1.MongoDBOpsManager, omConnectionFactoryFunc om.ConnectionFactory, log *zap.SugaredLogger) (*ReconcileAppDbReplicaSet, error) {
	commonController := NewReconcileCommonController(ctx, c)
	return NewAppDBReplicaSetReconciler(ctx, nil, "", opsManager, commonController, omConnectionFactoryFunc, nil, architectures.NonStatic, log)
}

func newAppDbMultiReconciler(ctx context.Context, c client.Client, opsManager *omv1.MongoDBOpsManager, memberClusterMap map[string]client.Client, log *zap.SugaredLogger, omConnectionFactoryFunc om.ConnectionFactory) (*ReconcileAppDbReplicaSet, error) {
	_ = c.Update(ctx, opsManager)
	commonController := NewReconcileCommonController(ctx, c)
	return NewAppDBReplicaSetReconciler(ctx, nil, "", opsManager, commonController, omConnectionFactoryFunc, memberClusterMap, architectures.NonStatic, log)
}

func TestChangingFCVAppDB(t *testing.T) {
	ctx := context.Background()
	builder := DefaultOpsManagerBuilder().SetAppDbMembers(3)
	opsManager := builder.Build()
	fakeClient, omConnectionFactory := mock.NewDefaultFakeClient()
	reconciler := createRunningAppDB(ctx, t, 3, fakeClient, opsManager, omConnectionFactory)

	// Helper function to update and verify FCV
	verifyFCV := func(version, expectedFCV string, fcvOverride *string, t *testing.T) {
		if fcvOverride != nil {
			opsManager.Spec.AppDB.FeatureCompatibilityVersion = fcvOverride
		}

		opsManager.Spec.AppDB.Version = version
		_ = fakeClient.Update(ctx, opsManager)
		_, err := reconciler.ReconcileAppDB(ctx, opsManager)
		assert.NoError(t, err)
		assert.Equal(t, expectedFCV, opsManager.Status.AppDbStatus.FeatureCompatibilityVersion)
	}

	testFCVsCases(t, verifyFCV)
}

// createOpsManagerUserPasswordSecret creates the secret which holds the password that will be used for the Ops Manager user.
func createOpsManagerUserPasswordSecret(ctx context.Context, kubeClient client.Client, om *omv1.MongoDBOpsManager, password string) error {
	sec := secret.Builder().
		SetName(om.Spec.AppDB.GetOpsManagerUserPasswordSecretName()).
		SetNamespace(om.Namespace).
		SetField("password", password).
		Build()
	return kubeClient.Create(ctx, &sec)
}

func readAutomationConfigSecret(ctx context.Context, t *testing.T, kubeClient client.Client, opsManager *omv1.MongoDBOpsManager) *corev1.Secret {
	s := &corev1.Secret{}
	key := kube.ObjectKey(opsManager.Namespace, opsManager.Spec.AppDB.AutomationConfigSecretName())
	assert.NoError(t, kubeClient.Get(ctx, key, s))
	return s
}

func readAutomationConfigMonitoringSecret(ctx context.Context, t *testing.T, kubeClient client.Client, opsManager *omv1.MongoDBOpsManager) *corev1.Secret {
	s := &corev1.Secret{}
	key := kube.ObjectKey(opsManager.Namespace, monitoringAutomationConfigSecretName(opsManager.Spec.AppDB))
	assert.NoError(t, kubeClient.Get(ctx, key, s))
	return s
}

func createRunningAppDB(ctx context.Context, t *testing.T, startingMembers int, fakeClient kubernetesClient.Client, opsManager *omv1.MongoDBOpsManager, omConnectionFactory *om.CachedOMConnectionFactory) *ReconcileAppDbReplicaSet {
	err := createOpsManagerUserPasswordSecret(ctx, fakeClient, opsManager, "pass")
	assert.NoError(t, err)
	reconciler, err := newAppDbReconciler(ctx, fakeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
	require.NoError(t, err)

	// create the apiKey and OM user
	data := map[string]string{
		util.OmPublicApiKey: "publicApiKey",
		util.OmPrivateKey:   "privateApiKey",
	}

	APIKeySecretName, err := opsManager.APIKeySecretName(ctx, secrets.SecretClient{KubeClient: fakeClient}, "")
	assert.NoError(t, err)

	apiKeySecret := secret.Builder().
		SetNamespace(operatorNamespace()).
		SetName(APIKeySecretName).
		SetStringMapToData(data).
		Build()

	err = reconciler.client.CreateSecret(ctx, apiKeySecret)
	assert.NoError(t, err)

	err = fakeClient.Create(ctx, opsManager)
	assert.NoError(t, err)

	matchLabels, serviceName := appDBStatefulSetLabelsAndServiceName(opsManager.Name)
	// app db sts should exist before monitoring is configured
	appDbSts, err := statefulset.NewBuilder().
		SetName(opsManager.Spec.AppDB.Name()).
		SetNamespace(opsManager.Namespace).
		SetMatchLabels(matchLabels).
		SetServiceName(serviceName).
		AddVolumeClaimTemplates(appDBStatefulSetVolumeClaimTemplates()).
		SetReplicas(startingMembers).
		// the operator sets the OM OwnerReference on the AppDB StatefulSet it creates
		// (AppDBOwnerReferenceForMemberCluster); without it the re-adoption gate reads the
		// StatefulSet as mid-migration and blocks ReconcileAppDB.
		SetOwnerReference(kube.BaseOwnerReference(opsManager)).
		Build()

	assert.NoError(t, err)
	err = fakeClient.CreateStatefulSet(ctx, appDbSts)
	assert.NoError(t, err)

	res, err := reconciler.ReconcileAppDB(ctx, opsManager)

	assert.NoError(t, err)
	ok, _ := workflow.OK().ReconcileResult()
	assert.Equal(t, ok, res)
	return reconciler
}

func assertDefaultLogRotate(t *testing.T, lr *automationconfig.MonitoringLogRotate) {
	t.Helper()
	require.NotNil(t, lr)
	assert.Equal(t, 1000, lr.SizeThresholdMB)
	assert.Equal(t, 24, lr.TimeThresholdHrs)
}

func TestConfigureMonitoring_NonTLS(t *testing.T) {
	ac := automationconfig.AutomationConfig{
		Processes: []automationconfig.Process{{HostName: "host-0"}},
	}
	configureMonitoring(&ac, zap.S(), false, "myGroupId", "myApiKey", false, nil)

	require.Len(t, ac.MonitoringVersions, 1)
	params := ac.MonitoringVersions[0].AdditionalParams
	// Under Option B credentials are delivered to the agent as CLI flags, not via additionalParams.
	assert.NotContains(t, params, "mmsGroupId")
	assert.NotContains(t, params, "mmsApiKey")
	assert.NotContains(t, params, "useSslForAllConnections")
	assertDefaultLogRotate(t, ac.MonitoringVersions[0].LogRotate)
	assert.Equal(t, monitoringAgentLogFile, ac.MonitoringVersions[0].LogPath)
}

func TestConfigureMonitoring_TLS(t *testing.T) {
	ac := automationconfig.AutomationConfig{
		Processes: []automationconfig.Process{{HostName: "host-0"}},
	}
	configureMonitoring(&ac, zap.S(), true, "myGroupId", "myApiKey", true, nil)

	require.Len(t, ac.MonitoringVersions, 1)
	params := ac.MonitoringVersions[0].AdditionalParams
	assert.NotContains(t, params, "mmsGroupId")
	assert.NotContains(t, params, "mmsApiKey")
	assert.Equal(t, "true", params["useSslForAllConnections"])
	assert.Equal(t, "true", params["sslRequireValidMMSServerCertificates"])
	assert.Equal(t, appdbCAFilePath, params["sslTrustedServerCertificates"])
	assertDefaultLogRotate(t, ac.MonitoringVersions[0].LogRotate)
	assert.Equal(t, monitoringAgentLogFile, ac.MonitoringVersions[0].LogPath)
}

func TestConfigureMonitoring_TLS_RequireValidCertFalse(t *testing.T) {
	ac := automationconfig.AutomationConfig{
		Processes: []automationconfig.Process{{HostName: "host-0"}},
	}
	configureMonitoring(&ac, zap.S(), true, "myGroupId", "myApiKey", false, nil)

	require.Len(t, ac.MonitoringVersions, 1)
	params := ac.MonitoringVersions[0].AdditionalParams
	assert.NotContains(t, params, "mmsGroupId")
	assert.NotContains(t, params, "mmsApiKey")
	assert.Equal(t, "true", params["useSslForAllConnections"])
	assert.Equal(t, "false", params["sslRequireValidMMSServerCertificates"])
	assertDefaultLogRotate(t, ac.MonitoringVersions[0].LogRotate)
	assert.Equal(t, monitoringAgentLogFile, ac.MonitoringVersions[0].LogPath)
}

func TestConfigureMonitoring_ClearsWhenDisabled(t *testing.T) {
	ac := automationconfig.AutomationConfig{
		Processes: []automationconfig.Process{{HostName: "host-0"}},
		MonitoringVersions: []automationconfig.MonitoringVersion{
			{Hostname: "host-0", AdditionalParams: map[string]string{"mmsGroupId": "old"}},
		},
	}
	// called with empty credentials simulates monitoring disabled
	configureMonitoring(&ac, zap.S(), false, "", "", false, nil)

	assert.Empty(t, ac.MonitoringVersions)
}

// TestClearTLSParams tests CLOUDP-351614 fix:
// When TLS is disabled on AppDB, TLS-specific params should be cleared from
// the monitoring config's additionalParams to prevent the monitoring agent
// from trying to use certificate files that no longer exist.
func TestClearTLSParams(t *testing.T) {
	tests := []struct {
		name           string
		input          map[string]string
		expectedOutput map[string]string
	}{
		{
			name:           "nil map",
			input:          nil,
			expectedOutput: nil,
		},
		{
			name:           "empty map",
			input:          map[string]string{},
			expectedOutput: map[string]string{},
		},
		{
			name: "only TLS params",
			input: map[string]string{
				"useSslForAllConnections":      "true",
				"sslTrustedServerCertificates": "/some/path/ca.pem",
				"sslClientCertificate":         "/some/path/cert.pem",
			},
			expectedOutput: map[string]string{},
		},
		{
			name: "mixed params - TLS and non-TLS",
			input: map[string]string{
				"useSslForAllConnections":      "true",
				"sslTrustedServerCertificates": "/some/path/ca.pem",
				"sslClientCertificate":         "/some/path/cert.pem",
				"someOtherParam":               "someValue",
				"anotherParam":                 "anotherValue",
			},
			expectedOutput: map[string]string{
				"someOtherParam": "someValue",
				"anotherParam":   "anotherValue",
			},
		},
		{
			name: "only non-TLS params",
			input: map[string]string{
				"someOtherParam": "someValue",
				"anotherParam":   "anotherValue",
			},
			expectedOutput: map[string]string{
				"someOtherParam": "someValue",
				"anotherParam":   "anotherValue",
			},
		},
		{
			name: "partial TLS params",
			input: map[string]string{
				"useSslForAllConnections": "true",
				"someOtherParam":          "someValue",
			},
			expectedOutput: map[string]string{
				"someOtherParam": "someValue",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			om.ClearTLSParams(tt.input)
			assert.Equal(t, tt.expectedOutput, tt.input)
		})
	}
}

// TestAppDB_PVCStatusClearedAfterSuccessfulResize is a regression test for KUBE-108.
// After a PVC resize completes, the AppDB controller must clear the stale PVC status
// from the CRD. Without the fix, the PhaseSTSOrphaned entry persists indefinitely.
func TestAppDB_PVCStatusClearedAfterSuccessfulResize(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		builder := DefaultOpsManagerBuilder().SetAppDbMembers(3)
		opsManager := builder.Build()

		fakeClient, omConnectionFactory := mock.NewDefaultFakeClient()
		createRunningAppDB(ctx, t, 3, fakeClient, opsManager, omConnectionFactory)

		// Create the PVCs that Kubernetes would generate for the AppDB StatefulSet.
		// VolumeClaimTemplate name is "data" (AppDBSpec.DataVolumeName()), STS name is "<om-name>-db".
		stsName := opsManager.Spec.AppDB.Name()
		initialStorage := resource.MustParse("16G")
		newStorage := resource.MustParse("50G")

		var pvcs []corev1.PersistentVolumeClaim
		for i := 0; i < 3; i++ {
			p := corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("data-%s-%d", stsName, i),
					Namespace: opsManager.Namespace,
				},
				Spec: corev1.PersistentVolumeClaimSpec{
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceStorage: initialStorage},
					},
				},
				Status: corev1.PersistentVolumeClaimStatus{
					Capacity: corev1.ResourceList{corev1.ResourceStorage: initialStorage},
				},
			}
			require.NoError(t, fakeClient.Create(ctx, &p))
			pvcs = append(pvcs, p)
		}

		// Trigger a resize by increasing the storage in the AppDB spec.
		opsManager.Spec.AppDB.PodSpec.Persistence = &v1.Persistence{
			SingleConfig: &v1.PersistenceConfig{Storage: "50G"},
		}
		require.NoError(t, fakeClient.Update(ctx, opsManager))

		// Reconcile 1: resize detected; PVCs are patched but storage has not propagated yet.
		reconciler, err := newAppDbReconciler(ctx, fakeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
		require.NoError(t, err)
		_, err = reconciler.ReconcileAppDB(ctx, opsManager)
		require.NoError(t, err)
		require.NoError(t, fakeClient.Get(ctx, types.NamespacedName{Name: opsManager.Name, Namespace: opsManager.Namespace}, opsManager))
		require.Equal(t, pvc.PhasePVCResize, opsManager.Status.AppDbStatus.PVCs[0].Phase)

		// Simulate Kubernetes completing the PVC resize (update status.Capacity on each PVC).
		for i := range pvcs {
			var updatedPVC corev1.PersistentVolumeClaim
			require.NoError(t, fakeClient.Get(ctx, types.NamespacedName{Name: pvcs[i].Name, Namespace: pvcs[i].Namespace}, &updatedPVC))
			updatedPVC.Status.Capacity = corev1.ResourceList{corev1.ResourceStorage: newStorage}
			updatedPVC.Spec.Resources.Requests[corev1.ResourceStorage] = newStorage
			require.NoError(t, fakeClient.SubResource("status").Update(ctx, &updatedPVC))
		}

		// Reconcile 2: PVCs done; the STS is orphaned and recreated. The reconcile completes
		// successfully, so the final updateStatus clears the stale PVC entry in the same pass.
		reconciler, err = newAppDbReconciler(ctx, fakeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
		require.NoError(t, err)
		_, err = reconciler.ReconcileAppDB(ctx, opsManager)
		require.NoError(t, err)
		require.NoError(t, fakeClient.Get(ctx, types.NamespacedName{Name: opsManager.Name, Namespace: opsManager.Namespace}, opsManager))

		assert.Nil(t, opsManager.Status.AppDbStatus.PVCs,
			"PVC status must be cleared after a successful resize")
	})
}

func TestMonitoringAgentStartupParametersIgnored(t *testing.T) {
	t.Setenv(util.OpsManagerMonitorAppDB, "true")
	ctx := context.Background()
	builder := DefaultOpsManagerBuilder()
	opsManager := builder.Build()
	//nolint:staticcheck // intentionally sets the deprecated field to verify it is ignored
	opsManager.Spec.AppDB.MonitoringAgent.StartupParameters = map[string]string{"myCustomParam": "myValue"}

	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(opsManager)
	reconciler, err := newAppDbReconciler(ctx, kubeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
	require.NoError(t, err)
	_ = createOpsManagerUserPasswordSecret(ctx, kubeClient, opsManager, "my-password")

	podVars := &env.PodEnvVars{
		ProjectID:   "abc123",
		AgentAPIKey: "my-api-key",
	}

	ac, err := reconciler.buildAppDbAutomationConfig(ctx, opsManager, podVars, "", multicluster.LegacyCentralClusterName, zap.S())
	require.NoError(t, err)

	require.NotEmpty(t, ac.MonitoringVersions)
	assert.NotContains(t, ac.MonitoringVersions[0].AdditionalParams, "myCustomParam",
		"deprecated monitoringAgent.startupOptions must not be forwarded to monitoringVersions additionalParams")
}

// monitoringAutomationConfigSecretName / monitoringAutomationConfigConfigMapName reproduce the
// names of the pre-single-agent monitoring automation config resources. The operator no longer
// creates these; the helpers exist only so tests can assert their absence (or exercise legacy paths).
func monitoringAutomationConfigSecretName(appdb *omv1.AppDBSpec) string {
	return appdb.Name() + "-monitoring-config"
}

func monitoringAutomationConfigConfigMapName(appdb *omv1.AppDBSpec) string {
	return appdb.Name() + "-monitoring-automation-config-version"
}

func TestReconcileAppDbReplicaSet_BuildAppDBConnectionURL(t *testing.T) {
	ctx := context.Background()
	testOm := DefaultOpsManagerBuilder().Build()
	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()

	reconciler, _, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)
	appDbReconciler, err := reconciler.createNewAppDBReconciler(ctx, testOm, zap.S())
	require.NoError(t, err)

	connString, err := appDbReconciler.BuildAppDBConnectionURL(ctx, testOm, zap.S())
	require.NoError(t, err)
	assert.Contains(t, connString, util.OpsManagerMongoDBUserName)
}

func TestIsReAdoptedStatefulSetPendingReshape(t *testing.T) {
	tests := []struct {
		name            string
		containers      []string
		expectedPending bool
	}{
		{
			name:            "MongoDB-CR shape (non-static) is pending reshape",
			containers:      []string{util.DatabaseContainerName},
			expectedPending: true,
		},
		{
			name:            "MongoDB-CR shape (static architecture) is pending reshape",
			containers:      []string{util.AgentContainerName, util.DatabaseContainerName},
			expectedPending: true,
		},
		{
			name:            "internal-AppDB shape is not pending reshape",
			containers:      []string{util.AgentContainerName, util.MongodbContainerName},
			expectedPending: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sts := appsv1.StatefulSet{}
			for _, name := range tt.containers {
				sts.Spec.Template.Spec.Containers = append(sts.Spec.Template.Spec.Containers, corev1.Container{Name: name})
			}
			assert.Equal(t, tt.expectedPending, isReAdoptedStatefulSetPendingReshape(sts))
		})
	}
}

// TestReconcileAppDB_ReshapesReAdoptedStatefulSet reproduces the reverse-migration state right
// after re-adoption: the StatefulSet is OM-owned again but still carries the MongoDB CR's pod
// shape. A single ReconcileAppDB pass must rewrite the pod template to the internal-AppDB shape
// instead of deadlocking on the agent goal-state wait (which those CR-shaped pods can never
// satisfy - they run no headless agent).
func TestReconcileAppDB_ReshapesReAdoptedStatefulSet(t *testing.T) {
	ctx := context.Background()
	opsManager := DefaultOpsManagerBuilder().Build()
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(opsManager)
	require.NoError(t, createOpsManagerUserPasswordSecret(ctx, kubeClient, opsManager, "pass"))
	reconciler, err := newAppDbReconciler(ctx, kubeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
	require.NoError(t, err)

	matchLabels, serviceName := appDBStatefulSetLabelsAndServiceName(opsManager.Name)
	sts, err := statefulset.NewBuilder().
		SetName(opsManager.Spec.AppDB.Name()).
		SetNamespace(opsManager.Namespace).
		SetMatchLabels(matchLabels).
		SetServiceName(serviceName).
		AddVolumeClaimTemplates(appDBStatefulSetVolumeClaimTemplates()).
		SetReplicas(3).
		SetOwnerReference(kube.BaseOwnerReference(opsManager)).
		Build()
	require.NoError(t, err)
	sts.Spec.Template.Spec.Containers = []corev1.Container{{Name: util.DatabaseContainerName, Image: "busybox"}}
	require.NoError(t, kubeClient.CreateStatefulSet(ctx, sts))

	res, err := reconciler.ReconcileAppDB(ctx, opsManager)
	require.NoError(t, err)
	// first pass after re-adoption ends in the standard "requeue to configure monitoring" result;
	// the point is that it deploys the StatefulSet instead of blocking on the agent goal-state
	// wait (Pending, RequeueAfter 10s) which the CR-shaped pods can never satisfy
	assert.Equal(t, reconcile.Result{Requeue: true}, res, "reconcile must not block on the agent goal-state wait for a CR-shaped StatefulSet")

	result, err := kubeClient.GetStatefulSet(ctx, kube.ObjectKey(opsManager.Namespace, opsManager.Spec.AppDB.Name()))
	require.NoError(t, err)
	containerNames := make([]string, 0, len(result.Spec.Template.Spec.Containers))
	for _, c := range result.Spec.Template.Spec.Containers {
		containerNames = append(containerNames, c.Name)
	}
	assert.Contains(t, containerNames, util.MongodbContainerName, "pod template must be rewritten to the internal-AppDB shape")
	assert.NotContains(t, containerNames, util.DatabaseContainerName, "the MongoDB-CR container must not survive the reshape")
}

func TestEnsureAppDBStatefulSetOwnership(t *testing.T) {
	const crUID = "cr-uid-2222"

	tests := []struct {
		name string
		// sts builds the pre-existing AppDB StatefulSet; nil means it doesn't exist
		sts                       func(testOm *omv1.MongoDBOpsManager) appsv1.StatefulSet
		omUID                     types.UID
		expectedOwned             bool
		expectedOMOwnerRef        bool
		expectedReverseAnnotation bool
		expectedForwardAnnotation bool
	}{
		{
			name:          "StatefulSet absent: recreate-from-scratch path proceeds",
			expectedOwned: true,
		},
		{
			name:  "already OM-owned: proceeds untouched",
			omUID: "om-uid-1111",
			sts: func(testOm *omv1.MongoDBOpsManager) appsv1.StatefulSet {
				return DefaultStatefulSetBuilder().SetName(testOm.Spec.AppDB.Name()).
					SetOwnerReferences(kube.BaseOwnerReference(testOm)).Build()
			},
			expectedOwned:      true,
			expectedOMOwnerRef: true,
		},
		{
			name:  "CR-owned: requests release and blocks",
			omUID: "om-uid-1111",
			sts: func(testOm *omv1.MongoDBOpsManager) appsv1.StatefulSet {
				return DefaultStatefulSetBuilder().SetName(testOm.Spec.AppDB.Name()).
					SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "mongodb.com/v1", Kind: "MongoDB", Name: "test-om-db", UID: crUID}}).Build()
			},
			expectedOwned:             false,
			expectedReverseAnnotation: true,
		},
		{
			name:  "ownerless with release request: adopts and clears annotations",
			omUID: "om-uid-1111",
			sts: func(testOm *omv1.MongoDBOpsManager) appsv1.StatefulSet {
				return DefaultStatefulSetBuilder().SetName(testOm.Spec.AppDB.Name()).
					SetOwnerReferences(nil).
					SetAnnotations(map[string]string{util.AppDBReverseMigrationReadyAnnotation: "true"}).Build()
			},
			expectedOwned:      true,
			expectedOMOwnerRef: true,
		},
		{
			name:  "ownerless with stale forward annotation: adopts and clears it",
			omUID: "om-uid-1111",
			sts: func(testOm *omv1.MongoDBOpsManager) appsv1.StatefulSet {
				return DefaultStatefulSetBuilder().SetName(testOm.Spec.AppDB.Name()).
					SetOwnerReferences(nil).
					SetAnnotations(map[string]string{util.AppDBMigrationReadyAnnotation: "true"}).Build()
			},
			expectedOwned:      true,
			expectedOMOwnerRef: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			testOm := DefaultOpsManagerBuilder().SetName("test-om").Build()
			testOm.UID = tt.omUID

			kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(testOm)
			reconciler, err := newAppDbReconciler(ctx, kubeClient, testOm, omConnectionFactory.GetConnectionFunc, zap.S())
			require.NoError(t, err)
			if tt.sts != nil {
				sts := tt.sts(testOm)
				require.NoError(t, kubeClient.Create(ctx, &sts))
			}

			owned, err := reconciler.ensureAppDBStatefulSetOwnership(ctx, testOm)
			require.NoError(t, err)
			assert.Equal(t, tt.expectedOwned, owned)

			if tt.sts != nil {
				result := appsv1.StatefulSet{}
				require.NoError(t, kubeClient.Get(ctx, kube.ObjectKey(testOm.Namespace, testOm.Spec.AppDB.Name()), &result))
				hasOMRef := false
				for _, ref := range result.OwnerReferences {
					if ref.UID == testOm.UID {
						hasOMRef = true
					}
				}
				assert.Equal(t, tt.expectedOMOwnerRef, hasOMRef)
				assert.Equal(t, tt.expectedReverseAnnotation, result.Annotations[util.AppDBReverseMigrationReadyAnnotation] == "true")
				assert.Equal(t, tt.expectedForwardAnnotation, result.Annotations[util.AppDBMigrationReadyAnnotation] == "true")
			}
		})
	}
}

func TestEnsureAppDBStatefulSetOwnership_ClaimsSharedSecretsOnAdoption(t *testing.T) {
	ctx := context.Background()
	testOm := DefaultOpsManagerBuilder().SetName("test-om").Build()
	testOm.UID = types.UID("om-uid-1111")

	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(testOm)
	reconciler, err := newAppDbReconciler(ctx, kubeClient, testOm, omConnectionFactory.GetConnectionFunc, zap.S())
	require.NoError(t, err)

	sts := DefaultStatefulSetBuilder().SetName(testOm.Spec.AppDB.Name()).
		SetOwnerReferences(nil).
		SetAnnotations(map[string]string{util.AppDBReverseMigrationReadyAnnotation: "true"}).Build()
	require.NoError(t, kubeClient.Create(ctx, &sts))

	crOwnerRef := []metav1.OwnerReference{{APIVersion: "mongodb.com/v1", Kind: "MongoDB", Name: "test-om-db", UID: "cr-uid-2222"}}
	for _, name := range []string{omv1.OpsManagerUserPasswordSecretName("test-om-db"), testOm.Spec.AppDB.GetAgentKeyfileSecretNamespacedName().Name} {
		s := secret.Builder().SetName(name).SetNamespace(testOm.Namespace).SetField("k", "v").SetOwnerReferences(crOwnerRef).Build()
		require.NoError(t, kubeClient.CreateSecret(ctx, s))
	}

	owned, err := reconciler.ensureAppDBStatefulSetOwnership(ctx, testOm)
	require.NoError(t, err)
	require.True(t, owned)

	for _, name := range []string{omv1.OpsManagerUserPasswordSecretName("test-om-db"), testOm.Spec.AppDB.GetAgentKeyfileSecretNamespacedName().Name} {
		s := corev1.Secret{}
		require.NoError(t, kubeClient.Get(ctx, kube.ObjectKey(testOm.Namespace, name), &s))
		require.Len(t, s.OwnerReferences, 1, name)
		assert.Equal(t, testOm.UID, s.OwnerReferences[0].UID, "secret %s must be claimed by the OM at adoption", name)
	}
}
